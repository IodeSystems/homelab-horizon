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
