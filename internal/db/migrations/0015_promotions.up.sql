-- Who promoted what, when — the record PCI asks for (plan/plan.md, "PCI
-- evidence": "the promotion record").
--
-- plan/plan.md step H2. One row per successful `hz env promote`. The row PINS
-- the artifact: artifact_sha256 is the one the source rung last reported for
-- this version at promote time, so a bundle rebuilt afterwards under the same
-- version string does not match it, and `GET /api/v1/deploys/check` (H3)
-- refuses it.
--
-- downgrade is 1 when --allow-downgrade was NEEDED: the target's declared
-- version was higher, or could not be compared. A rollback is legitimate; it is
-- recorded as one rather than hidden.
--
-- promoted_by is TEXT — the attribution string the audit log already uses
-- (server.adminActor): "user:carl (token:ci-deploy)", "vpn-admin:<peer>", or
-- "admin-token" for the shared credential that names nobody. Not a foreign key,
-- so removing a user later does not take the record with them.
--
-- Append-only, by trigger, for the reason 0014 gives.

CREATE TABLE promotions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT NOT NULL CHECK (project <> ''),
    from_env        TEXT NOT NULL CHECK (from_env <> ''),
    to_env          TEXT NOT NULL CHECK (to_env <> ''),
    version         TEXT NOT NULL CHECK (version <> ''),
    artifact_sha256 TEXT NOT NULL CHECK (length(artifact_sha256) = 64),
    promoted_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    promoted_by     TEXT NOT NULL CHECK (promoted_by <> ''),
    downgrade       INTEGER NOT NULL DEFAULT 0 CHECK (downgrade IN (0, 1))
);

CREATE INDEX promotions_target ON promotions (project, to_env, version, id);

CREATE TRIGGER promotions_no_update BEFORE UPDATE ON promotions
BEGIN
    SELECT RAISE(ABORT, 'promotions is append-only: the promotion record is never edited');
END;

CREATE TRIGGER promotions_no_delete BEFORE DELETE ON promotions
BEGIN
    SELECT RAISE(ABORT, 'promotions is append-only: the promotion record is never deleted');
END;
