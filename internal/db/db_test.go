package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// open builds a real database in a temp dir and migrates it. Everything here
// runs against actual SQLite rather than a fake: the constraints under test —
// the partial unique index, the foreign keys, the CHECKs — only exist there.
func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hz.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := first.CreateUser(context.Background(), "carl", "", RoleAdmin); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = first.Close()

	// Reopening must migrate to no-op and leave the data alone.
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = second.Close() }()

	if _, err := second.UserByUsername(context.Background(), "carl"); err != nil {
		t.Fatalf("user lost across reopen: %v", err)
	}
}

// An applied migration is immutable; a changed one must stop the process
// rather than silently mean different things on different boxes.
func TestMigrateRefusesEditedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hz.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Simulate the file having been edited after it was applied.
	if _, err := d.Exec(`UPDATE applied_migrations SET checksum = 'tampered'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = d.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("reopen accepted a modified migration")
	}
	if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("error should name the rule, got: %v", err)
	}
}

func TestUsernameIsNormalized(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	if _, err := d.CreateUser(ctx, "  Carl  ", "", RoleAdmin); err != nil {
		t.Fatalf("create: %v", err)
	}
	u, err := d.UserByUsername(ctx, "CARL")
	if err != nil {
		t.Fatalf("lookup by different case failed: %v", err)
	}
	if u.Username != "carl" {
		t.Fatalf("stored username = %q, want %q", u.Username, "carl")
	}
	if _, err := d.CreateUser(ctx, "CARL", "", RoleAdmin); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("case-variant duplicate allowed: %v", err)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	if err := d.SetPassword(ctx, u.ID, "short"); err == nil {
		t.Fatal("accepted a password under the minimum length")
	}
	if err := d.SetPassword(ctx, u.ID, "correct-horse-battery"); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := d.VerifyPassword(ctx, "carl", "correct-horse-battery")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("wrong user: %s", got.ID)
	}
	if got.LastLoginAt == nil {
		t.Error("last_login_at not stamped")
	}

	if _, err := d.VerifyPassword(ctx, "carl", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password = %v, want ErrBadCredentials", err)
	}
	// An unknown user must be indistinguishable from a wrong password.
	if _, err := d.VerifyPassword(ctx, "nobody", "whatever"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user = %v, want ErrBadCredentials", err)
	}
}

// Re-setting a password must replace the row, not accumulate credentials —
// otherwise the old password keeps working.
func TestPasswordReplaceDoesNotAccumulate(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	if err := d.SetPassword(ctx, u.ID, "first-password-here"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := d.SetPassword(ctx, u.ID, "second-password-xyz"); err != nil {
		t.Fatalf("second: %v", err)
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE user_id = ? AND kind = 'password'`, u.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("password rows = %d, want 1", n)
	}
	if _, err := d.VerifyPassword(ctx, "carl", "first-password-here"); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old password still works")
	}
	if _, err := d.VerifyPassword(ctx, "carl", "second-password-xyz"); err != nil {
		t.Errorf("the new password does not: %v", err)
	}
}

func TestDisabledUserCannotAuthenticate(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)
	_ = d.SetPassword(ctx, u.ID, "correct-horse-battery")

	token, _, err := d.CreateSession(ctx, u.ID, time.Hour, "10.0.0.1", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := d.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := d.VerifyPassword(ctx, "carl", "correct-horse-battery"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("login = %v, want ErrUserDisabled", err)
	}
	// The live session must die with the account, not outlive it.
	if _, _, err := d.LookupSession(ctx, token, 0); err == nil {
		t.Error("a disabled user's existing session still resolves")
	}
}

func TestSessionTokenIsNotStored(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	token, _, err := d.CreateSession(ctx, u.ID, time.Hour, "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var stored string
	if err := d.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored == token {
		t.Fatal("the raw session token is in the database")
	}
	if stored != hashToken(token) {
		t.Fatal("stored value is not the token hash")
	}
}

func TestSessionLimits(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	t.Run("valid session resolves", func(t *testing.T) {
		token, _, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
		got, _, err := d.LookupSession(ctx, token, time.Hour)
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if got.ID != u.ID {
			t.Fatal("wrong user")
		}
	})

	t.Run("absolute expiry", func(t *testing.T) {
		// Backdated rather than created with a negative TTL: CreateSession
		// treats <= 0 as "use the default", so a negative value would have
		// tested the guard instead of the expiry.
		token, s, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
		if _, err := d.Exec(`UPDATE sessions SET expires_at = datetime('now', '-1 minute') WHERE id = ?`, s.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if _, _, err := d.LookupSession(ctx, token, 0); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("= %v, want ErrSessionExpired", err)
		}
	})

	t.Run("idle timeout", func(t *testing.T) {
		token, s, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
		// Backdate last activity past the idle window.
		if _, err := d.Exec(`UPDATE sessions SET last_seen_at = datetime('now', '-30 minutes') WHERE id = ?`, s.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if _, _, err := d.LookupSession(ctx, token, 15*time.Minute); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("= %v, want ErrSessionExpired", err)
		}
		// Same session, idle check disabled: still good.
		token2, s2, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
		if _, err := d.Exec(`UPDATE sessions SET last_seen_at = datetime('now', '-30 minutes') WHERE id = ?`, s2.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if _, _, err := d.LookupSession(ctx, token2, 0); err != nil {
			t.Fatalf("idle disabled should still resolve: %v", err)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		token, s, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
		if err := d.RevokeSession(ctx, s.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if _, _, err := d.LookupSession(ctx, token, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("= %v, want ErrNotFound", err)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		if _, _, err := d.LookupSession(ctx, "not-a-real-token", 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("= %v, want ErrNotFound", err)
		}
	})
}

func TestLookupSlidesIdleWindow(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	token, s, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
	if _, err := d.Exec(`UPDATE sessions SET last_seen_at = datetime('now', '-10 minutes') WHERE id = ?`, s.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Inside the window: resolving must push last_seen_at forward, or an
	// actively used session would expire while in use.
	if _, _, err := d.LookupSession(ctx, token, 15*time.Minute); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	var age float64
	if err := d.QueryRow(
		`SELECT (julianday('now') - julianday(last_seen_at)) * 86400 FROM sessions WHERE id = ?`, s.ID).Scan(&age); err != nil {
		t.Fatalf("read: %v", err)
	}
	if age > 5 {
		t.Fatalf("last_seen_at is %.0fs old, window did not slide", age)
	}
}

// The count that decides whether disabling the shared admin token is safe. An
// account with no credentials cannot log in and must not be counted.
func TestCountEnabledUsersRequiresACredential(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	invited, _ := d.CreateUser(ctx, "invited", "", RoleAdmin)
	if n, _ := d.CountEnabledUsers(ctx); n != 0 {
		t.Fatalf("a credential-less user counted: %d", n)
	}

	_ = d.SetPassword(ctx, invited.ID, "correct-horse-battery")
	if n, _ := d.CountEnabledUsers(ctx); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}

	_ = d.SetUserDisabled(ctx, invited.ID, true)
	if n, _ := d.CountEnabledUsers(ctx); n != 0 {
		t.Fatalf("disabled user counted: %d", n)
	}
}

func TestRoleIsConstrained(t *testing.T) {
	d := open(t)
	if _, err := d.CreateUser(context.Background(), "carl", "", "superuser"); err == nil {
		t.Fatal("accepted an unknown role")
	}
}

func TestPurgeKeepsRecentEvidence(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "carl", "", RoleAdmin)

	_, recent, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
	_ = d.RevokeSession(ctx, recent.ID)

	_, old, _ := d.CreateSession(ctx, u.ID, time.Hour, "", "")
	if _, err := d.Exec(
		`UPDATE sessions SET revoked_at = datetime('now', '-30 days') WHERE id = ?`, old.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := d.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	var left int
	_ = d.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, recent.ID).Scan(&left)
	if left != 1 {
		t.Fatal("a just-revoked session was purged; it is still audit evidence")
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	d := open(t)
	// PRAGMA foreign_keys is per-connection and off by default in SQLite; if
	// the DSN pragma ever stops being applied, orphan rows appear silently.
	if _, err := d.Exec(
		`INSERT INTO credentials (id, user_id, kind, secret) VALUES ('x', 'nosuchuser', 'password', 'y')`); err == nil {
		t.Fatal("inserted a credential for a nonexistent user")
	}
}
