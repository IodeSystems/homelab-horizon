-- Which environment key is CURRENT for an address.
--
-- hz stores an ID here, never a key. It cannot read one and has nowhere to put
-- one; the id is derived from key material the client holds and is safe to
-- publish, the way a fingerprint is.
--
-- This table exists because OPENING is self-describing and SEALING is not. An
-- envelope names the key that sealed it, so a client opening a blob needs no
-- help. A client about to seal a NEW value has no id in hand and must choose,
-- and the only local signals are a filename an operator can typo and an mtime
-- that lies after any copy. So the fleet needs one shared answer, and hz is the
-- only thing every client already talks to.
--
-- IT IS ADVISORY, AND A CLIENT MUST NOT OBEY IT. The rule the clients implement
-- is asymmetric on purpose: refuse to SEAL when the local key disagrees with
-- this pointer, and only warn when OPENING. Obeying it would let a compromised
-- hz pin every client to a key it had already stolen. Merely warning in both
-- directions would let anyone who can drop a file into a keystore — a shared
-- dev box, a hostile postinstall, an emailed key file — set a future created_at
-- and become the sealing key for everything that developer pushes afterwards.
-- Refusing to seal closes that; opening stays lenient because it is the
-- recoverable direction.
--
-- What it also buys: a machine holding key X cannot open anything sealed under
-- Y, so a rotation that is not followed by re-wrapping every approved
-- registration breaks config pulls fleet-wide. Comparing each registration's
-- wrap_key_id against this row turns "which boxes still hold the old key" from
-- a guess into a query. Without it there is nothing to compare against.
--
-- One row per address, because the key address IS the config address.
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

    -- The key id as the envelope carries it: lowercase hex, fixed width.
    key_id      TEXT NOT NULL
        CHECK (length(key_id) = 16 AND key_id NOT GLOB '*[^0-9a-f]*'),

    -- ON DELETE SET NULL, not CASCADE: removing an operator must not silently
    -- unset the fleet's current key for an address.
    set_by      TEXT REFERENCES users (id) ON DELETE SET NULL,
    set_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (environment, app, role)
);
