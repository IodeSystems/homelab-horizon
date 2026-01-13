package haproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
