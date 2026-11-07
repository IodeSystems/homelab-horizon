package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A backend that serves only TLS (backend_proto "https") must be probed over
// https — an http:// probe of a TLS port fails on every run, so the check
// would page forever on a service that works.

func httpsCheck(t *testing.T, cfg *config.Config, name string) config.ServiceCheck {
	t.Helper()
	for _, c := range New(cfg).getAllChecks() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %s", name)
	return config.ServiceCheck{}
}

func TestHTTPSBackendIsProbedOverHTTPS(t *testing.T) {
	cfg := &config.Config{Services: []config.Service{
		{Name: "kvm", Domains: []string{"kvm.example.com"}, Proxy: &config.ProxyConfig{
			Backend: "192.168.1.171:443", BackendProto: config.BackendProtoHTTPS,
			HealthCheck: &config.HealthCheck{Path: "/"},
		}},
		// No health path: still https, at /. A ping proves only the port.
		{Name: "kvm2", Domains: []string{"kvm2.example.com"}, Proxy: &config.ProxyConfig{
			Backend: "192.168.1.172:443", BackendProto: config.BackendProtoHTTPS,
		}},
		{Name: "plain", Domains: []string{"plain.example.com"}, Proxy: &config.ProxyConfig{
			Backend: "192.168.1.50:9000", HealthCheck: &config.HealthCheck{Path: "/healthz"},
		}},
	}}

	if c := httpsCheck(t, cfg, "svc:kvm"); c.Type != "http" || c.Target != "https://192.168.1.171:443/" || !c.InsecureTLS {
		t.Errorf("https backend check = %+v, want insecure http https://192.168.1.171:443/", c)
	}
	if c := httpsCheck(t, cfg, "svc:kvm2"); c.Type != "http" || c.Target != "https://192.168.1.172:443/" || !c.InsecureTLS {
		t.Errorf("https backend without a path = %+v, want insecure http https://192.168.1.172:443/", c)
	}
	if c := httpsCheck(t, cfg, "svc:plain"); c.InsecureTLS || c.Target != "http://192.168.1.50:9000/healthz" {
		t.Errorf("plain backend check = %+v, want verified http://192.168.1.50:9000/healthz", c)
	}
}

func TestHTTPSBackendBothDeploySlotsProbeOverHTTPS(t *testing.T) {
	cfg := &config.Config{Services: []config.Service{
		{Name: "kvm", Domains: []string{"kvm.example.com"}, Proxy: &config.ProxyConfig{
			Backend: "192.168.1.171:443", BackendProto: config.BackendProtoHTTPS,
			HealthCheck: &config.HealthCheck{Path: "/health"},
			Deploy:      &config.DeployConfig{NextBackend: "192.168.1.171:8443"},
		}},
	}}
	c := httpsCheck(t, cfg, "svc:kvm")
	if !c.InsecureTLS || len(c.Slots) != 2 {
		t.Fatalf("check = %+v, want insecure with 2 slots", c)
	}
	for _, s := range c.Slots {
		if !strings.HasPrefix(s.Target, "https://") {
			t.Errorf("slot %s target %q is not https", s.Slot, s.Target)
		}
	}
}

// The probe trusts a self-signed backend only when the check came from an
// https backend. The same URL in a check without InsecureTLS — every check an
// operator configures — must still fail verification (positive control: the
// self-signed server is otherwise healthy).
func TestOnlyADerivedHTTPSCheckSkipsVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	m := New(&config.Config{})

	if err := m.probe("http", srv.URL+"/", true); err != nil {
		t.Errorf("derived https check against a self-signed backend failed: %v", err)
	}
	err := m.probe("http", srv.URL+"/", false)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("verified probe of a self-signed server = %v, want a certificate error", err)
	}
}

// InsecureTLS is derived, never stored: a config.json cannot switch
// verification off for a check.
func TestInsecureTLSIsNeverStored(t *testing.T) {
	b, err := json.Marshal(config.ServiceCheck{Name: "x", InsecureTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(b)), "insecure") {
		t.Errorf("InsecureTLS reached JSON: %s", b)
	}
	var c config.ServiceCheck
	if err := json.Unmarshal([]byte(`{"name":"x","type":"http","InsecureTLS":true,"insecure_tls":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.InsecureTLS {
		t.Error("a stored check switched verification off")
	}
}
