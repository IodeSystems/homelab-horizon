package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	ashid "github.com/IodeSystems/ashid/go"
	"golang.org/x/crypto/bcrypt"
)

// Errors callers are expected to branch on.
var (
	ErrNotFound       = errors.New("not found")
	ErrUserDisabled   = errors.New("user is disabled")
	ErrBadCredentials = errors.New("invalid credentials")
	ErrUsernameTaken  = errors.New("username already taken")
)

// Roles. hz has had exactly one privilege level since it existed; viewer is
// here so that read-only access does not require a schema change later.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// Credential kinds.
const (
	KindPassword = "password"
	KindTOTP     = "totp"
	KindPasskey  = "passkey"
	KindOIDC     = "oidc"
)

// User is an hz operator.
type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email,omitempty"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Enabled reports whether the account may authenticate.
func (u *User) Enabled() bool { return u != nil && u.DisabledAt == nil }

// NormalizeUsername is the single definition of identity equality. Applied on
// write and on lookup both, so "Carl" and "carl" can never become two accounts.
func NormalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// CreateUser adds a user with no credentials. Factors are attached separately,
// which is what lets an invite exist before its holder has chosen a password.
func (d *DB) CreateUser(ctx context.Context, username, email, role string) (*User, error) {
	username = NormalizeUsername(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	if role == "" {
		role = RoleAdmin
	}
	if role != RoleAdmin && role != RoleViewer {
		return nil, fmt.Errorf("unknown role %q", role)
	}

	id := ashid.New("usr")
	_, err := d.ExecContext(ctx,
		`INSERT INTO users (id, username, email, role) VALUES (?, ?, ?, ?)`,
		id, username, nullString(email), role)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return d.UserByID(ctx, id)
}

// UserByID looks a user up by primary key.
func (d *DB) UserByID(ctx context.Context, id string) (*User, error) {
	return d.scanUser(d.QueryRowContext(ctx, selectUser+` WHERE id = ?`, id))
}

// UserByUsername looks a user up by normalized username.
func (d *DB) UserByUsername(ctx context.Context, username string) (*User, error) {
	return d.scanUser(d.QueryRowContext(ctx, selectUser+` WHERE username = ?`, NormalizeUsername(username)))
}

// ListUsers returns every user, oldest first.
func (d *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := d.QueryContext(ctx, selectUser+` ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// CountEnabledUsers reports how many accounts can currently log in.
//
// This is the number that decides whether disabling the shared admin token is
// safe: switching it off with no usable account is a lockout, and the console
// is the only way back.
func (d *DB) CountEnabledUsers(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT u.id)
		FROM users u
		JOIN credentials c ON c.user_id = u.id
		WHERE u.disabled_at IS NULL AND c.disabled_at IS NULL`).Scan(&n)
	return n, err
}

// SetUserDisabled disables or re-enables an account. Disabling revokes its
// live sessions in the same transaction — an account that cannot log in but
// whose existing tab keeps working is not disabled in any useful sense.
func (d *DB) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if disabled {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET disabled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE user_id = ? AND revoked_at IS NULL`, id); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx,
		`UPDATE users SET disabled_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPassword sets or replaces a user's password.
func (d *DB) SetPassword(ctx context.Context, userID, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	// DefaultCost, never a hardcoded number (AUTH-1): the library's default
	// tracks hardware, a literal does not.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	_, err = d.ExecContext(ctx, `
		INSERT INTO credentials (id, user_id, kind, secret) VALUES (?, ?, 'password', ?)
		ON CONFLICT (user_id) WHERE kind = 'password'
		DO UPDATE SET secret = excluded.secret, created_at = CURRENT_TIMESTAMP, disabled_at = NULL`,
		ashid.New("cred"), userID, string(hash))
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// MinPasswordLength is the floor. PCI DSS 8.3.6 wants 12 for CDE access, and
// hz fronts services that may be in scope, so 12 is the default rather than
// NIST's 8.
const MinPasswordLength = 12

// VerifyPassword checks a username/password pair and returns the user.
//
// Every failure returns ErrBadCredentials, whatever the real reason: a
// distinguishable "no such user" turns the login form into a list of valid
// usernames. Disabled accounts are the exception the caller may want to
// surface, so they get their own error — but only after the password matched,
// so the distinction leaks nothing to someone who cannot authenticate.
func (d *DB) VerifyPassword(ctx context.Context, username, password string) (*User, error) {
	user, err := d.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Spend the time anyway: returning early on an unknown username
			// makes the response time itself the oracle.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			return nil, ErrBadCredentials
		}
		return nil, err
	}

	var hash string
	err = d.QueryRowContext(ctx,
		`SELECT secret FROM credentials WHERE user_id = ? AND kind = 'password' AND disabled_at IS NULL`,
		user.ID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadCredentials
	} else if err != nil {
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrBadCredentials
	}
	if !user.Enabled() {
		return nil, ErrUserDisabled
	}

	if _, err := d.ExecContext(ctx,
		`UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = ?`, user.ID); err != nil {
		return nil, err
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE credentials SET last_used_at = CURRENT_TIMESTAMP WHERE user_id = ? AND kind = 'password'`,
		user.ID); err != nil {
		return nil, err
	}
	// Re-read rather than returning the row fetched before the stamp: callers
	// use the returned user to render "last login", and a struct that predates
	// the login it just performed is wrong in the one field they came for.
	return d.UserByID(ctx, user.ID)
}

// dummyHash is a valid bcrypt hash of a value nothing can match, compared
// against when there is no real hash so that both paths cost the same.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

const selectUser = `
	SELECT id, username, COALESCE(email, ''), role, created_at, updated_at, disabled_at, last_login_at
	FROM users`

type rowScanner interface {
	Scan(dest ...any) error
}

func (d *DB) scanUser(row rowScanner) (*User, error) {
	u, err := scanUserRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func scanUserRow(row rowScanner) (*User, error) {
	var u User
	var disabled, lastLogin sql.NullTime
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Role,
		&u.CreatedAt, &u.UpdatedAt, &disabled, &lastLogin); err != nil {
		return nil, err
	}
	if disabled.Valid {
		u.DisabledAt = &disabled.Time
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	return &u, nil
}

func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
