// Package backup is the format of hz's backup zip, and the checks that decide
// whether one is a backup: the server runs them before serving an export, the
// importer before writing anything, and `hz backup pull|verify` on the
// caller's machine after the download.
//
// The hz server never sends a backup anywhere (CLAUDE.md #1). It answers an
// authenticated GET; the caller pulls and keeps the file. The one outbound
// copy is `homelab-horizon backup push` (package offsite), a root command on
// the box that uploads the zip ENCRYPTED, with a put-only principal.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Names inside the zip.
const (
	ManifestName = "manifest.json"
	DBName       = "hz.db"
	BuildsPrefix = "builds/"
)

// FormatVersion is the manifest's own version. A zip with no manifest is a
// backup from before hz.db was included.
const FormatVersion = 1

// Manifest describes a backup zip. Written by the export as manifest.json.
type Manifest struct {
	Format int `json:"format"`
	// HZVersion is the version of the hz that took the backup.
	HZVersion string `json:"hz_version"`
	// Host is the hostname of the box hz ran on (informational).
	Host string `json:"host,omitempty"`
	// SchemaVersion is hz.db's migration version at snapshot time, which the
	// export checked equals the running binary's latest migration.
	SchemaVersion uint      `json:"schema_version"`
	SnapshotAt    time.Time `json:"snapshot_at"`
	// RowCounts is the live database's per-table row counts at snapshot time.
	RowCounts map[string]int64 `json:"row_counts"`
	// Files is the sha256 of every other file in the zip, by name.
	Files map[string]string `json:"files"`
	// Excluded is what the zip deliberately does not carry, and why.
	Excluded []Exclusion `json:"excluded"`
}

// Exclusion names something left out on purpose.
type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Sum is the hex sha256 the manifest records.
func Sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// CheckSnapshot is the export's server-side check of a fresh snapshot: the
// file opens with integrity ok, its schema version EQUALS the running
// binary's latest migration, and its row counts equal the counts read from
// the live database. Any failure means the snapshot is not served.
func CheckSnapshot(info db.SnapshotInfo, latest uint, live map[string]int64) error {
	if info.Integrity != "ok" {
		return fmt.Errorf("integrity_check: %s", info.Integrity)
	}
	if info.Dirty {
		return fmt.Errorf("schema is dirty at version %d", info.MigrationVersion)
	}
	if info.MigrationVersion != latest {
		return fmt.Errorf("snapshot schema version %d is not this binary's latest migration %d", info.MigrationVersion, latest)
	}
	return compareCounts(live, info.RowCounts)
}

func compareCounts(want, got map[string]int64) error {
	if maps.Equal(want, got) {
		return nil
	}
	names := map[string]bool{}
	for n := range want {
		names[n] = true
	}
	for n := range got {
		names[n] = true
	}
	var diffs []string
	for n := range names {
		w, wok := want[n]
		g, gok := got[n]
		switch {
		case !wok:
			diffs = append(diffs, fmt.Sprintf("%s: in the snapshot, not in the manifest", n))
		case !gok:
			diffs = append(diffs, fmt.Sprintf("%s: in the manifest, missing from the snapshot", n))
		case w != g:
			diffs = append(diffs, fmt.Sprintf("%s: manifest %d rows, snapshot %d", n, w, g))
		}
	}
	sort.Strings(diffs)
	return fmt.Errorf("row counts differ: %s", strings.Join(diffs, "; "))
}

// Verified is what Verify established about a zip.
type Verified struct {
	Manifest Manifest
	Snapshot db.SnapshotInfo
}

// Verify checks a backup zip as a whole, without trusting the producer:
//
//   - manifest.json is present and of a known format;
//   - every other file's sha256 equals the manifest's, and the manifest names
//     no file the zip lacks and the zip holds no file the manifest does not name;
//   - hz.db is present, opens with integrity ok, records a clean migration
//     version equal to the manifest's, and its row counts equal the manifest's.
//
// It does NOT judge whether this binary can restore it: that is
// db.CheckRestorable, which the restore paths add.
func Verify(ctx context.Context, data []byte) (*Verified, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a zip: %w", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if _, dup := files[f.Name]; dup {
			return nil, fmt.Errorf("%s appears twice in the zip", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		files[f.Name] = b
	}
	return VerifyFiles(ctx, files)
}

// VerifyFiles is Verify over an already-unpacked zip (name -> contents).
func VerifyFiles(ctx context.Context, files map[string][]byte) (*Verified, error) {
	raw, ok := files[ManifestName]
	if !ok {
		return nil, errors.New("no manifest.json: this is not a backup that carries hz.db")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Format != FormatVersion {
		return nil, fmt.Errorf("manifest format %d; this hz reads format %d", m.Format, FormatVersion)
	}

	var problems []string
	for name, data := range files {
		if name == ManifestName {
			continue
		}
		want, ok := m.Files[name]
		switch {
		case !ok:
			problems = append(problems, name+": in the zip, not in the manifest")
		case Sum(data) != want:
			problems = append(problems, fmt.Sprintf("%s: sha256 %s, manifest says %s", name, Sum(data), want))
		}
	}
	for name := range m.Files {
		if _, ok := files[name]; !ok {
			problems = append(problems, name+": in the manifest, missing from the zip")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("manifest mismatch: %s", strings.Join(problems, "; "))
	}

	dbData, ok := files[DBName]
	if !ok {
		return nil, errors.New("no hz.db in the zip")
	}
	info, err := inspectBytes(ctx, dbData)
	if err != nil {
		return nil, err
	}
	switch {
	case info.Integrity != "ok":
		return nil, fmt.Errorf("hz.db integrity_check: %s", info.Integrity)
	case info.Dirty:
		return nil, fmt.Errorf("hz.db schema is dirty at version %d", info.MigrationVersion)
	case info.MigrationVersion == 0:
		return nil, errors.New("hz.db records no schema migration version")
	case info.MigrationVersion != m.SchemaVersion:
		return nil, fmt.Errorf("hz.db is at schema version %d, the manifest says %d", info.MigrationVersion, m.SchemaVersion)
	}
	if err := compareCounts(m.RowCounts, info.RowCounts); err != nil {
		return nil, fmt.Errorf("hz.db: %w", err)
	}
	return &Verified{Manifest: m, Snapshot: info}, nil
}

// inspectBytes writes the snapshot to a private temp file and inspects it.
func inspectBytes(ctx context.Context, data []byte) (db.SnapshotInfo, error) {
	dir, err := os.MkdirTemp("", "hz-backup-verify-")
	if err != nil {
		return db.SnapshotInfo{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	p := filepath.Join(dir, DBName)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return db.SnapshotInfo{}, err
	}
	info, err := db.InspectSnapshot(ctx, p)
	if err != nil {
		return info, fmt.Errorf("hz.db does not open as a database: %w", err)
	}
	return info, nil
}

// WriteFileAtomic writes data to path, mode 0600, so that path holds either
// its old contents or all of data and never a partial file: a temp file in the
// same directory is written, synced and renamed over it.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
