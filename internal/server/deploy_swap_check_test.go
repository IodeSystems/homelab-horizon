package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// POST /api/deploy/swap is gone. Next is listed first under `balance first`,
// so swapping the labels would put the slot still running the old code first.
// The refusal must change nothing — not the active slot, not the check.
func TestDeploySwapIsRefusedAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		HAProxyConfigPath: filepath.Join(dir, "haproxy.cfg"),
		Services: []config.Service{{
			Name: "kc", Domains: []string{"kc.example.com"},
			Proxy: &config.ProxyConfig{
				Backend: "192.0.2.10:6302",
				Deploy:  &config.DeployConfig{NextBackend: "192.0.2.10:6300", Token: "swap-token", ActiveSlot: "a"},
			},
		}},
	}
	s := newTestServer(t, cfg)
	t.Cleanup(s.monitor.Stop)
	s.haproxy = haproxy.New(cfg.HAProxyConfigPath, filepath.Join(dir, "haproxy.sock"))
	s.monitor.RefreshChecks(s.cfg())

	// Positive control: the token is good, so the refusal below is the verb's,
	// not an auth failure.
	req := httptest.NewRequest(http.MethodGet, "/api/deploy/status", nil)
	req.Header.Set("Authorization", "Bearer swap-token")
	rec := httptest.NewRecorder()
	s.handleDeployAPI(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("status with the service token = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/deploy/swap", nil)
	req.Header.Set("Authorization", "Bearer swap-token")
	rec = httptest.NewRecorder()
	s.handleDeployAPI(rec, req)

	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "rolling") {
		t.Fatalf("swap = %d %q, want 410 pointing at the rolling sequence", rec.Code, rec.Body.String())
	}
	if got := s.cfg().Services[0].Proxy.Deploy.ActiveSlot; got != "a" {
		t.Errorf("active slot = %q after a refused swap, want a", got)
	}
	if st := s.monitor.GetStatus("svc:kc"); st == nil || !strings.HasPrefix(st.Target, "slot a (active) 192.0.2.10:6302") {
		t.Errorf("check after a refused swap: %+v, want slot a still active", st)
	}
}
