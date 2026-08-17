package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLockoutAfterThreshold(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)
	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")

	policy := LockoutPolicy{MaxAttempts: 3, Duration: 30 * time.Minute}

	for i := range 2 {
		if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", policy); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d = %v, want ErrBadCredentials", i+1, err)
		}
	}

	// The third failure trips the lock and says so, rather than returning the
	// same bad-credentials error and leaving the user to discover it.
	var locked *ErrAccountLocked
	if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", policy); !errors.As(err, &locked) {
		t.Fatalf("third failure = %v, want ErrAccountLocked", err)
	}

	// And the correct password is refused while locked — otherwise the lock
	// protects nothing, since guessing right is what an attacker is doing.
	if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "correct-horse-battery", policy); !errors.As(err, &locked) {
		t.Fatalf("correct password while locked = %v, want ErrAccountLocked", err)
	}
}

func TestLockoutExpires(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)
	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")

	policy := LockoutPolicy{MaxAttempts: 1, Duration: 30 * time.Minute}
	_, _ = d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", policy)

	if _, err := d.Exec(
		`UPDATE users SET locked_until = datetime('now', '-1 minute') WHERE id = ?`, u.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "correct-horse-battery", policy); err != nil {
		t.Fatalf("an expired lock still refuses: %v", err)
	}
}

// A success has to clear the counter, or an account accumulates failures
// across weeks of normal use and locks out for no reason.
func TestSuccessClearsFailureCount(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)
	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")

	policy := LockoutPolicy{MaxAttempts: 3, Duration: 30 * time.Minute}
	_, _ = d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", policy)
	_, _ = d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", policy)

	if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "correct-horse-battery", policy); err != nil {
		t.Fatalf("login: %v", err)
	}

	attempts, _, err := d.lockState(ctx, u.ID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("failure count = %d after a success, want 0", attempts)
	}
}

func TestLockoutDisabledDoesNothing(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)
	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")

	for range 20 {
		_, _ = d.VerifyPasswordWithPolicy(ctx, "carl", "wrong-password-here", LockoutPolicy{})
	}
	if _, err := d.VerifyPasswordWithPolicy(ctx, "carl", "correct-horse-battery", LockoutPolicy{}); err != nil {
		t.Fatalf("with lockout off, a correct password must still work: %v", err)
	}
}

func TestPasswordHistoryBlocksReuse(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	passwords := []string{"first-password-aa", "second-password-b", "third-password-cc"}
	for _, p := range passwords {
		if err := d.SetPasswordWithHistory(ctx, u.ID, p, 4); err != nil {
			t.Fatalf("set %q: %v", p, err)
		}
	}

	for _, p := range passwords {
		if err := d.SetPasswordWithHistory(ctx, u.ID, p, 4); !errors.Is(err, ErrPasswordReused) {
			t.Errorf("reusing %q = %v, want ErrPasswordReused", p, err)
		}
	}

	if err := d.SetPasswordWithHistory(ctx, u.ID, "fourth-password-d", 4); err != nil {
		t.Fatalf("a fresh password was refused: %v", err)
	}
}

// Only the retained window is compared, so a password aged out of history can
// be used again — which is what a bounded history means.
func TestPasswordHistoryWindowIsBounded(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	const oldest = "aaaa-password-one"
	_ = d.SetPasswordWithHistory(ctx, u.ID, oldest, 2)
	_ = d.SetPasswordWithHistory(ctx, u.ID, "bbbb-password-two", 2)
	_ = d.SetPasswordWithHistory(ctx, u.ID, "cccc-password-three", 2)

	if err := d.SetPasswordWithHistory(ctx, u.ID, oldest, 2); err != nil {
		t.Fatalf("a password outside the window should be allowed: %v", err)
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM password_history WHERE user_id = ?`, u.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n > 2 {
		t.Fatalf("history kept %d rows for a window of 2", n)
	}
}

func TestPasswordHistoryOffKeepsNothing(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	_ = d.SetPasswordWithHistory(ctx, u.ID, "first-password-aa", 0)
	if err := d.SetPasswordWithHistory(ctx, u.ID, "first-password-aa", 0); err != nil {
		t.Fatalf("with history off, reuse must be allowed: %v", err)
	}

	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM password_history WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("history off still stored %d hashes", n)
	}
}

func TestPasswordAgeTracksTheChange(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	if _, err := d.PasswordAge(ctx, u.ID); !errors.Is(err, ErrNoFactor) {
		t.Fatalf("an account with no password = %v, want ErrNoFactor", err)
	}

	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")
	age, err := d.PasswordAge(ctx, u.ID)
	if err != nil {
		t.Fatalf("age: %v", err)
	}
	if age > time.Minute {
		t.Fatalf("a fresh password reads %v old", age)
	}

	// Setting it again must reset the clock, or rotation would never clear.
	if _, err := d.Exec(
		`UPDATE credentials SET created_at = datetime('now', '-200 days') WHERE user_id = ? AND kind = 'password'`,
		u.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if age, _ := d.PasswordAge(ctx, u.ID); age < 100*24*time.Hour {
		t.Fatal("backdating did not take")
	}

	_ = d.SetPassword(ctx, u.ID, "a-brand-new-password")
	if age, _ := d.PasswordAge(ctx, u.ID); age > time.Minute {
		t.Fatalf("changing the password left the age at %v", age)
	}
}

// The current password is the most obvious reuse, and it lives in credentials
// rather than password_history — including for accounts created before any
// history existed.
func TestCurrentPasswordCountsAsReuse(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	// Set with history off, so nothing is recorded in the table.
	if err := d.SetPassword(ctx, u.ID, "current-password-x"); err != nil {
		t.Fatalf("set: %v", err)
	}
	var rows int
	_ = d.QueryRow(`SELECT COUNT(*) FROM password_history WHERE user_id = ?`, u.ID).Scan(&rows)
	if rows != 0 {
		t.Fatalf("setup stored %d history rows, expected none", rows)
	}

	if err := d.SetPasswordWithHistory(ctx, u.ID, "current-password-x", 4); !errors.Is(err, ErrPasswordReused) {
		t.Fatalf("reusing the current password = %v, want ErrPasswordReused", err)
	}
}

// The viewer role is gone. It was never enforced, so an account holding it
// could log in and then be refused by everything.
func TestViewerRoleIsRejected(t *testing.T) {
	d := open(t)
	if _, err := d.CreateUser(context.Background(), "carl", "", "viewer"); err == nil {
		t.Fatal("the viewer role was accepted")
	}
}

// Migration 0003 must not silently promote a viewer into a working admin: an
// upgrade that grants privileges is worse than one that asks a question.
func TestExistingViewersBecomeDisabledAdmins(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	// Insert straight past the application layer, the way a row written by an
	// older build would look. The CHECK constraint still permits the value.
	if _, err := d.Exec(
		`INSERT INTO users (id, username, role) VALUES ('usr_legacy', 'legacy', 'viewer')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Re-run the conversion the migration performs.
	if _, err := d.Exec(`
		UPDATE users
		SET role = 'admin', disabled_at = COALESCE(disabled_at, CURRENT_TIMESTAMP)
		WHERE role = 'viewer'`); err != nil {
		t.Fatalf("convert: %v", err)
	}

	u, err := d.UserByID(ctx, "usr_legacy")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if u.Enabled() {
		t.Error("a converted viewer must be disabled, not silently promoted")
	}
}

// The migration itself, not a re-run of its SQL: rewind the recorded version,
// plant a viewer the way an older build would have, and reopen so 0003
// actually executes on an existing database.
func TestMigration0003RunsOnAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hz.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO users (id, username, role) VALUES ('usr_v', 'legacy', 'viewer')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Rewind to the state a v0002 install would be in.
	if _, err := d.Exec(`UPDATE schema_migrations SET version = 2, dirty = 0`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM applied_migrations WHERE version = '0003'`); err != nil {
		t.Fatalf("rewind checksum: %v", err)
	}
	_ = d.Close()

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = upgraded.Close() }()

	u, err := upgraded.UserByID(context.Background(), "usr_v")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if u.Role != RoleAdmin || u.Enabled() {
		t.Fatalf("after upgrade: role=%q enabled=%v, want admin and disabled", u.Role, u.Enabled())
	}

	// And the checksum row is back, so a later edit to 0003 would be caught.
	var n int
	if err := upgraded.QueryRow(
		`SELECT COUNT(*) FROM applied_migrations WHERE version = '0003'`).Scan(&n); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if n != 1 {
		t.Fatal("the migration ran without recording its checksum")
	}
}
