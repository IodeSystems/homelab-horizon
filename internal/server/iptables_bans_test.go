package server

import (
	"slices"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// This file covers the hz-web half of "bans become rules the reconciler can
// see" (plan/design/privilege-audit.md §8.3, blocker 2). The iptables half — the
// widened read and the proof that it did not widen the delete — is
// internal/iptables/bans_test.go.

// TestActiveBanIPsDropsExpired. An expired ban is on its way out via
// startBanExpiry's 30s tick, and reconcileIPTables runs on a 60s one. Emitting
// it as expected in between would have the reconciler install the rule the
// expiry loop is about to remove.
func TestActiveBanIPsDropsExpired(t *testing.T) {
	const now int64 = 1_000_000

	bans := []config.IPBan{
		{IP: "192.0.2.5"},                        // permanent
		{IP: "192.0.2.6", ExpiresAt: now + 60},   // still running
		{IP: "192.0.2.7", ExpiresAt: now},        // expired this second
		{IP: "192.0.2.8", ExpiresAt: now - 3600}, // expired an hour ago
	}

	got := activeBanIPs(bans, now)
	want := []string{"192.0.2.5", "192.0.2.6"}
	if !slices.Equal(got, want) {
		t.Errorf("activeBanIPs = %v, want %v", got, want)
	}
}

func TestActiveBanIPsOnNoBans(t *testing.T) {
	if got := activeBanIPs(nil, 1); len(got) != 0 {
		t.Errorf("want no addresses, got %v", got)
	}
}

// TestABanReachesTheAgentsDesiredSet is the end of the wire this change opens.
//
// hz publishes rule SETS to the agent (iptablesSectionFor), built from the same
// generator reconcileIPTables uses. With bans in the expected set, a ban is
// something the agent can see, diff and — once somebody adds --apply to its
// unit — install. Nothing here arms it: cmd/hz-agent/install.go never emits
// --apply, so this is the precondition for moving bans, not the move.
func TestABanReachesTheAgentsDesiredSet(t *testing.T) {
	expected := iptables.ExpectedRules(iptables.Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     "192.0.2.0/24",
		Peers:       []iptables.PeerInput{{Name: "laptop", AllowedIPs: "10.100.0.2/32"}},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
		BannedIPs:   activeBanIPs([]config.IPBan{{IP: "198.51.100.7"}}, 1),
	})

	sec := iptablesSectionFor(expected, nil, nil, "eth0", "eth0")
	if sec == nil {
		t.Fatal("no section published for a managed gateway")
	}
	want := "filter|INPUT|-s 198.51.100.7/32 -j DROP"
	for _, r := range sec.Expected {
		if r.Canonical() == want {
			return
		}
	}
	t.Errorf("the agent's desired set carries no ban rule; want %q in:\n%v", want, sec.Expected)
}
