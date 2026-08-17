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

// Roles. hz has exactly one privilege level.
//
// A viewer role existed briefly and was removed in migration 0003: nothing
// enforced it, and enforcing it would mean auditing what every response body
// exposes rather than which verb was used — peer configurations carry private
// keys. See plan/icebox.md if read-only is wanted for real.
const RoleAdmin = "admin"

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
	if role != RoleAdmin {
		return nil, fmt.Errorf("unknown role %q: hz has only %q", role, RoleAdmin)
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

// SetPassword sets or replaces a user's password, keeping no history.
func (d *DB) SetPassword(ctx context.Context, userID, password string) error {
	return d.SetPasswordWithHistory(ctx, userID, password, 0)
}

// SetPasswordWithHistory sets a password, refusing the last `history` ones.
//
// The comparison is bcrypt against every retained hash, which is deliberately
// the slow way round: hashes are salted, so there is nothing to look up. That
// caps how many can sensibly be retained, and four — what PCI DSS 8.3.7 asks
// for — is well inside it.
func (d *DB) SetPasswordWithHistory(ctx context.Context, userID, password string, history int) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	if history > 0 {
		reused, err := d.passwordWasUsed(ctx, userID, password, history)
		if err != nil {
			return err
		}
		if reused {
			return ErrPasswordReused
		}
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

	if history > 0 {
		if err := d.recordPasswordHistory(ctx, userID, string(hash), history); err != nil {
			return err
		}
	}
	return nil
}

// passwordWasUsed reports whether the candidate matches a retained hash.
//
// The current password counts, and is checked separately: it lives in
// credentials rather than password_history, and an account whose password
// predates the history feature has no rows at all — so comparing only the
// table would wave through "change it to what it already is", which is the
// most obvious reuse there is.
func (d *DB) passwordWasUsed(ctx context.Context, userID, password string, history int) (bool, error) {
	var current string
	err := d.QueryRowContext(ctx,
		`SELECT secret FROM credentials WHERE user_id = ? AND kind = 'password'`, userID).Scan(&current)
	switch {
	case err == nil:
		if bcrypt.CompareHashAndPassword([]byte(current), []byte(password)) == nil {
			return true, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}

	// Ordered by id as well as time: CURRENT_TIMESTAMP is second-granularity,
	// so two changes inside the same second are indistinguishable by time
	// alone and the window would be chosen arbitrarily. ashid is
	// time-sortable, which makes it the tiebreaker rather than a second
	// column to maintain.
	rows, err := d.QueryContext(ctx,
		`SELECT hash FROM password_history
		 WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		userID, history)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return false, err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
			return true, nil
		}
	}
	return false, rows.Err()
}

// recordPasswordHistory stores the new hash and trims to the retained window.
func (d *DB) recordPasswordHistory(ctx context.Context, userID, hash string, history int) error {
	if _, err := d.ExecContext(ctx,
		`INSERT INTO password_history (id, user_id, hash) VALUES (?, ?, ?)`,
		ashid.New("pwh"), userID, hash); err != nil {
		return err
	}
	// Trim rather than keep forever: a retired hash is a credential nobody
	// uses, and there is no reason to hold more than the policy compares.
	_, err := d.ExecContext(ctx, `
		DELETE FROM password_history
		WHERE user_id = ?
		  AND id NOT IN (
			SELECT id FROM password_history
			WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
		  )`, userID, userID, history)
	return err
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
	return d.VerifyPasswordWithPolicy(ctx, username, password, LockoutPolicy{})
}

// VerifyPasswordWithPolicy checks credentials and enforces lockout.
//
// A locked account is refused before the password is compared, so the lock
// cannot be probed for correctness by timing — and so that a locked-out
// attacker cannot keep confirming a password they already guessed.
func (d *DB) VerifyPasswordWithPolicy(ctx context.Context, username, password string, policy LockoutPolicy) (*User, error) {
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

	if policy.MaxAttempts > 0 {
		_, until, err := d.lockState(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		if !until.IsZero() && time.Now().Before(until) {
			return nil, &ErrAccountLocked{Until: until}
		}
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
		if until, ferr := d.recordFailure(ctx, user.ID, policy); ferr == nil && !until.IsZero() {
			return nil, &ErrAccountLocked{Until: until}
		}
		return nil, ErrBadCredentials
	}
	if err := d.clearFailures(ctx, user.ID); err != nil {
		return nil, err
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

// Account policy: lockout and password reuse.
//
// Enforced in the store rather than the handlers because both are properties
// of the account, and a second caller that forgot to check would be a silent
// hole rather than a visible bug.

// ErrAccountLocked means too many failures have locked the account.
type ErrAccountLocked struct{ Until time.Time }

func (e *ErrAccountLocked) Error() string {
	return "account locked until " + e.Until.Format(time.RFC3339)
}

// ErrPasswordReused means the new password matches a recent one.
var ErrPasswordReused = errors.New("that password was used recently; choose one you have not used before")

// LockoutPolicy is what the store needs to know to enforce lockout.
type LockoutPolicy struct {
	MaxAttempts int
	Duration    time.Duration
}

// lockState reads an account's lockout position.
func (d *DB) lockState(ctx context.Context, userID string) (attempts int, until time.Time, err error) {
	var lockedUntil sql.NullTime
	err = d.QueryRowContext(ctx,
		`SELECT failed_attempts, locked_until FROM users WHERE id = ?`, userID).Scan(&attempts, &lockedUntil)
	if lockedUntil.Valid {
		until = lockedUntil.Time
	}
	return attempts, until, err
}

// recordFailure counts a failed attempt and locks the account at the
// threshold. Returns the lock expiry when this failure caused one.
func (d *DB) recordFailure(ctx context.Context, userID string, policy LockoutPolicy) (time.Time, error) {
	if policy.MaxAttempts <= 0 {
		return time.Time{}, nil
	}

	attempts, _, err := d.lockState(ctx, userID)
	if err != nil {
		return time.Time{}, err
	}
	attempts++

	if attempts < policy.MaxAttempts {
		_, err := d.ExecContext(ctx,
			`UPDATE users SET failed_attempts = ?, last_failed_at = CURRENT_TIMESTAMP WHERE id = ?`,
			attempts, userID)
		return time.Time{}, err
	}

	// At the threshold: lock, and reset the counter so the next lock needs a
	// fresh run of failures rather than one more attempt forever.
	until := time.Now().Add(policy.Duration)
	_, err = d.ExecContext(ctx, `
		UPDATE users
		SET failed_attempts = 0, last_failed_at = CURRENT_TIMESTAMP, locked_until = ?
		WHERE id = ?`, until, userID)
	return until, err
}

// clearFailures resets the counter after a success.
func (d *DB) clearFailures(ctx context.Context, userID string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE users SET failed_attempts = 0, locked_until = NULL WHERE id = ?`, userID)
	return err
}

// PasswordAge reports how long ago the password was set.
func (d *DB) PasswordAge(ctx context.Context, userID string) (time.Duration, error) {
	var created time.Time
	err := d.QueryRowContext(ctx,
		`SELECT created_at FROM credentials WHERE user_id = ? AND kind = 'password'`, userID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoFactor
	} else if err != nil {
		return 0, err
	}
	return time.Since(created), nil
}
