package wireguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfig(t *testing.T) {
	cfg := NewConfig("/etc/wireguard/wg0.conf", "wg0")
	if cfg.path != "/etc/wireguard/wg0.conf" {
		t.Errorf("Expected path /etc/wireguard/wg0.conf, got %s", cfg.path)
	}
	if cfg.iface != "wg0" {
		t.Errorf("Expected iface wg0, got %s", cfg.iface)
	}
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wg0.conf")

	configData := `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24
ListenPort = 51820
PostUp = iptables -A FORWARD -i %i -j ACCEPT
PostDown = iptables -D FORWARD -i %i -j ACCEPT

[Peer]
# alice
PublicKey = YWxpY2VrZXk=
AllowedIPs = 10.100.0.2/32

[Peer]
# bob
PublicKey = Ym9ia2V5
AllowedIPs = 10.100.0.3/32
`

	err := os.WriteFile(configPath, []byte(configData), 0600)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg := NewConfig(configPath, "wg0")
	err = cfg.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.privateKey != "cGFzc3dvcmQ=" {
		t.Errorf("Expected privateKey cGFzc3dvcmQ=, got %s", cfg.privateKey)
	}
	if cfg.address != "10.100.0.1/24" {
		t.Errorf("Expected address 10.100.0.1/24, got %s", cfg.address)
	}
	if cfg.listenPort != "51820" {
		t.Errorf("Expected listenPort 51820, got %s", cfg.listenPort)
	}

	peers := cfg.GetPeers()
	if len(peers) != 2 {
		t.Fatalf("Expected 2 peers, got %d", len(peers))
	}

	if peers[0].Name != "alice" {
		t.Errorf("Expected first peer name alice, got %s", peers[0].Name)
	}
	if peers[0].PublicKey != "YWxpY2VrZXk=" {
		t.Errorf("Expected first peer key YWxpY2VrZXk=, got %s", peers[0].PublicKey)
	}
	if peers[0].AllowedIPs != "10.100.0.2/32" {
		t.Errorf("Expected first peer AllowedIPs 10.100.0.2/32, got %s", peers[0].AllowedIPs)
	}

	if peers[1].Name != "bob" {
		t.Errorf("Expected second peer name bob, got %s", peers[1].Name)
	}
}

func TestLoadNonExistent(t *testing.T) {
	cfg := NewConfig("/nonexistent/wg0.conf", "wg0")
	err := cfg.Load()
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestGetPeerByIP(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wg0.conf")

	configData := `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24

[Peer]
# alice
PublicKey = YWxpY2VrZXk=
AllowedIPs = 10.100.0.2/32

[Peer]
# bob
PublicKey = Ym9ia2V5
AllowedIPs = 10.100.0.3/32
`

	os.WriteFile(configPath, []byte(configData), 0600)
	cfg := NewConfig(configPath, "wg0")
	cfg.Load()

	peer := cfg.GetPeerByIP("10.100.0.2")
	if peer == nil {
		t.Fatal("Expected to find peer for 10.100.0.2")
	}
	if peer.Name != "alice" {
		t.Errorf("Expected peer name alice, got %s", peer.Name)
	}

	peer = cfg.GetPeerByIP("10.100.0.99")
	if peer != nil {
		t.Error("Expected nil for nonexistent IP")
	}
}

func TestExtractValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"PrivateKey = abc123", "abc123"},
		{"Address = 10.0.0.1/24", "10.0.0.1/24"},
		{"ListenPort = 51820", "51820"},
		{"NoEquals", ""},
		{"Key=ValueNoSpaces", "ValueNoSpaces"},
		{"Key = Value With Spaces", "Value With Spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractValue(tt.input)
			if got != tt.want {
				t.Errorf("extractValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetAddress(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wg0.conf")

	configData := `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24
`
	os.WriteFile(configPath, []byte(configData), 0600)

	cfg := NewConfig(configPath, "wg0")
	cfg.Load()

	addr := cfg.GetAddress()
	if addr != "10.100.0.1/24" {
		t.Errorf("GetAddress() = %s, want 10.100.0.1/24", addr)
	}
}

func TestValidatePublicKey(t *testing.T) {
	tests := []struct {
		key   string
		valid bool
	}{
		{"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=", true},
		{"", false},
		{"short", false},
		{"has spaces in it", false},
		{"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := ValidatePublicKey(tt.key)
			if got != tt.valid {
				t.Errorf("ValidatePublicKey(%q) = %v, want %v", tt.key, got, tt.valid)
			}
		})
	}
}

func TestGetNextIP(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wg0.conf")

	configData := `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24

[Peer]
PublicKey = YWxpY2VrZXk=
AllowedIPs = 10.100.0.2/32

[Peer]
PublicKey = Ym9ia2V5
AllowedIPs = 10.100.0.3/32
`
	os.WriteFile(configPath, []byte(configData), 0600)

	cfg := NewConfig(configPath, "wg0")
	cfg.Load()

	nextIP, err := cfg.GetNextIP("10.100.0.0/24")
	if err != nil {
		t.Fatalf("GetNextIP() error = %v", err)
	}
	if nextIP != "10.100.0.4/32" {
		t.Errorf("GetNextIP() = %s, want 10.100.0.4/32", nextIP)
	}
}

func TestGetNextIPEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wg0.conf")

	configData := `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24
`
	os.WriteFile(configPath, []byte(configData), 0600)

	cfg := NewConfig(configPath, "wg0")
	cfg.Load()

	nextIP, err := cfg.GetNextIP("10.100.0.0/24")
	if err != nil {
		t.Fatalf("GetNextIP() error = %v", err)
	}
	if nextIP != "10.100.0.2/32" {
		t.Errorf("GetNextIP() = %s, want 10.100.0.2/32", nextIP)
	}
}

func TestSystemStatus(t *testing.T) {
	cfg := NewConfig("/etc/wireguard/wg0.conf", "wg0")
	status := cfg.CheckSystem("10.100.0.0/24")

	t.Logf("InterfaceUp: %v", status.InterfaceUp)
	t.Logf("IPForwarding: %v", status.IPForwarding)
	t.Logf("Masquerading: %v", status.Masquerading)
}
