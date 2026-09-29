package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// pushBatchLimit bounds one report. A long outage's backlog goes over
// several reports rather than one enormous body, and the agent is told to
// come straight back for the rest.
const pushBatchLimit = 500

// Pusher reports an agent's results to hz.
//
// It holds hz's URL and a token, which is the one asymmetry worth noting
// against pull: in push mode the agent does know where hz is. That buys
// away the public address, the inbound rule, the self-signed certificate and
// the pinning, because hz's endpoint has an ordinary CA certificate that
// verifies normally.
//
// The vantage may also hold a notification URL of its own (Alert), which is
// not an hz credential: it is how the vantage says "hz is down" when hz,
// the only other notifier, is the thing that is down.
type Pusher struct {
	// URL is hz's base, e.g. https://kiosk.vpn.example.com.
	URL string

	// Token authenticates the agent. On first contact it may be an install
	// grant, which hz converts into a registered vantage.
	Token string

	// Timeout bounds one report. Zero means 30 seconds.
	Timeout time.Duration

	// Alert, when set, notifies a human after consecutive failed reports and
	// again on recovery — "hz unreachable", or "hz rejects this vantage" when
	// hz answers 401/403 (see Alerter). Nil is off. It never affects what is reported or
	// acknowledged.
	Alert *Alerter

	http *http.Client
}

func (p *Pusher) client() *http.Client {
	if p.http == nil {
		timeout := p.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		// Deliberately the default transport: hz's certificate is expected to
		// verify like any other public endpoint. There is nothing to pin and
		// nothing to skip.
		p.http = &http.Client{Timeout: timeout}
	}
	return p.http
}

// Report sends one batch and returns hz's answer.
func (p *Pusher) Report(ctx context.Context, req PushRequest) (*PushResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSuffix(p.URL, "/") + "/api/v1/probe/report"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.Token)
	httpReq.Header.Set("User-Agent", "hz-probe/"+req.Version)

	resp, err := p.client().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &HTTPError{Status: resp.StatusCode, Message: hzMessage(msg)}
	}

	var out PushResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("could not read hz's response: %w", err)
	}
	return &out, nil
}

// HTTPError is hz ANSWERING a report with something other than 200. It is a
// type so the alerter can tell "hz said no" from "hz is not there" without
// matching on error text. A transport error is never an *HTTPError.
type HTTPError struct {
	Status  int    // hz's HTTP status
	Message string // hz's own words: the "error" field of its JSON body, or the body
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("hz returned %d: %s", e.Status, e.Message)
}

// Rejected reports whether hz refused this vantage itself (401 or 403): its
// token is not one hz accepts — rotated, or the vantage was removed from hz.
// That is a different fault from hz being down, with a different fix, so it
// is a different alert.
func (e *HTTPError) Rejected() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// hzMessage is the text of an error body: the "error" field of hz's JSON
// errors (writeJSONError), or the trimmed body when it is anything else.
func hzMessage(body []byte) string {
	var j struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &j) == nil && j.Error != "" {
		return j.Error
	}
	return strings.TrimSpace(string(body))
}

// PushLoop reports to hz on an interval until ctx is done.
//
// Results are dropped from the buffer only once hz says it took them, so a
// failed report is retried rather than lost — which is what makes an hz
// outage a delay instead of a hole.
func (a *Agent) PushLoop(ctx context.Context, p *Pusher, interval time.Duration) {
	if interval <= 0 {
		interval = defaultInterval
	}

	// sent is the watermark of what hz has acknowledged.
	var sent time.Time

	for {
		next := interval

		results, more := a.Since(sent, pushBatchLimit)
		held := a.Targets()

		resp, err := p.Report(ctx, PushRequest{
			Vantage:        a.vantage,
			Version:        a.version,
			TargetsVersion: held.Version,
			Results:        results,
			Buffered:       more,
		})
		switch {
		case err != nil:
			// Keep the watermark where it is; the batch goes again next time.
			slog.Warn("probe: could not report to hz",
				"error", err, "buffered", len(results))
			p.Alert.Failed(ctx, err)
		default:
			p.Alert.Succeeded(ctx)
			if resp.Targets != nil {
				a.SetTargets(*resp.Targets)
				slog.Info("probe: hz sent a new target set",
					"version", resp.Targets.Version, "targets", len(resp.Targets.Targets))
			}

			// Say it loudly and keep saying it. A vantage quietly running an
			// old build is the kind of thing nobody discovers until they are
			// debugging something else.
			if resp.AgentVersion != "" && resp.AgentVersion != a.version {
				slog.Warn("probe: this agent is not the build hz holds — update it",
					"running", a.version, "available", resp.AgentVersion)
			}
			if resp.Notice != "" {
				slog.Warn("probe: notice from hz", "notice", resp.Notice)
			}
			// Advance only over what hz acknowledged.
			if resp.Accepted > 0 && resp.Accepted <= len(results) {
				sent = results[resp.Accepted-1].At
			}
			if resp.Interval > 0 {
				next = time.Duration(resp.Interval) * time.Second
			}
			// A backlog is drained promptly rather than one batch per
			// interval, which would take hours to catch up after an outage.
			if more && resp.Accepted > 0 {
				next = time.Second
			}

		}

		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.Probed():
			// A round just finished, so there is something to send. This is
			// what makes a freshly installed vantage show real data in
			// seconds: the first round takes as long as its slowest target's
			// timeout, and any fixed delay would either race it or idle.
			timer.Stop()
		case <-timer.C:
		}
	}
}
