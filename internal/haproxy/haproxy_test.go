package haproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestCert writes a self-signed leaf cert with the given DNS SANs to path.
func writeTestCert(t *testing.T, path string, sans []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: sans[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"my-service", "my_service"},
		{"my.service", "my_service"},
		{"my_service", "my_service"},
		{"MyService", "myservice"},
		{"service-name-123", "service_name_123"},
		{"foo@bar!baz", "foo_bar_baz"},
		{"123numeric", "123numeric"},
		{"UPPERCASE", "uppercase"},
		{"mix-ED.Case_123", "mix_ed_case_123"},
		{"", ""},
		{"a", "a"},
		{"a-b-c", "a_b_c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDomainToACLPattern(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Exact domains should pass through unchanged
		{"grafana.example.com", "grafana.example.com"},
		{"api.example.com", "api.example.com"},
		{"example.com", "example.com"},
		// Wildcard domains should be converted to suffix patterns
		{"*.example.com", ".example.com"},
		{"*.api.example.com", ".api.example.com"},
		{"*.vpn.home.example.com", ".vpn.home.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := domainToACLPattern(tt.input)
			if got != tt.want {
				t.Errorf("domainToACLPattern(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGenerateConfig_NoBackends(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends(nil)

	config := h.GenerateConfig(80, 443, nil)

	requiredSections := []string{"global", "defaults", "listen stats", "frontend http_front"}
	for _, section := range requiredSections {
		if !strings.Contains(config, section) {
			t.Errorf("config missing required section: %s", section)
		}
	}
}

func TestGenerateConfig_WithBackends(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "grafana",
			DomainMatch: ".grafana.example.com",
			Server:      "192.168.1.50:3000",
			HTTPCheck:   false,
		},
		{
			Name:        "prometheus",
			DomainMatch: ".prom.example.com",
			Server:      "192.168.1.51:9090",
			HTTPCheck:   true,
			CheckPath:   "/api/health",
		},
	})

	config := h.GenerateConfig(80, 443, nil)

	expectedStrings := []string{
		"acl host_grafana hdr_end(host) -i .grafana.example.com",
		"acl host_prometheus hdr_end(host) -i .prom.example.com",
		"use_backend grafana_backend if host_grafana",
		"use_backend prometheus_backend if host_prometheus",
		"backend grafana_backend",
		"server grafana 192.168.1.50:3000",
		"option httpchk GET /api/health",
		"server prometheus 192.168.1.51:9090 check",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing: %s", expected)
		}
	}
}

func TestGenerateConfig_MetricsDeny(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "ragtag",
			DomainMatch: ".rt.example.com",
			Server:      "192.168.1.50:7700",
			MetricsPath: "/metrics",
		},
	})

	config := h.GenerateConfig(80, 443, nil)

	for _, expected := range []string{
		// local_access ACL must be emitted even with no internal-only backend.
		"acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8",
		// the metrics path is denied for non-local sources, scoped to the host.
		"http-request deny deny_status 403 if host_ragtag { path /metrics } !local_access",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing: %s", expected)
		}
	}
}

func TestGenerateConfig_NoMetricsDenyByDefault(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{Name: "plain", DomainMatch: ".plain.example.com", Server: "10.0.0.1:80"},
	})

	config := h.GenerateConfig(80, 443, nil)

	// No deny rules referencing local_access, and no local_access ACL at all,
	// when nothing is internal-only or metrics-restricted. (The router-check
	// uses `{ path /router-check }` legitimately, so we check the deny signal.)
	if strings.Contains(config, "!local_access") {
		t.Errorf("no local_access deny expected when nothing is restricted:\n%s", config)
	}
	if strings.Contains(config, "acl local_access") {
		t.Errorf("local_access ACL should not appear with no internal-only or metrics backend")
	}
}

func TestGenerateConfig_WithSSL(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "app",
			DomainMatch: ".app.example.com",
			Server:      "192.168.1.10:8080",
		},
	})

	tempDir := t.TempDir()
	pemFile := filepath.Join(tempDir, "test.pem")
	if err := os.WriteFile(pemFile, []byte("dummy cert"), 0644); err != nil {
		t.Fatalf("failed to create test pem file: %v", err)
	}

	ssl := &SSLConfig{
		Enabled: true,
		CertDir: tempDir,
	}

	config := h.GenerateConfig(80, 443, ssl)

	expectedStrings := []string{
		"frontend https_front",
		"bind *:443 ssl crt",
		"redirect scheme https",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(config, expected) {
			t.Errorf("SSL config missing: %s", expected)
		}
	}
}

func TestGenerateConfig_SSLRedirectCoversExactHost(t *testing.T) {
	// A cert named after an exact host (no wildcard) must still redirect that
	// host. The bug: the ACL was always a wildcard suffix (.hz.office...),
	// which can never match the host hz.office... itself.
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "hz",
			DomainMatch: "hz.office.iodesystems.com",
			Server:      "192.168.1.10:8080",
		},
	})

	tempDir := t.TempDir()
	pemFile := filepath.Join(tempDir, "hz.office.iodesystems.com.pem")
	if err := os.WriteFile(pemFile, []byte("dummy cert"), 0644); err != nil {
		t.Fatalf("failed to create test pem file: %v", err)
	}

	ssl := &SSLConfig{Enabled: true, CertDir: tempDir}
	config := h.GenerateConfig(80, 443, ssl)

	// The cert here is unparseable ("dummy cert"), so redirect patterns fall back
	// to the filename — matched both exactly (the host itself) and as a suffix
	// (deeper subdomains).
	expectedStrings := []string{
		"acl ssl_host hdr(host) -i hz.office.iodesystems.com",
		"acl ssl_host hdr_end(host) -i .hz.office.iodesystems.com",
		"redirect scheme https code 301 if ssl_host !is_router_check",
	}
	for _, expected := range expectedStrings {
		if !strings.Contains(config, expected) {
			t.Errorf("SSL config missing: %s\n--- config ---\n%s", expected, config)
		}
	}
}

func TestGenerateConfig_SSLRedirectFromSANs(t *testing.T) {
	// A single multi-SAN cert must redirect every host it covers, not just the
	// subzone its filename is named after. Redirect ACLs come from the cert's
	// SANs: non-wildcard SANs -> exact host match; wildcard SANs -> suffix match.
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{Name: "hz", DomainMatch: "hz.office.iodesystems.com", Server: "192.168.1.10:8080"},
	})

	tempDir := t.TempDir()
	// The cert filename is the primary subzone (*.vpn), but its SANs span the
	// whole zone — the case that exposed the filename-only bug.
	writeTestCert(t, filepath.Join(tempDir, "vpn.iodesystems.com.pem"),
		[]string{"*.vpn.iodesystems.com", "*.office.iodesystems.com", "iodesystems.com", "dev.iodesystems.com"})

	config := h.GenerateConfig(80, 443, &SSLConfig{Enabled: true, CertDir: tempDir})

	expected := []string{
		// exact matches (sorted): dev.iodesystems.com, iodesystems.com
		"acl ssl_host hdr(host) -i dev.iodesystems.com iodesystems.com",
		// suffix matches (sorted): .office..., .vpn...
		"acl ssl_host hdr_end(host) -i .office.iodesystems.com .vpn.iodesystems.com",
		"redirect scheme https code 301 if ssl_host !is_router_check",
	}
	for _, e := range expected {
		if !strings.Contains(config, e) {
			t.Errorf("SSL config missing: %s\n--- config ---\n%s", e, config)
		}
	}
	// hz.office.iodesystems.com must be covered (via the *.office suffix) — the
	// exact host it was previously missing.
	if !strings.Contains(config, ".office.iodesystems.com") {
		t.Error("hz.office.iodesystems.com not covered by redirect ACLs")
	}
}

func TestGenerateConfig_SSLDisabled(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "app",
			DomainMatch: ".app.example.com",
			Server:      "192.168.1.10:8080",
		},
	})

	ssl := &SSLConfig{
		Enabled: false,
		CertDir: "/nonexistent",
	}

	config := h.GenerateConfig(80, 443, ssl)

	forbiddenStrings := []string{
		"frontend https_front",
		"redirect scheme https",
	}

	for _, forbidden := range forbiddenStrings {
		if strings.Contains(config, forbidden) {
			t.Errorf("disabled SSL config should not contain: %s", forbidden)
		}
	}
}

func TestGenerateConfig_CustomPorts(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends(nil)

	config := h.GenerateConfig(8080, 8443, nil)

	if !strings.Contains(config, "bind *:8080") {
		t.Error("config should use custom HTTP port 8080")
	}
}

func TestGenerateConfig_WildcardBackend(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:        "wildcard-api",
			DomainMatch: "*.api.example.com",
			Server:      "192.168.1.100:8080",
			HTTPCheck:   false,
		},
		{
			Name:        "exact-app",
			DomainMatch: "app.example.com",
			Server:      "192.168.1.101:8080",
			HTTPCheck:   false,
		},
	})

	config := h.GenerateConfig(80, 443, nil)

	// Wildcard should be converted to suffix pattern
	if !strings.Contains(config, "acl host_wildcard_api hdr_end(host) -i .api.example.com") {
		t.Error("wildcard domain should generate suffix ACL pattern")
	}

	// Exact domain should remain unchanged
	if !strings.Contains(config, "acl host_exact_app hdr_end(host) -i app.example.com") {
		t.Error("exact domain should generate exact ACL pattern")
	}

	// Both backends should be defined
	if !strings.Contains(config, "backend wildcard_api_backend") {
		t.Error("wildcard backend should be defined")
	}
	if !strings.Contains(config, "backend exact_app_backend") {
		t.Error("exact backend should be defined")
	}
}

func TestGenerateConfig_MultiDomainBackend(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:          "multi-app",
			DomainMatches: []string{"app.example.com", "book.example.com", "portal.example.com"},
			Server:        "192.168.1.10:8080",
		},
	})

	config := h.GenerateConfig(80, 443, nil)

	// All three domains should be in a single ACL line
	expected := "acl host_multi_app hdr_end(host) -i app.example.com book.example.com portal.example.com"
	if !strings.Contains(config, expected) {
		t.Errorf("config missing multi-domain ACL: %s", expected)
	}

	// Single backend definition
	if !strings.Contains(config, "backend multi_app_backend") {
		t.Error("config missing backend definition")
	}

	// Single use_backend
	if !strings.Contains(config, "use_backend multi_app_backend if host_multi_app") {
		t.Error("config missing use_backend directive")
	}
}

func TestGenerateConfig_BackendTimeouts(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{
			Name:           "slow-app",
			DomainMatches:  []string{"slow.example.com"},
			Server:         "192.168.1.10:8080",
			TimeoutConnect: 10,
			TimeoutServer:  1800,
			TimeoutTunnel:  3600,
		},
		{
			Name:          "default-app",
			DomainMatches: []string{"fast.example.com"},
			Server:        "192.168.1.11:8080",
		},
	})

	config := h.GenerateConfig(80, 443, nil)

	// Overrides emitted in seconds for the backend that sets them
	for _, expected := range []string{
		"timeout connect 10s",
		"timeout server 1800s",
		"timeout tunnel 3600s",
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing timeout override: %s", expected)
		}
	}

	// The backend with no overrides emits no per-backend timeout lines.
	start := strings.Index(config, "backend default_app_backend")
	if start == -1 {
		t.Fatalf("config missing default_app backend:\n%s", config)
	}
	section := config[start:]
	if end := strings.Index(section, "\nbackend "); end != -1 {
		section = section[:end]
	}
	if strings.Contains(section, "timeout ") {
		t.Errorf("default_app backend should inherit defaults, got timeout line:\n%s", section)
	}
}

func TestGenerateConfig_Compression(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends(nil)

	config := h.GenerateConfig(80, 443, nil)

	expectedStrings := []string{
		"compression algo gzip",
		"compression type text/html text/plain text/css application/json",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing compression setting: %s", expected)
		}
	}

	// 'compression offload' must NOT be present: it strips Accept-Encoding before the backend,
	// which forces backends to send raw and blocks precompressed/brotli pass-through (HAProxy
	// only re-gzips). Keep compression pass-through so a brotli-precompressing backend reaches
	// clients directly.
	if strings.Contains(config, "compression offload") {
		t.Error("config has 'compression offload' — that blocks precompressed/brotli pass-through from backends")
	}
}

func TestGenerateConfig_Caching(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends(nil)

	config := h.GenerateConfig(80, 443, nil)

	expectedStrings := []string{
		"cache mycache",
		"total-max-size 1024",
		"max-object-size 524288",
		"http-request cache-use mycache",
		"http-response cache-store mycache",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(config, expected) {
			t.Errorf("config missing caching setting: %s", expected)
		}
	}
}

func TestGenerateConfig_OverlappingDomainsOrderedBySpecificity(t *testing.T) {
	// hdr_end(host) is a greedy suffix match: a host like ha.iodesystems.com
	// matches both `iodesystems.com` and `ha.iodesystems.com`. Whichever
	// use_backend comes first wins. Ensure the more-specific backend is emitted
	// first regardless of input order.
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{
		{Name: "root", DomainMatch: "iodesystems.com", Server: "10.0.0.1:80"},
		{Name: "ha", DomainMatch: "ha.iodesystems.com", Server: "10.0.0.2:80"},
	})

	config := h.GenerateConfig(80, 443, nil)

	haIdx := strings.Index(config, "use_backend ha_backend if host_ha")
	rootIdx := strings.Index(config, "use_backend root_backend if host_root")
	if haIdx < 0 || rootIdx < 0 {
		t.Fatalf("expected both use_backend lines, got:\n%s", config)
	}
	if haIdx > rootIdx {
		t.Errorf("ha.iodesystems.com use_backend (idx %d) must precede iodesystems.com (idx %d) so the suffix-match doesn't swallow ha.* requests", haIdx, rootIdx)
	}
}

func TestSortBackendsBySpecificity(t *testing.T) {
	in := []Backend{
		{Name: "a", DomainMatch: "iodesystems.com"},
		{Name: "b", DomainMatch: "ha.iodesystems.com"},
		{Name: "c", DomainMatch: "deep.app.iodesystems.com"},
		{Name: "d", DomainMatches: []string{"iodesystems.com", "x.foo.com"}}, // least specific = iodesystems.com
		{Name: "e", DomainMatch: "*.iodesystems.com"},                        // pattern .iodesystems.com (2 dots)
	}
	got := sortBackendsBySpecificity(in)
	gotNames := make([]string, len(got))
	for i, b := range got {
		gotNames[i] = b.Name
	}
	// c (3 dots) > b (2 dots) > e (2 dots, shorter pattern than b) > a/d (1 dot tie, alphabetical)
	want := []string{"c", "b", "e", "a", "d"}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("sort order: got %v, want %v", gotNames, want)
		}
	}
}

func TestGetBackends(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")

	if len(h.GetBackends()) != 0 {
		t.Error("backends should be empty initially")
	}

	backends := []Backend{
		{Name: "svc1", Server: "1.2.3.4:80"},
		{Name: "svc2", Server: "5.6.7.8:443"},
	}
	h.SetBackends(backends)

	got := h.GetBackends()
	if len(got) != 2 {
		t.Errorf("expected 2 backends, got %d", len(got))
	}
	if got[0].Name != "svc1" || got[1].Name != "svc2" {
		t.Errorf("backends not set correctly: %+v", got)
	}
}
