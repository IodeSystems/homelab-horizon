package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrate applies pending migrations, refusing first if any already-applied
// migration has been edited since.
func (d *DB) migrate(ctx context.Context) error {
	if err := d.ensureChecksumTable(ctx); err != nil {
		return err
	}
	if err := d.verifyChecksums(ctx); err != nil {
		return err
	}

	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	driver, err := sqlitemigrate.WithInstance(d.DB, &sqlitemigrate.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("migrator: %w", err)
	}

	switch err := m.Up(); {
	case err == nil:
		slog.Info("database schema migrated", "path", d.path)
	case errors.Is(err, migrate.ErrNoChange):
		// Already current.
	default:
		return fmt.Errorf("migrate: %w", err)
	}

	return d.recordChecksums(ctx)
}

// migrationChecksums returns version -> sha256 of each embedded up migration.
func migrationChecksums() (map[string]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out[versionOf(name)] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// versionOf pulls the leading numeric version out of a migration filename.
func versionOf(name string) string {
	if i := strings.Index(name, "_"); i > 0 {
		return name[:i]
	}
	return name
}

func (d *DB) ensureChecksumTable(ctx context.Context) error {
	// Tracked separately from the migration library's own bookkeeping, per
	// DEPLOY-10: this table is evidence of what was actually applied, and it
	// is not the library's to rewrite.
	_, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS applied_migrations (
			version    TEXT PRIMARY KEY,
			checksum   TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		return fmt.Errorf("create applied_migrations: %w", err)
	}
	return nil
}

// verifyChecksums refuses to start when an applied migration's SQL has
// changed.
//
// An applied migration is immutable. Editing one makes a single version number
// mean different SQL depending on when a box was built, and the divergence is
// invisible — every host reports the same version. Fixing a bad migration is a
// new migration.
func (d *DB) verifyChecksums(ctx context.Context) error {
	want, err := migrationChecksums()
	if err != nil {
		return err
	}

	rows, err := d.QueryContext(ctx, `SELECT version, checksum FROM applied_migrations`)
	if err != nil {
		return fmt.Errorf("read applied_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var drifted []string
	for rows.Next() {
		var version, sum string
		if err := rows.Scan(&version, &sum); err != nil {
			return err
		}
		// A version recorded here but absent from the binary is not drift:
		// it is an older hz meeting a newer database, which the version
		// check below is not the right place to litigate.
		if expected, ok := want[version]; ok && expected != sum {
			drifted = append(drifted, version)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(drifted) > 0 {
		sort.Strings(drifted)
		return fmt.Errorf(
			"migration %s was modified after it was applied; an applied migration is immutable — "+
				"revert the edit and add a new migration instead", strings.Join(drifted, ", "))
	}
	return nil
}

// recordChecksums stamps every migration the library reports as applied.
func (d *DB) recordChecksums(ctx context.Context) error {
	sums, err := migrationChecksums()
	if err != nil {
		return err
	}

	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for version, sum := range sums {
		if !applied[version] {
			continue
		}
		// DO NOTHING, never UPDATE: the row records what was applied. Bringing
		// it in line with an edited file is exactly how the check above would
		// be turned into a no-op.
		if _, err := d.ExecContext(ctx,
			`INSERT INTO applied_migrations (version, checksum) VALUES (?, ?)
			 ON CONFLICT (version) DO NOTHING`, version, sum); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}
	return nil
}

// appliedVersions reads the migration library's own table.
func (d *DB) appliedVersions(ctx context.Context) (map[string]bool, error) {
	// golang-migrate stores a single current version plus a dirty flag rather
	// than one row per migration, so everything at or below it is applied.
	var current int64
	var dirty bool
	err := d.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&current, &dirty)
	if err != nil {
		// No table or no row: nothing has been applied yet.
		return map[string]bool{}, nil //nolint:nilerr // absence is not an error here
	}
	if dirty {
		return nil, fmt.Errorf("schema is dirty at version %d: a migration failed part-way and must be resolved by hand", current)
	}

	sums, err := migrationChecksums()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(sums))
	for version := range sums {
		var n int64
		if _, err := fmt.Sscanf(version, "%d", &n); err != nil {
			continue
		}
		if n <= current {
			out[version] = true
		}
	}
	return out, nil
}
