package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openWALHeavy opens a store and adds n users that stay in the WAL: one
// connection, autocheckpoint off, so nothing moves them into the main file.
func openWALHeavy(t *testing.T, n int) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hz.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	d.SetMaxOpenConns(1)
	if _, err := d.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := d.CreateUser(ctx, fmt.Sprintf("user%02d", i), "", RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	return d, path
}

// The founding measurement: hz.db 430 KB, WAL 4 MB. A snapshot must carry what
// is in the WAL; a copy of the .db file does not, and the test proves that
// too, so a pass is a claim about the snapshot and not about a checkpoint that
// happened to run (CLAUDE.md #15).
func TestSnapshotCarriesRowsStillInTheWAL(t *testing.T) {
	const n = 40
	d, path := openWALHeavy(t, n)
	ctx := context.Background()

	wal, err := os.Stat(path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("instrument: the rows are not in a WAL (%v, %v)", wal, err)
	}

	// Positive control: copy the file the way a naive backup would.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "copied.db")
	if err := os.WriteFile(copied, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := InspectSnapshot(ctx, copied); err == nil && info.RowCounts["users"] == n {
		t.Fatalf("positive control failed: a copy of the .db file already holds all %d WAL rows, so this test proves nothing", n)
	}

	snap := filepath.Join(t.TempDir(), "snap.db")
	live, err := d.Snapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if live["users"] != n {
		t.Fatalf("live count at snapshot time: %d, want %d", live["users"], n)
	}
	info, err := InspectSnapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if info.RowCounts["users"] != n {
		t.Fatalf("snapshot holds %d users, want %d (the WAL rows were lost)", info.RowCounts["users"], n)
	}
	if info.Integrity != "ok" {
		t.Fatalf("integrity: %s", info.Integrity)
	}
	latest, err := LatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	if info.MigrationVersion != latest {
		t.Fatalf("snapshot at schema %d, binary's latest is %d", info.MigrationVersion, latest)
	}
	if st, err := os.Stat(snap); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode: %v %v", st.Mode(), err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(snap + sfx); err == nil {
			t.Fatalf("inspecting a snapshot created %s beside it", sfx)
		}
	}
}

func TestInspectSnapshotOfAMissingFileIsAnError(t *testing.T) {
	if _, err := InspectSnapshot(context.Background(), filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("a missing file inspected as a database")
	}
}

// setSchemaVersion rewrites a snapshot's recorded migration version.
func setSchemaVersion(t *testing.T, path string, v uint, dirty bool) {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Exec(`UPDATE schema_migrations SET version = ?, dirty = ?`, v, dirty); err != nil {
		t.Fatal(err)
	}
}

func snapshotOf(t *testing.T, users int) string {
	t.Helper()
	d, _ := openWALHeavy(t, users)
	snap := filepath.Join(t.TempDir(), "snap.db")
	if _, err := d.Snapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestCheckRestorable(t *testing.T) {
	latest, err := LatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	base := SnapshotInfo{Integrity: "ok", MigrationVersion: latest}
	if err := CheckRestorable(base, latest); err != nil {
		t.Fatalf("a current snapshot: %v", err)
	}
	older := base
	older.MigrationVersion = latest - 1
	if err := CheckRestorable(older, latest); err != nil {
		t.Fatalf("an older snapshot is restorable (Open migrates it): %v", err)
	}
	for name, tc := range map[string]struct {
		info SnapshotInfo
		want string
	}{
		"newer":     {SnapshotInfo{Integrity: "ok", MigrationVersion: latest + 1}, "newer than this binary"},
		"dirty":     {SnapshotInfo{Integrity: "ok", MigrationVersion: latest, Dirty: true}, "dirty"},
		"unversion": {SnapshotInfo{Integrity: "ok"}, "no schema migration version"},
		"corrupt":   {SnapshotInfo{Integrity: "row 3 missing from index", MigrationVersion: latest}, "integrity_check"},
	} {
		if err := CheckRestorable(tc.info, latest); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

func TestApplyPendingRestoreSwapsAndKeepsTheOld(t *testing.T) {
	snap := snapshotOf(t, 3)
	dir := t.TempDir()
	live := filepath.Join(dir, "hz.db")
	d, err := Open(live)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	if applied, err := ApplyPendingRestore(live); err != nil || applied {
		t.Fatalf("nothing staged: applied=%v err=%v", applied, err)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live+PendingRestoreSuffix, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(live); err != nil || !applied {
		t.Fatalf("staged restore: applied=%v err=%v", applied, err)
	}
	if _, err := os.Stat(live + PendingRestoreSuffix); err == nil {
		t.Fatal("the staged file is still there")
	}
	kept, _ := filepath.Glob(live + ".pre-restore-*")
	if len(kept) == 0 {
		t.Fatal("the previous database was not kept")
	}
	d, err = Open(live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("restored users: %d %v", n, err)
	}
}

// A staged snapshot from a NEWER hz is refused at boot and the live database
// is left exactly as it was.
func TestApplyPendingRestoreRefusesANewerSchema(t *testing.T) {
	latest, err := LatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshotOf(t, 2)
	setSchemaVersion(t, snap, latest+1, false)

	live := filepath.Join(t.TempDir(), "hz.db")
	d, err := Open(live)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(snap)
	if err := os.WriteFile(live+PendingRestoreSuffix, data, 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPendingRestore(live)
	if applied || err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("newer schema: applied=%v err=%v", applied, err)
	}
	after, _ := os.ReadFile(live)
	if string(before) != string(after) {
		t.Fatal("the live database changed")
	}
	if rej, _ := filepath.Glob(live + PendingRestoreSuffix + ".rejected-*"); len(rej) != 1 {
		t.Fatalf("the refused file was not set aside: %v", rej)
	}
}
