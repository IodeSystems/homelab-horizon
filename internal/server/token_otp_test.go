package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// A token exists to work unattended, so requiring a code is opt-in. These pin
// both halves: the default lets a 3am deploy job run, and the opt-in actually
// bites.
func TestTokenOTPRequirement(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, secret := enrolledUser(t, s)

	plain, _, err := s.users.CreateAPIToken(ctx, user.ID, "unattended", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	guarded, meta, err := s.users.CreateAPIToken(ctx, user.ID, "guarded", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.MFARequired {
		t.Error("the flag did not round-trip on creation")
	}

	req := func(token, code string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if code != "" {
			r.Header.Set(otpHeader, code)
		}
		return r
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("default token needs no code", func(t *testing.T) {
		if s.currentUser(req(plain, "")) == nil {
			t.Error("a token created without the flag was refused")
		}
	})

	t.Run("guarded token without a code is refused", func(t *testing.T) {
		if s.currentUser(req(guarded, "")) != nil {
			t.Error("a token marked MFA-required authenticated with no code")
		}
	})

	t.Run("guarded token with a wrong code is refused", func(t *testing.T) {
		if s.currentUser(req(guarded, "000000")) != nil {
			t.Error("a wrong code was accepted")
		}
	})

	t.Run("guarded token with the right code works", func(t *testing.T) {
		if s.currentUser(req(guarded, code)) == nil {
			t.Error("the correct code was refused")
		}
	})

	// A script making several calls inside one 30-second step must not fail on
	// the second: codes are not consumed, and the window is the protection.
	t.Run("the same code works twice in a row", func(t *testing.T) {
		for i := range 2 {
			if s.currentUser(req(guarded, code)) == nil {
				t.Fatalf("call %d with a still-valid code was refused", i+1)
			}
		}
	})

	// The refusal has to be diagnosable, or an operator concludes the token is
	// broken and makes another one.
	t.Run("the server says a code is what is missing", func(t *testing.T) {
		if !s.tokenNeedsOTP(req(guarded, "")) {
			t.Error("tokenNeedsOTP did not report the requirement")
		}
		if s.tokenNeedsOTP(req(plain, "")) {
			t.Error("an unguarded token was reported as needing a code")
		}
		if s.tokenNeedsOTP(req(guarded, code)) {
			t.Error("a satisfied requirement was still reported as outstanding")
		}
	})
}

// A token that demands a factor the account no longer has must fail closed.
func TestGuardedTokenWithoutAnEnrolledFactor(t *testing.T) {
	s := userServer(t)
	ctx := context.Background()

	user, err := s.users.CreateUser(ctx, "no-factor", "", db.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.users.CreateAPIToken(ctx, user.ID, "guarded", 0, true)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(otpHeader, "123456")
	if s.currentUser(r) != nil {
		t.Error("a token requiring a code authenticated for an account with no authenticator")
	}
}
