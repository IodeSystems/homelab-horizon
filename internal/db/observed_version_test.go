package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// The observed half of plan/architecture.md's desired/observed split, added in
// 0011. It is worth nothing unless it is actually written down, and until 0011
// it was not: a box reported its version on every register and every resolve
// and hz kept neither.

// The whole round trip, and — just as importantly — what must NOT move.
// Version is the version an admin reviewed when they blessed the tuple, so an
// upgrade may not rewrite it.
func TestObservedVersionRoundTripsAndLeavesTheReviewedVersionAlone(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-observed", "prod", testPublicKey(11))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}

	// A register is itself a report: the box says what it is running now.
	reg, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "app", "1.2.0")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if reg.ObservedVersion != "1.2.0" {
		t.Fatalf("observed after the first register = %q, want 1.2.0", reg.ObservedVersion)
	}
	if reg.ObservedAt == nil {
		t.Fatal("observed_at not stamped by a register")
	}

	// Then the resolve path, which is the one a running box repeats. A build
	// string that is emphatically NOT semver must store without complaint:
	// nothing compares it, and parseVersion's range test is a different job.
	const build = "v1.0.0-rc.1-1377-g406804d5"
	if err := d.RecordObservedVersion(ctx, reg.ID, "1.3.1", build); err != nil {
		t.Fatalf("record observed: %v", err)
	}
	got, err := d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ObservedVersion != "1.3.1" || got.ObservedBuild != build {
		t.Fatalf("observed = %q/%q, want 1.3.1/%s", got.ObservedVersion, got.ObservedBuild, build)
	}
	if got.ObservedAt == nil {
		t.Fatal("observed_at not stamped by a resolve")
	}
	// THE POINT OF TWO COLUMNS. The reviewed version is evidence about the
	// moment an admin approved this tuple; an upgrade must not edit it.
	if got.Version != "1.2.0" {
		t.Fatalf("the reviewed version moved to %q; an upgrade rewrote what was blessed", got.Version)
	}

	// A later register reports again, and blanks the build it no longer
	// carries rather than pairing an old build with a new version.
	again, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "app", "1.4.0")
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if again.ID != reg.ID {
		t.Fatal("the second register created a new row instead of updating the tuple")
	}
	if again.ObservedVersion != "1.4.0" || again.ObservedBuild != "" {
		t.Fatalf("after re-register observed = %q/%q, want 1.4.0 and no build",
			again.ObservedVersion, again.ObservedBuild)
	}
	if again.Version != "1.2.0" {
		t.Fatalf("the reviewed version moved to %q on a re-register", again.Version)
	}
}

// A client too old to report anything is not a failure. It is the state every
// registration was in before 0011, and recording "it said nothing" by blanking
// what it said last week would be strictly worse than leaving the last report
// standing.
func TestObservedVersionIsAbsentNotWrongWhenNothingIsReported(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-quiet", "prod", testPublicKey(12))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	reg, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "app", "1.0.0")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// No version at all: a no-op, and explicitly not an error.
	if err := d.RecordObservedVersion(ctx, reg.ID, "", ""); err != nil {
		t.Fatalf("an empty report must be a no-op, got %v", err)
	}
	got, err := d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ObservedVersion != "1.0.0" {
		t.Fatalf("a silent report overwrote the last real one: %q", got.ObservedVersion)
	}

	// A version with no build is the ordinary shape of a client that predates
	// the build string; the empty string must not be stored as if it had been
	// reported.
	if err := d.RecordObservedVersion(ctx, reg.ID, "1.1.0", ""); err != nil {
		t.Fatalf("record without a build: %v", err)
	}
	got, err = d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ObservedVersion != "1.1.0" || got.ObservedBuild != "" {
		t.Fatalf("observed = %q/%q, want 1.1.0 and no build", got.ObservedVersion, got.ObservedBuild)
	}
	var build any
	if err := d.QueryRowContext(ctx,
		`SELECT observed_build FROM cm_registrations WHERE id = ?`, reg.ID).Scan(&build); err != nil {
		t.Fatalf("read the raw column: %v", err)
	}
	if build != nil {
		t.Fatalf("observed_build = %v, want NULL: never-reported and reported-empty must differ", build)
	}

	if err := d.RecordObservedVersion(ctx, "reg_nonexistent", "1.1.0", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recording against an unknown registration = %v, want ErrNotFound", err)
	}
}

// Two instances on ONE box running different versions — not an edge case but
// the entire middle of a rolling deploy. This is why the columns are on the
// registration and not on the machine: a per-machine column would have to elect
// one of these and would report the rollout as finished halfway through.
func TestObservedVersionIsPerInstanceNotPerMachine(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-slots", "prod", testPublicKey(13))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	current, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "current", "1.2.0")
	if err != nil {
		t.Fatalf("register current: %v", err)
	}
	next, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "next", "1.2.0")
	if err != nil {
		t.Fatalf("register next: %v", err)
	}
	if current.ID == next.ID {
		t.Fatal("two roles collapsed into one registration")
	}
	if err := d.RecordObservedVersion(ctx, next.ID, "1.3.0", ""); err != nil {
		t.Fatalf("record next: %v", err)
	}

	regs, err := d.ListRegistrationsForMachine(ctx, m.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]string{}
	for _, r := range regs {
		seen[r.Role] = r.ObservedVersion
	}
	if seen["current"] != "1.2.0" || seen["next"] != "1.3.0" {
		t.Fatalf("observed per role = %v, want current 1.2.0 and next 1.3.0", seen)
	}
}

// Down and back up again, against data. 0011's loss is meant to be bounded —
// the columns are a cache of what boxes report — so the pair has to be
// reversible in fact rather than in intent.
func TestMigrate0011DownAndUpAgain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-0011", "prod", testPublicKey(14))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	reg := registerAt(t, ctx, d, m, "prod", "redline", "app", admin.ID)
	if err := d.RecordObservedVersion(ctx, reg.ID, "1.5.0", "v1.5.0-12-gdeadbee"); err != nil {
		t.Fatalf("record observed: %v", err)
	}

	if err := d.migrateTo(ctx, 10); err != nil {
		t.Fatalf("migrate down to 0010: %v", err)
	}
	var cols int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('cm_registrations') WHERE name LIKE 'observed_%'`,
	).Scan(&cols); err != nil {
		t.Fatalf("inspect 0010 columns: %v", err)
	}
	if cols != 0 {
		t.Fatalf("0010 still has %d observed_ columns; the down migration did not drop them", cols)
	}
	// The row itself survives: only the observation went.
	var version string
	if err := d.QueryRowContext(ctx,
		`SELECT version FROM cm_registrations WHERE id = ?`, reg.ID).Scan(&version); err != nil {
		t.Fatalf("read 0010 registration: %v", err)
	}
	if version != "1.0.0" {
		t.Fatalf("the reviewed version did not survive the way down: %q", version)
	}

	// Back up to the CURRENT schema rather than to 11: every accessor below is
	// compiled against the newest migration, so a hardcoded version here would
	// rot the moment 0012 lands. Same reason TestMigrate0009DownAndUpAgain
	// stopped hardcoding 9.
	if err := d.migrate(ctx); err != nil {
		t.Fatalf("migrate back up to the current schema: %v", err)
	}
	back, err := d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("registration after the round trip: %v", err)
	}
	if back.State != RegistrationApproved {
		t.Fatalf("state after the round trip = %q", back.State)
	}
	// The observation is gone, which is the documented cost; what matters is
	// that the column is usable again, so the box's next report lands.
	if back.ObservedVersion != "" {
		t.Fatalf("an observation survived a drop and recreate: %q", back.ObservedVersion)
	}
	if err := d.RecordObservedVersion(ctx, reg.ID, "1.6.0", ""); err != nil {
		t.Fatalf("record after the round trip: %v", err)
	}
	back, err = d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("read back after the round trip: %v", err)
	}
	if back.ObservedVersion != "1.6.0" {
		t.Fatalf("observed after the round trip = %q, want 1.6.0", back.ObservedVersion)
	}
}
