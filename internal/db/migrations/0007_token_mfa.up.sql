-- A token can demand a one-time code alongside it.
--
-- Default 0 — not required — because the point of a token is unattended use: a
-- deploy job at 3am has no one to read a code off a phone. The flag exists for
-- the tokens that live somewhere less trusted than a CI secret store, where
-- "something you have" should not be a file on disk alone.
ALTER TABLE api_tokens ADD COLUMN mfa_required INTEGER NOT NULL DEFAULT 0;
