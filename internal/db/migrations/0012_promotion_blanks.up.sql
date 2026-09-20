-- A promoted config can declare a key it has no value for: "awaiting".
--
-- This is the schema half of plan/architecture.md "Definition of done" step 6 —
-- promoting staging → prod copies invariants and BLANKS environment-bound keys,
-- which must be re-answered in the target before anything there can boot.
--
-- WHY A THIRD STATE RATHER THAN OMITTING THE KEY. Goal property 6 is that a
-- config key cannot be SILENTLY ABSENT, and the founding bug is an empty
-- BACKUP_BUCKET selecting the production bucket because absent and empty could
-- not be told apart. A promotion that simply left PUBLIC_URL out of prod's
-- config would rebuild exactly that: prod would hold a config in which the key
-- had never been heard of, indistinguishable from a key nobody ever declared,
-- and the app would fall back to its compiled default. So the blank is a ROW:
-- the key name is declared, its binding is recorded, its provenance names the
-- promotion that declared it, and it carries no bytes and never did.
--
-- Three states for one row, and they are mutually exclusive:
--
--   live        ciphertext present, key_id names the key that sealed it
--   tombstoned  bytes destroyed after the fact; row and lineage survive
--   awaiting    declared by a promotion, never answered; no bytes ever existed
--
-- "awaiting" is NOT a tombstone with a different name. A tombstone says a value
-- existed here and was destroyed; awaiting says a value is required here and was
-- never supplied. Reading one as the other would either invent a destruction
-- that never happened or hide a key nobody has answered, and both are the kind
-- of quiet wrongness this table's CHECKs exist to refuse.
--
-- Only an environment-bound key can be awaiting. An invariant promotes by
-- client-side re-seal, so a promotion that left one blank would have dropped a
-- value it was holding rather than declined to carry one it never had.
--
-- key_id becomes NULLABLE, which is the one column change and the reason this
-- file rebuilds the table rather than ALTERing it (SQLite cannot relax NOT NULL
-- in place). An awaiting row has no sealing key because it has nothing sealed;
-- a '' key_id would be the founding bug again, one level down.
--
-- Nothing hangs off cm_config_values — no table references it — so rebuilding it
-- cascades into nothing, unlike 0009's cm_machines.

CREATE TABLE cm_config_values_0011 (
    config_id           TEXT NOT NULL REFERENCES cm_configs (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    binding             TEXT NOT NULL CHECK (binding IN ('invariant', 'env')),
    -- The sealed envelope. NULL once tombstoned, and NULL while awaiting.
    ciphertext          BLOB,
    -- Which environment key sealed it. Kept through a tombstone: it is
    -- provenance, not payload. NULL only while awaiting, where there is no
    -- sealing key to name.
    key_id              TEXT,
    origin              TEXT NOT NULL CHECK (origin IN ('promoted', 'direct', 'awaiting')),
    source_config_id    TEXT REFERENCES cm_configs (id) ON DELETE RESTRICT,
    tombstoned_at       TIMESTAMP,
    tombstoned_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (config_id, key),

    -- Lineage, unchanged from 0009: a promoted value names where it came from,
    -- a direct one was set here, and nothing is promoted from itself.
    CHECK (origin != 'promoted' OR source_config_id IS NOT NULL),
    CHECK (origin != 'direct' OR source_config_id IS NULL),
    CHECK (source_config_id IS NULL OR source_config_id != config_id),

    -- An awaiting row is a declaration and nothing else: no bytes, no sealing
    -- key, not a tombstone, environment-bound, and it names the promotion that
    -- declared it so "who left this blank" is answerable from the row.
    CHECK (origin != 'awaiting' OR (
        ciphertext IS NULL
        AND key_id IS NULL
        AND tombstoned_at IS NULL
        AND source_config_id IS NOT NULL
        AND binding = 'env'
    )),
    -- Everything that is not awaiting names the key involved, live or destroyed.
    CHECK (origin = 'awaiting' OR key_id IS NOT NULL),

    -- The payload states. A zero-length blob is none of them: it is not an
    -- envelope.
    CHECK (
        (ciphertext IS NOT NULL AND length(ciphertext) > 0 AND tombstoned_at IS NULL)
        OR (ciphertext IS NULL AND tombstoned_at IS NOT NULL)
        OR (ciphertext IS NULL AND tombstoned_at IS NULL AND origin = 'awaiting')
    ),
    CHECK (tombstoned_by IS NULL OR tombstoned_at IS NOT NULL)
);

INSERT INTO cm_config_values_0011
    (config_id, key, binding, ciphertext, key_id, origin, source_config_id,
     tombstoned_at, tombstoned_by)
SELECT config_id, key, binding, ciphertext, key_id, origin, source_config_id,
       tombstoned_at, tombstoned_by
FROM cm_config_values;

DROP TABLE cm_config_values;

ALTER TABLE cm_config_values_0011 RENAME TO cm_config_values;

CREATE INDEX idx_cm_config_values_source ON cm_config_values (source_config_id);
