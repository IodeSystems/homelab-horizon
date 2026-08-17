package config

import (
	"testing"
	"time"
)

func TestMFAInactivityTimeoutIsFloored(t *testing.T) {
	tests := []struct {
		minutes int
		want    time.Duration
	}{
		{0, 0}, // off
		{1, 0}, // below the floor: off rather than nearly-instant
		{4, 0}, // still below
		{5, 5 * time.Minute},
		{30, 30 * time.Minute},
		{-1, 0},
	}
	for _, tc := range tests {
		cfg := Config{VPNMFAInactivityMinutes: tc.minutes}
		if got := cfg.MFAInactivityTimeout(); got != tc.want {
			t.Errorf("%d minutes -> %v, want %v", tc.minutes, got, tc.want)
		}
	}
}

func TestStaleMFASessions(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cfg := &Config{
		VPNMFAInactivityMinutes: 15,
		VPNMFASessions: map[string]int64{
			"idle":       now.Add(8 * time.Hour).Unix(),
			"active":     now.Add(8 * time.Hour).Unix(),
			"never":      now.Add(8 * time.Hour).Unix(),
			"unknown":    now.Add(8 * time.Hour).Unix(),
			"borderline": now.Add(8 * time.Hour).Unix(),
		},
	}

	stale := cfg.StaleMFASessions(map[string]time.Time{
		"idle":   now.Add(-30 * time.Minute),
		"active": now.Add(-2 * time.Minute),
		// Zero: never handshaked since the interface came up. Left alone —
		// see below for why that matters.
		"never": {},
		// "unknown" is absent from the map entirely.
		"borderline": now.Add(-15 * time.Minute), // exactly at the limit, not past it
	}, now)

	if len(stale) != 1 || stale[0] != "idle" {
		t.Fatalf("stale = %v, want [idle]", stale)
	}
}

// The case that would otherwise clear every session on the box: after a
// WireGuard restart every peer reports a zero handshake, and revoking on that
// would log out the entire VPN because an interface bounced.
func TestZeroHandshakeDoesNotRevoke(t *testing.T) {
	now := time.Now()
	cfg := &Config{
		VPNMFAInactivityMinutes: 5,
		VPNMFASessions: map[string]int64{
			"a": now.Add(time.Hour).Unix(),
			"b": now.Add(time.Hour).Unix(),
		},
	}

	stale := cfg.StaleMFASessions(map[string]time.Time{
		"a": {},
		"b": {},
	}, now)
	if len(stale) != 0 {
		t.Fatalf("a fresh interface revoked %v", stale)
	}
}

func TestStaleMFASessionsOffByDefault(t *testing.T) {
	now := time.Now()
	cfg := &Config{
		VPNMFASessions: map[string]int64{"idle": now.Add(time.Hour).Unix()},
	}
	// A peer idle for a day, with the feature off: nothing happens, because a
	// session that lasts as long as it was granted for is what the operator
	// already agreed to.
	if stale := cfg.StaleMFASessions(map[string]time.Time{
		"idle": now.Add(-24 * time.Hour),
	}, now); len(stale) != 0 {
		t.Fatalf("inactivity is off but revoked %v", stale)
	}
}

// A peer with no session cannot be revoked, however long it has been quiet —
// otherwise the pruner would churn the config for peers it has nothing to do.
func TestStaleMFASessionsIgnoresPeersWithoutSessions(t *testing.T) {
	now := time.Now()
	cfg := &Config{VPNMFAInactivityMinutes: 5, VPNMFASessions: map[string]int64{}}
	if stale := cfg.StaleMFASessions(map[string]time.Time{
		"stranger": now.Add(-time.Hour),
	}, now); len(stale) != 0 {
		t.Fatalf("revoked a peer with no session: %v", stale)
	}
}
