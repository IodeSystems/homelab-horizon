package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Update on report: the agent's half.
//
// Every accepted report tells the agent which build hz holds. The agent
// cannot act on that — it runs unprivileged and must never be told what to
// do — so all it may do is raise a flag: a file, update-wanted, in its own
// state directory. A root path unit sees the file and starts the same
// updater the daily timer runs, which asks hz for itself. The flag carries a
// timestamp and nothing else: no version, no URL, no command. The updater
// ignores its contents apart from its existence.
//
// The settle time is the operator's decision (2026-10-02): a deploy rolled
// back within it never reaches a vantage outside the network.

// UpdateSettle is how long the agent must see the SAME differing version, on
// every accepted report, before it raises the flag.
var UpdateSettle = 15 * time.Minute

// UpdateResignal is how long after raising the flag the agent raises it
// again for the same version. Equal to the updater's rate limit, so a flag
// the updater dropped as rate-limited is raised again once a run can act on
// it, and not every report in between.
var UpdateResignal = time.Hour

// UpdateFlagName is the flag's file name inside the agent's state directory.
const UpdateFlagName = "update-wanted"

// UpdateWatch decides when to raise the flag. Pure: a function of what the
// reports said and the clock it is handed. The zero value is ready.
type UpdateWatch struct {
	expected  string    // the differing version being timed; "" = none
	since     time.Time // first accepted report that carried it
	signalled time.Time // last time the flag was raised for it; zero = not yet
}

// Observe records one ACCEPTED report: the agent's own version, the one hz
// said it holds, and when. It returns true when the flag should be raised
// now.
//
// The clock resets when hz's version matches the agent's, when it changes,
// and when hz names none — "hz said nothing" is not "hz holds something
// else" (CLAUDE.md #2). A failed report is not an observation; the caller
// does not call Observe for it, so it neither resets nor advances the clock.
func (w *UpdateWatch) Observe(running, expected string, now time.Time) bool {
	if expected == "" || expected == running {
		*w = UpdateWatch{}
		return false
	}
	if expected != w.expected {
		*w = UpdateWatch{expected: expected, since: now}
	}
	if now.Sub(w.since) < UpdateSettle {
		return false
	}
	if !w.signalled.IsZero() && now.Sub(w.signalled) < UpdateResignal {
		return false
	}
	w.signalled = now
	return true
}

// Expected is the differing version being timed, and since when; "" when
// none is.
func (w *UpdateWatch) Expected() (string, time.Time) { return w.expected, w.since }

// WriteUpdateFlag raises the flag at path atomically: a temporary file in the
// same directory, renamed over it, so the path unit never sees a partial
// file. The content is a timestamp only.
func WriteUpdateFlag(path string, now time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "."+UpdateFlagName+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op once renamed
	if _, err := f.WriteString(now.UTC().Format(time.RFC3339) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
