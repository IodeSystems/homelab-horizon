package server

import (
	"github.com/iodesystems/homelab-horizon/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminTokenMigrationFromConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.Config{
		AdminToken: "existing-token",
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	// The .token file should NOT exist yet
	tokenFile := configPath + ".token"
	if _, err := os.Stat(tokenFile); err == nil {
		t.Error("Expected token file NOT to exist")
	}

	// Initialize server
	_, err = NewWithConfig(cfg, configPath, true, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Check if .token file was created with migrated token
	content, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("Expected token file to be created: %v", err)
	}
	if string(content) != "existing-token\n" {
		t.Errorf("Expected token content 'existing-token\\n', got %q", string(content))
	}

	// Check that config no longer has the token
	reloadedCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedCfg.AdminToken != "" {
		t.Errorf("Expected AdminToken to be cleared from config after migration, got %q", reloadedCfg.AdminToken)
	}
}

func TestAdminTokenFromFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")
	tokenFile := configPath + ".token"

	// Write token to file first
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Config has a different token (should be ignored)
	cfg := &config.Config{
		AdminToken: "config-token",
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Initialize server
	s, err := NewWithConfig(cfg, configPath, true, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Server should use the file token, not the config token
	if s.adminToken != "file-token" {
		t.Errorf("Expected server to use file token 'file-token', got %q", s.adminToken)
	}
}

func TestAdminTokenGeneration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")
	tokenFile := configPath + ".token"

	// Neither file nor config has a token
	cfg := &config.Config{}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Initialize server
	s, err := NewWithConfig(cfg, configPath, true, "test")
	if err != nil {
		t.Fatal(err)
	}

	// A token should have been generated
	if s.adminToken == "" {
		t.Error("Expected a token to be generated")
	}
	if len(s.adminToken) != 32 {
		t.Errorf("Expected 32-char token, got %d chars", len(s.adminToken))
	}

	// Token should be written to file
	content, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("Expected token file to be created: %v", err)
	}
	if strings.TrimSpace(string(content)) != s.adminToken {
		t.Errorf("Token file content mismatch: got %q, expected %q", strings.TrimSpace(string(content)), s.adminToken)
	}

	// Config should NOT have the token
	reloadedCfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedCfg.AdminToken != "" {
		t.Errorf("Expected AdminToken to not be in config, got %q", reloadedCfg.AdminToken)
	}
}
