-- Reverse 0016. LOSES every kept-backup record, every restore test, the lines
-- each promotion was checked against, and every report's and promotion's
-- build_url. The backups themselves are the app's and are untouched. Run this
-- only to test that it runs.

DROP TRIGGER IF EXISTS promotion_lines_no_delete;
DROP TRIGGER IF EXISTS promotion_lines_no_update;
DROP TRIGGER IF EXISTS restore_tests_no_delete;
DROP TRIGGER IF EXISTS restore_tests_no_update;
DROP TRIGGER IF EXISTS kept_backups_no_delete;
DROP TRIGGER IF EXISTS kept_backups_no_update;
DROP TABLE IF EXISTS promotion_lines;
DROP INDEX IF EXISTS restore_tests_evidence;
DROP TABLE IF EXISTS restore_tests;
DROP INDEX IF EXISTS kept_backups_line;
DROP TABLE IF EXISTS kept_backups;
ALTER TABLE promotions DROP COLUMN restore_gate;
ALTER TABLE promotions DROP COLUMN build_url;
ALTER TABLE deploy_reports DROP COLUMN build_url;
