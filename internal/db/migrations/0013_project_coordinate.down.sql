-- Reverse 0013: back to the three-part config address.
--
-- This LOSES the project, and the loss is total rather than partial: there is
-- no three-part column to fold it into, so `acme/prod/redline/app` and
-- `globex/prod/redline/app` come back as one address. That is the defect the up
-- migration exists to close, restored exactly. Run this only to test that it
-- runs.
--
-- It also loses nothing else, because the up migration already destroyed
-- everything a project could have been attached to: registrations are gone and
-- every config value is tombstoned, so going back down carries forward the same
-- rows it would have carried forward anyway. A cm_configs row whose project is
-- not 'unmigrated' — one blessed after 0013 landed — keeps its environment, app
-- and role and silently joins whatever else shares that triple. There is no
-- correct alternative: the down direction has nowhere to put the field.
--
-- Same SQLite constraints as the up direction: a column inside a UNIQUE or a
-- PRIMARY KEY cannot be dropped in place and every address column is named in a
-- CHECK, so the tables are restated and the children staged.

CREATE TABLE _0013d_configs AS SELECT * FROM cm_configs;
CREATE TABLE _0013d_config_values AS SELECT * FROM cm_config_values;
CREATE TABLE _0013d_secret_reads AS SELECT * FROM cm_secret_reads;
CREATE TABLE _0013d_registrations AS SELECT * FROM cm_registrations;

DROP TABLE cm_secret_reads;
DROP TABLE cm_config_values;
DROP TABLE cm_current_keys;
DROP TABLE cm_registrations;
DROP TABLE cm_configs;

-- 0009's cm_registrations plus 0011's observed_* columns.
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
    version             TEXT NOT NULL,
    state               TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'approved', 'denied')),
    wrapped_env_key     BLOB,
    wrap_key_id         TEXT,
    approved_by         TEXT REFERENCES users (id) ON DELETE SET NULL,
    approved_at         TIMESTAMP,
    denied_reason       TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at        TIMESTAMP,
    observed_version    TEXT,
    observed_build      TEXT,
    observed_at         TIMESTAMP,
    UNIQUE (machine_id, environment, app, role),
    CHECK (state != 'approved' OR (wrapped_env_key IS NOT NULL AND wrap_key_id IS NOT NULL
        AND approved_at IS NOT NULL)),
    CHECK (state != 'denied' OR denied_reason IS NOT NULL),
    CHECK (state != 'pending' OR (wrapped_env_key IS NULL AND wrap_key_id IS NULL
        AND approved_by IS NULL AND approved_at IS NULL))
);

CREATE INDEX idx_cm_registrations_state ON cm_registrations (state, created_at);
CREATE INDEX idx_cm_registrations_address ON cm_registrations (environment, app, role);
CREATE INDEX idx_cm_registrations_approved_by ON cm_registrations (approved_by);

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
    max_ver             TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          TEXT NOT NULL REFERENCES users (id)
);

CREATE INDEX idx_cm_configs_address ON cm_configs (environment, app, role, seq);
CREATE INDEX idx_cm_configs_created_by ON cm_configs (created_by);

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

CREATE TABLE cm_current_keys (
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
    key_id      TEXT NOT NULL
        CHECK (length(key_id) = 16 AND key_id NOT GLOB '*[^0-9a-f]*'),
    set_by      TEXT REFERENCES users (id) ON DELETE SET NULL,
    set_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (environment, app, role)
);

-- Restore, parents before children. Two projects' rungs of the same name now
-- collide on (environment, app, role); the UNIQUE constraints below are what
-- refuse that, loudly, rather than silently merging them.
INSERT INTO cm_configs (seq, id, environment, app, role, min_ver, max_ver,
                        created_at, created_by)
SELECT seq, id, environment, app, role, min_ver, max_ver, created_at, created_by
FROM _0013d_configs;

INSERT INTO cm_config_values
    (config_id, key, binding, ciphertext, key_id, origin, source_config_id,
     tombstoned_at, tombstoned_by)
SELECT config_id, key, binding, ciphertext, key_id, origin, source_config_id,
       tombstoned_at, tombstoned_by
FROM _0013d_config_values;

INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state,
                              wrapped_env_key, wrap_key_id, approved_by, approved_at,
                              denied_reason, created_at, last_seen_at,
                              observed_version, observed_build, observed_at)
SELECT id, machine_id, environment, app, role, version, state,
       wrapped_env_key, wrap_key_id, approved_by, approved_at,
       denied_reason, created_at, last_seen_at,
       observed_version, observed_build, observed_at
FROM _0013d_registrations;

INSERT INTO cm_secret_reads (id, at, actor, machine_id, config_id, secret_key, source_ip)
SELECT id, at, actor, machine_id, config_id, secret_key, source_ip
FROM _0013d_secret_reads;

DROP TABLE _0013d_registrations;
DROP TABLE _0013d_secret_reads;
DROP TABLE _0013d_config_values;
DROP TABLE _0013d_configs;
