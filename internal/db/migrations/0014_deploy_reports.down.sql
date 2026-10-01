-- Reverse 0014. LOSES every deploy report, and nothing else knows them: the next
-- deploy writes a new one, but the history of what each rung ran is gone. Run
-- this only to test that it runs.

DROP TRIGGER IF EXISTS deploy_reports_no_delete;
DROP TRIGGER IF EXISTS deploy_reports_no_update;
DROP INDEX IF EXISTS deploy_reports_rung;
DROP TABLE IF EXISTS deploy_reports;
