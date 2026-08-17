DROP TABLE IF EXISTS password_history;
-- SQLite has supported DROP COLUMN since 3.35; modernc's build is newer.
ALTER TABLE users DROP COLUMN locked_until;
ALTER TABLE users DROP COLUMN last_failed_at;
ALTER TABLE users DROP COLUMN failed_attempts;
