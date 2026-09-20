-- Reverse 0011.
--
-- The loss is bounded and self-healing, which is not true of every down
-- migration in this directory: these columns hold a CACHE of what boxes report,
-- and the next register or resolve from each box writes its own row back. Going
-- down costs one report cycle, not a fact nothing else knows.
--
-- Three plain DROP COLUMNs are legal here only because none of the three is
-- named in a CHECK constraint — SQLite refuses to drop a column any CHECK
-- mentions, which is exactly why 0009 had to rebuild every cm_ table instead of
-- altering one. Keeping these columns constraint-free was the point of
-- "observed_build is never compared" in the up migration; this is where that
-- pays.

ALTER TABLE cm_registrations DROP COLUMN observed_at;
ALTER TABLE cm_registrations DROP COLUMN observed_build;
ALTER TABLE cm_registrations DROP COLUMN observed_version;
