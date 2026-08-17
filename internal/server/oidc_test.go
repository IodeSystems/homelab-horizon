package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

func TestOIDCReadyRefusesIncompleteConfig(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.Config
		ready bool
		says  string
	}{
		{"disabled", config.Config{}, false, ""},
		{
			"no issuer",
			config.Config{AdminURL: "https://hz.test", OIDC: &config.OIDCConfig{Enabled: true, ClientID: "x"}},
			false, "issuer",
		},
		{
			"no client id",
			config.Config{AdminURL: "https://hz.test", OIDC: &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test"}},
			false, "client_id",
		},
		{
			"no admin url",
			config.Config{OIDC: &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x"}},
			false, "admin_url",
		},
		{
			// The redirect URI is derived from admin_url, and a provider will
			// not redirect to http.
			"http admin url",
			config.Config{AdminURL: "http://hz.test", OIDC: &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x"}},
			false, "https",
		},
		{
			"complete",
			config.Config{AdminURL: "https://hz.test", OIDC: &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x"}},
			true, "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ready, reason := tc.cfg.OIDCReady()
			if ready != tc.ready {
				t.Fatalf("ready = %v, want %v (%s)", ready, tc.ready, reason)
			}
			if tc.says != "" && !strings.Contains(reason, tc.says) {
				t.Fatalf("reason %q should mention %q", reason, tc.says)
			}
		})
	}
}

func TestOIDCRedirectURI(t *testing.T) {
	cfg := config.Config{AdminURL: "https://hz.test/"}
	// The trailing slash must not produce a double slash: providers match the
	// redirect URI exactly, so one stray character is a failed login.
	if got := cfg.OIDCRedirectURI(); got != "https://hz.test/api/v1/auth/oidc/callback" {
		t.Fatalf("redirect = %q", got)
	}
}

// State must be single use, or a captured callback URL can be replayed.
func TestOIDCFlowIsSingleUse(t *testing.T) {
	store := newOIDCFlowStore()
	state, nonce, verifier, err := store.start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(state) < 32 || len(nonce) < 32 || len(verifier) < 32 {
		t.Fatalf("flow values are too short to be unguessable: %d/%d/%d",
			len(state), len(nonce), len(verifier))
	}
	if state == nonce || state == verifier || nonce == verifier {
		t.Fatal("flow values must be independent")
	}

	if _, ok := store.take(state); !ok {
		t.Fatal("first take failed")
	}
	if _, ok := store.take(state); ok {
		t.Fatal("state was accepted twice")
	}
}

func TestOIDCFlowExpires(t *testing.T) {
	store := newOIDCFlowStore()
	state, _, _, _ := store.start()

	store.mu.Lock()
	entry := store.m[state]
	entry.expires = time.Now().Add(-time.Minute)
	store.m[state] = entry
	store.mu.Unlock()

	if _, ok := store.take(state); ok {
		t.Fatal("an expired flow was accepted")
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	// RFC 7636 appendix B's worked example: if this drifts, providers reject
	// the exchange with an error that says nothing useful.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := pkceChallenge(verifier); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestGroupsFromHandlesProviderShapes(t *testing.T) {
	tests := []struct {
		name  string
		raw   map[string]any
		claim string
		want  int
	}{
		{"list of strings", map[string]any{"groups": []any{"admins", "staff"}}, "groups", 2},
		{"single string", map[string]any{"groups": "admins"}, "groups", 1},
		{"missing claim", map[string]any{"other": "x"}, "groups", 0},
		{"no claim configured", map[string]any{"groups": []any{"admins"}}, "", 0},
		// Unknown shapes must yield nothing rather than something: the result
		// gates access, so it has to fail closed.
		{"nested objects", map[string]any{"groups": []any{map[string]any{"name": "admins"}}}, "groups", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupsFrom(tc.raw, tc.claim); len(got) != tc.want {
				t.Fatalf("got %v, want %d entries", got, tc.want)
			}
		})
	}
}

func TestUsernameForPrefersStableClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims oidcClaims
		want   string
	}{
		{"preferred username", oidcClaims{PreferredUsername: "carl", Email: "other@x.test", Subject: "s1"}, "carl"},
		{"verified email local part", oidcClaims{Email: "carl@x.test", EmailVerified: true, Subject: "s1"}, "carl"},
		// An unverified email is a claim about somebody else's address until
		// the provider says otherwise.
		{"unverified email falls back to subject", oidcClaims{Email: "carl@x.test", Subject: "s1"}, "s1"},
		{"nothing but subject", oidcClaims{Subject: "s1"}, "s1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.claims.usernameFor(); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// The link is by subject, so a provider-side rename must not orphan or
// hijack an account.
func TestOIDCLinkFollowsSubjectNotUsername(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if _, err := s.users.AddCredential(ctx, user.ID, db.KindOIDC, "subject-123", "", "sso"); err != nil {
		t.Fatalf("link: %v", err)
	}

	got, _, err := s.users.UserByOIDCSubject(ctx, "subject-123")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != user.ID {
		t.Fatal("subject resolved to the wrong account")
	}

	// A different subject with a colliding username must not resolve here.
	if _, _, err := s.users.UserByOIDCSubject(ctx, "subject-999"); err == nil {
		t.Fatal("an unlinked subject resolved to an account")
	}
}

// Auto-provisioning off means SSO attaches to accounts an operator made, and
// creates nothing on its own.
func TestOIDCRefusesUnknownUserWithoutAutoProvision(t *testing.T) {
	s := userServer(t)
	cfg := *s.cfg()
	cfg.AdminURL = "https://hz.test"
	cfg.OIDC = &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x"}
	s.config.Store(&cfg)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback", nil)
	_, err := s.resolveOIDCUser(r, oidcClaims{Subject: "s1", PreferredUsername: "ghost"}, db.RoleAdmin)
	if err == nil {
		t.Fatal("an unknown user was admitted with auto-provisioning off")
	}

	users, _ := s.users.ListUsers(context.Background())
	if len(users) != 0 {
		t.Fatalf("an account was created anyway: %+v", users)
	}
}

func TestOIDCAttachesToExistingAccount(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	cfg := *s.cfg()
	cfg.AdminURL = "https://hz.test"
	cfg.OIDC = &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x"}
	s.config.Store(&cfg)

	existing, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback", nil)
	got, err := s.resolveOIDCUser(r, oidcClaims{Subject: "s1", PreferredUsername: "carl"}, db.RoleAdmin)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != existing.ID {
		t.Fatal("SSO created a duplicate instead of attaching")
	}

	// And the link is recorded, so the next sign-in goes by subject.
	if _, _, err := s.users.UserByOIDCSubject(ctx, "s1"); err != nil {
		t.Fatalf("subject was not linked: %v", err)
	}
}

// A disabled account must not be a way back in through the provider.
func TestOIDCRefusesDisabledAccount(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	cfg := *s.cfg()
	cfg.AdminURL = "https://hz.test"
	cfg.OIDC = &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x", AutoProvision: true}
	s.config.Store(&cfg)

	user, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_, _ = s.users.AddCredential(ctx, user.ID, db.KindOIDC, "s1", "", "sso")
	_ = s.users.SetUserDisabled(ctx, user.ID, true)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback", nil)
	if _, err := s.resolveOIDCUser(r, oidcClaims{Subject: "s1", PreferredUsername: "carl"}, db.RoleAdmin); err == nil {
		t.Fatal("a disabled account signed in through SSO")
	}
}

func TestAnyGroupMatchesIsCaseInsensitive(t *testing.T) {
	if !anyGroupMatches([]string{"Admins"}, []string{"admins"}) {
		t.Error("group matching should ignore case")
	}
	if anyGroupMatches([]string{"staff"}, []string{"admins"}) {
		t.Error("unrelated groups must not match")
	}
	if anyGroupMatches(nil, []string{"admins"}) {
		t.Error("no groups cannot satisfy a required group")
	}
}

// SSO must never become the only way in: the login page has to keep offering
// what works when the provider does not.
func TestOIDCStatusReportsWithoutBreakingLocalLogin(t *testing.T) {
	s := userServer(t)
	cfg := *s.cfg()
	cfg.AdminURL = "https://hz.test"
	cfg.OIDC = &config.OIDCConfig{Enabled: true, Issuer: "https://idp.test", ClientID: "x", Name: "Authentik"}
	s.config.Store(&cfg)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/status", nil)
	w := httptest.NewRecorder()
	s.handleAPIOIDCStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Authentik") {
		t.Fatalf("status should name the provider: %s", w.Body.String())
	}

	// The local path is untouched by SSO being configured.
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")
	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("local login broke with SSO configured: %d %s", login.Code, login.Body)
	}
}
