package iptables

import (
	"testing"

	"homelab-horizon/internal/config"
)

func TestRuleCanonical(t *testing.T) {
	r := Rule{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}}
	want := "nat|POSTROUTING|-o eth0 -j MASQUERADE"
	if got := r.Canonical(); got != want {
		t.Errorf("Canonical() = %q, want %q", got, want)
	}
}

// TestRuleCanonicalNormalizesStateAndConntrack pins the equivalence between
// legacy `-m state --state X` and modern `-m conntrack --ctstate X`. Without
// this, the reconciler dup-inserts the FORWARD return-traffic rule on every
// 60s tick when iptables-nft rewrites the saved form to conntrack — slowly
// growing FORWARD until WG forwarding performance collapses.
func TestRuleCanonicalNormalizesStateAndConntrack(t *testing.T) {
	legacy := Rule{
		Table: "filter",
		Chain: "FORWARD",
		Args:  []string{"-o", "wg0", "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}
	modern := Rule{
		Table: "filter",
		Chain: "FORWARD",
		Args:  []string{"-o", "wg0", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}
	if legacy.Canonical() != modern.Canonical() {
		t.Errorf("state/conntrack forms must canonicalize equal:\n  legacy:   %q\n  modern:   %q",
			legacy.Canonical(), modern.Canonical())
	}
	want := "filter|FORWARD|-o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
	if legacy.Canonical() != want {
		t.Errorf("canonical form should be the conntrack one, got %q", legacy.Canonical())
	}
}

func TestRuleString(t *testing.T) {
	r := Rule{Table: "filter", Chain: "FORWARD", Args: []string{"-i", "wg0", "-j", "WG-FORWARD"}}
	want := "-t filter -A FORWARD -i wg0 -j WG-FORWARD"
	if got := r.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestExpectedRulesEmptyInputs(t *testing.T) {
	if got := ExpectedRules(Inputs{}); got != nil {
		t.Errorf("empty WGInterface should produce nil, got %v", got)
	}
}

func TestExpectedRulesMinimal(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
	}
	got := ExpectedRules(in)
	// Expect: MASQUERADE + FORWARD jump + FORWARD return + default drop.
	// No peers so no per-peer rules.
	if len(got) != 4 {
		t.Fatalf("want 4 rules, got %d: %v", len(got), got)
	}
	wantCanon := []string{
		"nat|POSTROUTING|-o eth0 -j MASQUERADE",
		"filter|FORWARD|-i wg0 -j WG-FORWARD",
		"filter|FORWARD|-o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"filter|WG-FORWARD|-j DROP",
	}
	for i, w := range wantCanon {
		if got[i].Canonical() != w {
			t.Errorf("rule[%d] = %q, want %q", i, got[i].Canonical(), w)
		}
	}
}

func TestExpectedRulesLanAccessProfile(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     "192.168.1.0/24",
		Peers:       []PeerInput{{Name: "alice", IP: "10.100.0.42"}},
		Profiles:    map[string]string{"alice": "lan-access"},
	}
	got := ExpectedRules(in)
	// alice gets: VPN allow + LAN allow + DROP = 3 peer rules.
	// Plus MASQUERADE + FORWARD x2 + default DROP = 4 framing rules.
	if len(got) != 7 {
		t.Fatalf("want 7 rules, got %d:\n%v", len(got), got)
	}
	// Verify alice's LAN-allow rule is present.
	found := false
	for _, r := range got {
		if r.Canonical() == "filter|WG-FORWARD|-s 10.100.0.42/32 -d 192.168.1.0/24 -j ACCEPT" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected alice's lan-access rule, not found in:\n%v", got)
	}
}

func TestExpectedRulesFullTunnelProfile(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		Peers:       []PeerInput{{Name: "bob", IP: "10.100.0.5"}},
		Profiles:    map[string]string{"bob": "full-tunnel"},
	}
	got := ExpectedRules(in)
	// bob gets a single bare ACCEPT (no VPN/LAN split, no drop).
	found := false
	for _, r := range got {
		if r.Canonical() == "filter|WG-FORWARD|-s 10.100.0.5/32 -j ACCEPT" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bob's full-tunnel accept, got:\n%v", got)
	}
}

func TestExpectedRulesMFAJailOverridesProfile(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		Peers:       []PeerInput{{Name: "mallory", IP: "10.100.0.99"}},
		Profiles:    map[string]string{"mallory": "full-tunnel"}, // would normally bypass
		JailedPeers: map[string]bool{"mallory": true},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
	}
	got := ExpectedRules(in)
	// Jail should replace full-tunnel with 2 rules: single-port ACCEPT + DROP.
	foundAccept := false
	foundDrop := false
	for _, r := range got {
		switch r.Canonical() {
		case "filter|WG-FORWARD|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT":
			foundAccept = true
		case "filter|WG-FORWARD|-s 10.100.0.99/32 -j DROP":
			foundDrop = true
		case "filter|WG-FORWARD|-s 10.100.0.99/32 -j ACCEPT":
			t.Errorf("MFA jail should suppress full-tunnel ACCEPT, but found it")
		}
	}
	if !foundAccept || !foundDrop {
		t.Errorf("MFA jail missing accept (%v) or drop (%v) in:\n%v", foundAccept, foundDrop, got)
	}
}

func TestStaleRulesEmptyWhenNoPriorState(t *testing.T) {
	cfg := &config.Config{WGInterface: "wg0"}
	if got := StaleRules(cfg, nil, "", ""); got != nil {
		t.Errorf("StaleRules with no LastLocalIface/LastLanCIDR should be nil, got %v", got)
	}
}

func TestStaleRulesDiffersFromExpectedOnIfaceChange(t *testing.T) {
	cfg := &config.Config{
		WGInterface:    "wg0",
		VPNRange:       "10.100.0.0/24",
		LastLocalIface: "eth0",
		LastLanCIDR:    "192.168.1.0/24",
		VPNProfiles:    map[string]string{"alice": "lan-access"},
	}
	peers := []PeerInput{{Name: "alice", IP: "10.100.0.42"}}

	stale := StaleRules(cfg, peers, "", "")
	// Should have a MASQUERADE rule pinned to the OLD iface.
	found := false
	for _, r := range stale {
		if r.Canonical() == "nat|POSTROUTING|-o eth0 -j MASQUERADE" {
			found = true
		}
	}
	if !found {
		t.Errorf("stale rules should include MASQUERADE for LastLocalIface=eth0, got:\n%v", stale)
	}

	// And alice's LAN-access rule should reference the OLD LAN CIDR.
	foundLan := false
	for _, r := range stale {
		if r.Canonical() == "filter|WG-FORWARD|-s 10.100.0.42/32 -d 192.168.1.0/24 -j ACCEPT" {
			foundLan = true
		}
	}
	if !foundLan {
		t.Errorf("stale rules should include alice's lan-access to old LanCIDR, got:\n%v", stale)
	}
}

func TestPeerIPExtraction(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"10.100.0.42/32", "10.100.0.42"},
		{"10.100.0.42/32, fd00::42/128", "10.100.0.42"},
		{"10.100.0.42/24", "10.100.0.42"},
		{"", ""},
	}
	for _, c := range cases {
		if got := peerIP(c.in); got != c.want {
			t.Errorf("peerIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
