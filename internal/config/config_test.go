package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.ListenAddr != ":8080" {
		t.Errorf("Expected listen addr :8080, got %s", cfg.ListenAddr)
	}

	if cfg.WGInterface != "wg0" {
		t.Errorf("Expected wg interface wg0, got %s", cfg.WGInterface)
	}

	if cfg.VPNRange != "10.100.0.0/24" {
		t.Errorf("Expected VPN range 10.100.0.0/24, got %s", cfg.VPNRange)
	}

	if !cfg.DNSMasqEnabled {
		t.Error("Expected dnsmasq enabled by default")
	}
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.json")

	configData := `{
		"listen_addr": ":9090",
		"wg_interface": "testwg",
		"vpn_range": "192.168.100.0/24"
	}`

	err := os.WriteFile(configPath, []byte(configData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("Expected listen addr :9090, got %s", cfg.ListenAddr)
	}

	if cfg.WGInterface != "testwg" {
		t.Errorf("Expected wg interface testwg, got %s", cfg.WGInterface)
	}

	if cfg.VPNRange != "192.168.100.0/24" {
		t.Errorf("Expected VPN range 192.168.100.0/24, got %s", cfg.VPNRange)
	}
}

func TestLoadWithComments(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.jsonc")

	configData := `{
		// This is a comment
		"listen_addr": ":9090",
		// Another comment
		"wg_interface": "testwg"
	}`

	err := os.WriteFile(configPath, []byte(configData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config with comments: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("Expected listen addr :9090, got %s", cfg.ListenAddr)
	}

	if cfg.WGInterface != "testwg" {
		t.Errorf("Expected wg interface testwg, got %s", cfg.WGInterface)
	}
}

func TestLoadNonExistent(t *testing.T) {
	cfg, err := Load("/non/existent/config.json")
	if err != nil {
		t.Errorf("Expected no error for non-existent config, got %v", err)
	}

	if cfg == nil {
		t.Error("Expected default config for non-existent file")
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("Expected default listen addr :8080, got %s", cfg.ListenAddr)
	}
}

func TestSave(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.json")

	cfg := Default()
	cfg.ListenAddr = ":9999"
	cfg.WGInterface = "savetest"

	err := Save(configPath, cfg)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	loadedCfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to reload saved config: %v", err)
	}

	if loadedCfg.ListenAddr != ":9999" {
		t.Errorf("Expected saved listen addr :9999, got %s", loadedCfg.ListenAddr)
	}

	if loadedCfg.WGInterface != "savetest" {
		t.Errorf("Expected saved wg interface savetest, got %s", loadedCfg.WGInterface)
	}
}

func TestDetectLocalInterface(t *testing.T) {
	cfg := Default()

	ip := cfg.DetectLocalInterface()
	if ip == "" {
		t.Error("Expected some IP address, got empty string")
	}
}

func TestEnsureLocalInterface(t *testing.T) {
	cfg := &Config{
		LocalInterface: "",
		VPNRange:       "10.100.0.0/24",
	}

	cfg.EnsureLocalInterface()
	if cfg.LocalInterface == "" {
		t.Error("Expected LocalInterface to be set")
	}

	cfg2 := &Config{
		LocalInterface: "192.168.1.100",
		VPNRange:       "10.100.0.0/24",
	}

	cfg2.EnsureLocalInterface()
	if cfg2.LocalInterface != "192.168.1.100" {
		t.Error("Expected existing LocalInterface to be preserved")
	}
}

func TestDeriveAllowedIPs(t *testing.T) {
	tests := []struct {
		name     string
		vpnRange string
		want     string
	}{
		{
			name:     "standard VPN range",
			vpnRange: "10.100.0.0/24",
			want:     "10.100.0.0/24",
		},
		{
			name:     "different VPN range",
			vpnRange: "192.168.100.0/24",
			want:     "192.168.100.0/24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{VPNRange: tt.vpnRange}
			got := cfg.DeriveAllowedIPs()

			if got != tt.want {
				t.Errorf("DeriveAllowedIPs() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetAllowedIPs(t *testing.T) {
	cfg := &Config{
		VPNRange:   "10.100.0.0/24",
		AllowedIPs: "10.100.0.0/24, 192.168.1.0/24",
	}

	got := cfg.GetAllowedIPs()
	want := "10.100.0.0/24, 192.168.1.0/24"
	if got != want {
		t.Errorf("GetAllowedIPs() = %s, want %s", got, want)
	}

	cfg2 := &Config{
		VPNRange:   "10.100.0.0/24",
		AllowedIPs: "",
	}

	got2 := cfg2.GetAllowedIPs()
	want2 := "10.100.0.0/24"
	if got2 != want2 {
		t.Errorf("GetAllowedIPs() = %s, want %s", got2, want2)
	}
}

func TestGetWGGatewayIP(t *testing.T) {
	tests := []struct {
		vpnRange string
		want     string
	}{
		{"10.100.0.0/24", "10.100.0.1"},
		{"192.168.100.0/24", "192.168.100.1"},
		{"10.0.0.0/8", "10.0.0.1"},
		{"", "10.100.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.vpnRange, func(t *testing.T) {
			cfg := &Config{VPNRange: tt.vpnRange}
			got := cfg.GetWGGatewayIP()

			if got != tt.want {
				t.Errorf("GetWGGatewayIP() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFind(t *testing.T) {
	tmpDir := t.TempDir()

	config1 := filepath.Join(tmpDir, "config.json")
	config2 := filepath.Join(tmpDir, "homelab-horizon.json")

	err := os.WriteFile(config1, []byte(`{}`), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	originalSearchPaths := SearchPaths
	SearchPaths = []string{
		config1,
		config2,
		"/non/existent/path",
	}
	defer func() { SearchPaths = originalSearchPaths }()

	path, found := Find()
	if !found {
		t.Error("Expected to find config file")
	}

	if path != config1 {
		t.Errorf("Expected path %s, got %s", config1, path)
	}
}

func TestFindNotFound(t *testing.T) {
	originalSearchPaths := SearchPaths
	SearchPaths = []string{
		"/non/existent/path1",
		"/non/existent/path2",
	}
	defer func() { SearchPaths = originalSearchPaths }()

	path, found := Find()
	if found {
		t.Error("Expected not to find config file")
	}
	if path != "/non/existent/path1" {
		t.Errorf("Expected first path as default, got %s", path)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.json")

	err := os.WriteFile(configPath, []byte(`{invalid json`), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, err = Load(configPath)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "config.json")

	cfg := Default()
	err := Save(configPath, cfg)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Expected config file to be created")
	}
}

func TestStripJSONCComments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no comments",
			input: `{"key": "value"}`,
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "full line comment skipped",
			input: "// comment\n{\"key\": \"value\"}",
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "inline comment stripped",
			input: "{\"key\": \"value\"} // comment",
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "mixed comments",
			input: "// first\n{\"key\": \"value\"} // inline\n// third",
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "url in string preserved",
			input: "{\"url\": \"http://example.com\"}",
			want:  "{\"url\": \"http://example.com\"}\n",
		},
		{
			name:  "url with trailing comment",
			input: "{\"url\": \"http://example.com\"} // comment",
			want:  "{\"url\": \"http://example.com\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripJSONCComments([]byte(tt.input))
			if string(got) != tt.want {
				t.Errorf("stripJSONCComments() = %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestGetInterfaceIP(t *testing.T) {
	ip, err := GetInterfaceIP("lo")
	if err == nil {
		if ip != "127.0.0.1" {
			t.Errorf("Expected 127.0.0.1 for loopback, got %s", ip)
		}
	}

	_, err = GetInterfaceIP("nonexistent999")
	if err == nil {
		t.Error("Expected error for nonexistent interface")
	}
}

func TestGetLocalNetworkCIDR(t *testing.T) {
	cidr := GetLocalNetworkCIDR("lo")
	t.Logf("Loopback CIDR: %s", cidr)

	cidr = GetLocalNetworkCIDR("nonexistent999")
	if cidr != "" {
		t.Errorf("Expected empty string for nonexistent interface, got %s", cidr)
	}
}

func TestDNSProviderConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  DNSProviderConfig
		wantErr bool
	}{
		{
			name:    "route53 with profile",
			config:  DNSProviderConfig{Type: DNSProviderRoute53, AWSProfile: "default"},
			wantErr: false,
		},
		{
			name:    "route53 with credentials",
			config:  DNSProviderConfig{Type: DNSProviderRoute53, AWSAccessKeyID: "key", AWSSecretAccessKey: "secret"},
			wantErr: false,
		},
		{
			name:    "route53 missing credentials",
			config:  DNSProviderConfig{Type: DNSProviderRoute53},
			wantErr: true,
		},
		{
			name:    "namecom valid",
			config:  DNSProviderConfig{Type: DNSProviderNamecom, NamecomUsername: "user", NamecomAPIToken: "token"},
			wantErr: false,
		},
		{
			name:    "namecom missing token",
			config:  DNSProviderConfig{Type: DNSProviderNamecom, NamecomUsername: "user"},
			wantErr: true,
		},
		{
			name:    "unknown provider",
			config:  DNSProviderConfig{Type: "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
