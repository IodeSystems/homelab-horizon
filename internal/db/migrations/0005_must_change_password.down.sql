-- SQLite has supported DROP COLUMN since 3.35; modernc's driver is newer.
ALTER TABLE credentials DROP COLUMN must_change;
