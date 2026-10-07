package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// An account created for a PERSON can sign in (loadtest child, 2026-10-07:
// `user create carl --admin --token-file F` made an account with no password,
// so carl could not sign in to the UI, and nothing said so). --sign-in prints
// a one-time password to the terminal, once, which must be replaced at the
// first sign-in.
func TestUserCreateForAPersonCanSignIn(t *testing.T) {
	asRoot(t, 0)
	asTerminal(t, true)
	c := newTokenCLI(t)
	ctx := context.Background()

	stdout, stderr, err := c.run("", "user", "create", "carl", "--admin", "--sign-in")
	if err != nil {
		t.Fatalf("user create --sign-in: %v\n%s", err, stderr)
	}
	pw := strings.TrimSpace(stdout)
	if pw == "" || strings.Contains(pw, "\n") || strings.Contains(stderr, pw) {
		t.Fatalf("stdout must hold the password alone, stderr never: stdout %q", stdout)
	}
	user, err := c.store.VerifyPassword(ctx, "carl", pw)
	if err != nil || user.Username != "carl" || user.Role != db.RoleAdmin {
		t.Fatalf("the printed password does not sign carl in: %+v %v", user, err)
	}
	if must, err := c.store.PasswordMustChange(ctx, user.ID); err != nil || !must {
		t.Fatalf("the one-time password is not forced to change (must=%v, %v)", must, err)
	}

	// Instrument: a service account (token file only) cannot — the state carl
	// was in. Without this the check above could pass for any account.
	if _, _, err := c.run("", "user", "create", "svc", "--admin", "--token-file", filepath.Join(t.TempDir(), "svc.token")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.VerifyPassword(ctx, "svc", pw); err == nil {
		t.Fatal("instrument: a token-only account signs in with carl's password")
	}
}

func TestUserCreateSaysWhoItIsFor(t *testing.T) {
	asRoot(t, 0)
	c := newTokenCLI(t)

	asTerminal(t, true)
	if _, _, err := c.run("", "user", "create", "dave", "--admin"); err == nil || !strings.Contains(err.Error(), "--sign-in") {
		t.Fatalf("neither --sign-in nor --token-file: %v", err)
	}
	asTerminal(t, false)
	if _, _, err := c.run("", "user", "create", "dave", "--admin", "--sign-in"); err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("--sign-in to a pipe: %v", err)
	}
	if _, err := c.store.UserByUsername(context.Background(), "dave"); err == nil {
		t.Fatal("a refused create made the account")
	}
}
