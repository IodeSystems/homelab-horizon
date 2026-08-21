package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newUser(t *testing.T, d *DB, name string) *User {
	t.Helper()
	u, err := d.CreateUser(context.Background(), name, "", RoleAdmin)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func TestAPITokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	user := newUser(t, d, "carl")

	raw, meta, err := d.CreateAPIToken(ctx, user.ID, "ci-deploy", 0, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(raw, APITokenPrefix) {
		t.Errorf("token %q has no recognisable prefix", raw)
	}
	if meta.ExpiresAt != nil {
		t.Error("ttl 0 should mean no expiry")
	}

	// The raw token must not be recoverable from the database.
	var stored string
	if err := d.QueryRowContext(ctx,
		`SELECT token_hash FROM api_tokens WHERE id = ?`, meta.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored == raw || strings.Contains(stored, strings.TrimPrefix(raw, APITokenPrefix)) {
		t.Error("the token itself was stored; only a hash may be")
	}

	got, tok, err := d.LookupAPIToken(ctx, raw, "192.0.2.7")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("resolved to %s, want %s", got.Username, user.Username)
	}
	if tok.Name != "ci-deploy" {
		t.Errorf("name = %q", tok.Name)
	}

	// Using it records where it was used, which is the point of having it.
	tokens, err := d.ListAPITokens(ctx, user.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list: %v (%d tokens)", err, len(tokens))
	}
	if tokens[0].LastUsedAt == nil {
		t.Error("last used was not recorded")
	}
	if tokens[0].LastUsedIP != "192.0.2.7" {
		t.Errorf("last used ip = %q", tokens[0].LastUsedIP)
	}
}

func TestAPITokenRejections(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	user := newUser(t, d, "carl")

	t.Run("unknown token", func(t *testing.T) {
		if _, _, err := d.LookupAPIToken(ctx, APITokenPrefix+"nope", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		raw, meta, err := d.CreateAPIToken(ctx, user.ID, "short", time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		// Backdate rather than sleep.
		if _, err := d.ExecContext(ctx,
			`UPDATE api_tokens SET expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute), meta.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.LookupAPIToken(ctx, raw, ""); !errors.Is(err, ErrTokenExpired) {
			t.Errorf("err = %v, want ErrTokenExpired — a script's operator needs to know why", err)
		}
	})

	t.Run("revoked token", func(t *testing.T) {
		raw, meta, err := d.CreateAPIToken(ctx, user.ID, "revoke-me", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.RevokeAPIToken(ctx, user.ID, meta.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.LookupAPIToken(ctx, raw, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("a revoked token still authenticated: %v", err)
		}
	})

	// Disabling an account must stop its automation too, or removing someone's
	// access leaves their scripts running as them.
	t.Run("token of a disabled account", func(t *testing.T) {
		other := newUser(t, d, "leaver")
		raw, _, err := d.CreateAPIToken(ctx, other.ID, "theirs", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetUserDisabled(ctx, other.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.LookupAPIToken(ctx, raw, ""); err == nil {
			t.Error("a disabled user's token still authenticated")
		}
	})

	// Guessing an id must not revoke someone else's credential.
	t.Run("revoking another user's token", func(t *testing.T) {
		victim := newUser(t, d, "victim")
		raw, meta, err := d.CreateAPIToken(ctx, victim.ID, "victims-token", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.RevokeAPIToken(ctx, user.ID, meta.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
		if _, _, err := d.LookupAPIToken(ctx, raw, ""); err != nil {
			t.Errorf("the victim's token was revoked by another user: %v", err)
		}
	})

	t.Run("a token needs a name", func(t *testing.T) {
		if _, _, err := d.CreateAPIToken(ctx, user.ID, "   ", 0, false); err == nil {
			t.Error("an unnamed token was created")
		}
	})
}

// Two tokens must never collide, and each must resolve to its own row.
func TestAPITokensAreDistinct(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	user := newUser(t, d, "carl")

	first, _, err := d.CreateAPIToken(ctx, user.ID, "one", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := d.CreateAPIToken(ctx, user.ID, "two", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two tokens came out identical")
	}

	_, a, err := d.LookupAPIToken(ctx, first, "")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := d.LookupAPIToken(ctx, second, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "one" || b.Name != "two" {
		t.Errorf("tokens resolved to %q and %q", a.Name, b.Name)
	}
}
