package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// The settle time: the same differing build on every report for 15 minutes,
// and not a report sooner.
func TestUpdateWatchRaisesOnlyAfterTheSettleTime(t *testing.T) {
	var w UpdateWatch
	for m := 0; m < 15; m++ {
		if w.Observe("v1", "v2", t0.Add(time.Duration(m)*time.Minute)) {
			t.Fatalf("raised after %d minutes; the settle time is 15", m)
		}
	}
	if w.Observe("v1", "v2", t0.Add(15*time.Minute-time.Second)) {
		t.Fatal("raised a second before 15 minutes")
	}
	if !w.Observe("v1", "v2", t0.Add(15*time.Minute)) {
		t.Fatal("no flag at 15 minutes")
	}
}

// A deploy that changes again inside the settle time starts the clock over;
// a rollback to the running build stops it.
func TestUpdateWatchResetsWhenTheExpectedVersionChanges(t *testing.T) {
	var w UpdateWatch
	w.Observe("v1", "v2", t0)
	w.Observe("v1", "v3", t0.Add(10*time.Minute)) // a new deploy
	if w.Observe("v1", "v3", t0.Add(16*time.Minute)) {
		t.Fatal("raised 6 minutes after the version changed; the clock should have reset")
	}
	if !w.Observe("v1", "v3", t0.Add(25*time.Minute)) {
		t.Fatal("no flag 15 minutes after the version settled")
	}

	// Rolled back: hz holds what is running, then the bad build again.
	var r UpdateWatch
	r.Observe("v1", "v2", t0)
	r.Observe("v1", "v1", t0.Add(10*time.Minute))
	if r.Observe("v1", "v2", t0.Add(20*time.Minute)) {
		t.Fatal("a match in between must reset the clock")
	}
	// hz naming no version is not hz holding a different one.
	var e UpdateWatch
	e.Observe("v1", "v2", t0)
	e.Observe("v1", "", t0.Add(10*time.Minute))
	if e.Observe("v1", "v2", t0.Add(20*time.Minute)) {
		t.Fatal("a reply with no version must reset the clock")
	}
}

func TestUpdateWatchNeverRaisesWhenTheVersionMatches(t *testing.T) {
	var w UpdateWatch
	for m := 0; m <= 180; m += 5 {
		if w.Observe("v1", "v1", t0.Add(time.Duration(m)*time.Minute)) {
			t.Fatalf("raised at %d minutes with the versions matching", m)
		}
		if w.Observe("v1", "", t0.Add(time.Duration(m)*time.Minute)) {
			t.Fatalf("raised at %d minutes with hz naming no version", m)
		}
	}
}

// Raised once, then again only after the updater's rate limit could let a
// run act on it — not on every report in between.
func TestUpdateWatchResignalsHourly(t *testing.T) {
	var w UpdateWatch
	w.Observe("v1", "v2", t0)
	if !w.Observe("v1", "v2", t0.Add(15*time.Minute)) {
		t.Fatal("no first flag")
	}
	for m := 16; m < 75; m++ {
		if w.Observe("v1", "v2", t0.Add(time.Duration(m)*time.Minute)) {
			t.Fatalf("raised again at %d minutes, inside the hour", m)
		}
	}
	if !w.Observe("v1", "v2", t0.Add(75*time.Minute)) {
		t.Fatal("not raised again an hour after the first")
	}
}

// The flag is written by rename, holds a timestamp and nothing else, and
// leaves no temporary file behind.
func TestWriteUpdateFlagIsAtomicAndNamesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, UpdateFlagName)
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteUpdateFlag(path, t0); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("the flag was rewritten in place; it must be replaced by a rename")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != t0.Format(time.RFC3339)+"\n" {
		t.Fatalf("flag content = %q, want only a timestamp", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left %d files in the state directory, want only the flag", len(entries))
	}
}

// Through the push path: the Pusher writes the flag at 15 minutes, and what
// it writes carries neither the version nor hz's URL.
func TestPusherRaisesTheFlagWithNoVersionInIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), UpdateFlagName)
	p := &Pusher{URL: "https://hz.example.com", UpdateFlag: path}

	p.observeVersion("v1", "v2-candidate", t0)
	p.observeVersion("v1", "v2-candidate", t0.Add(14*time.Minute))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("flag present before the settle time (stat err %v)", err)
	}
	p.observeVersion("v1", "v2-candidate", t0.Add(15*time.Minute))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no flag at 15 minutes: %v", err)
	}
	for _, leak := range []string{"v2-candidate", "v1", "example.com", "http"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("the flag carries %q; it must hold a timestamp only: %q", leak, b)
		}
	}

	// Off when no path is set.
	off := &Pusher{}
	off.observeVersion("v1", "v2", t0)
	off.observeVersion("v1", "v2", t0.Add(time.Hour))
}
