-- Which account a WireGuard peer belongs to.
--
-- Peers live in the WireGuard config, not here: they are gateway state, and the
-- kernel is the authority on which exist. This table says only whose device a
-- peer is, keyed by the peer's name because that is the identifier the config
-- and the UI already share.
--
-- A row is not a permission. Administration of peers stays with the existing
-- VPN page; this exists so a person can find their own devices without reading
-- a list of everybody's.
CREATE TABLE peer_owners (
    peer_name           TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_peer_owners_user ON peer_owners (user_id);
