package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// sslTestServer builds a Server whose config lives in a temp file and whose
// public IP is pinned, so mutations neither touch the real config nor hit the
// network during the syncServices() call the handlers make.
func sslTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		configPath: filepath.Join(t.TempDir(), "hz.yml"),
		adminToken: "test-admin-token",
	}
	s.config.Store(&config.Config{
		PublicIPOverride: "203.0.113.1",
		Zones: []config.Zone{{
			Name:     "example.com",
			SSL:      &config.ZoneSSL{Enabled: true, Email: "ops@example.com"},
			SubZones: []string{"app", "other"},
		}},
		Services: []config.Service{{
			Name:    "app",
			Domains: []string{"app.example.com"},
		}},
	})
	return s
}

func postSSLRemove(t *testing.T, s *Server, req apitypes.DomainSSLRemoveRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/domains/ssl/remove", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPIDomainSSLRemove(w, r)
	return w
}

func zoneSubZones(t *testing.T, s *Server, zone string) []string {
	t.Helper()
	for _, z := range s.cfg().Zones {
		if z.Name == zone {
			return z.SubZones
		}
	}
	t.Fatalf("zone %s not found", zone)
	return nil
}

// A service domain's only SSL coverage is protected: dropping it would silently
// take the service off HTTPS, so the plain request is refused.
func TestDomainSSLRemoveRejectsServiceDependency(t *testing.T) {
	s := sslTestServer(t)
	w := postSSLRemove(t, s, apitypes.DomainSSLRemoveRequest{Domain: "app.example.com"})

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusConflict, w.Body.String())
	}
	if got := zoneSubZones(t, s, "example.com"); !slices.Contains(got, "app") {
		t.Errorf("SubZone was removed despite the conflict: %v", got)
	}
}

// Force is the operator's explicit confirmation (hz domain ssl rm --confirm,
// hz service --https=false --confirm) that the domain should fall back to HTTP.
func TestDomainSSLRemoveForceOverridesServiceDependency(t *testing.T) {
	s := sslTestServer(t)
	w := postSSLRemove(t, s, apitypes.DomainSSLRemoveRequest{Domain: "app.example.com", Force: true})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	got := zoneSubZones(t, s, "example.com")
	if slices.Contains(got, "app") {
		t.Errorf("SubZone %q not removed: %v", "app", got)
	}
	if !slices.Contains(got, "other") {
		t.Errorf("unrelated SubZone was dropped: %v", got)
	}
}
