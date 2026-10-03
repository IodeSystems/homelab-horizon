package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// CheckRestorable reports why a snapshot may not become this binary's
// database, or nil. A snapshot at an OLDER schema is restorable: Open migrates
// it forward. A NEWER one is not — this binary cannot know what the extra
// migrations mean, and running against them would be guessing.
func CheckRestorable(info SnapshotInfo, latest uint) error {
	switch {
	case info.Integrity != "ok":
		return fmt.Errorf("integrity_check: %s", info.Integrity)
	case info.Dirty:
		return fmt.Errorf("schema is dirty at version %d: a migration failed part-way on the source", info.MigrationVersion)
	case info.MigrationVersion == 0:
		return errors.New("no schema migration version is recorded")
	case info.MigrationVersion > latest:
		return fmt.Errorf("snapshot is at schema version %d, newer than this binary's %d; restore it with an hz at least as new as the one that took it",
			info.MigrationVersion, latest)
	}
	return nil
}

// ApplyPendingRestore swaps a staged restore (path + PendingRestoreSuffix)
// into place. It must run BEFORE Open, with nothing holding the database: that
// is why an import stages the file instead of writing over the live one.
//
// The current database, with its -wal and -shm, is kept beside it as
// path.pre-restore-<UTC time>; nothing is deleted. A staged file that fails
// the checks is renamed to .rejected-<time> and the current database is left
// untouched: refusing to boot over a bad restore would be a worse outage than
// starting on the data hz already had.
//
// Returns whether a restore was applied.
func ApplyPendingRestore(path string) (bool, error) {
	if path == "" {
		path = DefaultPath
	}
	staged := path + PendingRestoreSuffix
	if _, err := os.Stat(staged); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")

	latest, err := LatestMigration()
	if err != nil {
		return false, err
	}
	info, err := InspectSnapshot(context.Background(), staged)
	if err == nil {
		err = CheckRestorable(info, latest)
	}
	if err != nil {
		rejected := staged + ".rejected-" + stamp
		if rerr := os.Rename(staged, rejected); rerr != nil {
			return false, fmt.Errorf("staged restore %s refused (%v), and could not be moved aside: %w", staged, err, rerr)
		}
		return false, fmt.Errorf("staged restore refused, moved to %s: %w", rejected, err)
	}

	keep := path + ".pre-restore-" + stamp
	var moved []string
	putBack := func() {
		for _, suffix := range moved {
			_ = os.Rename(keep+suffix, path+suffix)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Rename(path+suffix, keep+suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			putBack()
			return false, fmt.Errorf("move %s aside: %w", path+suffix, err)
		}
		moved = append(moved, suffix)
	}
	if err := os.Rename(staged, path); err != nil {
		putBack()
		return false, fmt.Errorf("put restored database in place: %w", err)
	}
	return true, nil
}
