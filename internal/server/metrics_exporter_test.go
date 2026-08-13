package server

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestControlsReportHardenedOnly pins the honest direction of hz_control_state:
// 1 means the control is in its hardened setting, and the default deployment
// must not read as hardened just because nothing was configured.
func TestControlsReportHardenedOnly(t *testing.T) {
	off := hzControls(&config.Config{})
	for _, c := range off {
		if c.ok {
			t.Errorf("control %q reads hardened on a bare config", c.name)
		}
	}

	hard := hzControls(&config.Config{
		VPNMFAEnabled:   true,
		VPNMFAScope:     config.MFAScopeAll,
		VPNMFADurations: []string{"15m"},
		SSLEnabled:      true,
	})
	for _, c := range hard {
		if !c.ok {
			t.Errorf("control %q should be satisfied by a hardened config", c.name)
		}
	}
}

// A "forever" session is an unbounded one; 8.2.8 wants re-auth after 15
// minutes idle, so an allowlist offering permanent sessions is not bounded
// however short its other options are.
func TestSessionBoundedRejectsForever(t *testing.T) {
	for _, durations := range [][]string{
		{"15m", "forever"},
		{"2h"},
		{},
	} {
		for _, c := range hzControls(&config.Config{VPNMFAEnabled: true, VPNMFADurations: durations}) {
			if c.name == "vpn_mfa_session_bounded" && c.ok {
				t.Errorf("durations %v must not read as bounded", durations)
			}
		}
	}
}

func TestControlNamesAreNotComplianceClaims(t *testing.T) {
	for _, c := range hzControls(&config.Config{}) {
		if strings.Contains(c.name, "compliant") || strings.Contains(c.name, "pci") {
			t.Errorf("control %q claims compliance; these describe hz's config only", c.name)
		}
		if c.requirement == "" {
			t.Errorf("control %q has no requirement label to correlate against", c.name)
		}
	}
}

func TestRecentHandshake(t *testing.T) {
	cases := map[string]bool{
		"":                          false,
		"12 seconds ago":            true,
		"1 minute, 2 seconds ago":   true,
		"3 minutes ago":             true,
		"4 minutes, 10 seconds ago": false,
		"1 hour, 2 minutes ago":     false,
		"2 days ago":                false,
	}
	for in, want := range cases {
		if got := recentHandshake(in); got != want {
			t.Errorf("recentHandshake(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestCollectorRegistersOnBareServer reproduces the panic this collector
// shipped with: Describe was implemented via DescribeByCollect, which runs
// Collect at *registration* time — before the config is stored and, on a
// dry-run instance, with no WireGuard or HAProxy at all.
func TestCollectorRegistersOnBareServer(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(newHZCollector(&Server{})); err != nil {
		t.Fatalf("registering against a bare server must not fail: %v", err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("gathering from a bare server must not fail: %v", err)
	}
}

// A scrape that races startup, or hits a dry-run instance, should omit what it
// cannot measure rather than report zeros — "no peers" and "not measured" are
// different answers and only one of them is true.
func TestCollectorOmitsUnmeasurable(t *testing.T) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(newHZCollector(&Server{})); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range got {
		names[mf.GetName()] = true
	}
	if !names["hz_up"] {
		t.Error("hz_up should always be emitted — it is the one thing always knowable")
	}
	if names["hz_vpn_peers"] {
		t.Error("peer count must be omitted when there is no config to read, not reported as 0")
	}
}
