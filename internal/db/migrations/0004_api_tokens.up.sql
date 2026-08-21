-- Personal API tokens: a credential that names a person.
--
-- The shared admin token could not answer "who did this" — every action it
-- authorised was attributable to whoever held it, which is what PCI DSS 8.2.1
-- exists to prevent. A script still needs a non-interactive credential, so the
-- answer is a token that belongs to a user rather than to the installation.

CREATE TABLE api_tokens (
    id                  TEXT PRIMARY KEY,
    -- SHA-256 of the token, never the token itself (AUTH-2), same as sessions:
    -- a database leak must not hand over live credentials.
    token_hash          TEXT NOT NULL UNIQUE,
    user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- What it is for, in the operator's words. Shown in the list so an unused
    -- token can be revoked by someone who did not create it.
    name                TEXT NOT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Null means it never expires. Deliberate: a deploy key that dies silently
    -- at 90 days breaks a pipeline at the worst moment, so expiry is opt-in and
    -- the list shows age instead.
    expires_at          TIMESTAMP,
    -- Written on use, so a token nobody uses can be found and removed. Not on
    -- the hot path for correctness — a failed write here must not fail the
    -- request it was authorising.
    last_used_at        TIMESTAMP,
    last_used_ip        TEXT,
    revoked_at          TIMESTAMP
);

-- Lookup is by hash on every authenticated request.
CREATE UNIQUE INDEX idx_api_tokens_hash ON api_tokens (token_hash);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);
