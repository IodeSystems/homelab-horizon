-- Reverse 0015. LOSES the promotion record — the who/what/when PCI evidence —
-- and nothing else holds it. Run this only to test that it runs.

DROP TRIGGER IF EXISTS promotions_no_delete;
DROP TRIGGER IF EXISTS promotions_no_update;
DROP INDEX IF EXISTS promotions_target;
DROP TABLE IF EXISTS promotions;
