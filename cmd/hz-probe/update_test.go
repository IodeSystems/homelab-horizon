package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now0 = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// fakeUpdate is an updater with every effect recorded instead of performed.
type fakeUpdate struct {
	*updater
	asked, fetched, installed int
	out                       *bytes.Buffer
}

// newFakeUpdate: installed v1, hz holds want, and the download reports
// served. The flag is raised, as the agent would leave it.
func newFakeUpdate(t *testing.T, want, served string) *fakeUpdate {
	t.Helper()
	dir := t.TempDir()
	flag := filepath.Join(dir, "state", "update-wanted")
	if err := os.MkdirAll(filepath.Dir(flag), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, []byte(now0.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdate{out: &bytes.Buffer{}}
	f.updater = &updater{
		have:      "v1",
		flagPath:  flag,
		stampPath: filepath.Join(dir, "root", "last-attempt"),
		tmpPath:   filepath.Join(dir, "hz-probe.new"),
		root:      true,
		now:       func() time.Time { return now0 },
		out:       f.out,
		ask:       func() (string, error) { f.asked++; return want, nil },
		fetch: func(p string) error {
			f.fetched++
			return os.WriteFile(p, []byte("bin"), 0o755)
		},
		versionOf: func(string) (string, error) { return served, nil },
		install:   func(string) error { f.installed++; return nil },
	}
	return f
}

func (f *fakeUpdate) flagExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(f.flagPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestUpdateRunsAndStampsTheAttempt(t *testing.T) {
	f := newFakeUpdate(t, "v2", "v2")
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if f.installed != 1 {
		t.Fatalf("installed %d times, want 1", f.installed)
	}
	last, err := readStamp(f.stampPath)
	if err != nil || !last.Equal(now0) {
		t.Fatalf("stamp = %v, %v; want the attempt recorded at %v", last, err, now0)
	}
}

// Inside the hour the run does nothing — does not even ask hz — and exits 0.
func TestUpdateInsideTheHourIsANoOp(t *testing.T) {
	f := newFakeUpdate(t, "v2", "v2")
	if err := writeStamp(f.stampPath, now0.Add(-59*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err != nil {
		t.Fatalf("a rate-limited run must exit 0, got %v", err)
	}
	if f.asked != 0 || f.fetched != 0 || f.installed != 0 {
		t.Fatalf("asked %d, fetched %d, installed %d; a rate-limited run does nothing",
			f.asked, f.fetched, f.installed)
	}
	if !strings.Contains(f.out.String(), "within the hourly limit") {
		t.Fatalf("no log line for the rate limit: %q", f.out.String())
	}

	// An hour on, it acts.
	g := newFakeUpdate(t, "v2", "v2")
	if err := writeStamp(g.stampPath, now0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := g.run(); err != nil || g.installed != 1 {
		t.Fatalf("an hour after the last attempt: err %v, installed %d", err, g.installed)
	}
}

// The path unit fires while the flag exists, so every run removes it:
// success, current, rate-limited, refused. Otherwise it loops.
func TestUpdateRemovesTheFlagOnEveryRun(t *testing.T) {
	cases := map[string]func(*testing.T) *fakeUpdate{
		"updated": func(t *testing.T) *fakeUpdate { return newFakeUpdate(t, "v2", "v2") },
		"current": func(t *testing.T) *fakeUpdate { return newFakeUpdate(t, "v1", "v1") },
		"rate-limited": func(t *testing.T) *fakeUpdate {
			f := newFakeUpdate(t, "v2", "v2")
			if err := writeStamp(f.stampPath, now0.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			return f
		},
		"served the wrong build": func(t *testing.T) *fakeUpdate { return newFakeUpdate(t, "v2", "v3") },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			f := mk(t)
			_ = f.run()
			if f.flagExists(t) {
				t.Fatal("the flag survived the run; the path unit would start the updater again at once")
			}
		})
	}

	// A dry run changes nothing, the flag included.
	d := newFakeUpdate(t, "v2", "v2")
	d.dryRun = true
	if err := d.run(); err != nil {
		t.Fatal(err)
	}
	if !d.flagExists(t) {
		t.Fatal("a dry run removed the flag")
	}
}

// hz named one build and served another: LOUD, nothing installed, and the
// stamp holds the next attempt off for the hour.
func TestUpdateRefusesABuildThatIsNotTheOneHzNamed(t *testing.T) {
	f := newFakeUpdate(t, "v2", "v3")
	err := f.run()
	if err == nil || !strings.Contains(err.Error(), "LOUD") || !strings.Contains(err.Error(), "v3") {
		t.Fatalf("err = %v, want a LOUD refusal naming what was served", err)
	}
	if f.installed != 0 {
		t.Fatal("installed a build that is not the version hz named")
	}
	if _, err := os.Stat(f.tmpPath); !os.IsNotExist(err) {
		t.Fatal("the refused download was left on disk")
	}

	// Raised again by the agent ten minutes later: not retried.
	f.now = func() time.Time { return now0.Add(10 * time.Minute) }
	if err := os.WriteFile(f.flagPath, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err != nil {
		t.Fatalf("the retry inside the hour should be a no-op, got %v", err)
	}
	if f.fetched != 1 {
		t.Fatalf("fetched %d times; a refused build must not be fetched again inside the hour", f.fetched)
	}
}

// A stamp that cannot be read is not a missing one.
func TestUpdateUnreadableStampDoesNothing(t *testing.T) {
	f := newFakeUpdate(t, "v2", "v2")
	if err := os.MkdirAll(filepath.Dir(f.stampPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.stampPath, []byte("not a time\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err == nil || !strings.Contains(err.Error(), "LOUD") {
		t.Fatalf("err = %v, want LOUD", err)
	}
	if f.asked != 0 || f.installed != 0 {
		t.Fatal("acted on an unreadable rate-limit stamp")
	}
}

// A stamp from the future is a clock that was wrong; it must not block
// updates for as long as it was wrong.
func TestUpdateIgnoresAStampFromTheFuture(t *testing.T) {
	f := newFakeUpdate(t, "v2", "v2")
	if err := writeStamp(f.stampPath, now0.Add(365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err != nil || f.installed != 1 {
		t.Fatalf("err %v, installed %d", err, f.installed)
	}
}

func TestParseVersionLine(t *testing.T) {
	got, err := parseVersionLine("hz-probe v1.2.3 (built 2026-10-02T00:00:00Z)\n")
	if err != nil || got != "v1.2.3" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseVersionLine("something else"); err == nil {
		t.Fatal("accepted output that is not hz-probe's")
	}
}
