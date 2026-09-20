-- Reverse 0011: back to 0009's two-state cm_config_values.
--
-- This LOSES state, and the loss is a fact about 0009's schema rather than a
-- shortcut taken here: 0009 cannot represent a declared-but-unanswered key at
-- all, so every awaiting row is DELETED. A config that was promoted and never
-- answered comes back looking as though those keys were never declared — which
-- is precisely the absent-vs-empty confusion 0011 exists to remove. Run this
-- only to test that it runs.

CREATE TABLE cm_config_values_0009 (
    config_id           TEXT NOT NULL REFERENCES cm_configs (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    binding             TEXT NOT NULL CHECK (binding IN ('invariant', 'env')),
    ciphertext          BLOB,
    key_id              TEXT NOT NULL,
    origin              TEXT NOT NULL CHECK (origin IN ('promoted', 'direct')),
    source_config_id    TEXT REFERENCES cm_configs (id) ON DELETE RESTRICT,
    tombstoned_at       TIMESTAMP,
    tombstoned_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (config_id, key),
    CHECK (origin != 'promoted' OR source_config_id IS NOT NULL),
    CHECK (origin != 'direct' OR source_config_id IS NULL),
    CHECK (source_config_id IS NULL OR source_config_id != config_id),
    CHECK (
        (ciphertext IS NOT NULL AND length(ciphertext) > 0 AND tombstoned_at IS NULL)
        OR
        (ciphertext IS NULL AND tombstoned_at IS NOT NULL)
    ),
    CHECK (tombstoned_by IS NULL OR tombstoned_at IS NOT NULL)
);

INSERT INTO cm_config_values_0009
    (config_id, key, binding, ciphertext, key_id, origin, source_config_id,
     tombstoned_at, tombstoned_by)
SELECT config_id, key, binding, ciphertext, key_id, origin, source_config_id,
       tombstoned_at, tombstoned_by
FROM cm_config_values
WHERE origin != 'awaiting';

DROP TABLE cm_config_values;

ALTER TABLE cm_config_values_0009 RENAME TO cm_config_values;

CREATE INDEX idx_cm_config_values_source ON cm_config_values (source_config_id);
