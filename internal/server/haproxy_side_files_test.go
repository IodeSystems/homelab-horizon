package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// TestEveryHAProxyWriteCarriesTheJailList: a nested hz with MFA on, HAProxy on,
// a kiosk routed to hz's own UI, and NO jail list on disk — the start-sync's
// state on 2026-10-07. Every path that renders HAProxy goes through
// HAProxy.WriteConfig, so wiring the server's side files into it is the fix;
// this drives that wiring and asserts the list lands with the config.
func TestEveryHAProxyWriteCarriesTheJailList(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		HAProxyEnabled:    true,
		HAProxyHTTPPort:   80,
		HAProxyHTTPSPort:  443,
		HAProxyConfigPath: filepath.Join(dir, "haproxy.cfg"),
		VPNMFAEnabled:     true,
		KioskURL:          "https://kiosk.example.com",
		Services: []config.Service{
			{Name: "kiosk", Domains: []string{"kiosk.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
			{Name: "wiki", Domains: []string{"wiki.example.com"}, Proxy: &config.ProxyConfig{
				Backend: "192.0.2.10:80", MaintenancePage: "<p>back soon</p>",
			}},
		},
	}
	s := newTestServer(t, cfg)
	t.Cleanup(s.monitor.Stop)
	s.haproxy = haproxy.New(cfg.HAProxyConfigPath, filepath.Join(dir, "admin.sock"))
	s.haproxy.SetSideFiles(s.haproxySideFilesNow) // what New does
	s.syncHAProxyBackends()

	list := s.cfg().MFAJailACLPath()
	if _, err := os.Stat(list); !os.IsNotExist(err) {
		t.Fatalf("precondition: jail list must be absent, stat err = %v", err)
	}
	if err := s.haproxy.WriteConfig(80, 443, nil); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	rendered, _ := os.ReadFile(cfg.HAProxyConfigPath)
	if !strings.Contains(string(rendered), "src -f "+list) {
		t.Fatalf("instrument check: config does not name the jail list:\n%s", rendered)
	}
	got, err := os.ReadFile(list)
	if err != nil {
		t.Fatalf("jail list not written with the config: %v", err)
	}
	if string(got) != string(haproxy.RenderJailACL(nil)) {
		t.Errorf("empty jail = %q, want the header-only list", got)
	}
	page := filepath.Join(s.cfg().HAProxyErrorsDir(), config.MaintenancePageName("wiki"))
	if _, err := os.Stat(page); err != nil {
		t.Errorf("maintenance page the config names is not on disk: %v", err)
	}
}

// TestNewWiresTheSideFiles pins the one line in New that the test above
// stands in for, since New itself touches the machine and is not run here.
func TestNewWiresTheSideFiles(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "hap.SetSideFiles(s.haproxySideFilesNow)") {
		t.Fatal("New no longer hands HAProxy the side-file provider: every HAProxy write would again render a config naming files nothing wrote")
	}
}
