-- Config manager, corrected: the address is the unit of admission, and no
-- value is plaintext.
--
-- This file restates EVERY cm_ table rather than patching a few columns, for
-- two SQLite reasons that leave no cheaper option. SQLite cannot drop a column
-- named in a CHECK constraint, and every column moving out of cm_machines is
-- named in one. SQLite also cannot drop a parent table without running an
-- implicit DELETE first, which fires ON DELETE CASCADE into cm_registrations
-- and cm_machine_secrets and ON DELETE SET NULL into cm_secret_reads — so
-- rebuilding cm_machines means every table hanging off it is dropped and
-- restored in the same breath. The usual escape (PRAGMA foreign_keys = OFF)
-- is unavailable because golang-migrate wraps each migration in a transaction
-- and SQLite ignores that pragma inside one.
--
-- The consequence worth stating plainly: after this migration, THIS file is
-- the current config-manager schema. 0008 is history, and is immutable.
--
-- Five changes, each one a correction rather than an addition.
--
-- 1. ENVIRONMENT JOINS THE REGISTRATION TUPLE. 0008's
--    UNIQUE (machine_id, app, role) meant a box restarted with a different
--    --env hit the existing row and bumped last_seen_at, so a mistyped
--    environment never re-entered pending. plan/config-manager.md claims the
--    design fails closed on exactly that mistake; until now the claim was
--    false. The tuple is (machine_id, environment, app, role).
--
-- 2. THE WRAPPED KEY MOVES FROM THE MACHINE TO THE REGISTRATION, and the
--    approval state moves with it. The key address is exactly the config
--    address (environment, app, role), and one box may run several, so one
--    wrapped key per machine was wrong on both counts. Approval is a
--    cryptographic capability grant, not a flag; the grant therefore has to
--    sit on the thing it is a grant FOR. This also settles a contradiction
--    0008 recorded in the other direction: 0008's comment said admission was
--    per machine so a role change needed no approval workflow, while
--    plan/config-manager.md and configmgr/types.go said a role change
--    re-enters pending. The plan wins, unavoidably — a new address needs a new
--    key, and handing over a key is an approval ceremony whatever it is
--    called. The schema now expresses it instead of contradicting it.
--
-- 3. NO VALUE IS PLAINTEXT. cm_config_values.value is gone and the CHECK is
--    inverted: every value carries ciphertext, none carries plaintext. binding
--    narrows to invariant | env because secrecy stops being an axis when
--    everything is secret; what remains is promotion scope alone. A plaintext
--    value also carried no integrity protection at all, so this is a security
--    change and not only a policy one — hz can no longer substitute, swap or
--    invent any value.
--
-- 4. LINEAGE LIVES ON THE VALUE. A prod config is a mix by design: invariants
--    arrive by promotion while environment-bound keys are bound fresh in prod,
--    so origin belongs per key and not per config. The link is the source
--    config's id alone and never a copied seq, which would be a second source
--    of truth able to disagree with the first.
--
-- 5. A TOMBSTONE KEEPS THE ROW AND DROPS THE BYTES. Values are append-only and
--    lineage must survive, but append-only fights revocation, so lineage is
--    append-only while payloads are destructible.
--
--    THE SCHEMA CANNOT MAKE A TOMBSTONE A REVOCATION BY ITSELF. Superseded
--    configs at the same address hold ciphertext that opens under the same
--    key, so destroying the current value's bytes and stopping there leaves
--    the secret readable from the row behind it. A real revocation sweeps
--    EVERY config at the address. That is a caller's job — no CHECK can see
--    across rows — and internal/db/configmgr.go's TombstoneValueAtAddress is
--    where it is implemented.
--
-- WHAT THIS MIGRATION DOES TO EXISTING 0008 DATA:
--
-- * Plaintext values CANNOT be carried forward. hz holds no key, so it cannot
--   seal them, which is the entire point of the no-plaintext decision. Every
--   row with binding 'invariant' or 'env' is DISCARDED; only 'secret' rows,
--   which already carry ciphertext, survive, and they arrive as binding 'env'
--   because a 0008 secret was environment-bound by definition. A config left
--   with no values is left standing rather than deleted: deleting it would
--   run ON DELETE SET NULL over cm_secret_reads and blank the audit rows that
--   name it, and gutting evidence is the worse trade. Re-bless such configs
--   from a client that holds the key.
-- * Each machine's single grant is copied down onto every registration it
--   owns, which is the honest reading of the old model — one key per machine
--   meant every role on that machine shared it. A machine with no
--   registrations has no address to hold a grant and therefore keeps none; it
--   re-enters pending at its next boot, which is the correct outcome for a box
--   nobody has a record of ever running anything.
-- * Addresses are folded to lower case on the way in. A row that cannot be
--   folded into the charset below (or whose folding collides with another
--   row, or whose role is one of the reserved names) fails this migration
--   loudly. That is deliberate: an address hz cannot canonicalise is an
--   address whose AAD and keystore path nobody can reproduce.

-- Stage everything that is about to be dropped, in tables with no constraints
-- and no foreign keys, so the drops below cascade into nothing.
CREATE TABLE _0009_machines AS SELECT * FROM cm_machines;

-- The environment a registration gets is the one its machine enrolled with,
-- because in 0008 that was the only environment a box had.
CREATE TABLE _0009_registrations AS
SELECT r.id                AS id,
       r.machine_id        AS machine_id,
       m.environment       AS environment,
       r.app               AS app,
       r.role              AS role,
       r.version           AS version,
       m.state             AS state,
       m.wrapped_env_key   AS wrapped_env_key,
       m.wrap_key_id       AS wrap_key_id,
       m.approved_by       AS approved_by,
       m.approved_at       AS approved_at,
       m.denied_reason     AS denied_reason,
       r.created_at        AS created_at,
       r.last_seen_at      AS last_seen_at
FROM cm_registrations r
JOIN cm_machines m ON m.id = r.machine_id;

CREATE TABLE _0009_configs AS SELECT * FROM cm_configs;

-- Only sealed values survive; see the note above.
CREATE TABLE _0009_config_values AS
SELECT config_id, key, ciphertext, key_id FROM cm_config_values WHERE binding = 'secret';

CREATE TABLE _0009_machine_secrets AS SELECT * FROM cm_machine_secrets;
CREATE TABLE _0009_secret_reads AS SELECT * FROM cm_secret_reads;

DROP TABLE cm_secret_reads;
DROP TABLE cm_config_values;
DROP TABLE cm_machine_secrets;
DROP TABLE cm_registrations;
DROP TABLE cm_configs;
DROP TABLE cm_machines;

-- A box's identity, and nothing else. One row per machine, one keypair per
-- machine, independent of how many addresses it later registers.
--
-- Admission state, the wrapped key and the approver are NOT here any more —
-- they belong to a registration, because they belong to an address. What is
-- left is what a machine actually owns: a name, a public key whose private
-- half never leaves the box, and the times it was first and last seen.
--
-- enrolled_environment is 0008's `environment` under a name that says what it
-- now is. It was kept rather than dropped because it is a true record of what
-- the box CLAIMED when it enrolled, and a box whose registrations are for a
-- different environment than it enrolled with is a signal worth showing in the
-- approval queue (see plan/config-manager.md "machine-name squatting"). It is
-- NOT authoritative and nothing resolves against it: the environment that
-- decides which config and which key a process gets is the one in
-- cm_registrations. The rename is the point — a call site reading
-- `enrolled_environment` cannot mistake it for the live one.
CREATE TABLE cm_machines (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    enrolled_environment    TEXT NOT NULL
        CHECK (enrolled_environment GLOB '[a-z0-9]*'
           AND enrolled_environment NOT GLOB '*[^a-z0-9_-]*'),
    -- Generated by the agent at registration and NOT the WireGuard key:
    -- reusing key material across protocols is a cheap way to be wrong later,
    -- and this key's job (receiving wrapped secrets) is unrelated to
    -- WireGuard's.
    public_key              BLOB NOT NULL,
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at            TIMESTAMP
);

CREATE UNIQUE INDEX idx_cm_machines_name ON cm_machines (name);

-- One row per (machine, environment, app, role) a box has ever booted as, and
-- the unit of admission.
--
-- The tuple includes environment, and that is the whole correction. A launch
-- flag cannot self-authorize: a new --env or a new --serviceName is a new
-- tuple, so it re-enters pending, and only an approval delivers that address's
-- key. Later boots of a known tuple touch last_seen_at and nothing else — they
-- must never re-litigate approval, or a 3am OOM-restart on prod would block on
-- a human.
--
-- The grant lives here because the key address IS the config address. Approval
-- means an approver wrapped that address's environment key to this machine's
-- public key; hz relays and stores the blob and cannot open it, at rest or in
-- flight. An unapproved registration holds no key and can decrypt nothing even
-- if it captured every row in this database — there is no gate to bypass, only
-- a key that was never handed over.
--
-- The charset CHECKs are the real canonicalisation. Without them
-- `Prod/redline/app` and `prod/redline/app` are different addresses with
-- different AADs and different keystore paths, and nothing folds them.
-- internal/db/configmgr.go folds case before writing; this refuses anything
-- that reaches the table by another route. The reserved role names are
-- refused for a separate reason: redline's config files are named
-- `<role>.config.properties`, so a role called `config`, `secret` or `local`
-- stops that parsing, and `local` in particular names the one file that must
-- never be pushed.
--
-- approved_by is nullable even for an approved row, and the CHECK below does
-- not require it. The grant is the wrapped key; the approver's name is
-- evidence, and ON DELETE SET NULL can take it away when a user is deleted.
-- Requiring it would turn "delete this user" into an unexplainable constraint
-- failure years after the approval.
CREATE TABLE cm_registrations (
    id                  TEXT PRIMARY KEY,
    machine_id          TEXT NOT NULL REFERENCES cm_machines (id) ON DELETE CASCADE,
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
    -- public_key by the approver. Opaque to hz; see the table comment.
    wrapped_env_key     BLOB,
    -- Which environment key was used, so rotation can be gradual: re-wrap and
    -- bump this rather than swap the fleet atomically.
    wrap_key_id         TEXT,
    approved_by         TEXT REFERENCES users (id) ON DELETE SET NULL,
    approved_at         TIMESTAMP,
    denied_reason       TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP,
    UNIQUE (machine_id, environment, app, role),
    -- An approved registration carries the grant that makes it approved; a
    -- denied one carries why; a pending one carries neither. Enforced here and
    -- not only in Go, because the point of this feature is that a wrong row is
    -- refused rather than trusted.
    CHECK (state != 'approved' OR (wrapped_env_key IS NOT NULL AND wrap_key_id IS NOT NULL
        AND approved_at IS NOT NULL)),
    CHECK (state != 'denied' OR denied_reason IS NOT NULL),
    CHECK (state != 'pending' OR (wrapped_env_key IS NULL AND wrap_key_id IS NULL
        AND approved_by IS NULL AND approved_at IS NULL))
);

-- No separate index on machine_id: it leads the UNIQUE constraint above, which
-- SQLite already indexes.
CREATE INDEX idx_cm_registrations_state ON cm_registrations (state, created_at);
CREATE INDEX idx_cm_registrations_address ON cm_registrations (environment, app, role);
CREATE INDEX idx_cm_registrations_approved_by ON cm_registrations (approved_by);

-- A blessed config for one (environment, app, role) address, valid over a
-- version range. Unchanged from 0008 except for the address CHECKs, which are
-- here for the same reason as on cm_registrations: a config address and a key
-- address are the same string, and two spellings of it are two keystore paths.
--
-- seq is the ENTIRE resolution rule, and it is SQLite's own rowid, assigned
-- once by AUTOINCREMENT and never touched again — never "most recently
-- modified", which is exactly the bug the design names: if re-blessing an old
-- config could touch its ordering, promoting a rollback fix would silently
-- take over from a newer config nobody re-touched. AUTOINCREMENT gives that
-- atomically, including against a concurrent writer, which a MAX(seq)+1 in Go
-- cannot promise. `id` stays the ashid used everywhere else as the public
-- identifier; seq is an ordering key and is never exposed as anything else.
--
-- Ranges are immutable after insert — there is deliberately no UPDATE path in
-- Go for min_ver/max_ver/seq.
CREATE TABLE cm_configs (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    id                  TEXT NOT NULL UNIQUE,
    environment         TEXT NOT NULL
        CHECK (environment GLOB '[a-z0-9]*' AND environment NOT GLOB '*[^a-z0-9_-]*'),
    app                 TEXT NOT NULL
        CHECK (app GLOB '[a-z0-9]*' AND app NOT GLOB '*[^a-z0-9_-]*'),
    role                TEXT NOT NULL
        CHECK (role GLOB '[a-z0-9]*' AND role NOT GLOB '*[^a-z0-9_-]*')
        CHECK (role NOT IN ('config', 'secret', 'local')),
    min_ver             TEXT NOT NULL,
    -- NULL means open-ended. Exactly one config per address may be open-ended
    -- and that rule is enforced in Go (CreateConfig), NOT here. A partial
    -- unique index would express it perfectly and would also make this
    -- migration refuse to apply to any database already holding a pair 0008
    -- permitted — and a migration that can refuse to apply is a worse failure
    -- than a check at the single write path that creates these rows.
    max_ver             TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id)
);

CREATE INDEX idx_cm_configs_address ON cm_configs (environment, app, role, seq);
CREATE INDEX idx_cm_configs_created_by ON cm_configs (created_by);

-- The per-key contents of a config. Every row is sealed; there is no plaintext
-- column to populate, and hz reads nothing here.
--
-- binding is promotion scope and only that: an invariant promotes by
-- client-side re-seal, an env-bound value must already be bound in the target.
-- The old third value, 'secret', is gone because secrecy is no longer an axis.
-- What hz keeps is key NAMES and BINDINGS, which is exactly what the promotion
-- gate needs — it only ever asks whether a key is bound, never what it holds.
-- That key names leak is the deliberate limit of the property.
--
-- origin and source_config_id are the lineage. A direct set after a promotion
-- is not a silent difference: it is a new value with `direct` origin, in a new
-- config, superseding by seq, so drift is read off the graph rather than
-- inferred by comparison. source_config_id is the source config's ID ALONE —
-- copying its seq here would be a second source of truth able to disagree with
-- the first. ON DELETE RESTRICT rather than SET NULL because lineage is
-- append-only: a config that is somebody's provenance cannot be quietly
-- deleted out from under the value that names it.
--
-- tombstoned_at is the destructible half. The row, its key name, its binding
-- and its lineage survive a tombstone; the bytes do not. See the header for
-- why a tombstone is only a revocation when the whole address is swept.
--
-- An INTENTIONALLY EMPTY value is representable and always was the thing 0008
-- got wrong by rejecting an empty plaintext: an operator who needs an empty
-- value had to omit the key instead, at which point the app falls back to its
-- compiled default — verbatim the founding bug this whole feature exists to
-- prevent. There is now no emptiness rule anywhere, because hz cannot see
-- plaintext at all: sealing "" yields an ordinary envelope like any other.
-- What IS refused is omission — a row with no ciphertext and no tombstone.
CREATE TABLE cm_config_values (
    config_id           TEXT NOT NULL REFERENCES cm_configs (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    binding             TEXT NOT NULL CHECK (binding IN ('invariant', 'env')),
    -- The sealed envelope. NULL only once tombstoned.
    ciphertext          BLOB,
    -- Which environment key sealed it. Kept through a tombstone: it is
    -- provenance, not payload, and it names a key that by then opens nothing.
    key_id              TEXT NOT NULL,
    origin              TEXT NOT NULL CHECK (origin IN ('promoted', 'direct')),
    source_config_id    TEXT REFERENCES cm_configs (id) ON DELETE RESTRICT,
    tombstoned_at       TIMESTAMP,
    tombstoned_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (config_id, key),
    CHECK (origin != 'promoted' OR source_config_id IS NOT NULL),
    CHECK (origin != 'direct' OR source_config_id IS NULL),
    -- A value promoted from itself is not lineage, it is a loop.
    CHECK (source_config_id IS NULL OR source_config_id != config_id),
    -- A live value has bytes; a tombstoned one has none and keeps its row. A
    -- zero-length blob is neither: it is not an envelope.
    CHECK (
        (ciphertext IS NOT NULL AND length(ciphertext) > 0 AND tombstoned_at IS NULL)
        OR
        (ciphertext IS NULL AND tombstoned_at IS NOT NULL)
    ),
    CHECK (tombstoned_by IS NULL OR tombstoned_at IS NOT NULL)
);

-- No separate index on config_id: it leads the primary key above.
CREATE INDEX idx_cm_config_values_source ON cm_config_values (source_config_id);

-- A secret scoped to one MACHINE rather than one address — the one narrowing
-- the model makes, because per-device revocation is a real requirement an
-- environment-wide key cannot express. Sealed directly to the owning machine's
-- public_key, so the environment key is never in the path: setting one needs
-- only a public key hz already publishes. Unchanged from 0008; restated here
-- only because rebuilding cm_machines meant dropping everything that hangs off
-- it.
CREATE TABLE cm_machine_secrets (
    id                  TEXT PRIMARY KEY,
    machine_id          TEXT NOT NULL REFERENCES cm_machines (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    ciphertext          BLOB NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id),
    UNIQUE (machine_id, key)
);

-- No separate index on machine_id: it leads the UNIQUE constraint above.
CREATE INDEX idx_cm_machine_secrets_created_by ON cm_machine_secrets (created_by);

-- Audit trail for ciphertext relays. hz never sees plaintext, so a row here
-- records that a RELAY happened, not a decrypt — it cannot produce a decrypt
-- log. Unchanged from 0008; restated for the same reason as the table above.
--
-- machine_id and config_id are nullable and ON DELETE SET NULL rather than
-- CASCADE: this table is evidence, and evidence must outlive the thing it is
-- evidence about.
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

-- Restore, parents before children.
INSERT INTO cm_machines (id, name, enrolled_environment, public_key, created_at, last_seen_at)
SELECT id, name, lower(trim(environment)), public_key, created_at, last_seen_at
FROM _0009_machines;

INSERT INTO cm_configs (seq, id, environment, app, role, min_ver, max_ver, created_at, created_by)
SELECT seq, id, lower(trim(environment)), lower(trim(app)), lower(trim(role)),
       min_ver, max_ver, created_at, created_by
FROM _0009_configs;

INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state,
                              wrapped_env_key, wrap_key_id, approved_by, approved_at,
                              denied_reason, created_at, last_seen_at)
SELECT id, machine_id, lower(trim(environment)), lower(trim(app)), lower(trim(role)),
       version, state, wrapped_env_key, wrap_key_id, approved_by, approved_at,
       denied_reason, created_at, last_seen_at
FROM _0009_registrations;

-- A carried-forward 0008 secret is 'env': it was environment-bound by
-- definition, and 'direct' with no source is the truthful origin for a value
-- whose provenance predates lineage.
INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
SELECT config_id, key, 'env', ciphertext, key_id, 'direct'
FROM _0009_config_values;

INSERT INTO cm_machine_secrets (id, machine_id, key, ciphertext, created_at, created_by)
SELECT id, machine_id, key, ciphertext, created_at, created_by FROM _0009_machine_secrets;

INSERT INTO cm_secret_reads (id, at, actor, machine_id, config_id, secret_key, source_ip)
SELECT id, at, actor, machine_id, config_id, secret_key, source_ip FROM _0009_secret_reads;

DROP TABLE _0009_secret_reads;
DROP TABLE _0009_machine_secrets;
DROP TABLE _0009_config_values;
DROP TABLE _0009_configs;
DROP TABLE _0009_registrations;
DROP TABLE _0009_machines;
