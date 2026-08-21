package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The whole point of a personal token: a scripted request is attributable to a
// person. The shared admin token could authenticate but not identify, which is
// what PCI DSS 8.2.1 is about, and why disabling it needed this to exist first.
func TestPersonalTokenAuthenticatesAndNamesTheUser(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	raw, _, err := s.users.CreateAPIToken(ctx, user.ID, "ci-deploy", 0, false)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	withToken := func(tok string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}

	t.Run("resolves to its owner", func(t *testing.T) {
		got := s.currentUser(withToken(raw))
		if got == nil || got.Username != "carl" {
			t.Fatalf("currentUser = %v, want carl", got)
		}
	})

	t.Run("counts as admin", func(t *testing.T) {
		if !s.isAdmin(withToken(raw)) {
			t.Error("a token owned by an admin should authorise admin actions")
		}
	})

	t.Run("audit names the user and the token", func(t *testing.T) {
		actor := s.adminActor(withToken(raw))
		if !strings.Contains(actor, "carl") {
			t.Errorf("actor = %q, want the username in it", actor)
		}
		if !strings.Contains(actor, "ci-deploy") {
			t.Errorf("actor = %q, want the token name so a scripted change is traceable", actor)
		}
	})

	t.Run("a bad token is not silently anonymous-but-allowed", func(t *testing.T) {
		bad := withToken(db.APITokenPrefix + "not-a-real-token")
		if s.currentUser(bad) != nil {
			t.Error("an invalid token resolved to a user")
		}
		if s.isAdmin(bad) {
			t.Error("an invalid token authorised an admin action")
		}
	})
}

// A request presenting a token means to use it. Falling back to a cookie would
// authenticate it as somebody else and put the wrong name in the audit log.
func TestPersonalTokenDoesNotFallBackToTheCookie(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := s.users.CreateSession(ctx, user.ID, db.DefaultSessionTTL, "", "")
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
	r.AddCookie(&http.Cookie{Name: userSessionCookie, Value: session})
	r.Header.Set("Authorization", "Bearer "+db.APITokenPrefix+"revoked-or-wrong")

	if got := s.currentUser(r); got != nil {
		t.Errorf("a bad token fell through to the session cookie and authenticated as %s", got.Username)
	}
}

// Managing tokens needs an account, not merely an administrative credential:
// the shared token has no user to own them.
func TestTokenEndpointNeedsAnAccount(t *testing.T) {
	s := userServer(t)

	w := httptest.NewRecorder()
	s.handleAPIAccountTokens(w, httptest.NewRequest(http.MethodGet, "/api/v1/account/tokens", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
