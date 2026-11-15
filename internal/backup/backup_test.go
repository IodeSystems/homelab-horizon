package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// fixture is an unpacked, valid backup: a real snapshot plus a manifest.
func fixture(t *testing.T) (map[string][]byte, Manifest) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.CreateUser(ctx, "carl", "", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "snap.db")
	live, err := d.Snapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := db.LatestMigration()
	files := map[string][]byte{
		DBName:                   data,
		"config.json":            []byte(`{}`),
		BuildsPrefix + "p/1.log": []byte("built\n"),
	}
	m := Manifest{Format: FormatVersion, HZVersion: "test", SchemaVersion: latest,
		SnapshotAt: time.Now().UTC(), RowCounts: live, Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = Sum(b)
	}
	return files, m
}

func withManifest(t *testing.T, files map[string][]byte, m Manifest) map[string][]byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{ManifestName: raw}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVerifyAcceptsAGoodBackup(t *testing.T) {
	files, m := fixture(t)
	v, err := Verify(context.Background(), zipOf(t, withManifest(t, files, m)))
	if err != nil {
		t.Fatal(err)
	}
	if v.Snapshot.RowCounts["users"] != 1 || v.Manifest.SchemaVersion != v.Snapshot.MigrationVersion {
		t.Fatalf("verified: %+v", v)
	}
}

// Each of these is a way the zip can differ from what its manifest promises.
// The good case above is the positive control: the same fixture passes.
func TestVerifyRefuses(t *testing.T) {
	cases := map[string]struct {
		edit func(files map[string][]byte, m *Manifest)
		want string
	}{
		"a file's bytes changed": {func(f map[string][]byte, _ *Manifest) { f["config.json"] = []byte(`{"x":1}`) }, "config.json: sha256"},
		"the snapshot's bytes changed": {func(f map[string][]byte, _ *Manifest) {
			f[DBName] = append([]byte(nil), f[DBName]...)
			f[DBName][len(f[DBName])-1] ^= 0xff
		}, "hz.db: sha256"},
		"a file the manifest does not name": {func(f map[string][]byte, _ *Manifest) { f["extra"] = []byte("x") }, "extra: in the zip, not in the manifest"},
		"a file the zip lacks":              {func(f map[string][]byte, _ *Manifest) { delete(f, BuildsPrefix+"p/1.log") }, "missing from the zip"},
		"row counts differ": {func(_ map[string][]byte, m *Manifest) {
			m.RowCounts["users"] = 7
		}, "users: manifest 7 rows, snapshot 1"},
		"schema version differs from the manifest": {func(_ map[string][]byte, m *Manifest) { m.SchemaVersion++ }, "the manifest says"},
		"unknown format": {func(_ map[string][]byte, m *Manifest) { m.Format = 99 }, "manifest format 99"},
		"no database": {func(f map[string][]byte, m *Manifest) {
			delete(f, DBName)
			delete(m.Files, DBName)
		}, "no hz.db"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			files, m := fixture(t)
			counts := map[string]int64{}
			for k, v := range m.RowCounts {
				counts[k] = v
			}
			m.RowCounts = counts
			tc.edit(files, &m)
			_, err := Verify(context.Background(), zipOf(t, withManifest(t, files, m)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyRefusesAZipWithNoManifest(t *testing.T) {
	files, _ := fixture(t)
	if _, err := Verify(context.Background(), zipOf(t, files)); err == nil || !strings.Contains(err.Error(), "no manifest.json") {
		t.Fatalf("got %v", err)
	}
}

// The export's own check: exactly the binary's schema, and the live counts.
func TestCheckSnapshot(t *testing.T) {
	latest, _ := db.LatestMigration()
	good := db.SnapshotInfo{Integrity: "ok", MigrationVersion: latest, RowCounts: map[string]int64{"users": 2}}
	if err := CheckSnapshot(good, latest, map[string]int64{"users": 2}); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	older := good
	older.MigrationVersion = latest - 1
	if err := CheckSnapshot(older, latest, good.RowCounts); err == nil || !strings.Contains(err.Error(), "not this binary's latest") {
		t.Fatalf("version mismatch: %v", err)
	}
	if err := CheckSnapshot(good, latest, map[string]int64{"users": 3}); err == nil || !strings.Contains(err.Error(), "row counts differ") {
		t.Fatalf("count mismatch: %v", err)
	}
	if err := CheckSnapshot(good, latest, map[string]int64{"users": 2, "sessions": 0}); err == nil || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("a table missing from the snapshot: %v", err)
	}
	bad := good
	bad.Integrity = "page 4 is never used"
	if err := CheckSnapshot(bad, latest, good.RowCounts); err == nil || !strings.Contains(err.Error(), "integrity_check") {
		t.Fatalf("integrity: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.zip")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("new contents")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(got) != "new contents" || st.Mode().Perm() != 0o600 {
		t.Fatalf("got %q mode %v", got, st.Mode())
	}

	// A write that cannot complete leaves the destination as it was and no
	// temp file behind: here the destination is a non-empty directory, so the
	// final rename fails after the data was written.
	blocker := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(blocker, []byte("data")); err == nil {
		t.Fatal("a rename over a directory succeeded")
	}
	if st, err := os.Stat(blocker); err != nil || !st.IsDir() {
		t.Fatalf("the destination changed: %v %v", st, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
