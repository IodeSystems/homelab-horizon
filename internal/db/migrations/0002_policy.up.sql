-- Account policy: lockout state and password history.
--
-- The counters live on the user rather than in a side table because they are
-- read on every login attempt and written on most of them; a join to decide
-- whether to check a password would be work on the hottest path here.

ALTER TABLE users ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN last_failed_at TIMESTAMP;
-- Set while an account is locked out. A timestamp rather than a flag: a lock
-- that needs a second actor to clear it is a lock that outlives the incident
-- and becomes a support ticket.
ALTER TABLE users ADD COLUMN locked_until TIMESTAMP;

-- Previous password hashes, so a rotation cannot cycle straight back.
--
-- Separate from credentials: those are things you can authenticate with, and
-- these explicitly are not. Keeping a retired hash in the credentials table
-- would put "can log in" and "used to be able to log in" one WHERE clause
-- apart.
CREATE TABLE password_history (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX password_history_user_idx ON password_history (user_id, created_at DESC);
