-- N4a: the artifact store, the apply record, rung holds, and when a nested hz
-- last pulled (plan/plan.md N4a, decided by the operator 2026-10-01).
--
-- A PROMOTION APPROVES; AN APPLY IS WHAT A RUNG RUNS. `GET
-- /api/v1/deploys/desired` answers the newest apply into a rung, never the
-- newest promotion, so a rung is HELD BY DEFAULT: promoting changes nothing a
-- box pulls until someone applies. A hold is the emergency stop on top.
--
-- The artifact bytes live on disk (<data dir>/artifacts/<sha256>, beside
-- hz.db); these rows record that they were uploaded, by whom, and — when
-- retention removed the file — that it was deleted and why. "Never uploaded"
-- (no artifacts row) and "uploaded, and the file is gone" (a 'deleted' event)
-- are different answers (CLAUDE.md #2), so the row outlives the file.
--
-- Every table here except instance_pulls is append-only by trigger, for the
-- reason 0014 gives. instance_pulls is a last-seen observation, one row per
-- machine, overwritten on each pull: it is not a record anybody audits.

CREATE TABLE artifacts (
    sha256      TEXT PRIMARY KEY CHECK (length(sha256) = 64),
    project     TEXT NOT NULL CHECK (project <> ''),
    size        INTEGER NOT NULL CHECK (size >= 0),
    uploaded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    uploaded_by TEXT NOT NULL CHECK (uploaded_by <> '')
);

-- What happened to an artifact's FILE after its first upload. The newest event
-- by id is its state: none or 'restored' is stored, 'deleted' is gone.
-- 'restored' is a re-upload of bytes retention had deleted.
CREATE TABLE artifact_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256  TEXT NOT NULL REFERENCES artifacts (sha256),
    kind    TEXT NOT NULL CHECK (kind IN ('deleted', 'restored')),
    reason  TEXT NOT NULL CHECK (reason <> ''),
    event_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor    TEXT NOT NULL CHECK (actor <> '')
);

CREATE INDEX artifact_events_sha ON artifact_events (sha256, id);

-- One row per `hz env apply`. It pins the promotion it applied (the newest
-- promotion into the rung at the time — an apply never picks a build) and that
-- promotion's artifact.
CREATE TABLE applies (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT NOT NULL CHECK (project <> ''),
    environment     TEXT NOT NULL CHECK (environment <> ''),
    version         TEXT NOT NULL CHECK (version <> ''),
    artifact_sha256 TEXT NOT NULL CHECK (length(artifact_sha256) = 64),
    promotion_id    INTEGER NOT NULL REFERENCES promotions (id),
    applied_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    applied_by      TEXT NOT NULL CHECK (applied_by <> '')
);

CREATE INDEX applies_rung ON applies (project, environment, id);

-- Hold and unhold, as events. The newest event per rung is the rung's hold
-- state; none is "not held". A hold needs a reason; an unhold carries none.
CREATE TABLE rung_holds (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project     TEXT NOT NULL CHECK (project <> ''),
    environment TEXT NOT NULL CHECK (environment <> ''),
    kind        TEXT NOT NULL CHECK (kind IN ('hold', 'unhold')),
    reason      TEXT NOT NULL CHECK (kind = 'unhold' OR reason <> ''),
    event_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor       TEXT NOT NULL CHECK (actor <> '')
);

CREATE INDEX rung_holds_rung ON rung_holds (project, environment, id);

-- When a nested hz last asked for its desired state, by machine. An
-- observation (the parent cannot know more than "it asked"), so hz.db and not
-- config.json.
CREATE TABLE instance_pulls (
    machine      TEXT PRIMARY KEY CHECK (machine <> ''),
    project      TEXT NOT NULL,
    environment  TEXT NOT NULL,
    last_pull_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER artifacts_no_update BEFORE UPDATE ON artifacts
BEGIN
    SELECT RAISE(ABORT, 'artifacts is append-only: an upload record is never edited — a deletion is an artifact_events row');
END;

CREATE TRIGGER artifacts_no_delete BEFORE DELETE ON artifacts
BEGIN
    SELECT RAISE(ABORT, 'artifacts is append-only: an upload record is never deleted — the row outlives the file');
END;

CREATE TRIGGER artifact_events_no_update BEFORE UPDATE ON artifact_events
BEGIN
    SELECT RAISE(ABORT, 'artifact_events is append-only: an artifact event is never edited');
END;

CREATE TRIGGER artifact_events_no_delete BEFORE DELETE ON artifact_events
BEGIN
    SELECT RAISE(ABORT, 'artifact_events is append-only: an artifact event is never deleted');
END;

CREATE TRIGGER applies_no_update BEFORE UPDATE ON applies
BEGIN
    SELECT RAISE(ABORT, 'applies is append-only: the apply record is never edited');
END;

CREATE TRIGGER applies_no_delete BEFORE DELETE ON applies
BEGIN
    SELECT RAISE(ABORT, 'applies is append-only: the apply record is never deleted');
END;

CREATE TRIGGER rung_holds_no_update BEFORE UPDATE ON rung_holds
BEGIN
    SELECT RAISE(ABORT, 'rung_holds is append-only: a hold is lifted by an unhold row, never by an edit');
END;

CREATE TRIGGER rung_holds_no_delete BEFORE DELETE ON rung_holds
BEGIN
    SELECT RAISE(ABORT, 'rung_holds is append-only: a hold event is never deleted');
END;
