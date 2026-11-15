-- The project joins the config address: (project, environment, app, role).
--
-- An environment belongs to exactly one project and environment names are
-- unique PER PROJECT, not globally — internal/config/config.go says so in the
-- type itself ("every project gets to have a prod"). So the triple
-- (environment, app, role) was never an identity; it only looked like one for
-- as long as exactly one project declared each app name. What has been
-- carrying the project all along is the APP coordinate, implicitly and by
-- luck, and internal/projection resolved it backwards by mapping app name ->
-- service -> project. Two projects that legitimately name an app the same
-- thing collapse onto one address.
--
-- That is a collision rather than a disclosure on its own, because an
-- environment key is minted per address. But `hz config key import` exists
-- precisely so one key CAN sit at several addresses, and the moment it does
-- the envelope's authenticated additional data is the only thing between the
-- two projects — and a three-field AAD does not name the project. One imported
-- key plus one identical triple is a clean cross-project read with every
-- authentication passing. configmgr's TestCrossProjectReadIsRefused is the
-- standing proof that it no longer is.
--
-- The model already said four. plan/architecture.md, plan/ui-redesign.md and
-- 0011's own header all write "an instance address is (project, environment,
-- app, role)" over a schema whose columns were three. This is the schema
-- catching up, not a new idea.
--
-- WHY THIS FILE RESTATES TABLES RATHER THAN ALTERING THEM, for the same two
-- SQLite reasons 0009 documents at length: a column inside a UNIQUE or a
-- PRIMARY KEY cannot be bolted on with ALTER TABLE, and every one of these
-- tables names an address column in a CHECK. Dropping a parent also runs an
-- implicit DELETE, which fires the foreign keys hanging off it, and
-- golang-migrate wraps each migration in a transaction where SQLite ignores
-- PRAGMA foreign_keys = OFF. So children are staged in constraint-free tables,
-- dropped, and restored.
--
-- The consequence worth stating plainly, exactly as 0009 stated it: after this
-- migration, THIS file is the current schema for cm_registrations, cm_configs
-- and cm_current_keys. 0009 and 0010 are history, and are immutable.
--
-- WHAT THIS MIGRATION DOES TO EXISTING DATA
--
-- There is NO correct value to backfill. The project is derivable only from
-- config.json's Services (svc.Name == app -> svc.Project), which is (a) the
-- exact backwards resolution this change exists to delete, (b) one-to-one only
-- while no two projects name an app the same, and (c) in a DIFFERENT STORE — a
-- SQL migration cannot read config.json. So this file does not guess.
--
-- * cm_registrations — EVERY ROW IS DELETED. Each row's wrapped_env_key is a
--   kind 0x02 envelope whose AAD no longer reproduces: re-addressing changes
--   the authenticated context, so the grant is dead bytes with the correct
--   private key in hand. A registration whose grant is dead is not approved,
--   whatever its state column says, and deleting is the truthful record. A box
--   re-registers at its next boot and re-enters 'pending', which is the
--   designed response to a new address. Nothing references cm_registrations.id
--   — cm_secret_reads names machine_id and config_id only — so no evidence is
--   orphaned. 0011's observed_version columns die with the row and are
--   re-reported at the next boot.
--
-- * cm_configs — ROWS ARE CARRIED FORWARD under the reserved project
--   'unmigrated', and their cm_config_values are TOMBSTONED. This is the one
--   place a sentinel earns its keep. Deleting the configs would fire ON DELETE
--   SET NULL into cm_secret_reads.config_id and blank the audit rows that name
--   them, and 0009 already ruled on exactly this trade: gutting evidence is the
--   worse trade. The bytes are worthless at any address — a kind 0x01 envelope
--   sealed under the old context does not open under the new one — so dropping
--   them costs nothing, and 'tombstoned' is the schema's own word for "the row,
--   its key name, its binding and its lineage survive; the bytes do not".
--   An 'awaiting' row is left exactly as it is: it never held bytes, and
--   tombstoning one would claim a destruction that did not happen.
--   'unmigrated' is a legal segment under the address charset, so nothing
--   refuses it, and it is never a live address because nothing will ever
--   register there. An operator may DELETE FROM cm_configs WHERE project =
--   'unmigrated' at leisure once the audit trail stops being interesting.
--
-- * cm_current_keys — EVERY ROW IS DELETED. The row names a key id at an
--   address that no longer exists. The key id itself is still valid and the key
--   material is still on disk — a key file carries no address, so the keystore
--   migration is a `mv` — so re-publish it with
--   `hz config key current <project>/<env>/<app>/<role> --set <keyid>` after
--   moving the tree. It is not carried forward under a sentinel because, unlike
--   a config, it is evidence of nothing: it is a live pointer, and a live
--   pointer at a dead address is a trap.
--
-- * cm_machines, cm_machine_secrets, cm_secret_reads — UNTOUCHED in content.
--   A machine-scoped secret is addressed by (machine id, key name); a machine
--   id is globally unique, so there is no project field to add and no envelope
--   to invalidate. cm_secret_reads is restated only because rebuilding
--   cm_configs means dropping what references it.
--
-- cm_machines.enrolled_project was considered and deliberately NOT added. It
-- would be the informational twin of enrolled_environment ("what did the box
-- CLAIM"), which is a real thing to want, but no client has ever sent one and a
-- column no writer fills is a column that lies by omission. It stays available
-- as a later migration.

-- Stage the children in constraint-free tables so the drops below cascade into
-- nothing. cm_config_values.source_config_id is ON DELETE RESTRICT against
-- cm_configs, so it must be gone before cm_configs is dropped or the implicit
-- DELETE aborts the migration.
CREATE TABLE _0013_configs AS SELECT * FROM cm_configs;
CREATE TABLE _0013_config_values AS SELECT * FROM cm_config_values;
CREATE TABLE _0013_secret_reads AS SELECT * FROM cm_secret_reads;

-- cm_registrations and cm_current_keys are staged nowhere: nothing about them
-- survives, and a staging table nobody reads is a promise this file does not
-- make.

DROP TABLE cm_secret_reads;
DROP TABLE cm_config_values;
DROP TABLE cm_current_keys;
DROP TABLE cm_registrations;
DROP TABLE cm_configs;

-- One row per (machine, project, environment, app, role) a box has ever booted
-- as, and the unit of admission. 0009's table with the project leading the
-- address, and 0011's observed_* columns folded in.
--
-- The tuple includes the project for the same reason 0009 gave for including
-- the environment: a launch flag cannot self-authorize, so a new coordinate is
-- a new tuple and re-enters pending. The project is stronger than a launch
-- flag — it is compiled into the client beside the app name — which is exactly
-- what makes authenticating it worth anything: the agent knows its project
-- independently of hz, so it is not feeding back a field hz chose for it.
CREATE TABLE cm_registrations (
    id                  TEXT PRIMARY KEY,
    machine_id          TEXT NOT NULL REFERENCES cm_machines (id) ON DELETE CASCADE,
    project             TEXT NOT NULL
        CHECK (project GLOB '[a-z0-9]*' AND project NOT GLOB '*[^a-z0-9_-]*'),
    environment         TEXT NOT NULL
        CHECK (environment GLOB '[a-z0-9]*' AND environment NOT GLOB '*[^a-z0-9_-]*'),
    app                 TEXT NOT NULL
        CHECK (app GLOB '[a-z0-9]*' AND app NOT GLOB '*[^a-z0-9_-]*'),
    role                TEXT NOT NULL
        CHECK (role GLOB '[a-z0-9]*' AND role NOT GLOB '*[^a-z0-9_-]*')
        CHECK (role NOT IN ('config', 'secret', 'local')),
    -- What the box was running when the tuple was FIRST seen — the version an
    -- admin reviewed, not a rolling "what runs now" counter.
    version             TEXT NOT NULL,
    state               TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'approved', 'denied')),
    -- This address's environment key, wrapped to the owning machine's
    -- public_key by the approver. Opaque to hz.
    wrapped_env_key     BLOB,
    wrap_key_id         TEXT,
    approved_by         TEXT REFERENCES users (id) ON DELETE SET NULL,
    approved_at         TIMESTAMP,
    denied_reason       TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP,
    -- 0011's rolling counters, restated: the version a box is actually
    -- RUNNING, beside the frozen reviewed one. All three stay nullable and
    -- carry no CHECK — absent is an honest answer and a build string is
    -- `git describe` output, not an ordering.
    observed_version    TEXT,
    observed_build      TEXT,
    observed_at         TIMESTAMP,
    UNIQUE (machine_id, project, environment, app, role),
    CHECK (state != 'approved' OR (wrapped_env_key IS NOT NULL AND wrap_key_id IS NOT NULL
        AND approved_at IS NOT NULL)),
    CHECK (state != 'denied' OR denied_reason IS NOT NULL),
    CHECK (state != 'pending' OR (wrapped_env_key IS NULL AND wrap_key_id IS NULL
        AND approved_by IS NULL AND approved_at IS NULL))
);

-- No separate index on machine_id: it leads the UNIQUE constraint above.
CREATE INDEX idx_cm_registrations_state ON cm_registrations (state, created_at);
CREATE INDEX idx_cm_registrations_address ON cm_registrations (project, environment, app, role);
CREATE INDEX idx_cm_registrations_approved_by ON cm_registrations (approved_by);

-- A blessed config for one (project, environment, app, role) address, valid
-- over a version range. 0009's table with the project leading.
--
-- seq remains the entire resolution rule and remains SQLite's own rowid,
-- assigned once by AUTOINCREMENT. The seq values of carried-forward rows are
-- preserved below, so ordering at the sentinel address is unchanged and no id
-- anybody has recorded goes stale.
CREATE TABLE cm_configs (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    id                  TEXT NOT NULL UNIQUE,
    project             TEXT NOT NULL
        CHECK (project GLOB '[a-z0-9]*' AND project NOT GLOB '*[^a-z0-9_-]*'),
    environment         TEXT NOT NULL
        CHECK (environment GLOB '[a-z0-9]*' AND environment NOT GLOB '*[^a-z0-9_-]*'),
    app                 TEXT NOT NULL
        CHECK (app GLOB '[a-z0-9]*' AND app NOT GLOB '*[^a-z0-9_-]*'),
    role                TEXT NOT NULL
        CHECK (role GLOB '[a-z0-9]*' AND role NOT GLOB '*[^a-z0-9_-]*')
        CHECK (role NOT IN ('config', 'secret', 'local')),
    min_ver             TEXT NOT NULL,
    -- NULL means open-ended. Exactly one config per address may be open-ended
    -- and that rule is enforced in Go (CreateConfig), NOT here — see 0009.
    max_ver             TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id)
);

CREATE INDEX idx_cm_configs_address ON cm_configs (project, environment, app, role, seq);
CREATE INDEX idx_cm_configs_created_by ON cm_configs (created_by);

-- 0012's cm_config_values, restated verbatim. It gains NO column: a value is
-- addressed by its config, and its config now carries the project. Restated
-- only because rebuilding cm_configs meant dropping what references it.
CREATE TABLE cm_config_values (
    config_id           TEXT NOT NULL REFERENCES cm_configs (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    binding             TEXT NOT NULL CHECK (binding IN ('invariant', 'env')),
    ciphertext          BLOB,
    key_id              TEXT,
    origin              TEXT NOT NULL CHECK (origin IN ('promoted', 'direct', 'awaiting')),
    source_config_id    TEXT REFERENCES cm_configs (id) ON DELETE RESTRICT,
    tombstoned_at       TIMESTAMP,
    tombstoned_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (config_id, key),
    CHECK (origin != 'promoted' OR source_config_id IS NOT NULL),
    CHECK (origin != 'direct' OR source_config_id IS NULL),
    CHECK (source_config_id IS NULL OR source_config_id != config_id),
    CHECK (origin != 'awaiting' OR (
        ciphertext IS NULL
        AND key_id IS NULL
        AND tombstoned_at IS NULL
        AND source_config_id IS NOT NULL
        AND binding = 'env'
    )),
    CHECK (origin = 'awaiting' OR key_id IS NOT NULL),
    CHECK (
        (ciphertext IS NOT NULL AND length(ciphertext) > 0 AND tombstoned_at IS NULL)
        OR (ciphertext IS NULL AND tombstoned_at IS NOT NULL)
        OR (ciphertext IS NULL AND tombstoned_at IS NULL AND origin = 'awaiting')
    ),
    CHECK (tombstoned_by IS NULL OR tombstoned_at IS NOT NULL)
);

CREATE INDEX idx_cm_config_values_source ON cm_config_values (source_config_id);

-- 0009's cm_secret_reads, restated verbatim. Evidence must outlive the thing it
-- is evidence about, which is why machine_id and config_id are nullable and ON
-- DELETE SET NULL rather than CASCADE.
CREATE TABLE cm_secret_reads (
    id                  TEXT PRIMARY KEY,
    at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor               TEXT NOT NULL,
    machine_id          TEXT REFERENCES cm_machines (id) ON DELETE SET NULL,
    config_id           TEXT REFERENCES cm_configs (id) ON DELETE SET NULL,
    secret_key          TEXT NOT NULL,
    source_ip           TEXT
);

CREATE INDEX idx_cm_secret_reads_machine ON cm_secret_reads (machine_id);
CREATE INDEX idx_cm_secret_reads_config ON cm_secret_reads (config_id);
CREATE INDEX idx_cm_secret_reads_at ON cm_secret_reads (at);

-- 0010's cm_current_keys with the project leading the primary key. Still one
-- row per address, still an ID and never a key, still advisory — a client
-- refuses to SEAL when its local key disagrees and only warns when OPENING, so
-- that a compromised hz cannot pin the fleet to a key it had already stolen.
CREATE TABLE cm_current_keys (
    project     TEXT NOT NULL
        CHECK (project GLOB '[a-z0-9]*'
           AND project NOT GLOB '*[^a-z0-9_-]*'),
    environment TEXT NOT NULL
        CHECK (environment GLOB '[a-z0-9]*'
           AND environment NOT GLOB '*[^a-z0-9_-]*'),
    app         TEXT NOT NULL
        CHECK (app GLOB '[a-z0-9]*'
           AND app NOT GLOB '*[^a-z0-9_-]*'),
    role        TEXT NOT NULL
        CHECK (role GLOB '[a-z0-9]*'
           AND role NOT GLOB '*[^a-z0-9_-]*'
           AND role NOT IN ('config', 'secret', 'local')),

    -- The key id as the envelope carries it: lowercase hex, fixed width.
    key_id      TEXT NOT NULL
        CHECK (length(key_id) = 16 AND key_id NOT GLOB '*[^0-9a-f]*'),

    -- ON DELETE SET NULL, not CASCADE: removing an operator must not silently
    -- unset the fleet's current key for an address.
    set_by      TEXT REFERENCES users (id) ON DELETE SET NULL,
    set_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (project, environment, app, role)
);

-- Restore, parents before children.
--
-- Configs keep their seq, their id, their range and their authorship, and land
-- at the sentinel project. Their environment, app and role are kept verbatim so
-- an operator reading the audit trail can still see which rung the evidence
-- came from.
INSERT INTO cm_configs (seq, id, project, environment, app, role, min_ver, max_ver,
                        created_at, created_by)
SELECT seq, id, 'unmigrated', environment, app, role, min_ver, max_ver,
       created_at, created_by
FROM _0013_configs;

-- Every value that held bytes is tombstoned: the bytes no longer open at any
-- address, and the row is what the audit trail names. An already-tombstoned row
-- keeps its own tombstoned_at and tombstoned_by — this migration did not
-- destroy it, and overwriting them would rewrite who did.
--
-- An 'awaiting' row passes through untouched. It never held bytes, so there is
-- nothing to destroy, and the awaiting CHECK requires tombstoned_at IS NULL.
--
-- tombstoned_by is NULL on a row this migration tombstoned: no user did it.
INSERT INTO cm_config_values
    (config_id, key, binding, ciphertext, key_id, origin, source_config_id,
     tombstoned_at, tombstoned_by)
SELECT config_id, key, binding,
       NULL,
       key_id,
       origin,
       source_config_id,
       CASE WHEN origin = 'awaiting' THEN NULL
            ELSE COALESCE(tombstoned_at, CURRENT_TIMESTAMP) END,
       tombstoned_by
FROM _0013_config_values;

INSERT INTO cm_secret_reads (id, at, actor, machine_id, config_id, secret_key, source_ip)
SELECT id, at, actor, machine_id, config_id, secret_key, source_ip
FROM _0013_secret_reads;

DROP TABLE _0013_secret_reads;
DROP TABLE _0013_config_values;
DROP TABLE _0013_configs;
