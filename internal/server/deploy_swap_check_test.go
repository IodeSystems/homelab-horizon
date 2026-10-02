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

// POST /api/deploy/swap changes only active_slot. The svc: check names the
// active slot, so the swap has to reach the monitor with no other edit —
// otherwise the check keeps describing, and warning about, the old one.
func TestDeploySwapMovesTheServiceCheck(t *testing.T) {
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

	if st := s.monitor.GetStatus("svc:kc"); st == nil || !strings.HasPrefix(st.Target, "slot a (active) 192.0.2.10:6302") {
		t.Fatalf("before swap: %+v, want slot a active", st)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/deploy/swap", nil)
	req.Header.Set("Authorization", "Bearer swap-token")
	rec := httptest.NewRecorder()
	// The response may be a 500: the test box has no haproxy binary to
	// validate and reload with. The config write and the check move first.
	s.handleDeployAPI(rec, req)

	if got := s.cfg().Services[0].Proxy.Deploy.ActiveSlot; got != "b" {
		t.Fatalf("active slot = %q after swap (response %d %s)", got, rec.Code, rec.Body.String())
	}
	st := s.monitor.GetStatus("svc:kc")
	if st == nil || !strings.HasPrefix(st.Target, "slot b (active) 192.0.2.10:6300") {
		t.Fatalf("after swap: %+v, want slot b active", st)
	}
}
