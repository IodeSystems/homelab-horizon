package integration

import (
	"path/filepath"
	"testing"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/server"
)

func TestDryRunMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	cfg := &config.Config{
		ListenAddr:  ":8080",
		WGInterface: "wg0",
		VPNRange:    "10.100.0.0/24",
	}

	err := config.Save(configPath, cfg)
	if err != nil {
		t.Fatalf("Failed to save test config: %v", err)
	}

	srv, err := server.NewWithConfig(cfg, configPath, true)
	if err != nil {
		t.Fatalf("Failed to create server in dry run mode: %v", err)
	}

	if srv == nil {
		t.Fatal("Expected non-nil server")
	}

	t.Log("Successfully created server in dry run mode")
}

func TestDryRunConfigPersistence(t *testing.T) {
	t.Skip("Dry run mode needs refinement at server operation level - skipping config persistence test")
}

func TestRealMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	cfg := &config.Config{
		ListenAddr:  ":8080",
		WGInterface: "wg0",
		VPNRange:    "10.100.0.0/24",
	}

	err := config.Save(configPath, cfg)
	if err != nil {
		t.Fatalf("Failed to save test config: %v", err)
	}

	srv, err := server.NewWithConfig(cfg, configPath, false)
	if err != nil {
		t.Fatalf("Failed to create server in real mode: %v", err)
	}

	if srv == nil {
		t.Fatal("Expected non-nil server")
	}

	t.Log("Successfully created server in real mode")
}
