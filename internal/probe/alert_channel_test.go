package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The alert channel the operator sets in hz: handed out in the push reply,
// cached on the vantage, used when hz is the thing that is down.

// channelHz is an hz report endpoint that hands out a channel.
type channelHz struct {
	mu      sync.Mutex
	down    bool
	status  int // what a down hz answers; zero means 502
	alert   *AlertChannel
	reports int
}

func (h *channelHz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports++
	if h.down {
		st := h.status
		if st == 0 {
			st = http.StatusBadGateway
		}
		http.Error(w, `{"error":"down"}`, st)
		return
	}
	var req PushRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	_ = json.NewEncoder(w).Encode(PushResponse{Accepted: len(req.Results), Alert: h.alert})
}

func (h *channelHz) set(fn func(h *channelHz)) {
	h.mu.Lock()
	fn(h)
	h.mu.Unlock()
}

func (h *channelHz) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reports
}

// runLoop starts a push loop and returns a stop function that waits for it.
func runLoop(t *testing.T, a *Agent, p *Pusher) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.PushLoop(ctx, p, 5*time.Millisecond); close(done) }()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

// seedState writes a state file holding ch, as a previous run would have.
func seedState(t *testing.T, path string, ch *AlertChannel) {
	t.Helper()
	b, err := json.Marshal(persisted{Targets: TargetSet{Version: "v1"}, Alert: ch})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, path string) persisted {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// hz sets the channel; the vantage caches it (0600) and, when hz then goes
// down, alerts there with the token hz gave it.
func TestHzChannelIsCachedAndAlertsWithHzDown(t *testing.T) {
	fn := &fakeNtfy{}
	ntfy := httptest.NewServer(fn)
	defer ntfy.Close()
	hz := &channelHz{alert: &AlertChannel{URL: ntfy.URL + "/from-hz", Token: "tk-from-hz"}}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	state := filepath.Join(t.TempDir(), "state.json")
	a := NewAgent("vps", "test", "tok", state)
	p := &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2}
	runLoop(t, a, p)

	waitFor(t, "the channel to be cached", func() bool { return a.AlertChannel() != nil })
	st, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v, want 0600 — it holds a secret", st.Mode().Perm())
	}
	if got := readState(t, state).Alert; got == nil || got.URL != ntfy.URL+"/from-hz" || got.Token != "tk-from-hz" {
		t.Fatalf("cached channel %+v", got)
	}

	hz.set(func(h *channelHz) { h.down = true })
	waitFor(t, "an alert to hz's channel", func() bool { return len(fn.got()) >= 1 })
	m := fn.got()[0]
	if m.Title != "hz unreachable" || m.Auth != "Bearer tk-from-hz" {
		t.Fatalf("alert %+v, want 'hz unreachable' with hz's token", m)
	}
}

// Hot: a changed channel is followed without a restart, and a pending
// recovery is not lost.
func TestHzChannelChangeIsFollowedHot(t *testing.T) {
	first, second := &fakeNtfy{}, &fakeNtfy{}
	s1, s2 := httptest.NewServer(first), httptest.NewServer(second)
	defer s1.Close()
	defer s2.Close()
	hz := &channelHz{alert: &AlertChannel{URL: s1.URL + "/a"}}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	a := NewAgent("vps", "test", "tok", filepath.Join(t.TempDir(), "state.json"))
	p := &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2}
	runLoop(t, a, p)

	waitFor(t, "the first channel", func() bool { ch := a.AlertChannel(); return ch != nil && ch.URL == s1.URL+"/a" })
	hz.set(func(h *channelHz) { h.alert = &AlertChannel{URL: s2.URL + "/b"} })
	waitFor(t, "the second channel", func() bool { ch := a.AlertChannel(); return ch != nil && ch.URL == s2.URL+"/b" })
	hz.set(func(h *channelHz) { h.down = true })
	waitFor(t, "an alert to the second channel", func() bool { return len(second.got()) >= 1 })
	if n := len(first.got()); n != 0 {
		t.Fatalf("the replaced channel got %d messages", n)
	}
}

// A topic on this host wins over the one set in hz. hz's is still cached, so
// removing the host file later needs no second trip to hz.
func TestHostChannelWinsOverHz(t *testing.T) {
	host, fromHz := &fakeNtfy{}, &fakeNtfy{}
	hs, zs := httptest.NewServer(host), httptest.NewServer(fromHz)
	defer hs.Close()
	defer zs.Close()
	hz := &channelHz{alert: &AlertChannel{URL: zs.URL + "/hz"}}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	a := NewAgent("vps", "test", "tok", filepath.Join(t.TempDir(), "state.json"))
	p := &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2,
		Alert: &Alerter{URL: hs.URL + "/host", Vantage: "vps", Threshold: 2}}
	runLoop(t, a, p)

	waitFor(t, "hz's channel to be cached", func() bool { return a.AlertChannel() != nil })
	hz.set(func(h *channelHz) { h.down = true })
	waitFor(t, "an alert to the host's channel", func() bool { return len(host.got()) >= 1 })
	if n := len(fromHz.got()); n != 0 {
		t.Fatalf("hz's channel got %d messages while the host set its own", n)
	}
}

// The operator cleared the topic in hz: an accepted reply with no channel
// clears the cache, and the vantage stops alerting.
func TestEmptyChannelReplyClearsTheCache(t *testing.T) {
	fn := &fakeNtfy{}
	ntfy := httptest.NewServer(fn)
	defer ntfy.Close()
	state := filepath.Join(t.TempDir(), "state.json")
	seedState(t, state, &AlertChannel{URL: ntfy.URL + "/old", Token: "old-tk"})

	hz := &channelHz{} // up, no channel set
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	a := NewAgent("vps", "test", "tok", state)
	if a.AlertChannel() == nil {
		t.Fatal("the seeded channel did not load — the rest of this test proves nothing")
	}
	p := &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2}
	runLoop(t, a, p)

	waitFor(t, "the channel to be cleared", func() bool { return a.AlertChannel() == nil })
	if got := readState(t, state); got.Alert != nil {
		t.Fatalf("the state file still holds the cleared channel: %+v", got.Alert)
	}
	if got := readState(t, state); got.Targets.Version != "v1" {
		t.Fatalf("clearing the channel lost the targets: %+v", got.Targets)
	}

	hz.set(func(h *channelHz) { h.down = true })
	n0 := hz.count()
	waitFor(t, "failures past the threshold", func() bool { return hz.count() >= n0+5 })
	if n := len(fn.got()); n != 0 {
		t.Fatalf("a cleared channel still got %d messages", n)
	}
}

// Boot with hz unreachable — or refusing — from a cache written three years
// ago: the channel is used, and a FAILED reply never clears it (#5).
func TestFailedReplyKeepsTheCacheAndBootsFromIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		title  string
	}{
		{"unreachable", 0, "hz unreachable"},
		{"rejected", http.StatusUnauthorized, "hz rejects this vantage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn := &fakeNtfy{}
			ntfy := httptest.NewServer(fn)
			defer ntfy.Close()
			state := filepath.Join(t.TempDir(), "state.json")
			ch := &AlertChannel{URL: ntfy.URL + "/cached", Token: "cached-tk"}
			seedState(t, state, ch)
			old := time.Now().AddDate(-3, 0, 0)
			if err := os.Chtimes(state, old, old); err != nil {
				t.Fatal(err)
			}

			hz := &channelHz{down: true, status: tc.status}
			hzSrv := httptest.NewServer(hz)
			defer hzSrv.Close()

			a := NewAgent("vps", "test", "tok", state)
			p := &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2}
			runLoop(t, a, p)

			waitFor(t, "an alert from the cached channel", func() bool { return len(fn.got()) >= 1 })
			if m := fn.got()[0]; m.Title != tc.title || m.Auth != "Bearer cached-tk" {
				t.Fatalf("alert %+v", m)
			}
			n0 := hz.count()
			waitFor(t, "more failed reports", func() bool { return hz.count() >= n0+3 })
			if got := a.AlertChannel(); got == nil || *got != *ch {
				t.Fatalf("a failed reply changed the cached channel: %+v", got)
			}
			if got := readState(t, state).Alert; got == nil || *got != *ch {
				t.Fatalf("a failed reply changed the state file: %+v", got)
			}
		})
	}
}

// A reply carrying a channel that is not an http(s) URL does not replace the
// one held.
func TestInvalidChannelFromHzIsIgnored(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	ch := &AlertChannel{URL: "https://ntfy.example/kept"}
	seedState(t, state, ch)
	hz := &channelHz{alert: &AlertChannel{URL: "ftp://nope"}}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()

	a := NewAgent("vps", "test", "tok", state)
	runLoop(t, a, &Pusher{URL: hzSrv.URL, Token: "tok"})
	waitFor(t, "a few accepted reports", func() bool { return hz.count() >= 3 })
	if got := a.AlertChannel(); got == nil || *got != *ch {
		t.Fatalf("an invalid channel replaced the held one: %+v", got)
	}
}

// The channel is a secret end to end: set, alert, clear — none of it names
// the URL or the token in a log line.
func TestAlertChannelNeverLogged(t *testing.T) {
	logs := captureLogs(t)
	fn := &fakeNtfy{}
	ntfy := httptest.NewServer(fn)
	defer ntfy.Close()
	// A dead topic too, so the delivery-failure path logs as well.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()

	hz := &channelHz{alert: &AlertChannel{URL: ntfy.URL + "/secret-topic-name", Token: "secret-ntfy-token"}}
	hzSrv := httptest.NewServer(hz)
	defer hzSrv.Close()
	state := filepath.Join(t.TempDir(), "state.json")
	a := NewAgent("vps", "test", "tok", state)
	stop := runLoop(t, a, &Pusher{URL: hzSrv.URL, Token: "tok", AlertThreshold: 2})

	waitFor(t, "the channel", func() bool { return a.AlertChannel() != nil })
	hz.set(func(h *channelHz) { h.down = true })
	waitFor(t, "an alert", func() bool { return len(fn.got()) >= 1 })
	hz.set(func(h *channelHz) {
		h.down = false
		h.alert = &AlertChannel{URL: dead.URL + "/secret-topic-name", Token: "secret-ntfy-token"}
	})
	waitFor(t, "the dead channel", func() bool { ch := a.AlertChannel(); return ch != nil && ch.URL == dead.URL+"/secret-topic-name" })
	hz.set(func(h *channelHz) { h.down = true })
	n0 := hz.count()
	waitFor(t, "failures to the dead channel", func() bool { return hz.count() >= n0+4 })
	hz.set(func(h *channelHz) { h.down = false; h.alert = nil })
	waitFor(t, "the clear", func() bool { return a.AlertChannel() == nil })
	stop()

	// Restart from the cache too: loadState logs.
	seedState(t, state, &AlertChannel{URL: ntfy.URL + "/secret-topic-name", Token: "secret-ntfy-token"})
	_ = NewAgent("vps", "test", "tok", state)

	out := logs.String()
	if !strings.Contains(out, "hz set this vantage's alert channel") ||
		!strings.Contains(out, "hz cleared this vantage's alert channel") {
		t.Fatalf("the log lines this test inspects are missing — it proves nothing:\n%s", out)
	}
	for _, secret := range []string{"secret-topic-name", "secret-ntfy-token"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the logs name the channel (%s):\n%s", secret, out)
		}
	}
}
