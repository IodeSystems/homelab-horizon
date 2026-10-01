-- Reverse 0017. LOSES the apply record, every hold, every upload and deletion
-- record, and the last-pull times. The artifact FILES on disk are untouched.
-- Run this only to test that it runs.

DROP TRIGGER IF EXISTS rung_holds_no_delete;
DROP TRIGGER IF EXISTS rung_holds_no_update;
DROP TRIGGER IF EXISTS applies_no_delete;
DROP TRIGGER IF EXISTS applies_no_update;
DROP TRIGGER IF EXISTS artifact_events_no_delete;
DROP TRIGGER IF EXISTS artifact_events_no_update;
DROP TRIGGER IF EXISTS artifacts_no_delete;
DROP TRIGGER IF EXISTS artifacts_no_update;
DROP TABLE IF EXISTS instance_pulls;
DROP INDEX IF EXISTS rung_holds_rung;
DROP TABLE IF EXISTS rung_holds;
DROP INDEX IF EXISTS applies_rung;
DROP TABLE IF EXISTS applies;
DROP INDEX IF EXISTS artifact_events_sha;
DROP TABLE IF EXISTS artifact_events;
DROP TABLE IF EXISTS artifacts;
