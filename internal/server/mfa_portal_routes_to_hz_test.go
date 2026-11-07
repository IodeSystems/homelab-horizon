package server

import (
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// The office's shape (placeholder names): the kiosk is a plain proxy to hz's
// own listener, hz runs with --listen 127.0.0.1:8080, and the MFA jail is on.
// Only `self` counted as the portal, so a jailed peer got 403 at the kiosk
// instead of the redirect to the MFA page.
func officeJailCfg() *config.Config {
	cfg := &config.Config{
		KioskURL:       "https://kiosk.vpn.example.com",
		ListenAddr:     ":8080",
		LocalInterface: "192.168.1.160",
		VPNMFAEnabled:  true,
		HAProxyEnabled: true,
		Services: []config.Service{
			{Name: "vpn-kiosk", Domains: []string{"kiosk.vpn.example.com"}, Proxy: &config.ProxyConfig{Backend: "localhost:8080"}},
			{Name: "hz.office.example.com", Domains: []string{"hz.office.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
			{Name: "app", Domains: []string{"app.example.com"}, Proxy: &config.ProxyConfig{Backend: "192.168.1.50:9000"}},
			{Name: "other8080", Domains: []string{"other.example.com"}, Proxy: &config.ProxyConfig{Backend: "192.168.1.76:8080"}},
		},
	}
	cfg.SetListenOverride("127.0.0.1:8080")
	return cfg
}

func renderWith(cfg *config.Config, backends []haproxy.Backend) string {
	h := haproxy.New("/tmp/hz-test.cfg", "/tmp/hz-test.sock")
	h.SetBackends(backends)
	h.SetMFAJail(mfaJailFor(cfg, backends))
	return h.GenerateConfig(80, 443, nil)
}

func lineDiff(before, after string) (removed, added []string) {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	b, a := count(before), count(after)
	for l, n := range b {
		for i := a[l]; i < n; i++ {
			removed = append(removed, l)
		}
	}
	for l, n := range a {
		for i := b[l]; i < n; i++ {
			added = append(added, l)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	return removed, added
}

func TestMFAPortalIncludesAPlainProxyToHZ(t *testing.T) {
	cfg := officeJailCfg()
	backends := cfg.DeriveHAProxyBackends()
	portal := map[string]bool{}
	for _, b := range backends {
		portal[b.Name] = b.MFAPortal
	}
	if !portal["vpn-kiosk"] || !portal["hz.office.example.com"] {
		t.Errorf("portal backends = %v; the kiosk and the self service both serve hz", portal)
	}
	if portal["app"] || portal["other8080"] {
		t.Errorf("portal backends = %v; app and another box's 8080 are not hz", portal)
	}
	if got := portalRedirectURL(cfg, backends); got != "https://kiosk.vpn.example.com/app/mfa" {
		t.Errorf("portal redirect = %q, want the kiosk's MFA page", got)
	}
}

// The whole rendered change, pinned: against the old rule (only `self` is the
// portal), the ONLY line that differs is the jail's — the 403 becomes the
// redirect, with the kiosk added to the exceptions. Anything else moving is a
// regression this test exists to catch. (No SSL here, so one frontend; with
// SSL the same line appears once per frontend — checked against the live
// office's config-preview before any sync.)
func TestMFAPortalChangeRendersOnlyTheJailLines(t *testing.T) {
	cfg := officeJailCfg()
	after := cfg.DeriveHAProxyBackends()
	before := make([]haproxy.Backend, len(after))
	copy(before, after)
	for i := range before {
		before[i].MFAPortal = cfg.Services[i].Proxy.Self // the old rule
	}

	removed, added := lineDiff(renderWith(cfg, before), renderWith(cfg, after))
	oldLine := "    http-request deny deny_status 403 if mfa_jailed !host_hz_office_example_com"
	newLine := "    http-request redirect location https://kiosk.vpn.example.com/app/mfa code 302 if mfa_jailed !host_hz_office_example_com !host_vpn_kiosk"
	if len(removed) != 1 || removed[0] != oldLine {
		t.Errorf("removed lines %q, want exactly %q", removed, oldLine)
	}
	if len(added) != 1 || added[0] != newLine {
		t.Errorf("added lines %q, want exactly %q", added, newLine)
	}
}
