package main

import (
	"testing"
	"time"
)

const gen = "aaaa1111"

func TestTheFirstFailureDoesNotBackOff(t *testing.T) {
	var h applyHold
	now := time.Now()
	// One failure is as likely to be a reload racing a write as a broken
	// unit. Escalating immediately would slow the common recoverable case.
	if got := h.fail(gen, now, 5*time.Second); got != 5*time.Second {
		t.Fatalf("first failure waits %v, want the plain interval 5s", got)
	}
}

func TestConsecutiveFailuresDoubleAndCap(t *testing.T) {
	var h applyHold
	now := time.Now()
	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		160 * time.Second,
		maxApplyBackoff, // 320s would exceed the 5m cap
		maxApplyBackoff,
		maxApplyBackoff,
	}
	for i, w := range want {
		if got := h.fail(gen, now, 5*time.Second); got != w {
			t.Fatalf("failure %d waited %v, want %v", i+1, got, w)
		}
	}
}

func TestASuccessfulApplyClearsTheHold(t *testing.T) {
	var h applyHold
	now := time.Now()
	h.fail(gen, now, 5*time.Second)
	h.fail(gen, now, 5*time.Second)
	h.clear()

	if got := h.remaining(gen, now); got != 0 {
		t.Fatalf("still held for %v after a success, want 0", got)
	}
	// And the count restarts, rather than resuming where it left off.
	if got := h.fail(gen, now, 5*time.Second); got != 5*time.Second {
		t.Fatalf("after a success the next failure waited %v, want 5s", got)
	}
}

// clear must leave the struct ZERO, not merely harmless.
//
// This assertion exists because of what a positive control found: deleting
// `h.fails = 0` from clear() broke nothing, since fail() resets the count
// anyway when the payload differs from the held one (and clear blanks the
// payload). That makes the reset redundant TODAY and load-bearing the moment
// anyone changes fail()'s mismatch branch — a stale count would then leak
// across a success and hold off an apply that has no reason to be held. So the
// field is checked directly rather than through behaviour that hides it.
func TestClearLeavesNoStaleState(t *testing.T) {
	var h applyHold
	h.fail(gen, time.Now(), 5*time.Second)
	h.fail(gen, time.Now(), 5*time.Second)
	h.clear()

	if h != (applyHold{}) {
		t.Fatalf("clear() left %+v, want the zero value", h)
	}
}

// This is the test the whole type exists for. A failed apply does not advance
// the ETag, so the next poll re-fetches the payload; if the operator has
// published a fix in the meantime, it must be tried AT ONCE rather than
// waiting out a backoff the broken version earned.
func TestANewPayloadIsNeverHeldByTheOldOnesBackoff(t *testing.T) {
	var h applyHold
	now := time.Now()
	for i := 0; i < 8; i++ {
		h.fail(gen, now, 5*time.Second)
	}
	if got := h.remaining(gen, now); got == 0 {
		t.Fatal("the failing payload should still be held")
	}
	if got := h.remaining("bbbb2222", now); got != 0 {
		t.Fatalf("a DIFFERENT payload was held for %v; a fix must not wait out the backoff its broken predecessor earned", got)
	}
	// And failing on the new one starts its own clock rather than inheriting.
	if got := h.fail("bbbb2222", now, 5*time.Second); got != 5*time.Second {
		t.Fatalf("first failure of a new payload waited %v, want 5s", got)
	}
}

func TestTheHoldExpires(t *testing.T) {
	var h applyHold
	now := time.Now()
	h.fail(gen, now, 5*time.Second)

	if got := h.remaining(gen, now.Add(4*time.Second)); got != time.Second {
		t.Fatalf("remaining at t+4s = %v, want 1s", got)
	}
	if got := h.remaining(gen, now.Add(5*time.Second)); got != 0 {
		t.Fatalf("still held at the deadline (%v); the agent must retry once the hold is up", got)
	}
	if got := h.remaining(gen, now.Add(time.Hour)); got != 0 {
		t.Fatalf("still held an hour later (%v)", got)
	}
}

func TestNothingIsHeldBeforeAnyFailure(t *testing.T) {
	var h applyHold
	if got := h.remaining(gen, time.Now()); got != 0 {
		t.Fatalf("a fresh hold blocked for %v, want 0", got)
	}
}

// A backoff that overflowed would wrap negative, read as "not held", and apply
// on every pass forever — the exact hot loop this is meant to stop.
func TestTheBackoffNeverOverflows(t *testing.T) {
	for _, fails := range []int{63, 64, 65, 1000, 1 << 20} {
		if got := backoffFor(fails, 5*time.Second); got != maxApplyBackoff {
			t.Fatalf("backoffFor(%d) = %v, want the cap %v", fails, got, maxApplyBackoff)
		}
	}
}

func TestAZeroIntervalFallsBackToTheDefault(t *testing.T) {
	// --interval 0 would otherwise make every backoff zero, which is no
	// backoff at all.
	if got := backoffFor(1, 0); got != defaultInterval {
		t.Fatalf("backoffFor(1, 0) = %v, want %v", got, defaultInterval)
	}
	if got := backoffFor(3, 0); got != 4*defaultInterval {
		t.Fatalf("backoffFor(3, 0) = %v, want %v", got, 4*defaultInterval)
	}
}
