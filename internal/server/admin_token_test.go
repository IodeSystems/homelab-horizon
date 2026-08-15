package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A disabled token must also invalidate sessions it already minted. Leaving
// them alive would mean the shared credential keeps working until a cookie
// happens to expire, which is not "disabled".
func TestDisabledTokenAlsoRejectsItsSessions(t *testing.T) {
	s := &Server{csrfSecret: "test-secret", adminToken: "tok"}
	s.config.Store(&config.Config{})

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	if !s.isAdmin(req) {
		t.Fatal("a valid session should authenticate while the token is enabled")
	}

	s.config.Store(&config.Config{AdminTokenDisabled: true})
	if s.isAdmin(req) {
		t.Error("the same session must stop working once the token is disabled")
	}
}

// The control must only read met when the token is actually off — this is the
// gauge an assessor would look at for 8.2.1.
func TestSharedTokenControl(t *testing.T) {
	on := hzControls(&config.Config{}, hostFactsSnapshot{})
	for _, c := range on {
		if c.name == "no_shared_admin_token" && c.ok {
			t.Error("an enabled shared token must not read as met")
		}
	}
	off := hzControls(&config.Config{AdminTokenDisabled: true}, hostFactsSnapshot{})
	found := false
	for _, c := range off {
		if c.name == "no_shared_admin_token" {
			found = true
			if !c.ok {
				t.Error("a disabled token should satisfy 8.2.1")
			}
			if c.requirement != "8.2.1" {
				t.Errorf("requirement = %q", c.requirement)
			}
		}
	}
	if !found {
		t.Error("the control should always be reported")
	}
}
