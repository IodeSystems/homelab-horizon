// Package db owns hz's SQLite store: the identity data that must not live in
// config.json.
//
// config.json is synced between HA peers, so anything placed there is
// replicated as configuration. That is right for services, peers and zones,
// and wrong for users, credentials and sessions — this database is node-local
// state, kept under /var/lib rather than /etc for the same reason.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: cgo would end the static binary
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DefaultPath is where the store lives. /var/lib because it is state, not
// configuration; the systemd unit already creates the directory.
const DefaultPath = "/var/lib/homelab-horizon/hz.db"

// DB wraps the connection pool.
type DB struct {
	*sql.DB
	path string
}

// Open opens (creating if absent) the store at path and brings the schema up
// to date.
//
// Migrating on open is a deliberate departure from DEPLOY-11, which keeps
// migrations out of the boot path. That rule assumes a Postgres owner role and
// a rolling slot flip — a second actor able to apply them. hz is one process
// that owns one file on one box, so there is nobody else to do it, and a
// gateway that refused to serve until a human ran a migration would be a
// worse outage than the one the rule prevents.
func Open(path string) (*DB, error) {
	if path == "" {
		path = DefaultPath
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	// _busy_timeout: SQLite fails a write immediately when the file is locked
	// rather than waiting, and hz has concurrent request handlers.
	// journal_mode=WAL lets reads proceed during a write, which matters
	// because every authenticated request touches sessions.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	// The file holds credentials; it is nobody else's business.
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("could not tighten database permissions", "path", path, "error", err)
	}

	d := &DB{DB: sqlDB, path: path}
	if err := d.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// openRaw opens the file without migrating it. Only OpenAt uses this, to build
// a database as it stood at an older schema version.
func openRaw(path string) (*DB, error) {
	if path == "" {
		path = DefaultPath
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return &DB{DB: sqlDB, path: path}, nil
}

// Path reports where the store lives.
func (d *DB) Path() string { return d.path }
