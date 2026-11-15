-- Reverse 0018. LOSES every build record, every project's next build number
-- (a re-run of 0018 then re-seeds from the recorded versions), the grandfathered
-- list (a re-run would grandfather every artifact then present) and each
-- promotion's provenance. The log files on disk are untouched. Run this only to
-- test that it runs.

ALTER TABLE promotions DROP COLUMN provenance;
ALTER TABLE promotions DROP COLUMN build_id;
DROP TRIGGER IF EXISTS artifact_grandfathered_no_delete;
DROP TRIGGER IF EXISTS artifact_grandfathered_no_update;
DROP TRIGGER IF EXISTS artifact_grandfathered_closed;
DROP TABLE IF EXISTS artifact_grandfathered;
DROP TRIGGER IF EXISTS builds_no_delete;
DROP TRIGGER IF EXISTS builds_finish_is_final;
DROP TRIGGER IF EXISTS builds_identity_fixed;
DROP INDEX IF EXISTS builds_artifact;
DROP TABLE IF EXISTS builds;
DROP TRIGGER IF EXISTS build_sequences_no_delete;
DROP TRIGGER IF EXISTS build_sequences_never_lower;
DROP TABLE IF EXISTS build_sequences;
