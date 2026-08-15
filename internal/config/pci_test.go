package config

import "testing"

func scopedCfg() *Config {
	return &Config{
		Services: []Service{
			{Name: "shop", PCIScope: PCIScopeCDE, Domains: []string{"shop.example.com"},
				Proxy: &ProxyConfig{Backend: "127.0.0.1:9000", InternalOnly: true}},
			{Name: "wiki", Domains: []string{"wiki.example.com"},
				Proxy: &ProxyConfig{Backend: "192.168.1.20:80"}},
		},
	}
}

// An unassessed service must produce no controls at all. "Not evaluated" and
// "compliant" looking the same is the failure mode this whole design exists to
// avoid.
func TestOnlyScopedServicesAreEvaluated(t *testing.T) {
	covered := map[string]bool{"shop.example.com": true}
	for _, c := range scopedCfg().ServiceControls(covered, nil) {
		if c.Service == "wiki" {
			t.Errorf("out-of-scope service produced a control: %+v", c)
		}
	}
	if len(scopedCfg().ServiceControls(covered, nil)) == 0 {
		t.Error("the in-scope service should produce controls")
	}
}

func TestScopeDefaultsToOut(t *testing.T) {
	s := Service{Name: "x"}
	if s.InPCIScope() {
		t.Error("an unmarked service must default to out of scope")
	}
	if s.EffectivePCIScope() != PCIScopeOut {
		t.Errorf("scope = %q", s.EffectivePCIScope())
	}
	if (&Service{PCIScope: "nonsense"}).InPCIScope() {
		t.Error("an unrecognised scope must fall back to out, not in")
	}
}

func TestControlsReflectRealPosture(t *testing.T) {
	cfg := &Config{Services: []Service{{
		Name: "shop", PCIScope: PCIScopeCDE, Domains: []string{"shop.example.com"},
		Proxy: &ProxyConfig{Backend: "10.0.0.5:8080"}, // remote, cleartext
	}}}
	got := map[string]ServiceControl{}
	for _, c := range cfg.ServiceControls(map[string]bool{}, nil) {
		got[c.Control] = c
	}

	if got["not_internet_exposed"].OK {
		t.Error("a service without internal_only is internet exposed")
	}
	if got["tls_covered"].OK {
		t.Error("an uncovered domain must fail the TLS control")
	}
	if got["backend_not_cleartext_offhost"].OK {
		t.Error("a remote cleartext backend must fail")
	}
	for name, c := range got {
		if !c.OK && c.Detail == "" {
			t.Errorf("failing control %q gives no reason", name)
		}
	}
}

func TestWildcardCertCoversSubdomain(t *testing.T) {
	covered := map[string]bool{"*.example.com": true}
	if got := uncoveredDomains([]string{"app.example.com"}, covered); len(got) != 0 {
		t.Errorf("wildcard should cover a subdomain, got %v", got)
	}
	// A wildcard covers one label, not the apex and not deeper names.
	if got := uncoveredDomains([]string{"example.com"}, covered); len(got) != 1 {
		t.Errorf("wildcard must not cover the apex, got %v", got)
	}
	if got := uncoveredDomains([]string{"a.b.example.com"}, covered); len(got) != 1 {
		t.Errorf("wildcard must not cover a deeper label, got %v", got)
	}
}

func TestBackendIsLocal(t *testing.T) {
	cases := map[string]bool{
		"":                true, // static or proxy.self — no network hop
		"127.0.0.1:8080":  true,
		"localhost:3000":  true,
		"[::1]:8080":      true,
		"192.168.1.20:80": false,
		"10.0.0.5:8080":   false,
	}
	for in, want := range cases {
		if got := backendIsLocal(in); got != want {
			t.Errorf("backendIsLocal(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTLSFloor(t *testing.T) {
	if (&Config{}).TLSMinVersion() != TLSMinDefault {
		t.Error("unset floor should default to TLSv1.2")
	}
	if (&Config{HAProxyTLSMinVersion: "garbage"}).TLSMinVersion() != TLSMinDefault {
		t.Error("an invalid floor must fall back to the secure default, not be emitted")
	}
	if !(&Config{HAProxyTLSMinVersion: "TLSv1.3"}).TLSFloorMeetsPCI() {
		t.Error("1.3 meets 4.2.1")
	}
	// Configurable includes configurably-worse; hz reports it rather than
	// refusing, and the control metric tells the truth.
	if (&Config{HAProxyTLSMinVersion: "TLSv1.0"}).TLSFloorMeetsPCI() {
		t.Error("TLS 1.0 has been prohibited since 2018 and must not read as compliant")
	}
	if (&Config{HAProxyTLSMinVersion: "TLSv1.0"}).TLSMinVersion() != "TLSv1.0" {
		t.Error("an explicit legacy floor should still be honoured in the config")
	}
}

func TestCertExpiryControl(t *testing.T) {
	cfg := &Config{SSLEnabled: true, Services: []Service{{
		Name: "shop", PCIScope: PCIScopeCDE, Domains: []string{"shop.example.com"},
		Proxy: &ProxyConfig{Backend: "127.0.0.1:9000", InternalOnly: true},
	}}}
	covered := map[string]bool{"shop.example.com": true}

	for _, c := range cfg.ServiceControls(covered, nil) {
		if c.Control == "cert_not_expiring" && !c.OK {
			t.Error("a cert with plenty of life left should pass")
		}
	}
	for _, c := range cfg.ServiceControls(covered, map[string]bool{"shop.example.com": true}) {
		if c.Control == "cert_not_expiring" && c.OK {
			t.Error("an expiring cert must fail")
		}
	}
}

// A certificate existing is not the same as TLS being served: with ssl_enabled
// off hz serves plain HTTP on every vhost regardless of what is on disk.
func TestServedOverHTTPSNeedsSSLEnabled(t *testing.T) {
	svc := Service{Name: "shop", PCIScope: PCIScopeCDE, Domains: []string{"shop.example.com"},
		Proxy: &ProxyConfig{Backend: "127.0.0.1:9000"}}
	covered := map[string]bool{"shop.example.com": true}

	off := &Config{SSLEnabled: false, Services: []Service{svc}}
	for _, c := range off.ServiceControls(covered, nil) {
		if c.Control == "served_over_https" && c.OK {
			t.Error("ssl_enabled off must fail served_over_https even with a valid cert")
		}
		if c.Control == "tls_covered" && !c.OK {
			t.Error("the cert does cover it; tls_covered should still pass")
		}
	}
	on := &Config{SSLEnabled: true, Services: []Service{svc}}
	for _, c := range on.ServiceControls(covered, nil) {
		if c.Control == "served_over_https" && !c.OK {
			t.Error("ssl on plus a covering cert should pass")
		}
	}
}
