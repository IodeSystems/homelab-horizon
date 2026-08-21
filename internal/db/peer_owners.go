package db

import (
	"context"
	"database/sql"
	"errors"
)

// Peer ownership.
//
// The WireGuard config remains the authority on which peers exist; this only
// records whose device each one is. A name that has been deleted from the
// config leaves a harmless orphan row here, so every read joins against the
// live peer list rather than trusting this table alone.

// SetPeerOwner records that a peer belongs to a user, replacing any previous
// owner.
func (d *DB) SetPeerOwner(ctx context.Context, peerName, userID string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO peer_owners (peer_name, user_id) VALUES (?, ?)
		ON CONFLICT (peer_name) DO UPDATE SET user_id = excluded.user_id`,
		peerName, userID)
	return err
}

// ClearPeerOwner removes ownership, returning the peer to the unowned pool.
func (d *DB) ClearPeerOwner(ctx context.Context, peerName string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM peer_owners WHERE peer_name = ?`, peerName)
	return err
}

// PeerOwner returns the user id that owns a peer, or ErrNotFound.
func (d *DB) PeerOwner(ctx context.Context, peerName string) (string, error) {
	var userID string
	err := d.QueryRowContext(ctx,
		`SELECT user_id FROM peer_owners WHERE peer_name = ?`, peerName).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// PeersOwnedBy lists the peer names a user owns.
func (d *DB) PeersOwnedBy(ctx context.Context, userID string) ([]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT peer_name FROM peer_owners WHERE user_id = ? ORDER BY peer_name`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// PeerOwners returns every recorded ownership, peer name to username. Used by
// the administrative peer list, which shows all peers and whose they are.
func (d *DB) PeerOwners(ctx context.Context) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.peer_name, u.username
		FROM peer_owners p JOIN users u ON u.id = p.user_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var name, username string
		if err := rows.Scan(&name, &username); err != nil {
			return nil, err
		}
		out[name] = username
	}
	return out, rows.Err()
}
