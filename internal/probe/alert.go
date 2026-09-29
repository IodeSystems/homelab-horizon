package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAlertAfter is how many consecutive failed reports make an outage.
// One failure is a blip; three at the default interval is minutes of hz not
// answering.
const defaultAlertAfter = 3

// defaultAlertTimeout bounds one ntfy POST. The push loop waits for it, so it
// must be short: a vantage whose own network is down would otherwise stall
// reporting on a notification that cannot leave either.
const defaultAlertTimeout = 10 * time.Second

// Alerter tells a human when this vantage cannot report to hz.
//
// In push mode hz is the only notifier, and a whole-gateway outage takes hz
// down with everything else — so the one failure the vantage exists to see
// notified nobody. The vantage posts to an ntfy topic of its own instead.
//
// A failed report is one of two classes, and each is its own alert:
//
//   - UNREACHABLE: hz did not answer, or answered with anything but 401/403
//     (a connection error, a timeout, a 5xx, a 4xx that is not about the
//     vantage). "hz unreachable" / "hz reachable again".
//   - REJECTED: hz answered 401 or 403 (*HTTPError.Rejected). hz is up and
//     refuses this vantage — its token was rotated, or it was removed from
//     hz. "hz rejects this vantage" / "hz accepts this vantage again".
//
// Each class alerts once per occurrence: after Threshold consecutive failed
// reports OF THAT CLASS, one message; a streak shorter than Threshold sends
// nothing. A delivery that fails is retried on the next failure of the same
// class, so "once" means once delivered, not once attempted.
//
// THE CLASS-SWITCH RULE. A failure of one class ends the other class's
// STREAK (its count goes to zero, so switching back starts counting again),
// but it does not end the other class's ALERT. An alert that was delivered
// stays open until a report is ACCEPTED, and only then is its recovery sent —
// because neither "hz reachable again" nor "hz accepts this vantage again" is
// true while reports are still failing. So unreachable ×N then rejected ×N
// leaves two open alerts, and the next accepted report closes both
// (unreachable's recovery first). An open alert is not repeated when its
// class comes back before an accepted report: it is the same occurrence.
//
// A nil *Alerter is valid and does nothing: that is the feature switched off.
// Not safe for concurrent use; the push loop is its only caller.
type Alerter struct {
	// URL is the ntfy topic URL. It is a capability — anyone holding it can
	// post to and read the topic — so it is handled like the token.
	URL string

	// Token, when set, is an ntfy access token, sent as
	// "Authorization: Bearer <token>". Empty means an unauthenticated POST
	// (a public topic whose name is the secret). A secret: never logged.
	Token string

	// Vantage names this host in the message.
	Vantage string

	// Threshold is the consecutive-failure count that makes an outage. Zero
	// means defaultAlertAfter.
	Threshold int

	// Timeout bounds one POST. Zero means defaultAlertTimeout.
	Timeout time.Duration

	// Now is the clock. Nil means time.Now; tests set it.
	Now func() time.Time

	http *http.Client

	unreachable streak
	rejected    streak
}

// streak is one alert class's state.
type streak struct {
	count   int       // consecutive failed reports of this class
	first   time.Time // when the current occurrence started
	lastErr error
	alerted bool // an alert was delivered and no recovery has been
}

func (al *Alerter) now() time.Time {
	if al.Now != nil {
		return al.Now()
	}
	return time.Now()
}

func (al *Alerter) threshold() int {
	if al.Threshold > 0 {
		return al.Threshold
	}
	return defaultAlertAfter
}

func (al *Alerter) client() *http.Client {
	if al.http == nil {
		timeout := al.Timeout
		if timeout == 0 {
			timeout = defaultAlertTimeout
		}
		al.http = &http.Client{Timeout: timeout}
	}
	return al.http
}

// Failed records one failed report, and alerts if it completes an
// occurrence of its class.
func (al *Alerter) Failed(ctx context.Context, err error) {
	if al == nil {
		return
	}
	var herr *HTTPError
	isRejected := errors.As(err, &herr) && herr.Rejected()

	cur, other := &al.unreachable, &al.rejected
	if isRejected {
		cur, other = &al.rejected, &al.unreachable
	}
	// The other class's streak ends; its open alert, if any, does not.
	other.count = 0

	if cur.count == 0 && !cur.alerted {
		cur.first = al.now()
	}
	cur.count++
	cur.lastErr = err

	if cur.alerted || cur.count < al.threshold() {
		return
	}
	since := cur.first.UTC().Format(time.RFC3339)
	var delivered bool
	if isRejected {
		msg := fmt.Sprintf("hz rejected reports from %s (HTTP %d): %s — re-add it in hz or check its token, since %s",
			al.Vantage, herr.Status, herr.Message, since)
		delivered = al.send(ctx, "hz rejects this vantage", "high", "no_entry", msg)
	} else {
		msg := fmt.Sprintf("hz unreachable from %s: %v, since %s", al.Vantage, cur.lastErr, since)
		delivered = al.send(ctx, "hz unreachable", "high", "warning", msg)
	}
	if delivered {
		cur.alerted = true
	}
}

// Succeeded records one accepted report, and sends the recovery for each
// class whose alert went out.
func (al *Alerter) Succeeded(ctx context.Context) {
	if al == nil {
		return
	}
	al.unreachable.count, al.unreachable.lastErr = 0, nil
	al.rejected.count, al.rejected.lastErr = 0, nil

	// Undelivered: stay alerted, so the next success tries again and the
	// next failure does not open a second occurrence the human was never
	// told the first one ended.
	if s := &al.unreachable; s.alerted {
		down := al.now().Sub(s.first).Round(time.Second)
		msg := fmt.Sprintf("hz reachable again from %s after %s", al.Vantage, down)
		if al.send(ctx, "hz reachable again", "default", "white_check_mark", msg) {
			s.alerted = false
		}
	}
	if s := &al.rejected; s.alerted {
		down := al.now().Sub(s.first).Round(time.Second)
		msg := fmt.Sprintf("hz accepts reports from %s again after %s", al.Vantage, down)
		if al.send(ctx, "hz accepts this vantage again", "default", "white_check_mark", msg) {
			s.alerted = false
		}
	}
}

// send posts one ntfy message and reports whether ntfy took it. Failure is
// logged and nothing more — the push loop must not care. Neither the URL nor
// the token is ever logged.
func (al *Alerter) send(ctx context.Context, title, priority, tags, msg string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, al.URL, strings.NewReader(msg))
	if err != nil {
		slog.Warn("probe: could not build the ntfy request")
		return false
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if al.Token != "" {
		req.Header.Set("Authorization", "Bearer "+al.Token)
	}
	resp, err := al.client().Do(req)
	if err != nil {
		// net/http names the URL in its error, and the URL is the secret.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		slog.Warn("probe: could not notify ntfy", "error", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		slog.Warn("probe: ntfy refused the notification",
			"status", resp.StatusCode, "body", strings.TrimSpace(string(body)))
		return false
	}
	slog.Info("probe: notified ntfy", "title", title)
	return true
}
