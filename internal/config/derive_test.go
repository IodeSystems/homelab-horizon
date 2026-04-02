package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGetZoneForDomain(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{
			{Name: "example.com", ZoneID: "Z1"},
			{Name: "other.io", ZoneID: "Z2"},
			{Name: "sub.example.com", ZoneID: "Z3"},
		},
	}

	tests := []struct {
		domain   string
		wantZone string
		wantNil  bool
	}{
		{"example.com", "example.com", false},
		{"app.example.com", "example.com", false},
		{"deep.app.example.com", "example.com", false},
		{"other.io", "other.io", false},
		{"api.other.io", "other.io", false},
		{"sub.example.com", "example.com", false},
		{"app.sub.example.com", "example.com", false},
		{"notfound.net", "", true},
		{"exampleXcom", "", true},
		// Wildcard domain tests
		{"*.example.com", "example.com", false},
		{"*.api.example.com", "example.com", false},
		{"*.other.io", "other.io", false},
		{"*.notfound.net", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			zone := cfg.GetZoneForDomain(tt.domain)
			if tt.wantNil {
				if zone != nil {
					t.Errorf("GetZoneForDomain(%s) = %v, want nil", tt.domain, zone.Name)
				}
			} else {
				if zone == nil {
					t.Errorf("GetZoneForDomain(%s) = nil, want %s", tt.domain, tt.wantZone)
				} else if zone.Name != tt.wantZone {
					t.Errorf("GetZoneForDomain(%s) = %s, want %s", tt.domain, zone.Name, tt.wantZone)
				}
			}
		})
	}
}

func TestGetPublicIPForService(t *testing.T) {
	cfg := &Config{PublicIP: "1.2.3.4"}

	tests := []struct {
		name   string
		svc    Service
		wantIP string
	}{
		{
			name:   "no external DNS",
			svc:    Service{Name: "test", ExternalDNS: nil},
			wantIP: "",
		},
		{
			name:   "external DNS with specific IP",
			svc:    Service{Name: "test", ExternalDNS: &ExternalDNS{IP: "5.6.7.8"}},
			wantIP: "5.6.7.8",
		},
		{
			name:   "external DNS without IP uses global",
			svc:    Service{Name: "test", ExternalDNS: &ExternalDNS{}},
			wantIP: "1.2.3.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.GetPublicIPForService(&tt.svc)
			if got != tt.wantIP {
				t.Errorf("GetPublicIPForService() = %s, want %s", got, tt.wantIP)
			}
		})
	}
}

func TestDeriveDNSMappings(t *testing.T) {
	cfg := &Config{
		LocalInterface: "192.168.1.100",
		Services: []Service{
			{Domains: []string{"app.example.com"}, InternalDNS: &InternalDNS{IP: "192.168.1.50"}},
			{Domains: []string{"api.example.com"}, InternalDNS: &InternalDNS{IP: "192.168.1.51"}},
			{Domains: []string{"local.example.com"}, InternalDNS: &InternalDNS{IP: "localhost"}},
			{Domains: []string{"loopback.example.com"}, InternalDNS: &InternalDNS{IP: "127.0.0.1"}},
			{Domains: []string{"external.example.com"}, InternalDNS: nil},
			{Domains: []string{"empty.example.com"}, InternalDNS: &InternalDNS{IP: ""}},
		},
	}

	mappings := cfg.DeriveDNSMappings()

	tests := []struct {
		domain string
		wantIP string
		exists bool
	}{
		{"app.example.com", "192.168.1.50", true},
		{"api.example.com", "192.168.1.51", true},
		{"local.example.com", "192.168.1.100", true},
		{"loopback.example.com", "192.168.1.100", true},
		{"external.example.com", "", false},
		{"empty.example.com", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			ip, exists := mappings[tt.domain]
			if exists != tt.exists {
				t.Errorf("mappings[%s] exists = %v, want %v", tt.domain, exists, tt.exists)
			}
			if exists && ip != tt.wantIP {
				t.Errorf("mappings[%s] = %s, want %s", tt.domain, ip, tt.wantIP)
			}
		})
	}
}

func TestDeriveHAProxyBackends(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				Name:   "grafana",
				Domains: []string{"grafana.example.com"},
				Proxy:  &ProxyConfig{Backend: "192.168.1.50:3000"},
			},
			{
				Name:   "prom",
				Domains: []string{"prom.example.com"},
				Proxy: &ProxyConfig{
					Backend:     "192.168.1.51:9090",
					HealthCheck: &HealthCheck{Path: "/api/health"},
				},
			},
			{
				Name:   "internal",
				Domains: []string{"internal.example.com"},
				Proxy:  nil,
			},
			{
				Name:   "empty-backend",
				Domains: []string{"empty.example.com"},
				Proxy:  &ProxyConfig{Backend: ""},
			},
		},
	}

	backends := cfg.DeriveHAProxyBackends()

	if len(backends) != 2 {
		t.Fatalf("Expected 2 backends, got %d", len(backends))
	}

	if backends[0].Name != "grafana" {
		t.Errorf("Expected first backend grafana, got %s", backends[0].Name)
	}
	if backends[0].Server != "192.168.1.50:3000" {
		t.Errorf("Expected grafana server 192.168.1.50:3000, got %s", backends[0].Server)
	}
	if backends[0].HTTPCheck {
		t.Error("Expected grafana HTTPCheck false")
	}

	if backends[1].Name != "prom" {
		t.Errorf("Expected second backend prom, got %s", backends[1].Name)
	}
	if !backends[1].HTTPCheck {
		t.Error("Expected prom HTTPCheck true")
	}
	if backends[1].CheckPath != "/api/health" {
		t.Errorf("Expected prom CheckPath /api/health, got %s", backends[1].CheckPath)
	}
}

func TestDeriveHAProxyBackends_MultiDomain(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				Name:    "multi-app",
				Domains: []string{"app.example.com", "book.example.com", "portal.example.com"},
				Proxy:   &ProxyConfig{Backend: "192.168.1.50:8080"},
			},
		},
	}

	backends := cfg.DeriveHAProxyBackends()

	if len(backends) != 1 {
		t.Fatalf("Expected 1 backend, got %d", len(backends))
	}

	b := backends[0]
	if len(b.DomainMatches) != 3 {
		t.Fatalf("Expected 3 domain matches, got %d", len(b.DomainMatches))
	}
	if b.DomainMatches[0] != "app.example.com" {
		t.Errorf("Expected first domain app.example.com, got %s", b.DomainMatches[0])
	}
	if b.DomainMatches[1] != "book.example.com" {
		t.Errorf("Expected second domain book.example.com, got %s", b.DomainMatches[1])
	}
	if b.DomainMatches[2] != "portal.example.com" {
		t.Errorf("Expected third domain portal.example.com, got %s", b.DomainMatches[2])
	}
}

func TestDeriveDNSMappings_MultiDomain(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				Domains:     []string{"app.example.com", "book.example.com"},
				InternalDNS: &InternalDNS{IP: "192.168.1.50"},
			},
		},
	}

	mappings := cfg.DeriveDNSMappings()

	if len(mappings) != 2 {
		t.Fatalf("Expected 2 mappings, got %d", len(mappings))
	}
	if mappings["app.example.com"] != "192.168.1.50" {
		t.Errorf("Expected app mapping 192.168.1.50, got %s", mappings["app.example.com"])
	}
	if mappings["book.example.com"] != "192.168.1.50" {
		t.Errorf("Expected book mapping 192.168.1.50, got %s", mappings["book.example.com"])
	}
}

func TestGetServicesForZone(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{
			{Name: "example.com", ZoneID: "Z1"},
		},
		Services: []Service{
			{Name: "app", Domains: []string{"app.example.com"}},
			{Name: "api", Domains: []string{"api.example.com"}},
			{Name: "root", Domains: []string{"example.com"}},
			{Name: "other", Domains: []string{"app.other.io"}},
		},
	}

	zone := cfg.GetZoneForDomain("example.com")
	services := cfg.GetServicesForZone(zone)

	if len(services) != 3 {
		t.Fatalf("Expected 3 services, got %d", len(services))
	}

	names := make(map[string]bool)
	for _, svc := range services {
		names[svc.Name] = true
	}

	if !names["app"] || !names["api"] || !names["root"] {
		t.Errorf("Expected app, api, root services, got %v", names)
	}
	if names["other"] {
		t.Error("Did not expect 'other' service")
	}
}

func TestGetExternalServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "external1", ExternalDNS: &ExternalDNS{}},
			{Name: "internal1", ExternalDNS: nil},
			{Name: "external2", ExternalDNS: &ExternalDNS{IP: "1.2.3.4"}},
			{Name: "internal2"},
		},
	}

	services := cfg.GetExternalServices()
	if len(services) != 2 {
		t.Fatalf("Expected 2 external services, got %d", len(services))
	}

	names := []string{services[0].Name, services[1].Name}
	if names[0] != "external1" || names[1] != "external2" {
		t.Errorf("Expected external1, external2, got %v", names)
	}
}

func TestGetInternalOnlyServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "external1", ExternalDNS: &ExternalDNS{}},
			{Name: "internal1", ExternalDNS: nil},
			{Name: "external2", ExternalDNS: &ExternalDNS{IP: "1.2.3.4"}},
			{Name: "internal2"},
		},
	}

	services := cfg.GetInternalOnlyServices()
	if len(services) != 2 {
		t.Fatalf("Expected 2 internal services, got %d", len(services))
	}

	names := []string{services[0].Name, services[1].Name}
	if names[0] != "internal1" || names[1] != "internal2" {
		t.Errorf("Expected internal1, internal2, got %v", names)
	}
}

func TestGetProxiedServices(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "proxied1", Proxy: &ProxyConfig{Backend: "1.2.3.4:80"}},
			{Name: "direct1", Proxy: nil},
			{Name: "proxied2", Proxy: &ProxyConfig{Backend: "5.6.7.8:443"}},
		},
	}

	services := cfg.GetProxiedServices()
	if len(services) != 2 {
		t.Fatalf("Expected 2 proxied services, got %d", len(services))
	}
}

func TestValidateService(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{{Name: "example.com", ZoneID: "Z1"}},
	}

	tests := []struct {
		name    string
		svc     Service
		wantErr string
	}{
		{
			name:    "valid service",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}},
			wantErr: "",
		},
		{
			name:    "missing name",
			svc:     Service{Name: "", Domains: []string{"app.example.com"}},
			wantErr: "name",
		},
		{
			name:    "missing domain",
			svc:     Service{Name: "app", Domains: nil},
			wantErr: "domain",
		},
		{
			name:    "no zone for domain",
			svc:     Service{Name: "app", Domains: []string{"app.other.io"}},
			wantErr: "domain",
		},
		{
			name:    "valid internal DNS",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}, InternalDNS: &InternalDNS{IP: "192.168.1.1"}},
			wantErr: "",
		},
		{
			name:    "localhost internal DNS",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}, InternalDNS: &InternalDNS{IP: "localhost"}},
			wantErr: "",
		},
		{
			name:    "invalid internal DNS IP",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}, InternalDNS: &InternalDNS{IP: "not-an-ip"}},
			wantErr: "internal_dns.ip",
		},
		{
			name:    "valid proxy backend",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}, Proxy: &ProxyConfig{Backend: "192.168.1.1:8080"}},
			wantErr: "",
		},
		{
			name:    "invalid proxy backend",
			svc:     Service{Name: "app", Domains: []string{"app.example.com"}, Proxy: &ProxyConfig{Backend: "no-port"}},
			wantErr: "proxy.backend",
		},
		// Wildcard domain validation tests
		{
			name:    "valid wildcard domain",
			svc:     Service{Name: "wildcard", Domains: []string{"*.api.example.com"}},
			wantErr: "",
		},
		{
			name:    "wildcard at root level",
			svc:     Service{Name: "wildcard", Domains: []string{"*.example.com"}},
			wantErr: "",
		},
		{
			name:    "invalid wildcard format - missing dot",
			svc:     Service{Name: "bad", Domains: []string{"*example.com"}},
			wantErr: "domain",
		},
		{
			name:    "invalid wildcard - no domain after",
			svc:     Service{Name: "bad", Domains: []string{"*."}},
			wantErr: "domain",
		},
		{
			name:    "invalid wildcard - single label",
			svc:     Service{Name: "bad", Domains: []string{"*.com"}},
			wantErr: "domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cfg.ValidateService(&tt.svc)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ValidateService() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Errorf("ValidateService() error = nil, want error containing %s", tt.wantErr)
				} else {
					ve, ok := err.(*ValidationError)
					if !ok || ve.Field != tt.wantErr {
						t.Errorf("ValidateService() error field = %v, want %s", err, tt.wantErr)
					}
				}
			}
		})
	}
}

func TestValidateZone(t *testing.T) {
	cfg := &Config{}

	tests := []struct {
		name    string
		zone    Zone
		wantErr string
	}{
		{
			name:    "valid zone",
			zone:    Zone{Name: "example.com", ZoneID: "Z1234"},
			wantErr: "",
		},
		{
			name:    "missing name",
			zone:    Zone{Name: "", ZoneID: "Z1234"},
			wantErr: "name",
		},
		{
			name:    "missing zone ID",
			zone:    Zone{Name: "example.com", ZoneID: ""},
			wantErr: "zone_id",
		},
		{
			name:    "SSL enabled without email",
			zone:    Zone{Name: "example.com", ZoneID: "Z1234", SSL: &ZoneSSL{Enabled: true}},
			wantErr: "ssl.email",
		},
		{
			name:    "SSL enabled with email",
			zone:    Zone{Name: "example.com", ZoneID: "Z1234", SSL: &ZoneSSL{Enabled: true, Email: "admin@example.com"}},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cfg.ValidateZone(&tt.zone)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ValidateZone() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Errorf("ValidateZone() error = nil, want error containing %s", tt.wantErr)
				} else {
					ve, ok := err.(*ValidationError)
					if !ok || ve.Field != tt.wantErr {
						t.Errorf("ValidateZone() error field = %v, want %s", err, tt.wantErr)
					}
				}
			}
		})
	}
}

func TestAddService(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{{Name: "example.com", ZoneID: "Z1"}},
		Services: []Service{
			{Name: "existing", Domains: []string{"existing.example.com"}},
		},
	}

	err := cfg.AddService(Service{Name: "new", Domains: []string{"new.example.com"}})
	if err != nil {
		t.Errorf("AddService() error = %v", err)
	}
	if len(cfg.Services) != 2 {
		t.Errorf("Expected 2 services, got %d", len(cfg.Services))
	}

	err = cfg.AddService(Service{Name: "existing", Domains: []string{"another.example.com"}})
	if err == nil {
		t.Error("AddService() should fail for duplicate name")
	}

	err = cfg.AddService(Service{Name: "dup-domain", Domains: []string{"existing.example.com"}})
	if err == nil {
		t.Error("AddService() should fail for duplicate domain")
	}
}

func TestRemoveService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "svc1", Domains: []string{"svc1.example.com"}},
			{Name: "svc2", Domains: []string{"svc2.example.com"}},
			{Name: "svc3", Domains: []string{"svc3.example.com"}},
		},
	}

	removed := cfg.RemoveService("svc2")
	if !removed {
		t.Error("RemoveService() should return true")
	}
	if len(cfg.Services) != 2 {
		t.Errorf("Expected 2 services, got %d", len(cfg.Services))
	}

	removed = cfg.RemoveService("nonexistent")
	if removed {
		t.Error("RemoveService() should return false for nonexistent")
	}
}

func TestAddZone(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{{Name: "example.com", ZoneID: "Z1"}},
	}

	err := cfg.AddZone(Zone{Name: "other.io", ZoneID: "Z2"})
	if err != nil {
		t.Errorf("AddZone() error = %v", err)
	}
	if len(cfg.Zones) != 2 {
		t.Errorf("Expected 2 zones, got %d", len(cfg.Zones))
	}

	err = cfg.AddZone(Zone{Name: "example.com", ZoneID: "Z3"})
	if err == nil {
		t.Error("AddZone() should fail for duplicate name")
	}
}

func TestRemoveZone(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{
			{Name: "example.com", ZoneID: "Z1"},
			{Name: "other.io", ZoneID: "Z2"},
		},
		Services: []Service{
			{Name: "svc1", Domains: []string{"svc1.example.com"}},
			{Name: "svc2", Domains: []string{"svc2.example.com"}},
			{Name: "svc3", Domains: []string{"svc3.other.io"}},
		},
	}

	removed := cfg.RemoveZone("example.com")
	if !removed {
		t.Error("RemoveZone() should return true")
	}
	if len(cfg.Zones) != 1 {
		t.Errorf("Expected 1 zone, got %d", len(cfg.Zones))
	}
	if len(cfg.Services) != 1 {
		t.Errorf("Expected 1 service remaining, got %d", len(cfg.Services))
	}
	if cfg.Services[0].Name != "svc3" {
		t.Errorf("Expected svc3 to remain, got %s", cfg.Services[0].Name)
	}

	removed = cfg.RemoveZone("nonexistent")
	if removed {
		t.Error("RemoveZone() should return false for nonexistent")
	}
}

func TestGetZone(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{
			{Name: "example.com", ZoneID: "Z1"},
			{Name: "other.io", ZoneID: "Z2"},
		},
	}

	zone := cfg.GetZone("example.com")
	if zone == nil || zone.Name != "example.com" {
		t.Error("GetZone(example.com) failed")
	}

	zone = cfg.GetZone("*.example.com")
	if zone == nil || zone.Name != "example.com" {
		t.Error("GetZone(*.example.com) should strip wildcard")
	}

	zone = cfg.GetZone("nonexistent.net")
	if zone != nil {
		t.Error("GetZone(nonexistent.net) should return nil")
	}
}

func TestGetService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "svc1", Domains: []string{"svc1.example.com"}},
			{Name: "svc2", Domains: []string{"svc2.example.com"}},
		},
	}

	svc := cfg.GetService("svc1")
	if svc == nil || svc.Name != "svc1" {
		t.Error("GetService(svc1) failed")
	}

	svc = cfg.GetService("nonexistent")
	if svc != nil {
		t.Error("GetService(nonexistent) should return nil")
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"192.168.1.1:8080", "192.168.1.1"},
		{"192.168.1.1", "192.168.1.1"},
		{"hostname:8080", "hostname"},
		{"hostname", "hostname"},
		{"[::1]:8080", "::1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractIP(tt.input)
			if got != tt.want {
				t.Errorf("extractIP(%s) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestServiceJSON_BackwardsCompat(t *testing.T) {
	// Legacy format: "domain": "x"
	legacy := `{"name":"test","domain":"app.example.com"}`
	var svc Service
	if err := json.Unmarshal([]byte(legacy), &svc); err != nil {
		t.Fatalf("Unmarshal legacy format: %v", err)
	}
	if len(svc.Domains) != 1 || svc.Domains[0] != "app.example.com" {
		t.Errorf("Legacy domain not migrated: %v", svc.Domains)
	}

	// New format: "domains": ["x", "y"]
	multi := `{"name":"test","domains":["app.example.com","book.example.com"]}`
	var svc2 Service
	if err := json.Unmarshal([]byte(multi), &svc2); err != nil {
		t.Fatalf("Unmarshal multi format: %v", err)
	}
	if len(svc2.Domains) != 2 {
		t.Errorf("Expected 2 domains, got %d", len(svc2.Domains))
	}

	// Marshal produces "domains" key
	data, err := json.Marshal(svc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"domains"`) {
		t.Errorf("Marshal should use domains key: %s", data)
	}
}

func TestValidationError(t *testing.T) {
	err := &ValidationError{Field: "name", Message: "is required"}
	expected := "name: is required"
	if err.Error() != expected {
		t.Errorf("ValidationError.Error() = %s, want %s", err.Error(), expected)
	}
}

func TestDeriveSSLDomains(t *testing.T) {
	cfg := &Config{
		Zones: []Zone{
			{
				Name:     "example.com",
				ZoneID:   "Z1",
				SubZones: []string{"*", "*.vpn"},
				SSL:      &ZoneSSL{Enabled: true, Email: "admin@example.com"},
				DNSProvider: &DNSProviderConfig{
					Type:       "route53",
					AWSProfile: "default",
				},
			},
			{
				Name:     "nossl.io",
				ZoneID:   "Z2",
				SubZones: []string{"*"},
				SSL:      &ZoneSSL{Enabled: false},
			},
			{
				Name:     "nosub.io",
				ZoneID:   "Z3",
				SubZones: []string{},
				SSL:      &ZoneSSL{Enabled: true, Email: "admin@nosub.io"},
			},
			{
				Name:     "rootonly.io",
				ZoneID:   "Z4",
				SubZones: []string{"", "*"},
				SSL:      &ZoneSSL{Enabled: true, Email: "admin@rootonly.io"},
			},
		},
	}

	domains := cfg.DeriveSSLDomains()

	if len(domains) != 2 {
		t.Fatalf("Expected 2 SSL domain configs, got %d", len(domains))
	}

	if domains[0].Domain != "*.example.com" {
		t.Errorf("Expected primary domain *.example.com, got %s", domains[0].Domain)
	}
	if len(domains[0].ExtraSANs) != 1 || domains[0].ExtraSANs[0] != "*.vpn.example.com" {
		t.Errorf("Expected ExtraSANs [*.vpn.example.com], got %v", domains[0].ExtraSANs)
	}
	if domains[0].Email != "admin@example.com" {
		t.Errorf("Expected email admin@example.com, got %s", domains[0].Email)
	}

	if domains[1].Domain != "rootonly.io" {
		t.Errorf("Expected primary domain rootonly.io, got %s", domains[1].Domain)
	}
	if len(domains[1].ExtraSANs) != 1 || domains[1].ExtraSANs[0] != "*.rootonly.io" {
		t.Errorf("Expected ExtraSANs [*.rootonly.io], got %v", domains[1].ExtraSANs)
	}
}
