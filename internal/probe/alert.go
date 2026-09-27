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

// Alerter tells a human when this vantage cannot reach hz.
//
// In push mode hz is the only notifier, and a whole-gateway outage takes hz
// down with everything else — so the one failure the vantage exists to see
// notified nobody. The vantage posts to an ntfy topic of its own instead.
//
// It alerts once per outage: after Threshold consecutive failed reports, one
// message; when a report succeeds after that, one recovery. A streak shorter
// than Threshold sends nothing. A delivery that fails is retried on the next
// report, so "once" means once delivered, not once attempted.
//
// A nil *Alerter is valid and does nothing: that is the feature switched off.
// Not safe for concurrent use; the push loop is its only caller.
type Alerter struct {
	// URL is the ntfy topic URL. It is a capability — anyone holding it can
	// post to and read the topic — so it is handled like the token.
	URL string

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

	failures  int       // consecutive failed reports
	firstFail time.Time // when the current outage started
	lastErr   error
	alerted   bool // an outage alert was delivered and no recovery has been
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

// Failed records one failed report, and alerts if it completes an outage.
func (al *Alerter) Failed(ctx context.Context, err error) {
	if al == nil {
		return
	}
	if al.failures == 0 && !al.alerted {
		al.firstFail = al.now()
	}
	al.failures++
	al.lastErr = err

	if al.alerted || al.failures < al.threshold() {
		return
	}
	msg := fmt.Sprintf("hz unreachable from %s: %v, since %s",
		al.Vantage, al.lastErr, al.firstFail.UTC().Format(time.RFC3339))
	if al.send(ctx, "hz unreachable", "high", "warning", msg) {
		al.alerted = true
	}
}

// Succeeded records one successful report, and sends the recovery if an
// outage alert went out.
func (al *Alerter) Succeeded(ctx context.Context) {
	if al == nil {
		return
	}
	al.failures = 0
	al.lastErr = nil
	if !al.alerted {
		return
	}
	down := al.now().Sub(al.firstFail).Round(time.Second)
	msg := fmt.Sprintf("hz reachable again from %s after %s", al.Vantage, down)
	if al.send(ctx, "hz reachable again", "default", "white_check_mark", msg) {
		al.alerted = false
	}
	// Undelivered: stay alerted, so the next success tries again and the
	// next failure does not open a second outage the human was never told
	// the first one ended.
}

// send posts one ntfy message and reports whether ntfy took it. Failure is
// logged and nothing more — the push loop must not care.
func (al *Alerter) send(ctx context.Context, title, priority, tags, msg string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, al.URL, strings.NewReader(msg))
	if err != nil {
		slog.Warn("probe: could not build the ntfy request")
		return false
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
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
