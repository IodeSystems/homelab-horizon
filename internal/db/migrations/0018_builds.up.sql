-- The build registry: build numbers come FROM hz (operator, 2026-10-02).
--
-- Before this, redline minted its build number from a counter file on the dev
-- box, so two build machines would mint the same 1.0.0.0#N with different
-- artifacts. Now a BUILDER (a declared machine holding a builder token,
-- config.json Builders) asks hz to start a build, and hz allocates the number.
--
-- THE NUMBER IS A SEQUENCE, never a max over rows. build_sequences holds the
-- next number per PROJECT; a start bumps it in the same transaction that
-- inserts the build, so two concurrent starts cannot share a number, and a
-- failed or abandoned build keeps the number it was given. The first allocation
-- for a project seeds the sequence above every version already recorded for it
-- (db.StartBuild), because numbers were issued before hz issued them.
--
-- A build row is the one mutable record here: its state and its log activity
-- change while it runs. What does NOT change is pinned by trigger — its
-- identity always, and everything about its outcome once it is finished.

CREATE TABLE build_sequences (
    project TEXT PRIMARY KEY CHECK (project <> ''),
    next    INTEGER NOT NULL CHECK (next >= 1)
);

CREATE TRIGGER build_sequences_never_lower BEFORE UPDATE OF next ON build_sequences
WHEN NEW.next < OLD.next
BEGIN
    SELECT RAISE(ABORT, 'build_sequences never goes down: a number once reachable is never handed out again');
END;

CREATE TRIGGER build_sequences_no_delete BEFORE DELETE ON build_sequences
BEGIN
    SELECT RAISE(ABORT, 'build_sequences is never deleted: forgetting a project''s next number would reissue its numbers');
END;

CREATE TABLE builds (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project          TEXT NOT NULL CHECK (project <> ''),
    number           INTEGER NOT NULL CHECK (number >= 1),
    commit_sha       TEXT NOT NULL CHECK (length(commit_sha) = 40),
    version_core     TEXT NOT NULL CHECK (version_core <> ''),
    version          TEXT NOT NULL CHECK (version <> ''),
    describe         TEXT NOT NULL DEFAULT '',
    machine          TEXT NOT NULL CHECK (machine <> ''),
    started_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- The newest log chunk or state update. A build with no finish and no
    -- activity for 2h READS as abandoned; that is derived on read, never stored.
    last_activity_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    step             TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'passed', 'failed')),
    steps            TEXT NOT NULL DEFAULT '[]',
    finished_at      TIMESTAMP,
    result           TEXT CHECK (result IS NULL OR result IN ('passed', 'failed')),
    artifact_sha256  TEXT CHECK (artifact_sha256 IS NULL OR length(artifact_sha256) = 64),
    failed_step      TEXT NOT NULL DEFAULT '',
    -- Set when retention deleted the log file: "no log" and "log deleted"
    -- are different answers (CLAUDE.md #2).
    log_deleted_at   TIMESTAMP,
    log_deleted_why  TEXT NOT NULL DEFAULT '',
    UNIQUE (project, number),
    UNIQUE (project, version),
    CHECK ((result IS NULL) = (finished_at IS NULL)),
    CHECK (result IS NOT 'passed' OR artifact_sha256 IS NOT NULL),
    CHECK (result IS NOT 'failed' OR artifact_sha256 IS NULL)
);

CREATE INDEX builds_artifact ON builds (artifact_sha256);

CREATE TRIGGER builds_identity_fixed BEFORE UPDATE OF project, number, commit_sha, version_core, version, describe, machine, started_at ON builds
BEGIN
    SELECT RAISE(ABORT, 'a build''s identity is never edited: its number, commit, version and machine are what it was started as');
END;

CREATE TRIGGER builds_finish_is_final BEFORE UPDATE OF result, finished_at, artifact_sha256, failed_step, step, status, steps, last_activity_at ON builds
WHEN OLD.finished_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a finished build is final: its result, artifact and state are never edited');
END;

CREATE TRIGGER builds_no_delete BEFORE DELETE ON builds
BEGIN
    SELECT RAISE(ABORT, 'builds is never deleted: a number keeps its build, failed or abandoned');
END;

-- PROVENANCE. A promotion now requires its artifact to be a passed build's —
-- except an artifact that was already uploaded when this migration ran. Those
-- are listed HERE, from the rows that exist now, not by a time cutoff: an
-- upload clock is not a thing to trust, and a list cannot grow. The insert
-- trigger below is created after the list is filled, so nothing adds to it.
CREATE TABLE artifact_grandfathered (
    sha256 TEXT PRIMARY KEY REFERENCES artifacts (sha256)
);

INSERT INTO artifact_grandfathered (sha256) SELECT sha256 FROM artifacts;

CREATE TRIGGER artifact_grandfathered_closed BEFORE INSERT ON artifact_grandfathered
BEGIN
    SELECT RAISE(ABORT, 'artifact_grandfathered is closed: it lists the artifacts uploaded before migration 0018, and nothing else');
END;

CREATE TRIGGER artifact_grandfathered_no_update BEFORE UPDATE ON artifact_grandfathered
BEGIN
    SELECT RAISE(ABORT, 'artifact_grandfathered is never edited');
END;

CREATE TRIGGER artifact_grandfathered_no_delete BEFORE DELETE ON artifact_grandfathered
BEGIN
    SELECT RAISE(ABORT, 'artifact_grandfathered is never deleted from');
END;

-- What a promotion's provenance check answered: the passed build its artifact
-- came from ('build', build_id set), the grandfathered list ('grandfathered'),
-- or 'predates' on a row written before the check existed. build_id carries no
-- REFERENCES clause only because SQLite cannot DROP a foreign-key column, and
-- the down migration has to; the server writes it from the build it looked up.
ALTER TABLE promotions ADD COLUMN build_id INTEGER;
ALTER TABLE promotions ADD COLUMN provenance TEXT NOT NULL DEFAULT 'predates'
    CHECK (provenance IN ('predates', 'build', 'grandfathered'));
