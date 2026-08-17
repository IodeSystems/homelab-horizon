-- Retire the viewer role.
--
-- It was reserved in 0001 so read-only access would not need a schema change
-- later. Nothing ever enforced it: every admin surface checks for the admin
-- role, so a viewer could log in and then find an application that refused
-- every request. A role whose only effect is a confusing refusal is worse than
-- no role.
--
-- Read-only is not a permission bit here, which is the real reason it is going
-- rather than being implemented: hz serves WireGuard peer configurations, and
-- those carry private keys. Deciding what a viewer may READ is an audit of
-- every response body, not a check on the verb.
--
-- Existing viewers become disabled admins. Silently promoting them to working
-- admins would be a privilege escalation performed by an upgrade; disabling
-- makes an operator look at the account and decide.
UPDATE users
SET role = 'admin',
    disabled_at = COALESCE(disabled_at, CURRENT_TIMESTAMP),
    updated_at = CURRENT_TIMESTAMP
WHERE role = 'viewer';

-- The CHECK constraint from 0001 still lists 'viewer'. Dropping a value from a
-- SQLite CHECK means rebuilding the table, copying every row and recreating
-- the indexes and foreign keys that point at it — a real risk to the one table
-- holding credentials, to remove a value the application layer no longer
-- accepts. It stays as dead vocabulary.
