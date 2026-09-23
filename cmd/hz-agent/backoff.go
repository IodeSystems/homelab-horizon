package main

import "time"

// maxApplyBackoff caps the hold. It exists because the agent still has to
// notice a box that was fixed by hand, and an uncapped doubling reaches hours
// — long enough that an operator watching the journal concludes the agent is
// dead rather than waiting.
const maxApplyBackoff = 5 * time.Minute

// applyHold is how long to stop TRYING to apply after an apply failed.
//
// It holds off the apply and NOTHING ELSE. The poll keeps its cadence and the
// report keeps going out, deliberately: a box that cannot apply has to keep
// telling hz so. If the backoff paced the whole loop instead, a box stuck
// failing would stop reporting and hz would render it SILENT — a different
// state, with a different cause and a different fix, and exactly the kind of
// conflation this repo's founding bug was.
//
// The hold exists because the failures worth backing off from do not clear on
// their own. A unit whose ExecStart is wrong fails identically every time; at
// the 5s poll interval that is seventeen thousand restart attempts a day
// against systemd, and a journal in which nothing else can be read.
//
// Two things clear it, and the second is the one that matters:
//
//   - an apply that succeeds, and
//   - a DIFFERENT payload arriving.
//
// The second is what keeps the hold from punishing the fix. A failed apply
// does not advance the ETag, so the next poll re-fetches the whole payload;
// comparing its fingerprint tells a repeat of the same broken desired state
// from an operator having just published a corrected one. A fix must never
// wait out a backoff earned by the version it replaces.
type applyHold struct {
	// fails counts CONSECUTIVE failures on payload. It is not a total: the
	// count is what sets the delay, so a count that survived a success would
	// hold off an apply that has no reason to be held.
	fails int

	// payload is the fingerprint that failed. Empty means nothing is held.
	payload string

	// until is when an apply may be attempted again.
	until time.Time
}

// remaining is how much longer an apply of this payload is held off, or zero
// if it may be attempted now.
//
// A payload that does not match the held one is never held, however recent the
// failure: it is new desired state and has not failed yet. Returning the
// duration rather than a bool is so the log line can say when the next attempt
// is, which is the difference between "the agent is stuck" and "the agent is
// waiting, here is until when".
func (h *applyHold) remaining(payload string, now time.Time) time.Duration {
	if h.payload == "" || h.payload != payload {
		return 0
	}
	if !now.Before(h.until) {
		return 0
	}
	return h.until.Sub(now)
}

// fail records a failed apply and returns how long the hold now runs for.
//
// The first failure does not back off — it waits the plain interval, because
// one failure is as likely to be a reload racing a write as a broken unit, and
// escalating immediately would slow down the common recoverable case. The
// doubling starts on the second consecutive failure of the same payload.
func (h *applyHold) fail(payload string, now time.Time, interval time.Duration) time.Duration {
	if h.payload != payload {
		h.payload = payload
		h.fails = 0
	}
	h.fails++
	d := backoffFor(h.fails, interval)
	h.until = now.Add(d)
	return d
}

// clear forgets the hold. Called on a successful apply.
func (h *applyHold) clear() {
	h.fails = 0
	h.payload = ""
	h.until = time.Time{}
}

// backoffFor is the delay after n consecutive failures at this poll interval.
// n <= 1 is the interval itself; after that it doubles to the cap.
//
// It doubles in a loop rather than by shifting so that a large n cannot
// overflow into a negative duration — a backoff that wrapped would apply
// immediately and forever, which is the bug this function exists to prevent.
func backoffFor(fails int, interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = defaultInterval
	}
	d := interval
	for i := 1; i < fails; i++ {
		if d >= maxApplyBackoff {
			return maxApplyBackoff
		}
		d *= 2
	}
	if d > maxApplyBackoff {
		return maxApplyBackoff
	}
	return d
}
