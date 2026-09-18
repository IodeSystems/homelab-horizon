-- Reverse 0009: put the config manager back into 0008's shape.
--
-- This exists so the migration is reversible and testable, not because rolling
-- it back is a sensible operational act. 0008 cannot represent what 0009
-- records, so going down LOSES state, and every loss below is a fact about
-- 0008's schema rather than a shortcut taken here:
--
-- * 0008 holds ONE wrapped key per machine, 0009 holds one per address. Each
--   machine keeps the grant from its earliest fully-formed approved
--   registration and every other grant is destroyed. A machine with no such
--   registration comes back pending, and a denied registration has nowhere to
--   put its denial, so it comes back pending too.
-- * 0008's registration tuple has no environment, so a machine running the
--   same (app, role) in two environments collapses to the earliest of the two
--   rows.
-- * 0008 has no lineage and no tombstones. origin, source_config_id and the
--   tombstone columns are dropped, and a tombstoned value — a row with no
--   bytes — cannot exist in 0008 at all, so it is deleted outright. The
--   destruction a tombstone recorded therefore becomes invisible, which is the
--   single worst thing about running this file.
-- * 0008's CHECK allows ciphertext only on binding 'secret', so every value
--   returns as a secret and the invariant/env distinction is lost.
--
-- The table-rebuild dance is the same one the up migration explains: SQLite
-- cannot drop a parent without cascading into its children, and the pragma
-- that would turn that off is ignored inside the transaction golang-migrate
-- wraps this in.

CREATE TABLE _0009d_machines AS SELECT * FROM cm_machines;
CREATE TABLE _0009d_registrations AS SELECT * FROM cm_registrations;
CREATE TABLE _0009d_configs AS SELECT * FROM cm_configs;
CREATE TABLE _0009d_config_values AS
SELECT config_id, key, ciphertext, key_id FROM cm_config_values WHERE ciphertext IS NOT NULL;
CREATE TABLE _0009d_machine_secrets AS SELECT * FROM cm_machine_secrets;
CREATE TABLE _0009d_secret_reads AS SELECT * FROM cm_secret_reads;

-- The one grant each machine gets to keep: earliest approval that carries
-- everything 0008's CHECK demands of an approved row.
CREATE TABLE _0009d_grant AS
SELECT machine_id, wrapped_env_key, wrap_key_id, approved_by, approved_at
FROM (
    SELECT machine_id, wrapped_env_key, wrap_key_id, approved_by, approved_at,
           ROW_NUMBER() OVER (PARTITION BY machine_id ORDER BY approved_at, id) AS rn
    FROM cm_registrations
    WHERE state = 'approved'
      AND wrapped_env_key IS NOT NULL AND wrap_key_id IS NOT NULL
      AND approved_by IS NOT NULL AND approved_at IS NOT NULL
)
WHERE rn = 1;

DROP TABLE cm_secret_reads;
DROP TABLE cm_config_values;
DROP TABLE cm_machine_secrets;
DROP TABLE cm_registrations;
DROP TABLE cm_configs;
DROP TABLE cm_machines;

CREATE TABLE cm_machines (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    environment         TEXT NOT NULL,
    public_key          BLOB NOT NULL,
    state               TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied')),
    wrapped_env_key     BLOB,
    wrap_key_id         TEXT,
    approved_by         TEXT REFERENCES users (id) ON DELETE SET NULL,
    approved_at         TIMESTAMP,
    denied_reason       TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP,
    CHECK (state != 'approved' OR (wrapped_env_key IS NOT NULL AND wrap_key_id IS NOT NULL
        AND approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (state != 'denied' OR denied_reason IS NOT NULL),
    CHECK (state != 'pending' OR (wrapped_env_key IS NULL AND wrap_key_id IS NULL
        AND approved_by IS NULL AND approved_at IS NULL))
);

CREATE UNIQUE INDEX idx_cm_machines_name ON cm_machines (name);
CREATE INDEX idx_cm_machines_state ON cm_machines (state);
CREATE INDEX idx_cm_machines_approved_by ON cm_machines (approved_by);

CREATE TABLE cm_registrations (
    id                  TEXT PRIMARY KEY,
    machine_id          TEXT NOT NULL REFERENCES cm_machines (id) ON DELETE CASCADE,
    app                 TEXT NOT NULL,
    role                TEXT NOT NULL,
    version             TEXT NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP,
    UNIQUE (machine_id, app, role)
);

CREATE TABLE cm_configs (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    id                  TEXT NOT NULL UNIQUE,
    environment         TEXT NOT NULL,
    app                 TEXT NOT NULL,
    role                TEXT NOT NULL,
    min_ver             TEXT NOT NULL,
    max_ver             TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id)
);

CREATE INDEX idx_cm_configs_address ON cm_configs (environment, app, role, seq);
CREATE INDEX idx_cm_configs_created_by ON cm_configs (created_by);

CREATE TABLE cm_config_values (
    config_id           TEXT NOT NULL REFERENCES cm_configs (id) ON DELETE CASCADE,
    key                  TEXT NOT NULL,
    binding              TEXT NOT NULL CHECK (binding IN ('invariant', 'env', 'secret')),
    value                TEXT,
    ciphertext           BLOB,
    key_id               TEXT,
    PRIMARY KEY (config_id, key),
    CHECK (
        (binding = 'secret' AND value IS NULL AND ciphertext IS NOT NULL AND key_id IS NOT NULL)
        OR
        (binding != 'secret' AND value IS NOT NULL AND ciphertext IS NULL AND key_id IS NULL)
    )
);

CREATE TABLE cm_machine_secrets (
    id                  TEXT PRIMARY KEY,
    machine_id          TEXT NOT NULL REFERENCES cm_machines (id) ON DELETE CASCADE,
    key                 TEXT NOT NULL,
    ciphertext          BLOB NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id),
    UNIQUE (machine_id, key)
);

CREATE INDEX idx_cm_machine_secrets_created_by ON cm_machine_secrets (created_by);

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

INSERT INTO cm_machines (id, name, environment, public_key, state, wrapped_env_key,
                         wrap_key_id, approved_by, approved_at, denied_reason,
                         created_at, last_seen_at)
SELECT m.id, m.name, m.enrolled_environment, m.public_key,
       CASE WHEN g.machine_id IS NULL THEN 'pending' ELSE 'approved' END,
       g.wrapped_env_key, g.wrap_key_id, g.approved_by, g.approved_at, NULL,
       m.created_at, m.last_seen_at
FROM _0009d_machines m
LEFT JOIN _0009d_grant g ON g.machine_id = m.id;

INSERT INTO cm_configs (seq, id, environment, app, role, min_ver, max_ver, created_at, created_by)
SELECT seq, id, environment, app, role, min_ver, max_ver, created_at, created_by
FROM _0009d_configs;

INSERT INTO cm_registrations (id, machine_id, app, role, version, created_at, last_seen_at)
SELECT id, machine_id, app, role, version, created_at, last_seen_at
FROM (
    SELECT id, machine_id, app, role, version, created_at, last_seen_at,
           ROW_NUMBER() OVER (PARTITION BY machine_id, app, role ORDER BY created_at, id) AS rn
    FROM _0009d_registrations
)
WHERE rn = 1;

INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
SELECT config_id, key, 'secret', NULL, ciphertext, key_id FROM _0009d_config_values;

INSERT INTO cm_machine_secrets (id, machine_id, key, ciphertext, created_at, created_by)
SELECT id, machine_id, key, ciphertext, created_at, created_by FROM _0009d_machine_secrets;

INSERT INTO cm_secret_reads (id, at, actor, machine_id, config_id, secret_key, source_ip)
SELECT id, at, actor, machine_id, config_id, secret_key, source_ip FROM _0009d_secret_reads;

DROP TABLE _0009d_grant;
DROP TABLE _0009d_secret_reads;
DROP TABLE _0009d_machine_secrets;
DROP TABLE _0009d_config_values;
DROP TABLE _0009d_configs;
DROP TABLE _0009d_registrations;
DROP TABLE _0009d_machines;
