package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

func portalBackends() []haproxy.Backend {
	return []haproxy.Backend{
		{Name: "portal", DomainMatches: []string{"vpn.example.com"}, MFAPortal: true},
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}},
	}
}

// TestPortalRedirectURLLoopGuard is the important one. The jail redirect fires
// on "not the portal host", so pointing it at a host that doesn't route to the
// portal backend produces an infinite redirect loop for every jailed peer —
// and only shows up once someone is actually jailed. Falling back to a 403 is
// worse UX and a far better failure.
func TestPortalRedirectURLLoopGuard(t *testing.T) {
	cases := []struct {
		name      string
		kioskURL  string
		backends  []haproxy.Backend
		want      string
		wantEmpty bool
	}{
		{
			name:     "kiosk host routes to the portal backend",
			kioskURL: "https://vpn.example.com",
			backends: portalBackends(),
			want:     "https://vpn.example.com/app/mfa",
		},
		{
			name:     "trailing slash trimmed",
			kioskURL: "https://vpn.example.com/",
			backends: portalBackends(),
			want:     "https://vpn.example.com/app/mfa",
		},
		{
			name:     "wildcard portal domain matches subdomain",
			kioskURL: "https://kiosk.vpn.example.com",
			backends: []haproxy.Backend{{Name: "portal", DomainMatches: []string{".vpn.example.com"}, MFAPortal: true}},
			want:     "https://kiosk.vpn.example.com/app/mfa",
		},
		{
			name:      "kiosk host routes somewhere else — would loop",
			kioskURL:  "https://wiki.example.com",
			backends:  portalBackends(),
			wantEmpty: true,
		},
		{
			name:      "kiosk host routes nowhere — would loop",
			kioskURL:  "https://kiosk.vpn.example.com",
			backends:  portalBackends(),
			wantEmpty: true,
		},
		{
			name:      "default placeholder kiosk URL",
			kioskURL:  "https://kiosk.vpn.example.com",
			backends:  nil,
			wantEmpty: true,
		},
		{
			name:      "empty kiosk URL",
			kioskURL:  "",
			backends:  portalBackends(),
			wantEmpty: true,
		},
		{
			name:      "unparseable kiosk URL",
			kioskURL:  "://nope",
			backends:  portalBackends(),
			wantEmpty: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := portalRedirectURL(&config.Config{KioskURL: c.kioskURL}, c.backends)
			if c.wantEmpty {
				if got != "" {
					t.Errorf("want fallback to 403 (empty), got %q", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestMFAJailForDisabledWhenMFAOff(t *testing.T) {
	cfg := &config.Config{VPNMFAEnabled: false, KioskURL: "https://vpn.example.com"}
	if j := mfaJailFor(cfg, portalBackends()); j.Enabled {
		t.Errorf("MFA off must yield a disabled jail, got %+v", j)
	}
}

func TestMFAJailForEnabled(t *testing.T) {
	cfg := &config.Config{
		VPNMFAEnabled:     true,
		KioskURL:          "https://vpn.example.com",
		HAProxyConfigPath: "/etc/haproxy/haproxy.cfg",
	}
	j := mfaJailFor(cfg, portalBackends())
	if !j.Enabled {
		t.Fatal("jail should be enabled")
	}
	if j.ACLPath != "/etc/haproxy/mfa-jailed.lst" {
		t.Errorf("ACLPath = %q", j.ACLPath)
	}
	if j.PortalURL != "https://vpn.example.com/app/mfa" {
		t.Errorf("PortalURL = %q", j.PortalURL)
	}
}

func TestHostMatchesDomain(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"vpn.example.com", "vpn.example.com", true},
		{"vpn.example.com", "VPN.Example.COM", true},
		{"kiosk.vpn.example.com", ".vpn.example.com", true},
		{"vpn.example.com", ".vpn.example.com", true}, // bare domain under a wildcard
		{"evil.com", ".vpn.example.com", false},
		{"notvpn.example.com", "vpn.example.com", false},
		{"vpn.example.com", "", false},
	}
	for _, c := range cases {
		if got := hostMatchesDomain(c.host, c.domain); got != c.want {
			t.Errorf("hostMatchesDomain(%q, %q) = %v, want %v", c.host, c.domain, got, c.want)
		}
	}
}

func TestJailedPeerIPs(t *testing.T) {
	cfg := &config.Config{
		VPNMFAEnabled: true,
		WGPeers: []config.WGPeer{
			{Name: "alice", AllowedIPs: "10.100.0.2/32"},
			{Name: "mallory", AllowedIPs: "10.100.0.9/32, 192.168.5.0/24"},
		},
	}
	// No sessions recorded → both jailed (neither is an admin).
	got := cfg.JailedPeerIPs()
	if len(got) != 2 {
		t.Fatalf("want both peers jailed, got %v", got)
	}

	cfg.SetMFASession("alice", 0) // forever
	got = cfg.JailedPeerIPs()
	if len(got) != 1 || got[0] != "10.100.0.9" {
		t.Errorf("want only mallory's /32, got %v", got)
	}
}

func TestHAProxyJailPorts(t *testing.T) {
	cfg := &config.Config{HAProxyEnabled: true, HAProxyHTTPPort: 80, HAProxyHTTPSPort: 443}
	got := cfg.HAProxyJailPorts()
	if len(got) != 2 || got[0] != "80" || got[1] != "443" {
		t.Errorf("got %v, want [80 443]", got)
	}

	cfg.HAProxyEnabled = false
	if got := cfg.HAProxyJailPorts(); got != nil {
		t.Errorf("HAProxy disabled must yield no ports, got %v — otherwise the L3 jail "+
			"opens ports nothing is listening on", got)
	}
}
