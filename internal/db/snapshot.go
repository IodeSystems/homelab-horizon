package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// LatestMigration is the newest schema version this binary carries: the
// version a database it opens ends up at.
func LatestMigration() (uint, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return 0, err
	}
	var latest uint
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		n, err := strconv.ParseUint(versionOf(e.Name()), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("migration %s: version is not a number", e.Name())
		}
		if uint(n) > latest {
			latest = uint(n)
		}
	}
	if latest == 0 {
		return 0, errors.New("this binary carries no migrations")
	}
	return latest, nil
}

// Snapshot writes a consistent copy of the live database to dest with
// VACUUM INTO, and returns the per-table row counts the copy must hold.
//
// VACUUM INTO reads through the open connection, so it sees every committed
// transaction whether it is in the main file or still in the WAL. Copying the
// .db file does not: on a box measured 2026-10-02 the file was 430 KB and the
// WAL 4 MB, so a file copy would have lost most recent data.
//
// The counts are read from the LIVE database before and after the vacuum, and
// the snapshot is kept only when both agree: a write that lands in between
// would make "the counts at snapshot time" two different answers. The caller
// then compares them with the snapshot's own counts — which is what makes the
// comparison a check of the snapshot rather than of itself.
func (d *DB) Snapshot(ctx context.Context, dest string) (map[string]int64, error) {
	const attempts = 5
	for i := 0; i < attempts; i++ {
		before, err := RowCounts(ctx, d.DB)
		if err != nil {
			return nil, fmt.Errorf("count live rows: %w", err)
		}
		_ = os.Remove(dest) // VACUUM INTO refuses an existing file
		if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
			return nil, fmt.Errorf("vacuum into %s: %w", dest, err)
		}
		after, err := RowCounts(ctx, d.DB)
		if err != nil {
			return nil, fmt.Errorf("count live rows: %w", err)
		}
		if maps.Equal(before, after) {
			if err := os.Chmod(dest, 0o600); err != nil {
				return nil, err
			}
			return before, nil
		}
	}
	_ = os.Remove(dest)
	return nil, fmt.Errorf("rows changed during each of %d snapshot attempts; retry when hz is quieter", attempts)
}

// SnapshotInfo is what a snapshot file says about itself.
type SnapshotInfo struct {
	// Integrity is PRAGMA integrity_check's answer, joined: "ok" or the
	// problems it found.
	Integrity string
	// MigrationVersion is schema_migrations.version; 0 when none is recorded.
	MigrationVersion uint
	Dirty            bool
	RowCounts        map[string]int64
}

// InspectSnapshot opens a snapshot file read-only and reports its integrity,
// schema version and row counts. An error is returned only when the file
// cannot be read as a database at all; every other finding is in the result
// for the caller to judge.
//
// immutable=1: a snapshot is a file nobody writes, so SQLite takes no locks
// and creates no -wal or -shm beside it. It also means a stray -wal next to the
// file is IGNORED — the snapshot has to be complete in itself, which is the
// property being checked.
func InspectSnapshot(ctx context.Context, path string) (SnapshotInfo, error) {
	var info SnapshotInfo
	if _, err := os.Stat(path); err != nil {
		return info, err // sqlite would otherwise report an empty database
	}
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: "mode=ro&immutable=1"}
	conn, err := sql.Open("sqlite", u.String())
	if err != nil {
		return info, fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = conn.Close() }()

	rows, err := conn.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return info, fmt.Errorf("integrity_check: %w", err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return info, err
		}
		problems = append(problems, line)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return info, fmt.Errorf("integrity_check: %w", err)
	}
	info.Integrity = strings.Join(problems, "; ")

	var version int64
	switch err := conn.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &info.Dirty); {
	case err == nil:
		if version > 0 {
			info.MigrationVersion = uint(version)
		}
	case errors.Is(err, sql.ErrNoRows), strings.Contains(err.Error(), "no such table"):
		// No version recorded: left at 0 for the caller to refuse.
	default:
		return info, fmt.Errorf("read schema version: %w", err)
	}

	if info.RowCounts, err = RowCounts(ctx, conn); err != nil {
		return info, err
	}
	return info, nil
}

// RowCounts counts the rows of every user table.
func RowCounts(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		var c int64
		quoted := `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+quoted).Scan(&c); err != nil {
			return nil, fmt.Errorf("count %s: %w", n, err)
		}
		out[n] = c
	}
	return out, nil
}

// PendingRestoreSuffix names the file an import stages a restored database in.
// It is swapped in by ApplyPendingRestore, before Open, on the next start: the
// live file cannot be replaced under an open connection pool.
const PendingRestoreSuffix = ".restore"
