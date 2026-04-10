package config

import (
	"testing"
	"time"
)

func TestIsPeerMFAJailed_Disabled(t *testing.T) {
	cfg := &Config{VPNMFAEnabled: false}
	if cfg.IsPeerMFAJailed("alice") {
		t.Error("should not be jailed when MFA disabled")
	}
}

func TestIsPeerMFAJailed_AdminBypass(t *testing.T) {
	cfg := &Config{
		VPNMFAEnabled: true,
		VPNAdmins:     []string{"alice"},
	}
	if cfg.IsPeerMFAJailed("alice") {
		t.Error("admin should bypass MFA")
	}
	if !cfg.IsPeerMFAJailed("bob") {
		t.Error("non-admin with no session should be jailed")
	}
}

func TestIsPeerMFAJailed_ActiveSession(t *testing.T) {
	cfg := &Config{
		VPNMFAEnabled:  true,
		VPNMFASessions: map[string]int64{"alice": time.Now().Add(1 * time.Hour).Unix()},
	}
	if cfg.IsPeerMFAJailed("alice") {
		t.Error("peer with active session should not be jailed")
	}
}

func TestIsPeerMFAJailed_ForeverSession(t *testing.T) {
	cfg := &Config{
		VPNMFAEnabled:  true,
		VPNMFASessions: map[string]int64{"alice": 0},
	}
	if cfg.IsPeerMFAJailed("alice") {
		t.Error("peer with forever session should not be jailed")
	}
}

func TestIsPeerMFAJailed_ExpiredSession(t *testing.T) {
	cfg := &Config{
		VPNMFAEnabled:  true,
		VPNMFASessions: map[string]int64{"alice": time.Now().Add(-1 * time.Hour).Unix()},
	}
	if !cfg.IsPeerMFAJailed("alice") {
		t.Error("peer with expired session should be jailed")
	}
}

func TestGetJailedPeers(t *testing.T) {
	cfg := &Config{
		VPNMFAEnabled:  true,
		VPNAdmins:      []string{"admin"},
		VPNMFASessions: map[string]int64{"active": time.Now().Add(1 * time.Hour).Unix()},
		WGPeers: []WGPeer{
			{Name: "admin"},
			{Name: "active"},
			{Name: "jailed"},
		},
	}
	jailed := cfg.GetJailedPeers()
	if jailed["admin"] {
		t.Error("admin should not be jailed")
	}
	if jailed["active"] {
		t.Error("peer with active session should not be jailed")
	}
	if !jailed["jailed"] {
		t.Error("peer with no session should be jailed")
	}
}

func TestPruneExpiredMFASessions(t *testing.T) {
	cfg := &Config{
		VPNMFASessions: map[string]int64{
			"expired":  time.Now().Add(-1 * time.Hour).Unix(),
			"active":   time.Now().Add(1 * time.Hour).Unix(),
			"forever":  0,
		},
	}
	pruned := cfg.PruneExpiredMFASessions()
	if !pruned {
		t.Error("should have pruned at least one session")
	}
	if _, ok := cfg.VPNMFASessions["expired"]; ok {
		t.Error("expired session should have been pruned")
	}
	if _, ok := cfg.VPNMFASessions["active"]; !ok {
		t.Error("active session should still exist")
	}
	if _, ok := cfg.VPNMFASessions["forever"]; !ok {
		t.Error("forever session should still exist")
	}
}

func TestSetClearMFASecret(t *testing.T) {
	cfg := &Config{}
	cfg.SetMFASecret("alice", "JBSWY3DPEHPK3PXP")
	if cfg.VPNMFASecrets["alice"] != "JBSWY3DPEHPK3PXP" {
		t.Error("secret not set")
	}
	cfg.SetMFASession("alice", 0)
	if cfg.VPNMFASessions["alice"] != 0 {
		t.Error("session not set")
	}
	cfg.ClearMFASecret("alice")
	if _, ok := cfg.VPNMFASecrets["alice"]; ok {
		t.Error("secret should be cleared")
	}
	if _, ok := cfg.VPNMFASessions["alice"]; ok {
		t.Error("session should be cleared when secret is cleared")
	}
}

func TestRenameMFAPeer(t *testing.T) {
	cfg := &Config{
		VPNMFASecrets:  map[string]string{"old": "secret"},
		VPNMFASessions: map[string]int64{"old": 12345},
	}
	cfg.RenameMFAPeer("old", "new")
	if _, ok := cfg.VPNMFASecrets["old"]; ok {
		t.Error("old secret should be removed")
	}
	if cfg.VPNMFASecrets["new"] != "secret" {
		t.Error("secret should be moved to new name")
	}
	if _, ok := cfg.VPNMFASessions["old"]; ok {
		t.Error("old session should be removed")
	}
	if cfg.VPNMFASessions["new"] != 12345 {
		t.Error("session should be moved to new name")
	}
}

func TestMFAConfigPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/cfg.json"

	cfg := &Config{
		ListenAddr:     ":8080",
		VPNMFAEnabled:  true,
		VPNMFADurations: []string{"2h", "4h"},
		VPNMFASecrets:  map[string]string{"alice": "JBSWY3DPEHPK3PXP"},
		VPNMFASessions: map[string]int64{"alice": 0},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.VPNMFAEnabled {
		t.Error("MFA enabled should persist")
	}
	if len(loaded.VPNMFADurations) != 2 {
		t.Errorf("durations should persist, got %d", len(loaded.VPNMFADurations))
	}
	if loaded.VPNMFASecrets["alice"] != "JBSWY3DPEHPK3PXP" {
		t.Error("secrets should persist")
	}
	if loaded.VPNMFASessions["alice"] != 0 {
		t.Error("sessions should persist")
	}
}
