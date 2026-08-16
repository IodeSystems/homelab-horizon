package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// enrolledUser returns a user with a confirmed TOTP factor and its secret.
func enrolledUser(t *testing.T, s *Server) (*db.User, string) {
	t.Helper()
	ctx := context.Background()

	u, err := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.users.SetPassword(ctx, u.ID, "correct-horse-battery"); err != nil {
		t.Fatalf("password: %v", err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "test", AccountName: "carl"})
	if err != nil {
		t.Fatalf("totp: %v", err)
	}
	if _, err := s.users.AddCredential(ctx, u.ID, db.KindTOTP, key.Secret(), "", "app"); err != nil {
		t.Fatalf("add factor: %v", err)
	}
	return u, key.Secret()
}

func decodeLogin(t *testing.T, w *httptest.ResponseRecorder) apitypes.LoginResponse {
	t.Helper()
	var resp apitypes.LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return resp
}

// The property the whole phase rests on: a correct password alone must not
// produce a session once a factor is enrolled.
func TestPasswordAloneIsNotASession(t *testing.T) {
	s := userServer(t)
	enrolledUser(t, s)

	w := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})

	for _, c := range w.Result().Cookies() {
		if c.Name == userSessionCookie && c.Value != "" {
			t.Fatal("a session cookie was issued before the second factor")
		}
	}
	resp := decodeLogin(t, w)
	if !resp.MFARequired || resp.PendingID == "" {
		t.Fatalf("want an MFA challenge, got %+v", resp)
	}
	if resp.OK {
		t.Fatal("response claims success before the factor was satisfied")
	}
}

func TestTOTPCompletesLogin(t *testing.T) {
	s := userServer(t)
	_, secret := enrolledUser(t, s)

	first := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	pending := decodeLogin(t, first).PendingID

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	w := postJSON(t, s, s.handleAPILoginTOTP, "/api/v1/auth/login/totp", map[string]string{
		"pendingId": pending, "code": code,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("finish = %d: %s", w.Code, w.Body)
	}

	cookie := sessionCookie(t, w)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.AddCookie(cookie)
	if u := s.currentUser(r); u == nil || u.Username != "carl" {
		t.Fatal("session does not resolve after the second factor")
	}
}

// A pending id must be single use, so a captured one cannot be used to grind
// codes at leisure.
func TestPendingLoginIsConsumedByAWrongCode(t *testing.T) {
	s := userServer(t)
	_, secret := enrolledUser(t, s)

	first := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	pending := decodeLogin(t, first).PendingID

	bad := postJSON(t, s, s.handleAPILoginTOTP, "/api/v1/auth/login/totp", map[string]string{
		"pendingId": pending, "code": "000000",
	})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d, want 401", bad.Code)
	}

	// Now the right code against the same id must also fail: the id is spent.
	code, _ := totp.GenerateCode(secret, time.Now())
	retry := postJSON(t, s, s.handleAPILoginTOTP, "/api/v1/auth/login/totp", map[string]string{
		"pendingId": pending, "code": code,
	})
	if retry.Code == http.StatusOK {
		t.Fatal("a spent pending id was accepted again")
	}
}

func TestPendingLoginExpires(t *testing.T) {
	p := newPendingLoginStore()
	id, err := p.add("usr_1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// Backdate past the TTL.
	p.mu.Lock()
	entry := p.m[id]
	entry.expires = time.Now().Add(-time.Minute)
	p.m[id] = entry
	p.mu.Unlock()

	if _, ok := p.peek(id); ok {
		t.Error("peek accepted an expired pending login")
	}
	if _, ok := p.take(id); ok {
		t.Error("take accepted an expired pending login")
	}
}

// An unguessable id is what stands in for the password having been checked.
func TestPendingIDIsNotTheUserID(t *testing.T) {
	p := newPendingLoginStore()
	id, _ := p.add("usr_1m04g30re4rjba9781vdze")
	if id == "usr_1m04g30re4rjba9781vdze" || len(id) < 32 {
		t.Fatalf("pending id is guessable: %q", id)
	}
}

// A TOTP secret must not become active until a code proves it was stored.
func TestTOTPIsNotActiveUntilConfirmed(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)

	enroll := postJSON(t, s, s.handleAPIAccountTOTPEnroll, "/api/v1/account/totp/enroll", nil, cookie)
	if enroll.Code != http.StatusOK {
		t.Fatalf("enroll = %d: %s", enroll.Code, enroll.Body)
	}
	var er apitypes.AccountTOTPEnrollResponse
	_ = json.Unmarshal(enroll.Body.Bytes(), &er)
	if er.Secret == "" {
		t.Fatal("no secret issued")
	}

	// Unconfirmed: the account must still have no second factor, or a failed
	// scan would lock the user out of their own account.
	if has, _ := s.users.HasSecondFactor(ctx, u.ID); has {
		t.Fatal("an unconfirmed secret counts as an enrolled factor")
	}

	code, _ := totp.GenerateCode(er.Secret, time.Now())
	confirm := postJSON(t, s, s.handleAPIAccountTOTPConfirm, "/api/v1/account/totp/confirm",
		map[string]string{"code": code}, cookie)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", confirm.Code, confirm.Body)
	}
	if has, _ := s.users.HasSecondFactor(ctx, u.ID); !has {
		t.Fatal("confirmed secret did not activate")
	}
}

func TestWrongConfirmCodeDoesNotEnrol(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()
	u, _ := s.users.CreateUser(ctx, "carl", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, u.ID, "correct-horse-battery")

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)
	postJSON(t, s, s.handleAPIAccountTOTPEnroll, "/api/v1/account/totp/enroll", nil, cookie)

	w := postJSON(t, s, s.handleAPIAccountTOTPConfirm, "/api/v1/account/totp/confirm",
		map[string]string{"code": "000000"}, cookie)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong confirm code = %d, want 401", w.Code)
	}
	if has, _ := s.users.HasSecondFactor(ctx, u.ID); has {
		t.Fatal("a rejected code still enrolled the factor")
	}
}

// Removing a factor is an account-scoped operation: one user must not be able
// to delete another's by id.
func TestCannotDeleteAnotherAccountsFactor(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	victim, secret := enrolledUser(t, s)
	_ = secret
	attacker, _ := s.users.CreateUser(ctx, "mallory", "", db.RoleAdmin)
	_ = s.users.SetPassword(ctx, attacker.ID, "correct-horse-battery")

	creds, _ := s.users.CredentialsFor(ctx, victim.ID, db.KindTOTP)
	if len(creds) != 1 {
		t.Fatalf("setup: victim has %d factors", len(creds))
	}

	login := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "mallory", "password": "correct-horse-battery",
	})
	cookie := sessionCookie(t, login)

	r := httptest.NewRequest(http.MethodDelete, "/api/v1/account/factors?id="+creds[0].ID, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.handleAPIAccountFactors(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-account delete = %d, want 404: %s", w.Code, w.Body)
	}
	if still, _ := s.users.CredentialsFor(ctx, victim.ID, db.KindTOTP); len(still) != 1 {
		t.Fatal("another account's factor was deleted")
	}
}

// Passkeys need an https admin_url; without one the option must be withheld
// rather than offered and failing after the browser prompt.
func TestPasskeysWithheldWithoutHTTPSAdminURL(t *testing.T) {
	s := userServer(t)

	if ok, reason := s.accountPasskeysAvailable(); ok || reason == "" {
		t.Fatalf("no admin_url should withhold passkeys, got ok=%v reason=%q", ok, reason)
	}

	cfg := *s.cfg()
	cfg.AdminURL = "http://hz.example.com"
	s.config.Store(&cfg)
	if ok, reason := s.accountPasskeysAvailable(); ok {
		t.Fatalf("plain http should withhold passkeys: %q", reason)
	}

	cfg.AdminURL = "https://hz.example.com"
	s.config.Store(&cfg)
	if ok, reason := s.accountPasskeysAvailable(); !ok {
		t.Fatalf("https admin_url should allow passkeys: %q", reason)
	}
}
