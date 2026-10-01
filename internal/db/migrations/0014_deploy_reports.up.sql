-- What a rung SAYS it is running, as told by the deploy that put it there.
--
-- plan/plan.md "Redline push to prod — the plan", step H1. A deploy script posts
-- one row after a successful slot flip: the rung, the semver version, the
-- `git describe` string, the sha256 of the bundle it shipped, and the host. The
-- promotion gate (0015) reads it as EVIDENCE: hz refuses to promote X out of a
-- rung that has not reported running X.
--
-- WHY hz.db AND NOT config.json. config.json is declared state and is synced to
-- every HA peer; a report is an OBSERVATION of one moment. Storing it beside the
-- declaration would make it a second answer to "what does staging run" that a
-- peer sync could overwrite.
--
-- APPEND-ONLY, and the triggers make that structural rather than a promise: a
-- report is a record of what was said at a time, so editing one rewrites
-- history and deleting one hides it. A rebuild of the same version is a NEW
-- row; the latest row (by id, never by clock) is what the gate reads.
--
-- No CHECK parses version: semver validation lives in Go (db.CompareVersions is
-- hz's one parser), and a CHECK would be a second one free to disagree.
-- describe is provenance only and is never compared. reported_by is TEXT, not a
-- foreign key: a service token is a legitimate reporter and names a service,
-- not a user.

CREATE TABLE deploy_reports (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT NOT NULL CHECK (project <> ''),
    environment     TEXT NOT NULL CHECK (environment <> ''),
    app             TEXT NOT NULL,
    version         TEXT NOT NULL CHECK (version <> ''),
    describe        TEXT NOT NULL,
    artifact_sha256 TEXT NOT NULL CHECK (length(artifact_sha256) = 64),
    host            TEXT NOT NULL,
    reported_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reported_by     TEXT NOT NULL CHECK (reported_by <> '')
);

CREATE INDEX deploy_reports_rung ON deploy_reports (project, environment, id);

CREATE TRIGGER deploy_reports_no_update BEFORE UPDATE ON deploy_reports
BEGIN
    SELECT RAISE(ABORT, 'deploy_reports is append-only: a report is an observation and is never edited');
END;

CREATE TRIGGER deploy_reports_no_delete BEFORE DELETE ON deploy_reports
BEGIN
    SELECT RAISE(ABORT, 'deploy_reports is append-only: a report is an observation and is never deleted');
END;
