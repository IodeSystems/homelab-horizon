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

// Revocation has to be about the door, not the list. A token that stops being
// shown but keeps opening requests is the worst failure this feature can have,
// so the check is a real request through the real handler before and after —
// the after-state is a 401, not an absence.
//
// The revoke itself goes through the same two store calls the console's
// `homelab-horizon token revoke` makes (resolve the name, then RevokeAPIToken),
// which is also what the web UI's revoke button reaches, so all three paths are
// this one behaviour.
func TestRevokedTokenStopsAuthenticating(t *testing.T) {
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

	// One request, replayed. Same method, same path, same header both times, so
	// the only thing that changes between the two calls is the revocation.
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/account/tokens", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleAPIAccountTokens(w, r)
		return w
	}

	before := call()
	if before.Code != http.StatusOK {
		t.Fatalf("before revoking, status = %d, want 200: the control is broken, not the feature", before.Code)
	}
	if !strings.Contains(before.Body.String(), "ci-deploy") {
		t.Fatalf("before revoking, the handler did not answer as carl")
	}

	matches, err := s.users.APITokensByName(ctx, user.ID, "ci-deploy")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("resolving %q matched %d tokens, want 1", "ci-deploy", len(matches))
	}
	if err := s.users.RevokeAPIToken(ctx, user.ID, matches[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	after := call()
	if after.Code != http.StatusUnauthorized {
		t.Errorf("after revoking, status = %d, want 401 — the token still authenticates", after.Code)
	}
	if strings.Contains(after.Body.String(), "ci-deploy") {
		t.Error("the revoked token's request was still served")
	}

	// The auth layer itself, not only this endpoint: a revoked token must
	// resolve to nobody everywhere, and must not authorise an admin action.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	if got := s.currentUser(r); got != nil {
		t.Errorf("a revoked token still resolves to %s", got.Username)
	}
	if s.isAdmin(r) {
		t.Error("a revoked token still authorises admin actions")
	}
}

// A name is not unique — nothing in the schema or in creation makes it so — and
// a caller revoking by name has to be told that rather than shown one of them.
func TestAPITokenNamesAreNotUnique(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	for range 2 {
		if _, _, err := s.users.CreateAPIToken(ctx, user.ID, "ci-deploy", 0, false); err != nil {
			t.Fatalf("the store refused a duplicate name: %v", err)
		}
	}
	if _, _, err := s.users.CreateAPIToken(ctx, user.ID, "other", 0, false); err != nil {
		t.Fatal(err)
	}

	matches, err := s.users.APITokensByName(ctx, user.ID, "ci-deploy")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matched %d tokens named ci-deploy, want both", len(matches))
	}
	if matches[0].ID == matches[1].ID {
		t.Error("the same token was returned twice")
	}

	// A revoked token drops out of the resolution, so a name freed by a
	// revocation is unambiguous again.
	if err := s.users.RevokeAPIToken(ctx, user.ID, matches[0].ID); err != nil {
		t.Fatal(err)
	}
	matches, err = s.users.APITokensByName(ctx, user.ID, "ci-deploy")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("after revoking one, %d live tokens named ci-deploy, want 1", len(matches))
	}

	// And it does not reach past the name it was given.
	if got, _ := s.users.APITokensByName(ctx, user.ID, "nothing-called-this"); len(got) != 0 {
		t.Errorf("matched %d tokens for a name nobody used", len(got))
	}
}
