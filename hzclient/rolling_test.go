package hzclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// The phase table, straight from bin/hz-client:299-316. This is the contract a
// consumer in another repository used to obtain by string-matching stdout, so
// it is worth pinning row by row.
func TestPhaseTable(t *testing.T) {
	cases := []struct {
		current, next string
		want          Phase
	}{
		{"up", "up", PhaseIdle},
		{"up", "maint", PhaseNextDown},
		{"up", "down", PhaseNextDown},
		{"up", "drain", PhaseDrainingNext},
		{"maint", "up", PhaseCurrentDown},
		{"down", "up", PhaseCurrentDown},
		{"drain", "up", PhaseDrainingCurrent},

		// Everything else is unknown, and each of these is a real situation:
		// both slots offline, a half-finished promote, and hz unable to read
		// the HAProxy socket at all.
		{"down", "down", PhaseUnknown},
		{"drain", "drain", PhaseUnknown},
		{"unknown", "up", PhaseUnknown},
		{"up", "unknown", PhaseUnknown},
		{"", "", PhaseUnknown},
	}
	for _, tc := range cases {
		st := &DeployStatus{
			Current: DeploySlotStatus{State: tc.current},
			Next:    DeploySlotStatus{State: tc.next},
		}
		got := PhaseFrom(st)
		if got.Phase != tc.want {
			t.Errorf("current=%q next=%q gave %s, want %s", tc.current, tc.next, got.Phase, tc.want)
		}
		// The evidence rides along in EVERY phase, not just the unknown one.
		if got.Current != tc.current || got.Next != tc.next {
			t.Errorf("current=%q next=%q lost the observed states: %+v", tc.current, tc.next, got)
		}
	}
}

// PhaseUnknown is the zero value, so an unset Phase can never read as a real
// one — a struct built by a caller and never filled in must not claim "idle".
func TestUnknownIsTheZeroPhase(t *testing.T) {
	var p Phase
	if p != PhaseUnknown {
		t.Fatalf("the zero Phase is %s", p)
	}
	if PhaseIdle == PhaseUnknown {
		t.Fatal("idle and unknown are the same value")
	}
}

func TestRollingPhaseReportsBothObservedStates(t *testing.T) {
	c, f := newFake(t)
	f.status.Current.State = "drain"
	f.status.Next.State = "up"

	got, err := c.RollingPhase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != PhaseDrainingCurrent {
		t.Errorf("phase = %s", got.Phase)
	}
	if got.Current != "drain" || got.Next != "up" {
		t.Errorf("observed states = %+v", got)
	}
	// String is for humans. Nothing parses it back — that is the habit this
	// type exists to break — but it must still be legible.
	if !strings.Contains(got.String(), "current=drain") {
		t.Errorf("String() = %q", got)
	}
}

// --- the three steps ------------------------------------------------------

// rollingFake applies state changes the way hz plus HAProxy would, so a whole
// rolling sequence can run. "down" and "drain" both land in maint immediately;
// slowUp delays a slot coming up by that many polls, which is what a real
// health check does.
type rollingFake struct {
	mu     sync.Mutex
	status DeployStatus
	slowUp map[SlotName]int
	calls  []string
}

func newRollingFake(t *testing.T, opts Options) (*Client, *rollingFake) {
	t.Helper()
	f := &rollingFake{status: upUpStatus(), slowUp: map[SlotName]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		hzapi.Advertise(w.Header())
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)

		if r.URL.Path == "/api/deploy/status" {
			// A slot on a delay reports "down" until its countdown runs out.
			st := f.status
			_ = json.NewEncoder(w).Encode(st)
			for slot, n := range f.slowUp {
				if n > 0 {
					f.slowUp[slot] = n - 1
					if f.slowUp[slot] == 0 {
						f.setState(slot, "up")
					}
				}
			}
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/deploy/"), "/")
		if len(parts) != 2 {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		slot, action := SlotName(parts[0]), parts[1]
		switch action {
		case "up":
			if n := f.slowUp[slot]; n > 0 {
				f.setState(slot, "down") // health checks have not passed yet
			} else {
				f.setState(slot, "up")
			}
		case "drain", "down":
			f.setState(slot, "maint")
		}
		_ = json.NewEncoder(w).Encode(DeployStateChangeResponse{Status: "ok", Server: string(slot), State: action})
	}))
	t.Cleanup(srv.Close)

	opts.BaseURL, opts.Token = srv.URL, "tok-123"
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	c.poll = time.Millisecond // keep the suite fast; the cadence is not under test
	return c, f
}

func (f *rollingFake) setState(slot SlotName, state string) {
	if slot == SlotCurrent {
		f.status.Current.State = state
	} else {
		f.status.Next.State = state
	}
}

func (f *rollingFake) phase() Phase {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	return PhaseFrom(&st).Phase
}

func TestRollingDeployRunsEndToEnd(t *testing.T) {
	c, f := newRollingFake(t, Options{})
	ctx := context.Background()

	step, err := c.RollingStart(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if step.Slot != SlotNext || step.Backend != "10.0.0.1:9002" || step.Phase != PhaseNextDown {
		t.Fatalf("start left %+v; the backend to deploy to is the point of the return value", step)
	}
	if f.phase() != PhaseNextDown {
		t.Fatalf("after start the deployment is %s", f.phase())
	}

	step, err = c.RollingContinue(ctx)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if step.Slot != SlotCurrent || step.Backend != "10.0.0.1:9001" || step.Phase != PhaseCurrentDown {
		t.Fatalf("continue left %+v", step)
	}
	if f.phase() != PhaseCurrentDown {
		t.Fatalf("after continue the deployment is %s", f.phase())
	}

	st, err := c.RollingFinalize(ctx)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if PhaseFrom(st).Phase != PhaseIdle {
		t.Fatalf("finalize left %s, want both slots up", PhaseFrom(st))
	}
}

// Continue must not touch current until next is actually serving. A deploy that
// fails its health check has to fail with traffic still on the old code.
func TestRollingContinueWaitsForHealthBeforeTouchingCurrent(t *testing.T) {
	c, f := newRollingFake(t, Options{})
	ctx := context.Background()
	if _, err := c.RollingStart(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.slowUp[SlotNext] = 3 // three polls before health checks pass
	f.calls = nil
	f.mu.Unlock()

	if _, err := c.RollingContinue(ctx); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	nextUp, currentDrain := -1, -1
	for i, call := range f.calls {
		if call == "POST /api/deploy/next/up" && nextUp < 0 {
			nextUp = i
		}
		if call == "POST /api/deploy/current/drain" && currentDrain < 0 {
			currentDrain = i
		}
	}
	if nextUp < 0 || currentDrain < 0 {
		t.Fatalf("calls: %v", f.calls)
	}
	// At least three status polls must sit between them: the health wait.
	polls := 0
	for _, call := range f.calls[nextUp:currentDrain] {
		if call == "GET /api/deploy/status" {
			polls++
		}
	}
	if polls < 3 {
		t.Errorf("only %d status polls between bringing next up and draining current: %v", polls, f.calls)
	}
}

// Each step refuses from the wrong phase, naming both the phase it needed and
// the one it found. Starting a rolling deploy from the middle of one is how
// both slots end up down.
func TestRollingStepsGuardOnPhase(t *testing.T) {
	type step struct {
		name string
		run  func(*Client, context.Context) error
		want Phase
	}
	steps := []step{
		{"rolling start", func(c *Client, ctx context.Context) error { _, err := c.RollingStart(ctx); return err }, PhaseIdle},
		{"rolling continue", func(c *Client, ctx context.Context) error { _, err := c.RollingContinue(ctx); return err }, PhaseNextDown},
		{"rolling finalize", func(c *Client, ctx context.Context) error { _, err := c.RollingFinalize(ctx); return err }, PhaseCurrentDown},
	}
	// Both slots down: a phase no step runs from.
	for _, s := range steps {
		c, f := newRollingFake(t, Options{})
		f.mu.Lock()
		f.status.Current.State, f.status.Next.State = "maint", "maint"
		f.calls = nil
		f.mu.Unlock()

		err := s.run(c, context.Background())
		var pe *PhaseError
		if !errors.As(err, &pe) {
			t.Fatalf("%s from both-down gave %v, want a *PhaseError", s.name, err)
		}
		if pe.Want != s.want {
			t.Errorf("%s says it wanted %s", s.name, pe.Want)
		}
		if pe.Got.Phase != PhaseUnknown || pe.Got.Current != "maint" {
			t.Errorf("%s did not carry what it found: %+v", s.name, pe.Got)
		}
		msg := pe.Error()
		for _, want := range []string{s.name, s.want.String(), "current=maint", "next=maint"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message %q does not contain %q", s.name, msg, want)
			}
		}
		// A refused step changes nothing.
		f.mu.Lock()
		for _, call := range f.calls {
			if strings.HasPrefix(call, "POST") {
				t.Errorf("%s refused and still called %s", s.name, call)
			}
		}
		f.mu.Unlock()
	}
}

// --- waiting --------------------------------------------------------------

// A slot already in the target state returns at once. The script slept first
// and then reported "after 1s" for a slot that had never not been ready.
func TestWaitReturnsImmediatelyWhenAlreadyThere(t *testing.T) {
	c, _ := newRollingFake(t, Options{})
	c.poll = time.Hour // any wait at all would hang the test

	var waited []time.Duration
	c.onProg = func(_ SlotName, _ string, d time.Duration) { waited = append(waited, d) }

	start := time.Now()
	state, err := c.WaitForState(context.Background(), SlotCurrent, "up")
	if err != nil {
		t.Fatal(err)
	}
	if state != "up" {
		t.Errorf("settled on %q", state)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waited %s for a slot that was already up", elapsed)
	}
	if len(waited) != 1 {
		t.Fatalf("OnProgress fired %d times on an immediate hit", len(waited))
	}
	if waited[0] > 500*time.Millisecond {
		t.Errorf("the first poll reported %s of waiting", waited[0])
	}
}

// OnProgress fires on every poll, including the first, and the elapsed time
// grows. A library must not print; this is the whole substitute.
func TestOnProgressFiresOnEveryPoll(t *testing.T) {
	c, f := newRollingFake(t, Options{})
	f.mu.Lock()
	f.status.Next.State = "down"
	f.slowUp[SlotNext] = 4
	f.mu.Unlock()

	var mu sync.Mutex
	var seen []string
	var durations []time.Duration
	c.onProg = func(slot SlotName, state string, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if slot != SlotNext {
			t.Errorf("progress reported for %s", slot)
		}
		seen = append(seen, state)
		durations = append(durations, d)
	}

	if _, err := c.WaitForState(context.Background(), SlotNext, "up"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 4 {
		t.Fatalf("OnProgress fired %d times over %d polls: %v", len(seen), 4, seen)
	}
	if seen[0] != "down" || seen[len(seen)-1] != "up" {
		t.Errorf("progress did not follow the slot: %v", seen)
	}
	for i := 1; i < len(durations); i++ {
		if durations[i] < durations[i-1] {
			t.Errorf("elapsed went backwards: %v", durations)
			break
		}
	}
}

// nil OnProgress is silent, not a panic.
func TestNilOnProgressIsSilent(t *testing.T) {
	c, _ := newRollingFake(t, Options{})
	if _, err := c.WaitForState(context.Background(), SlotCurrent, "up"); err != nil {
		t.Fatal(err)
	}
}

// The timeout error names the slot, the states it wanted, how long it waited
// and what it saw — plus the script's advice, which names the two things that
// are actually wrong when a slot will not come up.
func TestWaitTimeoutSaysWhatItWasWaitingFor(t *testing.T) {
	c, f := newRollingFake(t, Options{Timeout: 60 * time.Millisecond})
	f.mu.Lock()
	f.status.Next.State = "down"
	f.mu.Unlock()

	start := time.Now()
	_, err := c.WaitForState(context.Background(), SlotNext, "up")
	elapsed := time.Since(start)

	var te *WaitTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("got %v, want a *WaitTimeoutError", err)
	}
	if te.Slot != SlotNext || te.Last != "down" || len(te.Want) != 1 || te.Want[0] != "up" {
		t.Errorf("timeout error carried %+v", te)
	}
	if te.Backend != "10.0.0.1:9002" {
		t.Errorf("the backend that is not coming up was not named: %q", te.Backend)
	}
	if elapsed > time.Second {
		t.Errorf("a 60ms timeout took %s; the deadline is not bounding the poll loop", elapsed)
	}
	msg := te.Error()
	for _, want := range []string{"next", "up", "health check endpoint", "service is running"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
}

// A drain timeout must NOT carry the health-check advice: nothing about a slot
// refusing to drain is a health check problem.
func TestDrainTimeoutDoesNotGiveHealthCheckAdvice(t *testing.T) {
	c, f := newRollingFake(t, Options{Timeout: 40 * time.Millisecond})
	f.mu.Lock()
	f.status.Next.State = "drain" // stuck draining forever
	f.mu.Unlock()

	err := c.waitDrained(context.Background(), SlotNext)
	var te *WaitTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(te.Error(), "health check") {
		t.Errorf("a drain timeout gave health-check advice: %v", te)
	}
	if !strings.Contains(te.Error(), "maint or down") {
		t.Errorf("the message does not name the states it would have accepted: %v", te)
	}
}

// Cancellation is prompt and distinguishable from a timeout. The bash could do
// neither: its loop slept a whole second at a time and had no notion of an
// abort at all.
func TestContextCancellationIsPromptAndNotATimeout(t *testing.T) {
	c, f := newRollingFake(t, Options{Timeout: time.Hour})
	f.mu.Lock()
	f.status.Next.State = "down"
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.WaitForState(ctx, SlotNext, "up")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %s to take effect", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	var te *WaitTimeoutError
	if errors.As(err, &te) {
		t.Error("a cancellation was reported as a deployment timeout; those want different reactions")
	}
	if !strings.Contains(err.Error(), "next") {
		t.Errorf("the error does not say what was being waited for: %v", err)
	}
}

// A control plane that cannot be read is a STOP condition, not a state. The
// script's earlier version answered an unreachable hz with the string
// "unknown", which callers then compared against "up" as though it were real.
func TestAnUnreadableControlPlaneStopsTheWait(t *testing.T) {
	var fail bool
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hzapi.Advertise(w.Header())
		if fail {
			http.Error(w, "haproxy socket is gone", http.StatusInternalServerError)
			return
		}
		st := upUpStatus()
		st.Next.State = "down"
		_ = json.NewEncoder(w).Encode(st)
	}))
	defer srv.Close()

	c, err := New(Options{BaseURL: srv.URL, Token: "t", Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	c.poll = time.Millisecond
	go func() {
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		fail = true
		mu.Unlock()
	}()

	_, err = c.WaitForState(context.Background(), SlotNext, "up")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusInternalServerError {
		t.Fatalf("got %v, want the 500 to stop the wait", err)
	}
	if !strings.Contains(err.Error(), "next") {
		t.Errorf("the error does not say which slot was being waited for: %v", err)
	}
}

func TestWaitForStateNeedsATarget(t *testing.T) {
	c, _ := newRollingFake(t, Options{})
	if _, err := c.WaitForState(context.Background(), SlotCurrent); err == nil {
		t.Fatal("a wait with no target state was accepted")
	}
}
