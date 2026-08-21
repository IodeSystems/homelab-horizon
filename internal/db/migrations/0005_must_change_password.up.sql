-- An administrator can require a password change at the next sign-in.
--
-- Age-based expiry (8.3.9) could not express this: it exempts accounts holding
-- a second factor, which is what the requirement itself allows, so a reset for
-- an admin with TOTP enrolled would never be enforced. A reset is also a
-- different event from a rotation — the credential was handed over out of band
-- and must not survive the first login.

ALTER TABLE credentials ADD COLUMN must_change INTEGER NOT NULL DEFAULT 0;
