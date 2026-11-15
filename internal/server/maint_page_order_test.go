package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// TestMaintPageSetWritesItsFileBeforeTheConfig drives redline's real path —
// hzclient MaintPageSet → POST /api/deploy/maint-page/set with the service
// token — and checks the page file the rendered config names is on disk, and
// the write was not refused. Since N4i hz refuses to write a haproxy.cfg that
// names a missing file, so a set whose page landed AFTER the render would fail
// every maintenance deploy. Reload needs a haproxy binary and is not the
// subject: the render is.
func TestMaintPageSetWritesItsFileBeforeTheConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		HAProxyEnabled: true, HAProxyHTTPPort: 80, HAProxyHTTPSPort: 443,
		HAProxyConfigPath: filepath.Join(dir, "haproxy.cfg"),
		Services: []config.Service{{
			Name: "app", Domains: []string{"app.example.com"}, Token: "svc-token",
			Proxy: &config.ProxyConfig{
				Backend: "192.0.2.10:6400",
				Deploy:  &config.DeployConfig{NextBackend: "192.0.2.10:6402", ActiveSlot: "a"},
			},
		}},
	}
	s := newTestServer(t, cfg)
	t.Cleanup(s.monitor.Stop)
	s.haproxy = haproxy.New(cfg.HAProxyConfigPath, filepath.Join(dir, "admin.sock"))
	s.haproxy.SetSideFiles(s.haproxySideFilesNow) // what New does

	call := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer svc-token")
		w := httptest.NewRecorder()
		s.handleDeployAPI(w, r)
		return w
	}

	page := filepath.Join(s.cfg().HAProxyErrorsDir(), config.MaintenancePageName("app"))
	w := call("/api/deploy/maint-page/set", `{"html":"<p>back soon</p>"}`)
	if strings.Contains(w.Body.String(), "failed to write haproxy config") {
		t.Fatalf("maint-page set: the render was refused: %s", w.Body.String())
	}
	got, err := os.ReadFile(page)
	if err != nil || !strings.Contains(string(got), "back soon") {
		t.Fatalf("maint-page set: page %s not on disk with the posted content (err %v): %q", page, err, got)
	}
	rendered, _ := os.ReadFile(cfg.HAProxyConfigPath)
	if !strings.Contains(string(rendered), filepath.Base(page)) {
		t.Fatalf("instrument check: the rendered config does not name the page:\n%s", rendered)
	}

	w = call("/api/deploy/maint-page/clear", ``)
	if strings.Contains(w.Body.String(), "failed to write haproxy config") {
		t.Fatalf("maint-page clear: the render was refused: %s", w.Body.String())
	}
	if s.cfg().Services[0].Proxy.MaintenancePage != "" {
		t.Fatalf("clear did not clear the page")
	}
}
