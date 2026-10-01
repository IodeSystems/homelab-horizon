-- Release lines: the build that made a report, the backup each supported line
-- keeps, and the restore test that proves a version can take that backup.
--
-- plan/plan.md "Versions, lines and the restore test" (operator-approved
-- 2026-09-30). A version's LINE is its semver core (1.9.0-1.2 is on 1.9.0) and
-- is DERIVED in Go (db.LineOf, from parseVersion — hz's one parser); no column
-- here stores the line OF a version, only the line a backup or a test NAMES.
-- Which lines a rung SUPPORTS is derived too (db.DeriveSupportedLines: the
-- declared version's line, the most recent different line promoted into the
-- rung, and pins from config.json), never stored — CLAUDE.md #8.
--
-- build_url is where the build's tests and logs are kept. Optional, any scheme
-- (an R0 build may name a bucket path); '' means "the report carried none",
-- which is the state every row before this migration is in.
--
-- All three new tables are append-only by trigger, for the reason 0014 gives.
-- "Newest" is by id, never by clock.

ALTER TABLE deploy_reports ADD COLUMN build_url TEXT NOT NULL DEFAULT '';

-- The promotion pins the build_url of the report it pinned, beside its artifact.
ALTER TABLE promotions ADD COLUMN build_url TEXT NOT NULL DEFAULT '';

-- What the restore-test gate said when this row was written. 'predates' is every
-- row written before the gate existed — NOT "none required", which is an answer
-- the gate gave (the target had no supported line). A 'checked' row has one
-- promotion_lines row per line checked.
ALTER TABLE promotions ADD COLUMN restore_gate TEXT NOT NULL DEFAULT 'predates'
    CHECK (restore_gate IN ('predates', 'none-required', 'checked'));

-- One preserved backup per line, held by the APP; hz records that it exists,
-- where, and its digest. hz never reads, moves or deletes the backup.
CREATE TABLE kept_backups (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project          TEXT NOT NULL CHECK (project <> ''),
    line             TEXT NOT NULL CHECK (line <> ''),
    backup_sha256    TEXT NOT NULL CHECK (length(backup_sha256) = 64),
    location         TEXT NOT NULL CHECK (location <> ''),
    taken_by_version TEXT NOT NULL CHECK (taken_by_version <> ''),
    build_url        TEXT NOT NULL DEFAULT '',
    recorded_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    recorded_by      TEXT NOT NULL CHECK (recorded_by <> '')
);

CREATE INDEX kept_backups_line ON kept_backups (project, line, id);

-- "Build V restored line L's kept backup, migrated, tests passed" — or did not.
-- A failed test is recorded too: a failure is evidence, and hiding it would let
-- a retry read as the only attempt.
CREATE TABLE restore_tests (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project       TEXT NOT NULL CHECK (project <> ''),
    environment   TEXT NOT NULL CHECK (environment <> ''),
    version       TEXT NOT NULL CHECK (version <> ''),
    line          TEXT NOT NULL CHECK (line <> ''),
    backup_sha256 TEXT NOT NULL CHECK (length(backup_sha256) = 64),
    passed        INTEGER NOT NULL CHECK (passed IN (0, 1)),
    build_url     TEXT NOT NULL DEFAULT '',
    reported_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reported_by   TEXT NOT NULL CHECK (reported_by <> '')
);

CREATE INDEX restore_tests_evidence ON restore_tests (project, environment, version, line, id);

-- The lines a promotion was checked against, and the evidence that satisfied
-- each: the kept backup and the passing restore test. why is the derivation's
-- reason ("current", "prior", "pinned"; comma-joined when a line is several).
CREATE TABLE promotion_lines (
    promotion_id    INTEGER NOT NULL REFERENCES promotions (id),
    line            TEXT NOT NULL CHECK (line <> ''),
    why             TEXT NOT NULL CHECK (why <> ''),
    kept_backup_id  INTEGER NOT NULL REFERENCES kept_backups (id),
    restore_test_id INTEGER NOT NULL REFERENCES restore_tests (id),
    PRIMARY KEY (promotion_id, line)
);

CREATE TRIGGER kept_backups_no_update BEFORE UPDATE ON kept_backups
BEGIN
    SELECT RAISE(ABORT, 'kept_backups is append-only: a kept-backup record is never edited — record a new one');
END;

CREATE TRIGGER kept_backups_no_delete BEFORE DELETE ON kept_backups
BEGIN
    SELECT RAISE(ABORT, 'kept_backups is append-only: a kept-backup record is never deleted');
END;

CREATE TRIGGER restore_tests_no_update BEFORE UPDATE ON restore_tests
BEGIN
    SELECT RAISE(ABORT, 'restore_tests is append-only: a restore test is an observation and is never edited');
END;

CREATE TRIGGER restore_tests_no_delete BEFORE DELETE ON restore_tests
BEGIN
    SELECT RAISE(ABORT, 'restore_tests is append-only: a restore test is an observation and is never deleted');
END;

CREATE TRIGGER promotion_lines_no_update BEFORE UPDATE ON promotion_lines
BEGIN
    SELECT RAISE(ABORT, 'promotion_lines is append-only: the promotion record is never edited');
END;

CREATE TRIGGER promotion_lines_no_delete BEFORE DELETE ON promotion_lines
BEGIN
    SELECT RAISE(ABORT, 'promotion_lines is append-only: the promotion record is never deleted');
END;
