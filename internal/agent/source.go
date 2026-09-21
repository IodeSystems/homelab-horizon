package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// How the agent learns what to apply.
//
// THE AGENT POLLS. hz NEVER INITIATES. That is the constraint from
// plan/architecture.md ("Two channels"), and it is what lets a machine behind
// NAT be managed with no inbound credential, no port forward and no ssh key on
// hz. The gateway's agent polls localhost exactly the way a remote agent polls
// hz, so the local path is the remote path — the gateway is machine #1, not a
// special case.
//
// The poll is a CONDITIONAL GET. hz answers with an ETag that is the payload's
// content hash (Desired.Fingerprint); the agent sends it back as
// If-None-Match; an unchanged fleet gets 304 and a few hundred bytes. There is
// no generation counter to keep correct, no state to persist across a restart,
// and a rollback to an earlier config returns to the generation it came from
// rather than inventing a newer one.
//
// What was rejected, and why:
//
//   - A LOCAL UNIX SOCKET, so hz could hand the gateway's agent its config
//     synchronously. It is the tempting answer because it removes the poll
//     latency on the one box that has the problem, and it is exactly the
//     special case architecture.md refuses: the gateway would then exercise a
//     mechanism no other machine uses, and the remote path would be tested
//     only by remote machines — the ones you cannot walk to.
//
//   - LONG POLL. It converts the latency into a held connection per machine
//     through haproxy, which means idle-timeout tuning, a thundering herd
//     whenever hz restarts, and a connection count that grows with the fleet.
//     It buys a few seconds on a change that a human just made and is watching.
//
//   - `hz sync --wait` BLOCKING until the agent reports the generation
//     applied. This is the right answer eventually and it is the wrong one
//     now: it makes hz's own sync depend on an agent being alive, and this
//     agent ships inert. hz would wait on something that is deliberately not
//     running. It needs a report-back endpoint and an applied-generation
//     record, which is item 16's work — see plan/architecture.md.
//
// THE LATENCY COST. Today a service change reloads haproxy synchronously
// inside the hz request that made it. Once the agent owns the apply (item 12),
// the same change is rendered immediately but applied on the next poll: 0 to
// one interval, 2.5s on average at the 5s default, plus the reload itself. hz
// returns before the gateway has reloaded. That is the whole behavioural cost
// of de-rooting hz, and it is why the interval is seconds rather than minutes.

// DesiredPath is the endpoint hz serves the payload at.
const DesiredPath = "/api/v1/agent/desired"

// Source is where a Desired comes from.
//
// Fetch takes the ETag of what the caller already has and reports whether
// anything changed. An unchanged answer returns (nil, etag, false, nil) — the
// caller keeps what it had and does not re-plan.
type Source interface {
	Fetch(ctx context.Context, etag string) (d *Desired, newETag string, changed bool, err error)
}

// HTTPSource polls an hz instance.
type HTTPSource struct {
	// BaseURL is hz's address. On the gateway this is loopback.
	BaseURL string

	// Token is this machine's agent credential — see credential.go. It is
	// worth a read of this machine's rendered network config and nothing
	// else; it is NOT an hz admin token, and hz refuses one presented here.
	//
	// `hz-agent enroll` mints it and records its hash with hz. Item 13 moves
	// the minting to hz's Machine record; this field does not change.
	Token string

	Client *http.Client
}

// Fetch performs the conditional GET.
func (s *HTTPSource) Fetch(ctx context.Context, etag string) (*Desired, string, bool, error) {
	url := strings.TrimSuffix(s.BaseURL, "/") + DesiredPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, etag, false, err
	}
	// Through the shared helper, never a hand-built header: hz reads it back
	// with PresentedSecret four lines away in credential.go, and the pair
	// drifting apart is exactly the bug this fixed.
	Authorize(req, s.Token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, etag, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, false, nil
	case http.StatusOK:
	case http.StatusUnauthorized:
		// Named, because the answer is a specific command and the generic
		// message sent the last person reading it to isAdmin. No credential
		// in the text: this line goes to a log.
		return nil, etag, false, fmt.Errorf(
			"hz answered %s — this machine is not enrolled with hz. Run `sudo hz-agent enroll`",
			resp.Status)
	default:
		// Bounded read: the body of an error page is diagnostic, and an
		// unbounded one from a proxy that is not hz is a memory problem.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, etag, false, fmt.Errorf("hz answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var d Desired
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, etag, false, fmt.Errorf("decoding the desired state: %w", err)
	}

	// Trust the payload's own hash rather than the header. hz computes the
	// ETag from the same function, so a mismatch means something between here
	// and there rewrote the body — and then the ETag would pin the agent to a
	// generation it does not actually hold.
	return &d, d.Fingerprint(), true, nil
}

// FileSource reads the payload from a local JSON file.
//
// For `hz-agent diff --from`, and for exercising the daemon without an hz.
// Not a fallback for a failed poll: a stale file on disk quietly applying old
// network config is the failure mode the generation exists to prevent.
type FileSource struct{ Path string }

// Fetch reads and hashes the file, reporting unchanged when the hash matches.
func (s FileSource) Fetch(_ context.Context, etag string) (*Desired, string, bool, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, etag, false, err
	}
	var d Desired
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, etag, false, fmt.Errorf("decoding %s: %w", s.Path, err)
	}
	fp := d.Fingerprint()
	if fp == etag {
		return nil, etag, false, nil
	}
	return &d, fp, true, nil
}
