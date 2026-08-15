-- Identity for hz's own admin surface.
--
-- Deliberately NOT in config.json: that file is synced to HA peers, and a user
-- table replicated as configuration would push credentials around the fleet as
-- a side effect of editing a service. This is node-local state.

CREATE TABLE users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL,
    -- Stored lowercased; the index enforces that one person cannot register
    -- as both "carl" and "Carl".
    email               TEXT,
    role                TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'viewer')),
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Disable rather than delete: sessions, audit rows and passkeys all point
    -- here, and an offboarded admin must stay attributable.
    disabled_at         TIMESTAMP,
    last_login_at       TIMESTAMP
);

CREATE UNIQUE INDEX users_username_key ON users (username);

-- One row per authentication factor. Password today; TOTP, passkeys and OIDC
-- identities land in later phases without a schema change.
CREATE TABLE credentials (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('password', 'totp', 'passkey', 'oidc')),
    -- What "secret" means is per kind: a bcrypt hash, a TOTP seed, an OIDC
    -- subject. Never a plaintext password, and never a raw session token.
    secret              TEXT NOT NULL,
    -- Passkey attestation blob (public key, transports); NULL otherwise.
    data                TEXT,
    label               TEXT,
    -- AUTH-4 requires both to be persisted and updated per assertion: a
    -- sign_count that goes backwards means the authenticator was cloned.
    sign_count          INTEGER NOT NULL DEFAULT 0,
    clone_warning       INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at        TIMESTAMP,
    disabled_at         TIMESTAMP
);

CREATE INDEX credentials_user_idx ON credentials (user_id, kind);

-- A user has at most one password. Partial, so it does not constrain the
-- factor kinds where several are legitimate (two passkeys, say).
CREATE UNIQUE INDEX credentials_one_password ON credentials (user_id)
    WHERE kind = 'password';

-- Server-side sessions, replacing the stateless signed cookie.
--
-- Stateless cookies cannot express revocation or idle timeout, which are the
-- two things this table exists for: PCI DSS 8.2.8 wants an idle limit, and
-- offboarding wants a session to die when the account does.
CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY,
    -- SHA-256 of the token, never the token (AUTH-2). A database leak must not
    -- hand over live sessions.
    token_hash          TEXT NOT NULL,
    user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Idle timeout compares against this; absolute expiry against expires_at.
    last_seen_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at          TIMESTAMP NOT NULL,
    revoked_at          TIMESTAMP,
    -- Audit context, so "where is this session from" is answerable.
    ip                  TEXT,
    user_agent          TEXT
);

CREATE UNIQUE INDEX sessions_token_key ON sessions (token_hash);
CREATE INDEX sessions_user_idx ON sessions (user_id);
