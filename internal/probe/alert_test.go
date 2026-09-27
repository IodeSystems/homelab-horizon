package probe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ntfyMsg is one POST the fake ntfy took.
type ntfyMsg struct {
	Body, Title, Priority, Tags string
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
