package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestScrapeEndpointAuth(t *testing.T) {
	s, admin := adminServer(&config.Config{ScrapeToken: "sekret"})
	req := func(mod func(*http.Request)) int {
		r := httptest.NewRequest(http.MethodGet, "/integration/prometheus/scrape.yaml", nil)
		if mod != nil {
			mod(r)
		}
		w := httptest.NewRecorder()
		s.handleIntegrationPromScrape(w, r)
		return w.Code
	}
	if c := req(nil); c != http.StatusForbidden {
		t.Errorf("no auth = %d, want 403", c)
	}
	if c := req(func(r *http.Request) { r.RemoteAddr = "192.168.1.9:1234" }); c != http.StatusForbidden {
		t.Errorf("LAN client with no token must be 403 (no blanket RFC1918 allow), got %d", c)
	}
	if c := req(func(r *http.Request) { r.Header.Set("Authorization", "Bearer sekret") }); c != http.StatusOK {
		t.Errorf("scrape token = %d, want 200", c)
	}
	if c := req(func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }); c != http.StatusForbidden {
		t.Errorf("wrong token = %d, want 403", c)
	}
	if c := req(func(r *http.Request) { r.AddCookie(admin) }); c != http.StatusOK {
		t.Errorf("admin session = %d, want 200", c)
	}
	// ?token= query param also authorizes (for Prometheus http_sd URLs).
	q := httptest.NewRequest(http.MethodGet, "/integration/prometheus/scrape.yaml?token=sekret", nil)
	wq := httptest.NewRecorder()
	s.handleIntegrationPromScrape(wq, q)
	if wq.Code != http.StatusOK {
		t.Errorf("?token= = %d, want 200", wq.Code)
	}
}

func TestSetupScriptIsAdminOnly(t *testing.T) {
	// Pre-set the token so the admin path doesn't need config persistence.
	s, admin := adminServer(&config.Config{ScrapeToken: "pre"})
	r := httptest.NewRequest(http.MethodGet, "/integration/prometheus/setup.sh", nil)
	r.RemoteAddr = "192.168.1.9:1234" // LAN, but no admin
	w := httptest.NewRecorder()
	s.handleIntegrationSetupScript(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("setup.sh without admin = %d, want 401 (it embeds the token)", w.Code)
	}
	r2 := httptest.NewRequest(http.MethodGet, "/integration/prometheus/setup.sh", nil)
	r2.AddCookie(admin)
	w2 := httptest.NewRecorder()
	s.handleIntegrationSetupScript(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("admin setup.sh = %d, want 200", w2.Code)
	}
	if body := w2.Body.String(); !strings.Contains(body, `HZ_TOKEN="pre"`) {
		t.Error("setup.sh should bake the scrape token into HZ_TOKEN")
	}
}
