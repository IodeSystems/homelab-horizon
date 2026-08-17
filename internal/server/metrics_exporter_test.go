package server

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hardenedByDefault lists the controls hz satisfies with no configuration.
//
// Both are enforced out of the box because they only ever act on someone
// already failing or reusing a password, so they cost a correct user nothing.
// Reporting them unmet on a bare config would understate compliance, which is
// the same dishonesty as overstating it pointed the other way.
var hardenedByDefault = map[string]bool{
	"login_lockout":    true,
	"password_history": true,
}

// TestControlsReportHardenedOnly pins the honest direction of hz_control_state:
// 1 means the control is in its hardened setting. A control an operator has to
// turn on must not read as hardened just because nothing was configured, and a
// control hz enforces by default must not read as unmet.
func TestControlsReportHardenedOnly(t *testing.T) {
	off := hzControls(&config.Config{}, hostFactsSnapshot{})
	for _, c := range off {
		switch {
		case hardenedByDefault[c.name] && !c.ok:
			t.Errorf("control %q is enforced by default but reads unmet", c.name)
		case !hardenedByDefault[c.name] && c.ok:
			t.Errorf("control %q reads hardened on a bare config", c.name)
		}
	}

	hard := hzControls(&config.Config{
		VPNMFAEnabled:        true,
		VPNMFAScope:          config.MFAScopeAll,
		VPNMFADurations:      []string{"15m"},
		SSLEnabled:           true,
		HAProxyTLSMinVersion: "TLSv1.3",
		AdminTokenDisabled:   true,
		// Loopback-only: the admin UI is then reachable solely through
		// HAProxy's TLS frontend, which is what 2.2.7 asks for.
		ListenAddr: "127.0.0.1:8080",
		Policy: config.AccountPolicy{
			IdleMinutes:        15,
			PasswordMaxAgeDays: 90,
		},
	}, hostFactsSnapshot{
		measured:          true,
		timeSynced:        true,
		journalPersistent: true,
		journalRetention:  400 * 24 * time.Hour,
	})
	for _, c := range hard {
		if !c.ok {
			t.Errorf("control %q should be satisfied by a hardened config", c.name)
		}
	}
}

// The policy controls must track the thresholds the standard names rather than
// merely being set to something.
func TestPolicyControlsHonourThresholds(t *testing.T) {
	find := func(cfg config.Config, name string) bool {
		for _, c := range hzControls(&cfg, hostFactsSnapshot{}) {
			if c.name == name {
				return c.ok
			}
		}
		t.Fatalf("control %q not reported", name)
		return false
	}

	// 8.2.8 wants 15 minutes or less.
	if find(config.Config{Policy: config.AccountPolicy{IdleMinutes: 30}}, "session_idle_timeout") {
		t.Error("a 30 minute idle timeout should not satisfy 8.2.8")
	}
	if !find(config.Config{Policy: config.AccountPolicy{IdleMinutes: 15}}, "session_idle_timeout") {
		t.Error("15 minutes should satisfy 8.2.8")
	}

	// 8.3.4 wants at most 10 attempts and at least 30 minutes.
	if find(config.Config{Policy: config.AccountPolicy{MaxFailedAttempts: 20}}, "login_lockout") {
		t.Error("20 attempts should not satisfy 8.3.4")
	}
	if find(config.Config{Policy: config.AccountPolicy{LockoutMinutes: 5}}, "login_lockout") {
		t.Error("a 5 minute lockout should not satisfy 8.3.4")
	}
	// Explicitly disabled must read unmet rather than falling back to a default.
	if find(config.Config{Policy: config.AccountPolicy{MaxFailedAttempts: -1}}, "login_lockout") {
		t.Error("lockout turned off should not satisfy 8.3.4")
	}

	// 8.3.7 wants four.
	if find(config.Config{Policy: config.AccountPolicy{PasswordHistory: 2}}, "password_history") {
		t.Error("a history of 2 should not satisfy 8.3.7")
	}

	// 8.3.9 wants 90 days or less.
	if find(config.Config{Policy: config.AccountPolicy{PasswordMaxAgeDays: 365}}, "password_rotation") {
		t.Error("365 days should not satisfy 8.3.9")
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
		for _, c := range hzControls(&config.Config{VPNMFAEnabled: true, VPNMFADurations: durations}, hostFactsSnapshot{}) {
			if c.name == "vpn_mfa_session_bounded" && c.ok {
				t.Errorf("durations %v must not read as bounded", durations)
			}
		}
	}
}

func TestControlNamesAreNotComplianceClaims(t *testing.T) {
	for _, c := range hzControls(&config.Config{}, hostFactsSnapshot{}) {
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

func TestParseOpenSSLTime(t *testing.T) {
	// The exact shape `openssl x509 -noout -dates` emits.
	got, ok := parseOpenSSLTime("Nov  5 12:00:00 2026 GMT")
	if !ok {
		t.Fatal("should parse openssl's own date format")
	}
	if got.Year() != 2026 || got.Month() != 11 || got.Day() != 5 {
		t.Errorf("parsed %v", got)
	}
	// Double-digit days are not space-padded.
	if _, ok := parseOpenSSLTime("Dec 25 01:02:03 2027 GMT"); !ok {
		t.Error("should parse a two-digit day")
	}
	for _, bad := range []string{"", "not a date", "2026-11-05"} {
		if _, ok := parseOpenSSLTime(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// Host facts are measured on the health tick. Before the first tick the
// controls must read not-met rather than claiming a synchronised clock and a
// patched system nobody has looked at yet.
func TestHostFactControlsUnmeasured(t *testing.T) {
	for _, c := range hzControls(&config.Config{}, hostFactsSnapshot{}) {
		if (c.name == "time_synchronised" || c.name == "patches_current") && c.ok {
			t.Errorf("control %q reads met before anything was measured", c.name)
		}
	}
	measured := hostFactsSnapshot{measured: true, timeSynced: true, securityUpdates: 0}
	got := map[string]bool{}
	for _, c := range hzControls(&config.Config{}, measured) {
		got[c.name] = c.ok
	}
	if !got["time_synchronised"] || !got["patches_current"] {
		t.Errorf("a synced, patched host should satisfy both: %v", got)
	}
	pending := hostFactsSnapshot{measured: true, timeSynced: true, securityUpdates: 3}
	for _, c := range hzControls(&config.Config{}, pending) {
		if c.name == "patches_current" && c.ok {
			t.Error("pending security updates must not read as patched")
		}
	}
}

// 2.2.7 turns on where hz listens, so the parsing of that address is the
// control. A wildcard bind is the common default and the actual finding.
func TestAdminBoundToLoopback(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false},        // every interface — hz's own default
		{"0.0.0.0:8080", false}, // the same thing spelled out
		{"[::]:8080", false},    // and again, for v6
		{"192.168.1.160:8080", false},
		{"", false},
	}
	for _, tc := range tests {
		cfg := config.Config{ListenAddr: tc.addr}
		if got := cfg.AdminBoundToLoopback(); got != tc.want {
			t.Errorf("AdminBoundToLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// 10.5.1 needs both halves. Either one alone reports a compliance hz does not
// have: a volatile journal loses everything at reboot however long its
// retention says, and a persistent one with no retention limit rotates on size.
func TestLogPersistenceNeedsBothHalves(t *testing.T) {
	find := func(facts hostFactsSnapshot) bool {
		for _, c := range hzControls(&config.Config{}, facts) {
			if c.name == "log_persistence" {
				return c.ok
			}
		}
		t.Fatal("log_persistence not reported")
		return false
	}

	year := 400 * 24 * time.Hour
	if find(hostFactsSnapshot{measured: true, journalRetention: year}) {
		t.Error("a volatile journal should not satisfy 10.5.1")
	}
	if find(hostFactsSnapshot{measured: true, journalPersistent: true}) {
		t.Error("persistence with no retention limit should not satisfy 10.5.1")
	}
	if find(hostFactsSnapshot{measured: true, journalPersistent: true, journalRetention: 30 * 24 * time.Hour}) {
		t.Error("a month of retention should not satisfy a twelve month requirement")
	}
	if !find(hostFactsSnapshot{measured: true, journalPersistent: true, journalRetention: year}) {
		t.Error("persistent with a year of retention should satisfy 10.5.1")
	}
	// Unmeasured must never read as compliant.
	if find(hostFactsSnapshot{journalPersistent: true, journalRetention: year}) {
		t.Error("unmeasured facts should not satisfy 10.5.1")
	}
}
