package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// A console reset must be enforced AFTER the second factor.
//
// The tempting shape is to reuse the password-expired branch, which runs before
// the factor. That would hand anyone holding the freshly-set password a route to
// the change-password step without ever presenting the factor — an admin reset
// would become a way around MFA rather than a recovery from it.
func TestForcedResetIsEnforcedAfterTheSecondFactor(t *testing.T) {
	s := userServer(t)
	user, secret := enrolledUser(t, s)
	ctx := context.Background()

	if err := s.users.RequirePasswordChange(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	// Step one: the password alone must still be met with the factor challenge,
	// not with a change-password opportunity.
	first := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	resp := decodeLogin(t, first)
	if resp.PasswordExpired {
		t.Fatal("the reset was announced before the second factor was presented")
	}
	if !resp.MFARequired || resp.PendingID == "" {
		t.Fatalf("want an MFA challenge, got %+v", resp)
	}
	for _, c := range first.Result().Cookies() {
		if c.Name == userSessionCookie && c.Value != "" {
			t.Fatal("a session was issued before the factor")
		}
	}

	// Step two: with the factor satisfied, the change is demanded and no
	// session is issued.
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := postJSON(t, s, s.handleAPILoginTOTP, "/api/v1/auth/login/totp", map[string]string{
		"pendingId": resp.PendingID, "code": code,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("totp step = %d: %s", w.Code, w.Body)
	}
	after := decodeLogin(t, w)
	if !after.PasswordExpired || after.PendingID == "" {
		t.Fatalf("want a forced change after the factor, got %+v", after)
	}
	if after.OK {
		t.Error("login reported success while a reset was outstanding")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == userSessionCookie && c.Value != "" {
			t.Fatal("a session was issued despite the outstanding reset")
		}
	}
}

// Without a second factor there is nothing to wait for, so the demand comes at
// the password step — the path age-based expiry already used.
func TestForcedResetWithoutASecondFactor(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "plain", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.users.SetPassword(ctx, user.ID, "temporary-one"); err != nil {
		t.Fatal(err)
	}
	if err := s.users.RequirePasswordChange(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	w := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "plain", "password": "temporary-one",
	})
	resp := decodeLogin(t, w)
	if !resp.PasswordExpired || resp.PendingID == "" {
		t.Fatalf("want a forced change, got %+v", resp)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == userSessionCookie && c.Value != "" {
			t.Fatal("a session was issued despite the outstanding reset")
		}
	}
}

// The ordinary case must not regress: no reset outstanding, factor satisfied,
// session issued.
func TestNormalLoginStillIssuesASession(t *testing.T) {
	s := userServer(t)
	_, secret := enrolledUser(t, s)

	first := postJSON(t, s, s.handleAPILogin, "/api/v1/auth/login", map[string]string{
		"username": "carl", "password": "correct-horse-battery",
	})
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := postJSON(t, s, s.handleAPILoginTOTP, "/api/v1/auth/login/totp", map[string]string{
		"pendingId": decodeLogin(t, first).PendingID, "code": code,
	})
	if !decodeLogin(t, w).OK {
		t.Fatalf("normal login did not succeed: %s", w.Body)
	}
	if sessionCookie(t, w) == nil {
		t.Fatal("no session cookie on a normal login")
	}
}
