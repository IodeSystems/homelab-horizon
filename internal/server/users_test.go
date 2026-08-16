package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// userServer builds a Server with a real identity store and nothing else, so
// these tests exercise the auth paths rather than hz's startup.
func userServer(t *testing.T) *Server {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &Server{adminToken: "test-admin-token", users: store}
	s.config.Store(&config.Config{})
	return s
}

func postJSON(t *testing.T, s *Server, h http.HandlerFunc, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// sessionCookie pulls the account cookie out of a response.
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == userSessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", userSessionCookie)
	return nil
}

// The bootstrap door must be open exactly once. Left open, it is an
// unauthenticated way to mint an admin on an internet-facing gateway.
func TestBootstrapClosesAfterFirstAccount(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	if !s.bootstrapAllowed(ctx) {
		t.Fatal("bootstrap should be open with no accounts")
	}

	w := postJSON(t, s, s.handleAPIUsers, "/api/v1/users", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap create = %d: %s", w.Code, w.Body)
	}
	if s.bootstrapAllowed(ctx) {
		t.Fatal("bootstrap still open after an account exists")
	}

	// A second unauthenticated create must now be refused.
	w = postJSON(t, s, s.handleAPIUsers, "/api/v1/users", map[string]string{
		"username": "intruder", "password": "correct-horse-battery",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("second create = %d, want 401: %s", w.Code, w.Body)
	}
}

// The bootstrap user must not have to log in again immediately.
func TestBootstrapUserIsLoggedIn(t *testing.T) {
	s := userServer(t)

	w := postJSON(t, s, s.handleAPIUsers, "/api/v1/users", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, w)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.AddCookie(cookie)
	if u := s.currentUser(r); u == nil || u.Username != "carl" {
		t.Fatalf("bootstrap session does not resolve: %+v", u)
	}
	if !s.isAdmin(r) {
		t.Fatal("bootstrap user is not admin")
	}
}

// A user session must work when the shared token is disabled — that is the
// entire reason accounts exist.
func TestUserSessionSurvivesTokenDisable(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.users.SetPassword(ctx, user.ID, "correct-horse-battery"); err != nil {
		t.Fatalf("password: %v", err)
	}

	w := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)

	cfg := *s.cfg()
	cfg.AdminTokenDisabled = true
	s.config.Store(&cfg)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.AddCookie(cookie)
	if !s.isAdmin(r) {
		t.Fatal("account lost admin when the shared token was disabled")
	}

	// And the token-minted cookie must NOT survive it.
	rt := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rt.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	if s.isAdmin(rt) {
		t.Fatal("a token-minted session still authenticates after the token was disabled")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	wrongPass := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "nope-not-it-at-all",
	})
	noSuchUser := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "ghost", "password": "nope-not-it-at-all",
	})

	if wrongPass.Code != http.StatusUnauthorized || noSuchUser.Code != http.StatusUnauthorized {
		t.Fatalf("codes = %d / %d, want 401 / 401", wrongPass.Code, noSuchUser.Code)
	}
	if wrongPass.Body.String() != noSuchUser.Body.String() {
		t.Fatalf("responses differ and so enumerate accounts:\n  %s  %s",
			wrongPass.Body.String(), noSuchUser.Body.String())
	}
}

// Changing your own password must require the current one: a borrowed session
// should not convert into permanent ownership of the account.
func TestSelfPasswordChangeRequiresCurrent(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)

	bad := postJSON(t, s, s.handleAPIUserPassword, "/api/v1/users/password", map[string]string{
		"currentPassword": "wrong-one-entirely", "password": "new-password-here",
	}, cookie)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("change without the current password = %d, want 403: %s", bad.Code, bad.Body)
	}

	ok := postJSON(t, s, s.handleAPIUserPassword, "/api/v1/users/password", map[string]string{
		"currentPassword": "correct-horse-battery", "password": "new-password-here",
	}, cookie)
	if ok.Code != http.StatusOK {
		t.Fatalf("change = %d: %s", ok.Code, ok.Body)
	}
	if _, err := s.users.VerifyPassword(ctx, "carl", "new-password-here"); err != nil {
		t.Fatalf("new password does not work: %v", err)
	}
}

// The guard that stops the UI walking someone into a console-only recovery.
func TestCannotDisableLastAdminWithoutTheToken(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	cfg := *s.cfg()
	cfg.AdminTokenDisabled = true
	s.config.Store(&cfg)

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)

	w := postJSON(t, s, s.handleAPIUserDisable, "/api/v1/users/disable",
		map[string]any{"userId": u.ID, "disabled": true}, cookie)
	if w.Code != http.StatusConflict {
		t.Fatalf("disabling the last admin = %d, want 409: %s", w.Code, w.Body)
	}

	// With a second admin present it is allowed.
	other, _ := s.users.CreateUser(ctx, "second", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, other.ID, "another-password-x")
	w = postJSON(t, s, s.handleAPIUserDisable, "/api/v1/users/disable",
		map[string]any{"userId": u.ID, "disabled": true}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("disable with a spare admin = %d: %s", w.Code, w.Body)
	}
}

// hasUsableAccount gates disabling the shared token, so an invite that nobody
// has accepted must not count as a way back in.
func TestInviteIsNotAWayIn(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	if _, err := s.users.CreateUser(ctx, "invited", "", db.RoleAdmin); err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.hasUsableAccount(ctx) {
		t.Fatal("a credential-less invite counts as a usable account")
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)

	postJSON(t, s, s.handleAPILogout, "/api/v1/auth/logout", nil, cookie)

	// The cookie value must be dead server-side, not merely cleared in the
	// browser: a copied cookie has to stop working too.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.AddCookie(cookie)
	if u := s.currentUser(r); u != nil {
		t.Fatal("the session still resolves after logout")
	}
}

// With no store, hz must still authenticate the old ways rather than 500 or
// deny everything.
func TestNilStoreDegradesToTokenAuth(t *testing.T) {
	s := &Server{adminToken: "test-admin-token"}
	s.config.Store(&config.Config{})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	if !s.isAdmin(r) {
		t.Fatal("token auth broke when the identity store was unavailable")
	}
	if s.currentUser(r) != nil {
		t.Fatal("currentUser should be nil with no store")
	}
	if s.hasUsableAccount(context.Background()) {
		t.Fatal("no store cannot mean a usable account exists")
	}

	w := postJSON(t, s, s.handleAPIUsers, "/api/v1/users", map[string]string{"username": "x"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create with no store = %d, want 503", w.Code)
	}
}
