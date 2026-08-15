package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	ashid "github.com/IodeSystems/ashid/go"
)

// Session lifetimes.
//
// Two independent limits, because they answer different questions. Absolute
// expiry bounds how long a stolen cookie is useful. Idle timeout is PCI DSS
// 8.2.8's requirement that an unattended console stops being one — 15 minutes
// is the figure the standard names.
const (
	DefaultSessionTTL  = 24 * time.Hour
	DefaultIdleTimeout = 15 * time.Minute
)

// Session is a server-side login.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// ErrSessionExpired distinguishes a session that timed out from one that never
// existed, so the UI can say "signed out for inactivity" rather than a bare
// 401.
var ErrSessionExpired = errors.New("session expired")

// CreateSession issues a session and returns the raw token, which is the only
// time it exists outside the caller: the database keeps a SHA-256 of it.
//
// Opaque tokens are hashed, not bcrypted (AUTH-2). They are 256 bits of
// randomness, not a user-chosen secret, so there is nothing to slow down a
// guesser about — and bcrypt on every authenticated request would be a
// self-inflicted denial of service.
func (d *DB) CreateSession(ctx context.Context, userID string, ttl time.Duration, ip, userAgent string) (token string, s *Session, err error) {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)

	id := ashid.New("ses")
	expires := time.Now().Add(ttl)
	_, err = d.ExecContext(ctx, `
		INSERT INTO sessions (id, token_hash, user_id, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, hashToken(token), userID, expires, nullString(ip), nullString(userAgent))
	if err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}

	return token, &Session{
		ID: id, UserID: userID, CreatedAt: time.Now(),
		LastSeenAt: time.Now(), ExpiresAt: expires, IP: ip, UserAgent: userAgent,
	}, nil
}

// LookupSession resolves a raw token to its user, enforcing both limits and
// sliding the idle window forward.
//
// idleTimeout of zero disables the idle check, which is what a deployment that
// has not opted into 8.2.8 wants — the absolute expiry still applies.
func (d *DB) LookupSession(ctx context.Context, token string, idleTimeout time.Duration) (*User, *Session, error) {
	if token == "" {
		return nil, nil, ErrNotFound
	}

	var s Session
	var ip, ua sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, user_id, created_at, last_seen_at, expires_at, ip, user_agent
		FROM sessions
		WHERE token_hash = ? AND revoked_at IS NULL`,
		hashToken(token)).Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &ip, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	} else if err != nil {
		return nil, nil, err
	}
	s.IP, s.UserAgent = ip.String, ua.String

	now := time.Now()
	if now.After(s.ExpiresAt) {
		_ = d.RevokeSession(ctx, s.ID)
		return nil, nil, ErrSessionExpired
	}
	if idleTimeout > 0 && now.Sub(s.LastSeenAt) > idleTimeout {
		_ = d.RevokeSession(ctx, s.ID)
		return nil, nil, ErrSessionExpired
	}

	user, err := d.UserByID(ctx, s.UserID)
	if err != nil {
		return nil, nil, err
	}
	// A user disabled mid-session loses it here, not at next login.
	if !user.Enabled() {
		_ = d.RevokeSession(ctx, s.ID)
		return nil, nil, ErrUserDisabled
	}

	// Slide the idle window. Written every request, which is the cost of
	// having an idle timeout at all; WAL mode is what keeps it from blocking
	// concurrent reads.
	if _, err := d.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, s.ID); err != nil {
		return nil, nil, err
	}
	s.LastSeenAt = now

	return user, &s, nil
}

// RevokeSession ends one session.
func (d *DB) RevokeSession(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`, id)
	return err
}

// RevokeUserSessions ends every session a user holds — the "sign out
// everywhere" a password change should trigger.
func (d *DB) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE user_id = ? AND revoked_at IS NULL`, userID)
	return err
}

// ListUserSessions returns a user's live sessions, newest first, so somebody
// can see where they are logged in.
func (d *DB) ListUserSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, user_id, created_at, last_seen_at, expires_at, COALESCE(ip, ''), COALESCE(user_agent, '')
		FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.IP, &s.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PurgeExpiredSessions deletes sessions that are past use. Revoked rows are
// kept for a week as audit evidence rather than vanishing on logout.
func (d *DB) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := d.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE expires_at < datetime('now', '-7 days')
		   OR (revoked_at IS NOT NULL AND revoked_at < datetime('now', '-7 days'))`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// hashToken is the stored form of a session token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokensEqual compares two tokens without leaking their difference through
// timing. Lookups go through the hash index; this is for the places that hold
// two values directly.
func TokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
