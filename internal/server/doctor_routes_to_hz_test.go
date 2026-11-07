package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The office's shape, live 2026-11-05 (placeholder names): the kiosk is a
// PLAIN proxy to hz's own listener, not a `self` service, and hz runs with
// --listen 127.0.0.1:8080 over listen_addr ":8080". The kiosk works; the
// doctor read it as "no service routes that name to hz".
func officeKioskCfg(backend string) *config.Config {
	cfg := &config.Config{
		KioskURL:       "https://kiosk.vpn.example.com",
		ListenAddr:     ":8080",
		LocalInterface: "192.168.1.160",
		Services: []config.Service{
			{Name: "vpn-kiosk", Domains: []string{"kiosk.vpn.example.com"}, Proxy: &config.ProxyConfig{Backend: backend}},
			{Name: "hz-ui", Domains: []string{"hz.office.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
		},
	}
	cfg.SetListenOverride("127.0.0.1:8080")
	return cfg
}

func TestKioskOnAPlainProxyToHZIsServed(t *testing.T) {
	fs := doctorFindings(officeKioskCfg("localhost:8080"), doctorFactsFor())
	if f := finding(t, fs, "kiosk", "kiosk.vpn.example.com"); f.Status != "ok" {
		t.Errorf("kiosk on a plain proxy to hz = %+v, want ok", f)
	}
}

// Positive control for the test above: the same shape pointed anywhere hz
// does not listen must still fail. Each of these looks like hz at a glance.
func TestKioskOnAPlainProxyElsewhereStillFails(t *testing.T) {
	for _, backend := range []string{
		"localhost:8081",     // another port
		"192.168.1.160:8080", // hz's LAN address, but hz binds loopback only
		"192.168.1.76:8080",  // another box's 8080
		"not-a-host-port",    // unparseable
	} {
		fs := doctorFindings(officeKioskCfg(backend), doctorFactsFor())
		if f := finding(t, fs, "kiosk", "kiosk.vpn.example.com"); f.Status != "failing" {
			t.Errorf("backend %s = %+v, want failing", backend, f)
		}
	}
}

func TestRoutesToHZFollowsTheListener(t *testing.T) {
	cases := []struct {
		listen, backend string
		want            bool
	}{
		{":8080", "127.0.0.1:8080", true},
		{":8080", "localhost:8080", true},
		{":8080", "[::1]:8080", true},
		{":8080", "192.168.1.160:8080", true}, // every interface: the LAN address reaches hz
		{":8080", "192.168.1.161:8080", false},
		{"127.0.0.1:8080", "192.168.1.160:8080", false},
		{"192.168.1.160:9000", "192.168.1.160:9000", true},
		{"192.168.1.160:9000", "127.0.0.1:9000", false}, // bound to the LAN address only
		{"", "127.0.0.1:8080", true},                    // unset listen: hz's default port
	}
	for _, c := range cases {
		cfg := &config.Config{LocalInterface: "192.168.1.160"}
		cfg.SetListenOverride(c.listen)
		svc := config.Service{Name: "x", Proxy: &config.ProxyConfig{Backend: c.backend}}
		if got := cfg.RoutesToHZ(&svc); got != c.want {
			t.Errorf("listen %q backend %q = %v, want %v", c.listen, c.backend, got, c.want)
		}
	}
	// Static and blue-green services never route to hz, whatever the address.
	cfg := &config.Config{ListenAddr: ":8080"}
	for _, p := range []*config.ProxyConfig{
		{StaticRoot: "/srv/x", Backend: "127.0.0.1:8080"},
		{Backend: "127.0.0.1:8080", Deploy: &config.DeployConfig{NextBackend: "127.0.0.1:8082"}},
	} {
		if cfg.RoutesToHZ(&config.Service{Name: "x", Proxy: p}) {
			t.Errorf("%+v routes to hz", p)
		}
	}
}

// The same helper feeds public-admin: a PUBLISHED plain proxy to hz exposes
// hz's UI just as a `self` service does, and was missed.
func TestPublicAdminCountsAPlainProxyToHZ(t *testing.T) {
	cfg := doctorCfg()
	cfg.SetListenOverride("127.0.0.1:8080")
	for i := range cfg.Services {
		if cfg.Services[i].Name == "hz-ui" {
			cfg.Services[i].Proxy = &config.ProxyConfig{Backend: "localhost:8080"}
		}
	}
	if f := finding(t, doctorFindings(cfg, doctorFactsFor()), "public-admin", "hz.beta.new.example.com"); f.Status != "warning" {
		t.Errorf("published plain proxy to hz = %+v, want the same warning a self service gets", f)
	}
}
