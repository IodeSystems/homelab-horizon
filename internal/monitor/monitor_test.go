package monitor

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Auto-generated checks should cover proxied and static services, target the
// right address, and skip self services (hz monitoring itself is pointless).
func TestGetAllChecks_AutoGen(t *testing.T) {
	cfg := &config.Config{
		ListenAddr:      ":8080",
		StaticServePort: 8091,
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.com"}, Proxy: &config.ProxyConfig{Backend: "192.168.1.50:9000"}},
			{Name: "docs", Domains: []string{"docs.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: "/var/lib/homelab-horizon/docs"}},
			{Name: "admin", Domains: []string{"hz.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
			{Name: "plain", Domains: []string{"x.example.com"}}, // no proxy
		},
	}
	m := New(cfg)

	got := map[string]config.ServiceCheck{}
	for _, c := range m.getAllChecks() {
		got[c.Name] = c
	}

	if c, ok := got["svc:api"]; !ok || c.Type != "ping" || c.Target != "192.168.1.50:9000" {
		t.Errorf("proxied check = %+v, want ping 192.168.1.50:9000", c)
	}
	// Static service is checked against hz's internal file server (child liveness).
	if c, ok := got["svc:docs"]; !ok || c.Type != "ping" || c.Target != "127.0.0.1:8091" {
		t.Errorf("static check = %+v, want ping 127.0.0.1:8091", c)
	}
	if _, ok := got["svc:admin"]; ok {
		t.Error("self service should not get an auto-generated check")
	}
	if _, ok := got["svc:plain"]; ok {
		t.Error("non-proxy service should not get an auto-generated check")
	}
}

func TestGetAllChecks_HTTPWhenPathSet(t *testing.T) {
	cfg := &config.Config{
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.com"}, Proxy: &config.ProxyConfig{
				Backend:     "192.168.1.50:9000",
				HealthCheck: &config.HealthCheck{Path: "/healthz"},
			}},
		},
	}
	c := New(cfg).getAllChecks()
	var found bool
	for _, chk := range c {
		if chk.Name == "svc:api" {
			found = true
			if chk.Type != "http" || chk.Target != "http://192.168.1.50:9000/healthz" {
				t.Errorf("check = %+v, want http http://192.168.1.50:9000/healthz", chk)
			}
		}
	}
	if !found {
		t.Fatal("expected svc:api check")
	}
}

// Parking a service has to reach the monitor without restarting everything:
// the blunt Reload would cost the history of every unrelated check.
func TestRefreshChecksParksWithoutLosingHistory(t *testing.T) {
	cfg := &config.Config{
		Services: []config.Service{
			{Name: "live", Domains: []string{"live.example.com"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.1:9000"}},
			{Name: "parked", Domains: []string{"parked.example.com"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.2:9000"}},
		},
	}
	m := New(cfg)
	defer m.Stop()
	m.SeedForTest("svc:live", "ping", "10.0.0.1:9000")
	m.SeedForTest("svc:parked", "ping", "10.0.0.2:9000")

	next := *cfg
	next.Services = []config.Service{
		cfg.Services[0],
		{Name: "parked", Domains: []string{"parked.example.com"}, Dormant: true,
			Proxy: &config.ProxyConfig{Backend: "10.0.0.2:9000"}},
	}
	m.RefreshChecks(&next)

	parked := m.GetStatus("svc:parked")
	if parked == nil || parked.Status != StatusDormant {
		t.Fatalf("parked check = %+v, want dormant", parked)
	}
	if parked.Enabled {
		t.Error("a dormant check should not be enabled")
	}
	// Its past is worth keeping — it ran until it was parked.
	if len(m.GetHistory("svc:parked")) != 1 {
		t.Error("parking a service discarded its history")
	}
	// And nothing unrelated was disturbed.
	if live := m.GetStatus("svc:live"); live == nil || live.Status != StatusOK {
		t.Errorf("unrelated check = %+v, want untouched", live)
	}
	if len(m.GetHistory("svc:live")) != 1 {
		t.Error("parking one service cleared another's history")
	}
}

// Deleting a service takes its rows with it; a row nothing updates reads as
// current forever.
func TestRefreshChecksDropsDeletedServices(t *testing.T) {
	cfg := &config.Config{
		Services: []config.Service{
			{Name: "doomed", Domains: []string{"doomed.example.com"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.3:9000"}},
		},
	}
	m := New(cfg)
	defer m.Stop()
	m.SeedForTest("svc:doomed", "ping", "10.0.0.3:9000")

	next := *cfg
	next.Services = nil
	m.RefreshChecks(&next)

	if m.GetStatus("svc:doomed") != nil {
		t.Fatal("a deleted service left its check row behind")
	}
	if len(m.GetHistory("svc:doomed")) != 0 {
		t.Fatal("a deleted service left its history behind")
	}
}
