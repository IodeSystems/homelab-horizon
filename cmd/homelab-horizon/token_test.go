package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// `token create` without `token revoke` is the wrong half of a credential
// lifecycle: it is only useful until something goes wrong, and the case this
// command family exists for — the web UI is unreachable — is exactly when a
// leaked token most needs taking back. These tests hold the two halves
// together, and the one that matters is not "it left the list" but "it stopped
// authenticating".

// tokenCLI is one console against one identity store: a config file naming a
// temp database, plus the store opened alongside it so a test can assert on
// what the command actually did. Opening twice is the real arrangement — hz is
// running while the operator types this.
type tokenCLI struct {
	t          *testing.T
	configPath string
	store      *db.DB
}

func newTokenCLI(t *testing.T) *tokenCLI {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := &config.Config{
		ListenAddr:  "127.0.0.1:0",
		WGInterface: "wg0",
		VPNRange:    "10.100.0.0/24",
		UsersDB:     filepath.Join(dir, "hz.db"),
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	store, err := db.Open(cfg.UsersDBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &tokenCLI{t: t, configPath: configPath, store: store}
}

// run drives the real root command, so flag parsing, required flags and the
// subcommand wiring are all under test rather than the RunE body alone.
func (c *tokenCLI) run(stdin string, args ...string) (stdout, stderr string, err error) {
	c.t.Helper()
	var out, errOut bytes.Buffer
	root := newRoot()
	root.SetArgs(append([]string{"--config", c.configPath}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func (c *tokenCLI) user(username string) *db.User {
	c.t.Helper()
	u, err := c.store.CreateUser(context.Background(), username, "", db.RoleAdmin)
	if err != nil {
		c.t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// create mints through the CLI, which is the only way to get the raw value —
// the point of the round trip is that the thing being revoked is the thing the
// operator was handed.
func (c *tokenCLI) create(username, name string) string {
	c.t.Helper()
	stdout, stderr, err := c.run("", "token", "create", "--user", username, "--name", name)
	if err != nil {
		c.t.Fatalf("token create: %v (%s)", err, stderr)
	}
	token := strings.TrimSpace(stdout)
	if !strings.HasPrefix(token, db.APITokenPrefix) {
		c.t.Fatalf("token create printed something that is not a token on stdout")
	}
	return token
}

// authenticates asks the question the whole feature is about, through the
// function the HTTP layer calls for a Bearer token (internal/server's
// currentUser), not through the list.
func (c *tokenCLI) authenticates(token string) bool {
	c.t.Helper()
	user, _, err := c.store.LookupAPIToken(context.Background(), token, "")
	if err == nil {
		return user != nil
	}
	if errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrTokenExpired) {
		return false
	}
	c.t.Fatalf("lookup token: %v", err)
	return false
}

// The positive control. A token that has vanished from `token list` but still
// opens the door is the worst outcome this command could have, so the test
// resolves the credential before and after, and would fail if revoke only
// hid it.
func TestTokenRevokeStopsAuthentication(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	token := c.create("carl", "ci-deploy")

	if !c.authenticates(token) {
		t.Fatal("a freshly created token did not authenticate: the control is broken, not the feature")
	}

	_, stderr, err := c.run("", "token", "revoke", "--user", "carl", "--name", "ci-deploy", "--yes")
	if err != nil {
		t.Fatalf("token revoke: %v (%s)", err, stderr)
	}

	if c.authenticates(token) {
		t.Error("the revoked token still authenticates")
	}

	// And the list agrees — checked second, because on its own it proves
	// nothing about whether the credential works.
	stdout, _, err := c.run("", "token", "list", "--user", "carl")
	if err != nil {
		t.Fatalf("token list: %v", err)
	}
	if strings.Contains(stdout, "ci-deploy") {
		t.Errorf("revoked token is still listed:\n%s", stdout)
	}
}

// The token value must never reach a terminal log, a CI transcript or an error
// string: hz keeps only a hash, and the console is the one place tempted to
// echo what it just acted on.
func TestTokenRevokeNeverPrintsTheTokenValue(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	token := c.create("carl", "ci-deploy")

	stdout, stderr, err := c.run("", "token", "revoke", "--user", "carl", "--name", "ci-deploy", "--yes")
	if err != nil {
		t.Fatalf("token revoke: %v", err)
	}
	if strings.Contains(stdout+stderr, token) {
		t.Error("revoke echoed the token value")
	}

	// The error paths too: a "no such token" that quotes what was passed is
	// the same leak by another route.
	_, stderr, err = c.run("", "token", "revoke", "--user", "carl", "--id", token, "--yes")
	if err == nil {
		t.Fatal("revoking by a token value as if it were an id succeeded")
	}
	if strings.Contains(err.Error()+stderr, token) {
		t.Error("the failure echoed the token value")
	}
}

// (user, name) is NOT unique — the schema has no index over it and creation
// never looked — so a name can address two credentials. Picking one would be a
// coin flip over which pipeline keeps working.
func TestTokenRevokeRefusesAnAmbiguousName(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	first := c.create("carl", "ci-deploy")
	second := c.create("carl", "ci-deploy")

	_, stderr, err := c.run("", "token", "revoke", "--user", "carl", "--name", "ci-deploy", "--yes")
	if err == nil {
		t.Fatal("an ambiguous name revoked something")
	}
	if !strings.Contains(err.Error(), "--id") {
		t.Errorf("the refusal does not say how to proceed: %v", err)
	}

	// Both ids are named, so the operator can choose without a second command.
	for _, tok := range c.tokenIDs("carl") {
		if !strings.Contains(stderr, tok) {
			t.Errorf("the refusal does not list id %s:\n%s", tok, stderr)
		}
	}

	if !c.authenticates(first) || !c.authenticates(second) {
		t.Error("a refused revoke took a credential anyway")
	}
}

func (c *tokenCLI) tokenIDs(username string) []string {
	c.t.Helper()
	u, err := c.store.UserByUsername(context.Background(), username)
	if err != nil {
		c.t.Fatalf("user: %v", err)
	}
	tokens, err := c.store.ListAPITokens(context.Background(), u.ID)
	if err != nil {
		c.t.Fatalf("list: %v", err)
	}
	ids := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		ids = append(ids, tok.ID)
	}
	return ids
}

// --id is the way out of the ambiguity, so it has to take exactly the one
// asked for and leave its twin working.
func TestTokenRevokeByIDTakesOnlyThatToken(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	keep := c.create("carl", "ci-deploy")
	drop := c.create("carl", "ci-deploy")

	_, meta, err := c.store.LookupAPIToken(context.Background(), drop, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if _, stderr, err := c.run("", "token", "revoke", "--user", "carl",
		"--id", meta.ID, "--yes"); err != nil {
		t.Fatalf("token revoke --id: %v (%s)", err, stderr)
	}

	if c.authenticates(drop) {
		t.Error("the named token still authenticates")
	}
	if !c.authenticates(keep) {
		t.Error("revoking one token took its namesake with it")
	}
}

// An id belonging to someone else must not be revokable, and must not be
// distinguishable from one that does not exist.
func TestTokenRevokeCannotReachAnotherUsersToken(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	c.user("dana")
	carls := c.create("carl", "ci-deploy")
	ids := c.tokenIDs("carl")

	_, _, err := c.run("", "token", "revoke", "--user", "dana", "--id", ids[0], "--yes")
	if err == nil {
		t.Fatal("dana revoked carl's token")
	}
	if !c.authenticates(carls) {
		t.Error("carl's token stopped working after dana's failed attempt")
	}

	// Same wording as a plain typo, so the failure does not confirm that the
	// id exists somewhere. Compared with the id itself blanked out, since
	// each message legitimately quotes the one it was handed.
	const fake = "pat_definitely_not_real"
	_, _, missing := c.run("", "token", "revoke", "--user", "dana", "--id", fake, "--yes")
	if missing == nil {
		t.Fatal("a made-up id succeeded")
	}
	real := strings.ReplaceAll(err.Error(), ids[0], "<id>")
	absent := strings.ReplaceAll(missing.Error(), fake, "<id>")
	if real != absent {
		t.Errorf("an id owned by someone else reads differently from one that does not exist:\n %s\n %s",
			real, absent)
	}
}

// Without --yes the command asks, and anything but yes leaves the credential
// alone. The bar is one typed y rather than config remove's typed-back name:
// this is recoverable in one command (mint another), and it is the emergency
// path, where a ceremony is a cost paid every time to prevent a mistake that
// re-typing the same wrong name would not catch anyway.
func TestTokenRevokeAsksBeforeTakingAccess(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")

	t.Run("a refusal keeps the token working", func(t *testing.T) {
		token := c.create("carl", "declined")
		_, stderr, err := c.run("n\n", "token", "revoke", "--user", "carl", "--name", "declined")
		if err == nil {
			t.Fatal("answering n revoked the token")
		}
		if !strings.Contains(stderr, "Revoke it?") {
			t.Errorf("no prompt was shown:\n%s", stderr)
		}
		if !c.authenticates(token) {
			t.Error("a declined revoke took the credential anyway")
		}
	})

	t.Run("y revokes", func(t *testing.T) {
		token := c.create("carl", "accepted")
		if _, stderr, err := c.run("y\n", "token", "revoke", "--user", "carl", "--name", "accepted"); err != nil {
			t.Fatalf("token revoke: %v (%s)", err, stderr)
		}
		if c.authenticates(token) {
			t.Error("the confirmed token still authenticates")
		}
	})

	// A pipeline or a cron job reaching the prompt gets an answer, not a hang
	// and not a silent yes.
	t.Run("no stdin refuses and names --yes", func(t *testing.T) {
		token := c.create("carl", "unattended")
		_, stderr, err := c.run("", "token", "revoke", "--user", "carl", "--name", "unattended")
		if err == nil {
			t.Fatal("an unanswerable prompt revoked the token")
		}
		if !strings.Contains(err.Error(), "nothing was revoked") {
			t.Errorf("the failure does not say the token survived: %v", err)
		}
		if !strings.Contains(stderr, "--yes") {
			t.Errorf("nothing named the way through:\n%s", stderr)
		}
		if !c.authenticates(token) {
			t.Error("the token was revoked despite the failure")
		}
	})
}

// Revoking the last credential is allowed — the operator whose only token
// leaked needs it gone now, and root at this console can always mint another —
// but it is never silent, and it is said before the prompt rather than after
// the act.
func TestTokenRevokeWarnsWhenItIsTheLastWayIn(t *testing.T) {
	c := newTokenCLI(t)

	t.Run("no password left: the account has no way in at all", func(t *testing.T) {
		c.user("scripted")
		token := c.create("scripted", "only-credential")

		_, stderr, err := c.run("", "token", "revoke", "--user", "scripted",
			"--name", "only-credential", "--yes")
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if !strings.Contains(stderr, "no way in at all") {
			t.Errorf("no warning that this was the last credential:\n%s", stderr)
		}
		if !strings.Contains(stderr, "user set-password") {
			t.Errorf("the warning does not say how to give the account a way back:\n%s", stderr)
		}
		// Warned, not refused.
		if c.authenticates(token) {
			t.Error("the warning turned into a refusal: the token still works")
		}
	})

	t.Run("a password remains: a milder note, not the warning", func(t *testing.T) {
		u := c.user("human")
		if err := c.store.SetPassword(context.Background(), u.ID, "a-long-enough-password"); err != nil {
			t.Fatalf("set password: %v", err)
		}
		token := c.create("human", "last-token")

		_, stderr, err := c.run("", "token", "revoke", "--user", "human",
			"--name", "last-token", "--yes")
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if strings.Contains(stderr, "no way in at all") {
			t.Errorf("warned about a lockout that cannot happen:\n%s", stderr)
		}
		if !strings.Contains(stderr, "last API token") {
			t.Errorf("did not say this was the last token:\n%s", stderr)
		}
		if c.authenticates(token) {
			t.Error("the token still authenticates")
		}
	})

	t.Run("another token remains: nothing to warn about", func(t *testing.T) {
		c.user("busy")
		c.create("busy", "one")
		c.create("busy", "two")

		_, stderr, err := c.run("", "token", "revoke", "--user", "busy", "--name", "one", "--yes")
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if strings.Contains(stderr, "last API token") {
			t.Errorf("warned about a last token that is not the last:\n%s", stderr)
		}
	})
}

// --id is unusable if nothing ever shows an id, which is how a list becomes a
// screen you cannot act on.
func TestTokenListShowsTheIDToRevokeBy(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	c.create("carl", "ci-deploy")
	ids := c.tokenIDs("carl")

	stdout, _, err := c.run("", "token", "list", "--user", "carl")
	if err != nil {
		t.Fatalf("token list: %v", err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, ids[0]) {
		t.Errorf("token list does not show the id revoke needs:\n%s", stdout)
	}
}

func TestTokenRevokeNeedsExactlyOneSelector(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")
	c.create("carl", "ci-deploy")
	ids := c.tokenIDs("carl")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"neither", []string{"token", "revoke", "--user", "carl", "--yes"}},
		{"both", []string{"token", "revoke", "--user", "carl", "--name", "ci-deploy", "--id", ids[0], "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.run("", tc.args...); err == nil {
				t.Error("accepted an ambiguous selection")
			}
		})
	}

	// Neither attempt may have acted.
	if len(c.tokenIDs("carl")) != 1 {
		t.Error("a rejected command revoked something")
	}
}

func TestTokenRevokeReportsWhatIsNotThere(t *testing.T) {
	c := newTokenCLI(t)
	c.user("carl")

	_, _, err := c.run("", "token", "revoke", "--user", "nobody", "--name", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no such user") {
		t.Errorf("unknown user: %v", err)
	}

	_, stderr, err := c.run("", "token", "revoke", "--user", "carl", "--name", "never-existed", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no live token named") {
		t.Errorf("unknown token: %v", err)
	}
	// The way forward is prose on stderr, not crammed into the error: this
	// binary logs a returned error as a single slog field, where an embedded
	// newline becomes a literal \n nobody reads.
	if !strings.Contains(stderr, "token list --user carl") {
		t.Errorf("nothing pointed the operator at the list:\n%s", stderr)
	}
}
