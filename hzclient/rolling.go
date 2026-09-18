package hzclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The rolling deploy: three steps, with a human deploying code between them.
//
// # Why the phase is a typed value
//
// The server holds NO phase state. There is nothing to call: the phase is
// inferred from the two slot states in a single GET /api/deploy/status, and the
// script did that inference in bash and printed the answer as English.
//
// The one real consumer then read it back with CutPrefix(line, "Rolling
// phase:"), in another repository, returning "" on no match — which its caller
// treated as an unknown phase. Reword that line in hz and a fleet's deploys
// break silently, in exactly the shape of the bans bug: a contract nobody
// declared, drifting past everyone.
//
// So the phase is a constant here, the two observed states ride along with it
// rather than being formatted into a sentence, and the parse function on the
// other side is meant to be DELETED rather than ported.

// pollInterval is how often a wait re-reads the status, matching the script's
// one-second loop. HAProxy probes every 3s, so polling faster buys nothing.
const pollInterval = time.Second

// Phase is where a rolling deploy has got to, inferred from the two slot
// states. PhaseUnknown is the zero value, so an unset Phase is never mistaken
// for a real one.
type Phase int

const (
	// PhaseUnknown means the two states are not a combination the rolling
	// machine produces. It is not an error: a slot taken down by hand, a
	// half-finished promote, or hz being unable to read the HAProxy socket all
	// land here. RollingStatus carries both observed states so an operator can
	// see which.
	PhaseUnknown Phase = iota

	// PhaseIdle is both slots up. A rolling deploy starts here.
	PhaseIdle

	// PhaseNextDown is current up, next down: deploy to the next backend, then
	// RollingContinue.
	PhaseNextDown

	// PhaseDrainingNext is current up, next draining: RollingStart is still
	// waiting for connections to finish.
	PhaseDrainingNext

	// PhaseCurrentDown is next up, current down: deploy to the current
	// backend, then RollingFinalize.
	PhaseCurrentDown

	// PhaseDrainingCurrent is next up, current draining: RollingContinue is
	// still waiting for connections to finish.
	PhaseDrainingCurrent
)

// String is for display only. Nothing in this package parses it back, and
// nothing outside it should either — that is the habit this type exists to
// break.
func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseNextDown:
		return "next-down"
	case PhaseDrainingNext:
		return "draining-next"
	case PhaseCurrentDown:
		return "current-down"
	case PhaseDrainingCurrent:
		return "draining-current"
	}
	return "unknown"
}

// RollingStatus is a phase plus the evidence for it.
//
// Current and Next are HAProxy's own words for the two slots — "up", "down",
// "drain", "maint" — or "unknown" when hz could not read the socket. They ride
// along in EVERY phase, not just PhaseUnknown, because the answer to "why does
// it say that" is always these two strings.
type RollingStatus struct {
	Phase   Phase
	Current string
	Next    string
}

func (s RollingStatus) String() string {
	return fmt.Sprintf("%s (current=%s, next=%s)", s.Phase, s.Current, s.Next)
}

// PhaseFrom infers the phase from a deploy status already in hand.
//
// This is the whole state machine, and it reproduces bin/hz-client's table
// exactly. "down" and "maint" are one case: asking hz to take a slot "down"
// sets HAProxy's maint state, so the observed word depends on how the slot got
// there and not on where the deploy is.
func PhaseFrom(st *DeployStatus) RollingStatus {
	cur, next := st.Current.State, st.Next.State
	out := RollingStatus{Phase: PhaseUnknown, Current: cur, Next: next}
	switch {
	case cur == "up" && next == "up":
		out.Phase = PhaseIdle
	case cur == "up" && isDown(next):
		out.Phase = PhaseNextDown
	case cur == "up" && next == "drain":
		out.Phase = PhaseDrainingNext
	case isDown(cur) && next == "up":
		out.Phase = PhaseCurrentDown
	case cur == "drain" && next == "up":
		out.Phase = PhaseDrainingCurrent
	}
	return out
}

// isDown is the drained-or-offline test. See PhaseFrom on why the two words
// are one case.
func isDown(state string) bool { return state == "maint" || state == "down" }

// RollingPhase reports where a rolling deploy has got to, in one request.
func (c *Client) RollingPhase(ctx context.Context) (RollingStatus, error) {
	st, err := c.DeployStatus(ctx)
	if err != nil {
		return RollingStatus{}, err
	}
	return PhaseFrom(st), nil
}

// PhaseError is a rolling step refusing to run from the wrong place. It names
// both the phase it needed and the one it found, with the observed states, so a
// caller never has to re-read the status to understand the refusal.
type PhaseError struct {
	Op   string // the step that refused, e.g. "rolling start"
	Want Phase
	Got  RollingStatus
}

func (e *PhaseError) Error() string {
	return fmt.Sprintf("%s: needs phase %s, found %s", e.Op, e.Want, e.Got)
}

// WaitTimeoutError is a slot that did not reach the state it was asked for
// within the client's Timeout.
//
// It is deliberately distinct from a context deadline: this one means the
// DEPLOYMENT is not behaving, and the caller's context being cancelled means
// the operator or the surrounding program gave up. Those want different
// reactions, so they are different errors.
type WaitTimeoutError struct {
	Slot    SlotName
	Want    []string // the states that would have satisfied the wait
	Last    string   // the state actually observed last
	Waited  time.Duration
	Backend string // the backend address behind the slot, when known
}

func (e *WaitTimeoutError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s slot did not reach %s within %s (last seen %q)",
		e.Slot, strings.Join(e.Want, " or "), e.Waited.Round(time.Second), e.Last)
	if e.Backend != "" {
		fmt.Fprintf(&b, ", backend %s", e.Backend)
	}
	// The script's advice, kept because it names the two things that are
	// actually wrong when a slot will not come up.
	for _, w := range e.Want {
		if w == "up" {
			b.WriteString("; check that the service is running and responding to the health check endpoint")
			break
		}
	}
	return b.String()
}

// WaitForState polls the deploy status until a slot reads one of want, and
// returns the state it settled on.
//
// The first poll happens IMMEDIATELY. A slot already in the target state
// returns at once, having waited nothing — the script's loop slept first and
// then reported "after 1s" for a slot that had never not been ready.
//
// Options.OnProgress is called on every poll including that first one, with the
// state observed and how long the wait has run so far. A library must not
// print; what that becomes is the caller's decision.
//
// Three ways to fail, and they are distinguishable on purpose:
//   - *WaitTimeoutError — the slot did not get there in Options.Timeout.
//   - ctx.Err(), wrapped — the caller gave up. errors.Is finds
//     context.Canceled or context.DeadlineExceeded.
//   - anything from DeployStatus — hz could not be read. That is a STOP
//     condition, not a state: the script's earlier version answered an
//     unreachable control plane with the string "unknown", which callers then
//     compared against "up" as though it were real, so a network fault was
//     indistinguishable from a slot genuinely being odd.
func (c *Client) WaitForState(ctx context.Context, slot SlotName, want ...string) (string, error) {
	if len(want) == 0 {
		return "", errors.New("hzclient: WaitForState needs at least one state to wait for")
	}
	start := time.Now()
	deadline := time.NewTimer(c.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()

	var last, backend string
	for {
		st, err := c.DeployStatus(ctx)
		if err != nil {
			return last, fmt.Errorf("waiting for the %s slot: %w", slot, err)
		}
		s := st.Slot(slot)
		last, backend = s.State, s.Backend
		if c.onProg != nil {
			c.onProg(slot, s.State, time.Since(start))
		}
		for _, w := range want {
			if s.State == w {
				return s.State, nil
			}
		}

		select {
		case <-ctx.Done():
			return last, fmt.Errorf("waiting for the %s slot to be %s: %w",
				slot, strings.Join(want, " or "), ctx.Err())
		case <-deadline.C:
			return last, &WaitTimeoutError{
				Slot: slot, Want: want, Last: last,
				Waited: time.Since(start), Backend: backend,
			}
		case <-ticker.C:
		}
	}
}

// waitDrained waits for a slot to stop taking connections. Both words mean
// that: see PhaseFrom.
func (c *Client) waitDrained(ctx context.Context, slot SlotName) error {
	_, err := c.WaitForState(ctx, slot, "maint", "down")
	return err
}

// waitHealthy waits for a slot's health checks to pass. HAProxy probes every 3s
// with fall=2/rise=2, so this is about six seconds at best.
func (c *Client) waitHealthy(ctx context.Context, slot SlotName) error {
	_, err := c.WaitForState(ctx, slot, "up")
	return err
}

// RollingStep is what a rolling step leaves behind: the slot that is now
// offline, the backend address to deploy code to, and the phase the deployment
// has moved into.
type RollingStep struct {
	Slot    SlotName
	Backend string
	Phase   Phase
}

// RollingStart is phase 1: drain the next slot and take it down, leaving
// current serving all traffic.
//
// It requires PhaseIdle — both slots up — and refuses with a *PhaseError
// otherwise, because starting a rolling deploy from the middle of one is how
// both slots end up down.
//
// Deploy your code to the returned Backend, then call RollingContinue.
func (c *Client) RollingStart(ctx context.Context) (*RollingStep, error) {
	st, err := c.guard(ctx, "rolling start", PhaseIdle)
	if err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotNext, StateDrain); err != nil {
		return nil, err
	}
	if err := c.waitDrained(ctx, SlotNext); err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotNext, StateDown); err != nil {
		return nil, err
	}
	return &RollingStep{Slot: SlotNext, Backend: st.Next.Backend, Phase: PhaseNextDown}, nil
}

// RollingContinue is phase 2: bring the freshly deployed next slot up, wait for
// it to pass health checks, then drain and down the current slot.
//
// It requires PhaseNextDown. The health wait is the gate that matters: current
// is not touched until next is actually serving, so a bad deploy fails here
// with both slots' traffic still on the old code.
//
// Deploy your code to the returned Backend, then call RollingFinalize.
func (c *Client) RollingContinue(ctx context.Context) (*RollingStep, error) {
	st, err := c.guard(ctx, "rolling continue", PhaseNextDown)
	if err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotNext, StateUp); err != nil {
		return nil, err
	}
	if err := c.waitHealthy(ctx, SlotNext); err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotCurrent, StateDrain); err != nil {
		return nil, err
	}
	if err := c.waitDrained(ctx, SlotCurrent); err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotCurrent, StateDown); err != nil {
		return nil, err
	}
	return &RollingStep{Slot: SlotCurrent, Backend: st.Current.Backend, Phase: PhaseCurrentDown}, nil
}

// RollingFinalize is phase 3: bring the current slot back up and wait for it to
// pass health checks. Both slots are then on the new code.
//
// It requires PhaseCurrentDown, and returns the deploy status as it stands
// afterwards.
func (c *Client) RollingFinalize(ctx context.Context) (*DeployStatus, error) {
	if _, err := c.guard(ctx, "rolling finalize", PhaseCurrentDown); err != nil {
		return nil, err
	}
	if _, err := c.SetSlotState(ctx, SlotCurrent, StateUp); err != nil {
		return nil, err
	}
	if err := c.waitHealthy(ctx, SlotCurrent); err != nil {
		return nil, err
	}
	return c.DeployStatus(ctx)
}

// guard fetches the status once and refuses unless the phase is the one this
// step runs from. It returns that same status so the step does not fetch twice
// for a backend address the answer already held.
func (c *Client) guard(ctx context.Context, op string, want Phase) (*DeployStatus, error) {
	st, err := c.DeployStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if got := PhaseFrom(st); got.Phase != want {
		return nil, &PhaseError{Op: op, Want: want, Got: got}
	}
	return st, nil
}
