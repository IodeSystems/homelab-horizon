package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	ashid "github.com/IodeSystems/ashid/go"
)

// Personal API tokens.
//
// The shared admin token authenticated a request without identifying anyone.
// A script using it produced audit lines attributable to "whoever holds the
// token", which is exactly what PCI DSS 8.2.1 exists to prevent — and once the
// shared token is disabled, scripts need some other non-interactive way in.
//
// A personal token is that way in: it belongs to a user, carries their role,
// and every action taken with it names them.

// APITokenPrefix marks hz's personal tokens.
//
// Worth the eight characters: it makes a leaked token greppable in logs and
// repositories, and lets a secret scanner recognise one on sight. It is not a
// security control — the entropy after it is.
const APITokenPrefix = "hz_pat_"

// APIToken is a token's metadata. The token itself exists once, at creation.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	// MFARequired means a valid one-time code must accompany the token on
	// every request. Off by default: unattended use is what tokens are for.
	MFARequired bool
}

// ErrTokenExpired distinguishes an expired token from one that never existed,
// so a script's operator gets "your token expired" rather than a bare 401.
var ErrTokenExpired = errors.New("api token expired")

// CreateAPIToken issues a token for a user and returns the raw value, which is
// the only time it exists outside the caller: the database keeps a SHA-256.
//
// ttl of zero means it never expires — see the migration for why that is the
// default rather than a 90-day rotation.
func (d *DB) CreateAPIToken(ctx context.Context, userID, name string, ttl time.Duration, mfaRequired bool) (token string, t *APIToken, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, fmt.Errorf("a token needs a name, so it can be recognised later")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate api token: %w", err)
	}
	token = APITokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	id := ashid.New("pat")
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
	}

	_, err = d.ExecContext(ctx, `
		INSERT INTO api_tokens (id, token_hash, user_id, name, expires_at, mfa_required)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, hashToken(token), userID, name, expires, mfaRequired)
	if err != nil {
		return "", nil, fmt.Errorf("create api token: %w", err)
	}

	return token, &APIToken{
		ID: id, UserID: userID, Name: name, CreatedAt: time.Now(), ExpiresAt: expires,
		MFARequired: mfaRequired,
	}, nil
}

// LookupAPIToken resolves a raw token to its user.
//
// Does not slide any window: unlike a session, a token is a credential rather
// than a login, and an idle timeout on a deploy key would mean the pipeline
// that runs nightly stops working. last_used is recorded for the operator's
// benefit, not as an expiry input.
func (d *DB) LookupAPIToken(ctx context.Context, token, ip string) (*User, *APIToken, error) {
	if token == "" {
		return nil, nil, ErrNotFound
	}

	var t APIToken
	var expires, lastUsed sql.NullTime
	var lastIP sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, user_id, name, created_at, expires_at, last_used_at, last_used_ip, mfa_required
		FROM api_tokens
		WHERE token_hash = ? AND revoked_at IS NULL`,
		hashToken(token)).Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &expires, &lastUsed, &lastIP,
		&t.MFARequired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	} else if err != nil {
		return nil, nil, err
	}
	if expires.Valid {
		t.ExpiresAt = &expires.Time
	}
	if lastUsed.Valid {
		t.LastUsedAt = &lastUsed.Time
	}
	t.LastUsedIP = lastIP.String

	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return nil, nil, ErrTokenExpired
	}

	user, err := d.UserByID(ctx, t.UserID)
	if err != nil {
		return nil, nil, err
	}
	// A disabled account's tokens stop working with it. Otherwise disabling
	// someone's access would leave their automation running as them.
	if user.DisabledAt != nil {
		return nil, nil, ErrNotFound
	}

	// Best effort: the request is already authorised, and failing it because a
	// bookkeeping write failed would be the wrong trade.
	_, _ = d.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`,
		time.Now(), nullString(ip), t.ID)

	return user, &t, nil
}

// ListAPITokens returns a user's tokens, newest first. Revoked ones are not
// returned: the list is for deciding what to revoke next, not a history.
func (d *DB) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, user_id, name, created_at, expires_at, last_used_at, last_used_ip, mfa_required
		FROM api_tokens
		WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []APIToken
	for rows.Next() {
		var t APIToken
		var expires, lastUsed sql.NullTime
		var lastIP sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt,
			&expires, &lastUsed, &lastIP, &t.MFARequired); err != nil {
			return nil, err
		}
		if expires.Valid {
			t.ExpiresAt = &expires.Time
		}
		if lastUsed.Valid {
			t.LastUsedAt = &lastUsed.Time
		}
		t.LastUsedIP = lastIP.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes one token belonging to a user.
//
// Scoped by user rather than by id alone so a request cannot revoke someone
// else's token by guessing an identifier.
func (d *DB) RevokeAPIToken(ctx context.Context, userID, id string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		time.Now(), id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
