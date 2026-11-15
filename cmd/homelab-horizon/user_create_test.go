package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// asRoot makes the root-only console commands runnable under the test.
func asRoot(t *testing.T, uid int) {
	t.Helper()
	prev := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = prev })
}

// `user create` is how a box nobody can sign in to gets its first admin: the
// account, and a token that AUTHENTICATES, in a 0600 file, never on a terminal.
func TestUserCreateMakesAnAdminWhoseTokenAuthenticates(t *testing.T) {
	asRoot(t, 0)
	c := newTokenCLI(t)
	tokFile := filepath.Join(t.TempDir(), "admin.token")

	stdout, stderr, err := c.run("", "user", "create", "hz-local-admin", "--admin", "--token-file", tokFile)
	if err != nil {
		t.Fatalf("user create: %v\n%s", err, stderr)
	}
	raw, err := os.ReadFile(tokFile)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(token, db.APITokenPrefix) {
		t.Fatalf("the file does not hold a personal API token (%d bytes)", len(raw))
	}
	user, _, err := c.store.LookupAPIToken(context.Background(), token, "")
	if err != nil || user.Username != "hz-local-admin" || user.Role != db.RoleAdmin {
		t.Fatalf("the token does not authenticate as the new admin: %+v %v", user, err)
	}
	st, err := os.Stat(tokFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v (%v), want 0600", st.Mode().Perm(), err)
	}
	if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Fatal("the token was printed")
	}
	if stdout != "" {
		t.Errorf("stdout is not empty: %q", stdout)
	}
	if !strings.Contains(stderr, tokFile) {
		t.Errorf("stderr does not name the file: %q", stderr)
	}
}

// A second create for the same name is refused and mints nothing: re-running
// the bootstrap must never give one account two credentials.
func TestUserCreateRefusesADuplicate(t *testing.T) {
	asRoot(t, 0)
	c := newTokenCLI(t)
	dir := t.TempDir()
	if _, stderr, err := c.run("", "user", "create", "hz-local-admin", "--admin", "--token-file", filepath.Join(dir, "a.token")); err != nil {
		t.Fatalf("first create: %v\n%s", err, stderr)
	}
	second := filepath.Join(dir, "b.token")
	_, _, err := c.run("", "user", "create", "HZ-Local-Admin", "--admin", "--token-file", second)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create: %v", err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("a refused create left a token file behind: %v", err)
	}
	users, _ := c.store.ListUsers(context.Background())
	tokens, _ := c.store.ListAPITokens(context.Background(), users[0].ID)
	if len(users) != 1 || len(tokens) != 1 {
		t.Fatalf("after a refused duplicate: %d users, %d tokens", len(users), len(tokens))
	}
}

// An existing token file is never overwritten, and the refusal comes before
// the account: nothing is created.
func TestUserCreateRefusesAnExistingTokenFile(t *testing.T) {
	asRoot(t, 0)
	c := newTokenCLI(t)
	tokFile := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(tokFile, []byte("someone else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.run("", "user", "create", "hz-local-admin", "--admin", "--token-file", tokFile); err == nil {
		t.Fatal("an existing token file was accepted")
	}
	if got, _ := os.ReadFile(tokFile); string(got) != "someone else's\n" {
		t.Fatalf("the existing file was changed: %q", got)
	}
	if users, _ := c.store.ListUsers(context.Background()); len(users) != 0 {
		t.Fatalf("an account was created anyway: %d", len(users))
	}
}

// Root only, and --admin is said out loud.
func TestUserCreateNeedsRootAndAdmin(t *testing.T) {
	c := newTokenCLI(t)
	tokFile := filepath.Join(t.TempDir(), "admin.token")

	asRoot(t, 1000)
	if _, _, err := c.run("", "user", "create", "x", "--admin", "--token-file", tokFile); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("non-root: %v", err)
	}
	asRoot(t, 0)
	if _, _, err := c.run("", "user", "create", "x", "--token-file", tokFile); err == nil || !strings.Contains(err.Error(), "--admin") {
		t.Fatalf("without --admin: %v", err)
	}
	if users, _ := c.store.ListUsers(context.Background()); len(users) != 0 {
		t.Fatalf("a refused create made an account: %d", len(users))
	}
}
