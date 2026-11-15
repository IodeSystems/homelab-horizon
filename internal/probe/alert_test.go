package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ntfyMsg is one POST the fake ntfy took.
type ntfyMsg struct {
	Body, Title, Priority, Tags, Auth string
}

// fakeNtfy records what it is sent. status is what it answers with.
type fakeNtfy struct {
	mu     sync.Mutex
	msgs   []ntfyMsg
	status int
}

func (f *fakeNtfy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, ntfyMsg{
		Body: string(b), Title: r.Header.Get("Title"),
		Priority: r.Header.Get("Priority"), Tags: r.Header.Get("Tags"),
		Auth: r.Header.Get("Authorization"),
	})
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
}

func (f *fakeNtfy) got() []ntfyMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ntfyMsg(nil), f.msgs...)
}

func (f *fakeNtfy) setStatus(s int) {
	f.mu.Lock()
	f.status = s
	f.mu.Unlock()
}

// clock is a hand-advanced time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestAlerter(t *testing.T, threshold int) (*Alerter, *fakeNtfy, *clock) {
	t.Helper()
	fn := &fakeNtfy{}
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	c := &clock{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	return &Alerter{URL: srv.URL + "/topic", Vantage: "vps", Threshold: threshold, Now: c.now}, fn, c
}

var errDown = errors.New("dial tcp: connection refused")

func TestAlertNotSentBelowThreshold(t *testing.T) {
	al, fn, _ := newTestAlerter(t, 3)
	ctx := context.Background()
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown)
	if n := len(fn.got()); n != 0 {
		t.Fatalf("two failures under a threshold of three sent %d messages", n)
	}
}

func TestAlertOncePerOutage(t *testing.T) {
	al, fn, c := newTestAlerter(t, 3)
	ctx := context.Background()
	start := c.t
	for i := 0; i < 3; i++ {
		al.Failed(ctx, errDown)
		c.t = c.t.Add(time.Minute)
	}
	msgs := fn.got()
	if len(msgs) != 1 {
		t.Fatalf("reaching the threshold sent %d messages, want 1", len(msgs))
	}
	want := "hz unreachable from vps: " + errDown.Error() + ", since " + start.Format(time.RFC3339)
	if msgs[0].Body != want {
		t.Fatalf("alert body\n got %q\nwant %q", msgs[0].Body, want)
	}
	if msgs[0].Priority != "high" || msgs[0].Tags != "warning" {
		t.Fatalf("alert should be high priority and tagged, got priority=%q tags=%q", msgs[0].Priority, msgs[0].Tags)
	}

	// N+1 onward: the human already knows.
	for i := 0; i < 10; i++ {
		al.Failed(ctx, errDown)
	}
	if n := len(fn.got()); n != 1 {
		t.Fatalf("further failures re-alerted: %d messages, want 1", n)
	}
}

func TestRecoveryAfterAlertedOutage(t *testing.T) {
	al, fn, c := newTestAlerter(t, 3)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		al.Failed(ctx, errDown)
		c.t = c.t.Add(time.Minute)
	}
	al.Succeeded(ctx)
	al.Succeeded(ctx) // a second success is steady state, not a second recovery

	msgs := fn.got()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want an alert and one recovery", len(msgs))
	}
	if want := "hz reachable again from vps after 5m0s"; msgs[1].Body != want {
		t.Fatalf("recovery body\n got %q\nwant %q", msgs[1].Body, want)
	}

	// The next outage is a new one, and alerts again.
	for i := 0; i < 3; i++ {
		al.Failed(ctx, errDown)
	}
	if n := len(fn.got()); n != 3 {
		t.Fatalf("a second outage after recovery should alert: %d messages, want 3", n)
	}
}

func TestNoRecoveryAfterShortStreak(t *testing.T) {
	al, fn, _ := newTestAlerter(t, 3)
	ctx := context.Background()
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown)
	al.Succeeded(ctx)
	// And the streak really reset: two more is still under three.
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown)
	if n := len(fn.got()); n != 0 {
		t.Fatalf("short streaks sent %d messages, want 0", n)
	}
}

// An undelivered alert is not "alerted": the next failure tries again, and
// once it lands it is still sent only once.
func TestUndeliveredAlertIsRetried(t *testing.T) {
	al, fn, _ := newTestAlerter(t, 2)
	ctx := context.Background()
	fn.setStatus(http.StatusInternalServerError)
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown) // attempt 1, refused
	fn.setStatus(0)
	al.Failed(ctx, errDown) // attempt 2, delivered
	al.Failed(ctx, errDown) // already delivered
	if n := len(fn.got()); n != 2 {
		t.Fatalf("got %d attempts, want 2 (one refused, one delivered)", n)
	}
}

func TestNilAlerterIsOff(t *testing.T) {
	var al *Alerter
	al.Failed(context.Background(), errDown)
	al.Succeeded(context.Background())
}

// fakeHz is an hz report endpoint that fails while down is set, and
// otherwise accepts up to accept results per report.
type fakeHz struct {
	mu       sync.Mutex
	down     bool
	status   int // what a down hz answers, as hz's JSON error; zero means a plain 502
	accept   int
	reports  int
	received []time.Time // every result At it was sent, in order, duplicates included
	taken    []time.Time // what it acknowledged
}

func (h *fakeHz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports++
	if h.down {
		if h.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(h.status)
			_, _ = w.Write([]byte(`{"error":"unknown vantage token"}`))
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	var req PushRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	n := len(req.Results)
	if n > h.accept {
		n = h.accept
	}
	for _, res := range req.Results {
		h.received = append(h.received, res.At)
	}
	h.taken = append(h.taken, func() []time.Time {
		var out []time.Time
		for _, res := range req.Results[:n] {
			out = append(out, res.At)
		}
		return out
	}()...)
	_ = json.NewEncoder(w).Encode(PushResponse{Accepted: n})
}

func (h *fakeHz) snapshot() (reports int, taken []time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reports, append([]time.Time(nil), h.taken...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The alert path must never touch reporting: with ntfy refusing everything,
// the loop still reports through an outage, and hz ends up with every result
// exactly once, in order — acks advance only over what hz took.
func TestPushLoopReportsAndAcksWithNtfyDown(t *testing.T) {
	hz := &fakeHz{down: true, accept: 2}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	fn := &fakeNtfy{status: http.StatusServiceUnavailable}
	ntfySrv := httptest.NewServer(fn)
	defer ntfySrv.Close()

	agent := NewAgent("vps", "test", "tok", "")
	base := time.Now().Add(-time.Hour)
	var want []time.Time
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		want = append(want, at)
		agent.Record([]Result{{Target: "a", Host: "a.example.com", Kind: KindDNS, At: at, Status: "ok"}})
	}

	p := &Pusher{URL: hzSrv.URL, Token: "tok",
		Alert: &Alerter{URL: ntfySrv.URL + "/topic", Vantage: "vps", Threshold: 2}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.PushLoop(ctx, p, 10*time.Millisecond); close(done) }()

	// Through the outage, past the threshold, with ntfy refusing.
	waitFor(t, "failed reports past the threshold", func() bool {
		n, _ := hz.snapshot()
		return n >= 5 && len(fn.got()) >= 1
	})
	hz.mu.Lock()
	hz.down = false
	hz.mu.Unlock()

	waitFor(t, "all results acknowledged", func() bool {
		_, taken := hz.snapshot()
		return len(taken) >= len(want)
	})
	cancel()
	<-done

	_, taken := hz.snapshot()
	if len(taken) != len(want) {
		t.Fatalf("hz took %d results, want %d — a result was sent twice after being acked", len(taken), len(want))
	}
	for i := range want {
		if !taken[i].Equal(want[i]) {
			t.Fatalf("result %d: took %v, want %v", i, taken[i], want[i])
		}
	}
	for _, m := range fn.got() {
		if strings.Contains(m.Body, "reachable again") {
			t.Fatal("a recovery was sent for an outage whose alert was never delivered")
		}
	}
}

// End to end through the loop: one alert, one recovery.
func TestPushLoopAlertsAndRecovers(t *testing.T) {
	hz := &fakeHz{down: true, accept: 100}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()
	fn := &fakeNtfy{}
	ntfySrv := httptest.NewServer(fn)
	defer ntfySrv.Close()

	agent := NewAgent("vps", "test", "tok", "")
	p := &Pusher{URL: hzSrv.URL, Token: "tok",
		Alert: &Alerter{URL: ntfySrv.URL + "/topic", Vantage: "vps", Threshold: 3}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.PushLoop(ctx, p, 10*time.Millisecond); close(done) }()

	waitFor(t, "several reports past the threshold", func() bool {
		n, _ := hz.snapshot()
		return n >= 8
	})
	if n := len(fn.got()); n != 1 {
		t.Fatalf("after 8+ failures ntfy got %d messages, want exactly 1", n)
	}
	hz.mu.Lock()
	hz.down = false
	hz.mu.Unlock()
	waitFor(t, "the recovery", func() bool { return len(fn.got()) >= 2 })
	// Let a few steady-state reports go by.
	n0, _ := hz.snapshot()
	waitFor(t, "steady-state reports", func() bool { n, _ := hz.snapshot(); return n >= n0+3 })
	cancel()
	<-done

	msgs := fn.got()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want an alert and a recovery", len(msgs))
	}
	if !strings.HasPrefix(msgs[0].Body, "hz unreachable from vps: hz returned 502") {
		t.Fatalf("alert body %q", msgs[0].Body)
	}
	if !strings.HasPrefix(msgs[1].Body, "hz reachable again from vps after ") {
		t.Fatalf("recovery body %q", msgs[1].Body)
	}
}

// Off means off: a Pusher with no Alerter sends nothing anywhere but hz.
func TestPushLoopWithoutAlerter(t *testing.T) {
	hz := &fakeHz{down: true}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()
	agent := NewAgent("vps", "test", "tok", "")
	p := &Pusher{URL: hzSrv.URL, Token: "tok"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.PushLoop(ctx, p, 5*time.Millisecond); close(done) }()
	waitFor(t, "failed reports", func() bool { n, _ := hz.snapshot(); return n >= 5 })
	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// ntfy authentication
// ---------------------------------------------------------------------------

// captureLogs points slog at a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestAlertSendsBearerOnlyWhenATokenIsSet(t *testing.T) {
	ctx := context.Background()

	al, fn, _ := newTestAlerter(t, 1)
	al.Token = "tk_secret123"
	al.Failed(ctx, errDown)
	al.Succeeded(ctx)
	msgs := fn.got()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want an alert and a recovery", len(msgs))
	}
	for _, m := range msgs {
		if m.Auth != "Bearer tk_secret123" {
			t.Fatalf("%q: Authorization = %q, want the Bearer token", m.Title, m.Auth)
		}
	}

	// No token: no header at all — today's behaviour, a public topic.
	al, fn, _ = newTestAlerter(t, 1)
	al.Failed(ctx, errDown)
	if msgs := fn.got(); len(msgs) != 1 || msgs[0].Auth != "" {
		t.Fatalf("without a token, Authorization must be absent: %+v", msgs)
	}
}

// The token is a secret: not in any log line, whether ntfy refuses the post
// or cannot be reached at all.
func TestAlertTokenNeverLogged(t *testing.T) {
	const token = "tk_never_in_logs_42"
	logs := captureLogs(t)
	ctx := context.Background()

	al, fn, _ := newTestAlerter(t, 1)
	al.Token = token
	fn.setStatus(http.StatusUnauthorized) // ntfy refuses: the body is logged
	al.Failed(ctx, errDown)

	// Unreachable ntfy: net/http's error is logged.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	al2 := &Alerter{URL: deadURL + "/topic", Token: token, Vantage: "vps", Threshold: 1}
	al2.Failed(ctx, errDown)

	// Positive control: the capture really holds the warnings.
	if !strings.Contains(logs.String(), "ntfy refused") || !strings.Contains(logs.String(), "could not notify ntfy") {
		t.Fatalf("the log capture did not see the warnings:\n%s", logs)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("the ntfy token reached the log:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// The two alert classes
// ---------------------------------------------------------------------------

func rejectedErr(status int) error {
	return &HTTPError{Status: status, Message: "unknown vantage token"}
}

func TestRejectedStreakAlertsRejectedNotUnreachable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		al, fn, c := newTestAlerter(t, 3)
		ctx := context.Background()
		start := c.t
		for i := 0; i < 6; i++ {
			al.Failed(ctx, rejectedErr(status))
			c.t = c.t.Add(time.Minute)
		}
		msgs := fn.got()
		if len(msgs) != 1 {
			t.Fatalf("%d: six rejections sent %d messages, want exactly 1", status, len(msgs))
		}
		m := msgs[0]
		want := fmt.Sprintf("hz rejected reports from vps (HTTP %d): unknown vantage token — re-add it in hz or check its token, since %s",
			status, start.Format(time.RFC3339))
		if m.Title != "hz rejects this vantage" || m.Body != want {
			t.Fatalf("%d: rejected alert\n title %q\n  body %q\n  want %q", status, m.Title, m.Body, want)
		}
		if m.Priority != "high" || m.Tags != "no_entry" {
			t.Fatalf("%d: priority=%q tags=%q, want high/no_entry", status, m.Priority, m.Tags)
		}

		al.Succeeded(ctx)
		msgs = fn.got()
		if len(msgs) != 2 {
			t.Fatalf("%d: got %d messages after an accepted report, want 2", status, len(msgs))
		}
		if msgs[1].Title != "hz accepts this vantage again" ||
			msgs[1].Body != "hz accepts reports from vps again after 6m0s" ||
			msgs[1].Tags != "white_check_mark" {
			t.Fatalf("%d: recovery %+v", status, msgs[1])
		}
	}
}

// 5xx, other 4xx and transport errors are all "unreachable", never "rejected".
func TestNonRejectionsAlertUnreachable(t *testing.T) {
	for name, err := range map[string]error{
		"502":        &HTTPError{Status: http.StatusBadGateway, Message: "down"},
		"400":        &HTTPError{Status: http.StatusBadRequest, Message: "invalid JSON"},
		"connection": errDown,
	} {
		al, fn, _ := newTestAlerter(t, 2)
		ctx := context.Background()
		al.Failed(ctx, err)
		al.Failed(ctx, err)
		msgs := fn.got()
		if len(msgs) != 1 || msgs[0].Title != "hz unreachable" || msgs[0].Tags != "warning" {
			t.Fatalf("%s: want one unreachable alert, got %+v", name, msgs)
		}
		al.Succeeded(ctx)
		if msgs := fn.got(); len(msgs) != 2 || msgs[1].Title != "hz reachable again" {
			t.Fatalf("%s: want the unreachable recovery, got %+v", name, msgs)
		}
	}
}

// A switch of class ends the first streak: below-threshold streaks of
// alternating classes never add up to an alert.
func TestClassSwitchEndsTheStreak(t *testing.T) {
	al, fn, _ := newTestAlerter(t, 3)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		al.Failed(ctx, errDown)
		al.Failed(ctx, errDown)
		al.Failed(ctx, rejectedErr(401))
		al.Failed(ctx, rejectedErr(401))
	}
	if n := len(fn.got()); n != 0 {
		t.Fatalf("alternating short streaks sent %d messages, want 0", n)
	}
	// And a full streak of one class after that still alerts.
	al.Failed(ctx, rejectedErr(401))
	if msgs := fn.got(); len(msgs) != 1 || msgs[0].Title != "hz rejects this vantage" {
		t.Fatalf("a completed rejected streak: %+v", msgs)
	}
}

// An open alert is closed only by an ACCEPTED report, never by the other
// class failing; both open alerts close on that one accepted report, and a
// class returning before then is the same occurrence, not a new alert.
func TestClassSwitchRecoversOnlyOnAccept(t *testing.T) {
	al, fn, c := newTestAlerter(t, 2)
	ctx := context.Background()
	titles := func() []string {
		var out []string
		for _, m := range fn.got() {
			out = append(out, m.Title)
		}
		return out
	}

	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown) // unreachable alert
	c.t = c.t.Add(time.Minute)
	al.Failed(ctx, rejectedErr(401))
	al.Failed(ctx, rejectedErr(401)) // rejected alert; unreachable still open
	if got := titles(); len(got) != 2 || got[0] != "hz unreachable" || got[1] != "hz rejects this vantage" {
		t.Fatalf("after both classes: %q", got)
	}
	// Back to unreachable: already open, so nothing new.
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown)
	al.Failed(ctx, errDown)
	if n := len(fn.got()); n != 2 {
		t.Fatalf("a class returning while its alert is open re-alerted: %q", titles())
	}

	al.Succeeded(ctx)
	got := titles()
	want := []string{"hz unreachable", "hz rejects this vantage", "hz reachable again", "hz accepts this vantage again"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("recoveries\n got %q\nwant %q", got, want)
	}
	al.Succeeded(ctx)
	if n := len(fn.got()); n != 4 {
		t.Fatalf("a second accepted report sent another recovery: %q", titles())
	}
}

func TestUndeliveredRejectedAlertIsRetried(t *testing.T) {
	al, fn, _ := newTestAlerter(t, 2)
	ctx := context.Background()
	fn.setStatus(http.StatusInternalServerError)
	al.Failed(ctx, rejectedErr(403))
	al.Failed(ctx, rejectedErr(403)) // refused
	fn.setStatus(0)
	al.Failed(ctx, rejectedErr(403)) // delivered
	al.Failed(ctx, rejectedErr(403)) // already delivered
	if n := len(fn.got()); n != 2 {
		t.Fatalf("got %d attempts, want 2 (one refused, one delivered)", n)
	}
	fn.setStatus(http.StatusInternalServerError)
	al.Succeeded(ctx) // recovery refused
	fn.setStatus(0)
	al.Succeeded(ctx) // recovery delivered
	al.Succeeded(ctx)
	msgs := fn.got()
	if len(msgs) != 4 || msgs[3].Title != "hz accepts this vantage again" {
		t.Fatalf("want the recovery retried once and delivered once: %+v", msgs)
	}
}

// Report surfaces hz's status as a typed error, so the class is decided on
// the type and not on the text.
func TestReportReturnsTypedHTTPError(t *testing.T) {
	hz := &fakeHz{down: true, status: http.StatusUnauthorized}
	srv := httptest.NewServer(hz)
	_, err := (&Pusher{URL: srv.URL, Token: "t"}).Report(context.Background(), PushRequest{})
	var herr *HTTPError
	if !errors.As(err, &herr) || herr.Status != 401 || !herr.Rejected() || herr.Message != "unknown vantage token" {
		t.Fatalf("want a rejected *HTTPError with hz's message, got %#v", err)
	}

	srv.Close() // now a transport error
	_, err = (&Pusher{URL: srv.URL, Token: "t"}).Report(context.Background(), PushRequest{})
	if err == nil || errors.As(err, &herr) {
		t.Fatalf("a transport error must not be an *HTTPError: %#v", err)
	}
}

// End to end through the loop: hz answering 401 is a rejection, not an
// outage, and an accepted report closes it.
func TestPushLoopRejectedAndAccepted(t *testing.T) {
	hz := &fakeHz{down: true, status: http.StatusUnauthorized, accept: 100}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()
	fn := &fakeNtfy{}
	ntfySrv := httptest.NewServer(fn)
	defer ntfySrv.Close()

	agent := NewAgent("vps", "test", "tok", "")
	p := &Pusher{URL: hzSrv.URL, Token: "tok",
		Alert: &Alerter{URL: ntfySrv.URL + "/topic", Token: "ntfy-tok", Vantage: "vps", Threshold: 3}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { agent.PushLoop(ctx, p, 10*time.Millisecond); close(done) }()

	waitFor(t, "rejections past the threshold", func() bool { n, _ := hz.snapshot(); return n >= 6 })
	hz.mu.Lock()
	hz.down = false
	hz.mu.Unlock()
	waitFor(t, "the recovery", func() bool { return len(fn.got()) >= 2 })
	cancel()
	<-done

	msgs := fn.got()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want a rejection and its recovery: %+v", len(msgs), msgs)
	}
	if msgs[0].Title != "hz rejects this vantage" ||
		!strings.HasPrefix(msgs[0].Body, "hz rejected reports from vps (HTTP 401): unknown vantage token — ") {
		t.Fatalf("rejection %+v", msgs[0])
	}
	if msgs[1].Title != "hz accepts this vantage again" {
		t.Fatalf("recovery %+v", msgs[1])
	}
	for _, m := range msgs {
		if m.Auth != "Bearer ntfy-tok" {
			t.Fatalf("the loop's alerter did not send the ntfy token: %+v", m)
		}
	}
}
