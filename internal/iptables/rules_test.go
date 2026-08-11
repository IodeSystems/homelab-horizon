package iptables

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
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
	// Expect: MASQUERADE + FORWARD jump + FORWARD return + INPUT jump +
	// default drop. No peers so no per-peer rules — and in particular no
	// WG-INPUT body, since only jailed peers get entries there.
	if len(got) != 5 {
		t.Fatalf("want 5 rules, got %d: %v", len(got), got)
	}
	wantCanon := []string{
		"nat|POSTROUTING|-o eth0 -j MASQUERADE",
		"filter|FORWARD|-i wg0 -j WG-FORWARD",
		"filter|FORWARD|-o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"filter|INPUT|-i wg0 -j WG-INPUT",
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
	// alice gets: VPN allow + LAN allow + DROP = 3 peer rules. Nothing in
	// WG-INPUT — she isn't jailed, so she falls through it.
	// Plus MASQUERADE + FORWARD x2 + INPUT jump + default DROP = 5 framing.
	if len(got) != 8 {
		t.Fatalf("want 8 rules, got %d:\n%v", len(got), got)
	}
	for _, r := range got {
		if r.Chain == InputChainName {
			t.Errorf("unjailed peer should have no %s rules, got %v", InputChainName, r)
		}
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
	// Jail replaces full-tunnel with a bare DROP on the transit path, and
	// portal/DNS exceptions plus a DROP on the gateway-local path.
	want := map[string]bool{
		"filter|WG-FORWARD|-s 10.100.0.99/32 -j DROP":                                      false,
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT": false,
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p udp --dport 53 -j ACCEPT":   false,
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 53 -j ACCEPT":   false,
		"filter|WG-INPUT|-s 10.100.0.99/32 -j DROP":                                        false,
	}
	for _, r := range got {
		if _, ok := want[r.Canonical()]; ok {
			want[r.Canonical()] = true
		}
		if r.Canonical() == "filter|WG-FORWARD|-s 10.100.0.99/32 -j ACCEPT" {
			t.Errorf("MFA jail should suppress full-tunnel ACCEPT, but found it")
		}
	}
	for canon, found := range want {
		if !found {
			t.Errorf("MFA jail missing rule %q in:\n%v", canon, got)
		}
	}
}

// TestExpectedRulesMFAJailDropsAfterAcceptsInInputChain pins ordering inside
// WG-INPUT. iptables is first-match-wins, so a `-s peer -j DROP` emitted
// before the portal ACCEPT would jail the peer with no way to reach the page
// that un-jails it — a lockout that looks identical to a broken tunnel.
func TestExpectedRulesMFAJailDropsAfterAcceptsInInputChain(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		Peers:       []PeerInput{{Name: "mallory", IP: "10.100.0.99"}},
		JailedPeers: map[string]bool{"mallory": true},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
	}
	var inputChain []Rule
	for _, r := range ExpectedRules(in) {
		if r.Chain == InputChainName {
			inputChain = append(inputChain, r)
		}
	}
	if len(inputChain) != 4 {
		t.Fatalf("want 4 %s rules, got %d: %v", InputChainName, len(inputChain), inputChain)
	}
	for i, r := range inputChain[:3] {
		if r.Args[len(r.Args)-1] != "ACCEPT" {
			t.Errorf("%s rule[%d] should be an ACCEPT, got %v", InputChainName, i, r)
		}
	}
	if last := inputChain[3]; last.Args[len(last.Args)-1] != "DROP" {
		t.Errorf("%s must end in the peer DROP, got %v", InputChainName, last)
	}
}

// TestExpectedRulesMFAJailOpensHAProxyPorts covers the seam between the two
// jail layers: L3 must let a jailed peer reach HAProxy, or the L7 rules that
// decide portal-vs-deny never get a packet to judge and the peer just times
// out on the portal.
func TestExpectedRulesMFAJailOpensHAProxyPorts(t *testing.T) {
	in := Inputs{
		WGInterface:  "wg0",
		OutIface:     "eth0",
		Peers:        []PeerInput{{Name: "mallory", IP: "10.100.0.99"}},
		JailedPeers:  map[string]bool{"mallory": true},
		ServerWGIP:   "10.100.0.1",
		ListenPort:   "8080",
		HAProxyPorts: []string{"80", "443"},
	}
	want := []string{
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT",
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 80 -j ACCEPT",
		"filter|WG-INPUT|-s 10.100.0.99/32 -d 10.100.0.1/32 -p tcp --dport 443 -j ACCEPT",
		"filter|WG-INPUT|-s 10.100.0.99/32 -j DROP",
	}
	got := ExpectedRules(in)
	for _, w := range want {
		found := false
		for _, r := range got {
			if r.Canonical() == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in:\n%v", w, got)
		}
	}
}

// TestJailAllowsSkipsDuplicatePort: when horizon is itself reached on an
// HAProxy port, the same `--dport` rule would be emitted twice — harmless in
// iptables but it makes the chain drift-check compare unequal forever, which
// means a rebuild on every 60s tick.
func TestJailAllowsSkipsDuplicatePort(t *testing.T) {
	got := jailAllows("8080", []string{"80", "8080", ""})
	ports := map[string]int{}
	for _, a := range got {
		ports[a[len(a)-1]]++
	}
	if ports["8080"] != 1 {
		t.Errorf("port 8080 should appear once, got %d: %v", ports["8080"], got)
	}
	if ports["80"] != 1 {
		t.Errorf("port 80 should appear once, got %d: %v", ports["80"], got)
	}
}

// TestExpectedRulesMFAJailFailsOpenWithoutPortal covers the case where horizon
// can't name its own portal (no WG address or no listen port parsed). Emitting
// the DROPs anyway would strand the peer permanently, so the jail is skipped
// entirely and the peer keeps its profile.
func TestExpectedRulesMFAJailFailsOpenWithoutPortal(t *testing.T) {
	in := Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		Peers:       []PeerInput{{Name: "mallory", IP: "10.100.0.99"}},
		Profiles:    map[string]string{"mallory": "full-tunnel"},
		JailedPeers: map[string]bool{"mallory": true},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "", // unknown
	}
	got := ExpectedRules(in)
	for _, r := range got {
		if r.Chain == InputChainName {
			t.Errorf("no %s rules expected when the portal is unaddressable, got %v", InputChainName, r)
		}
	}
	found := false
	for _, r := range got {
		if r.Canonical() == "filter|WG-FORWARD|-s 10.100.0.99/32 -j ACCEPT" {
			found = true
		}
	}
	if !found {
		t.Errorf("jail should fail open to the peer's profile, got:\n%v", got)
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
