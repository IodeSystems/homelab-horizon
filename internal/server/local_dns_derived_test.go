package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The derived half of GET /dns/local carries each name's service and the
// ports that service uses, so the page can say what lives at an address.
func TestLocalDNSDerivedCarriesServiceAndPorts(t *testing.T) {
	s := dnsServer(t)
	s.config.Store(&config.Config{
		SSLEnabled: true,
		Services: []config.Service{
			{
				Name: "kc", Domains: []string{"kc.example.com"},
				InternalDNS: &config.InternalDNS{IP: "192.0.2.10"},
				Proxy: &config.ProxyConfig{
					Backend: "192.0.2.10:6302",
					Deploy:  &config.DeployConfig{NextBackend: "192.0.2.10:6300", ActiveSlot: "b"},
				},
			},
			{
				Name: "wiki", Domains: []string{"wiki.example.com"},
				InternalDNS: &config.InternalDNS{IP: "192.0.2.10"},
				Proxy:       &config.ProxyConfig{Backend: "192.0.2.10:8080"},
			},
			{Name: "nas", Domains: []string{"nas.example.com"}, InternalDNS: &config.InternalDNS{IP: "192.0.2.2"}},
		},
	})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/dns/local", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPILocalDNS(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.LocalDNSResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	got := map[string]apitypes.LocalDNSDerived{}
	for _, d := range resp.Derived {
		got[d.Name] = d
	}
	kc := got["kc.example.com"]
	if kc.Service != "kc" {
		t.Fatalf("kc = %+v, want service kc", kc)
	}
	want := []apitypes.ServicePort{
		{Port: 6302, Label: "slot a"},
		{Port: 6300, Label: "slot b", Active: true},
		{Port: 80, Label: "http"},
		{Port: 443, Label: "https"},
	}
	if len(kc.Ports) != len(want) {
		t.Fatalf("kc ports = %+v, want %+v", kc.Ports, want)
	}
	for i := range want {
		if kc.Ports[i] != want[i] {
			t.Errorf("kc port %d = %+v, want %+v", i, kc.Ports[i], want[i])
		}
	}
	if w := got["wiki.example.com"]; len(w.Ports) != 3 || w.Ports[0] != (apitypes.ServicePort{Port: 8080, Label: "backend"}) {
		t.Errorf("wiki ports = %+v, want backend 8080 then HAProxy", w.Ports)
	}
	// Not proxied: no ports, and an empty list rather than null.
	if n := got["nas.example.com"]; n.Ports == nil || len(n.Ports) != 0 {
		t.Errorf("nas ports = %#v, want []", n.Ports)
	}
}
