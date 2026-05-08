package iptables

import "testing"

func TestInferStaleIfaceFindsNonDefault(t *testing.T) {
	live := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}},
	}
	if got := inferStaleIface(live, "eth1"); got != "eth0" {
		t.Errorf("want eth0, got %q", got)
	}
}

func TestInferStaleIfaceSkipsCurrentDefault(t *testing.T) {
	live := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth1", "-j", "MASQUERADE"}},
	}
	if got := inferStaleIface(live, "eth1"); got != "" {
		t.Errorf("expected no inference when default matches, got %q", got)
	}
}

func TestInferStaleIfaceIgnoresNonMasquerade(t *testing.T) {
	live := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "SNAT", "--to", "1.2.3.4"}},
	}
	if got := inferStaleIface(live, "eth1"); got != "" {
		t.Errorf("should only match MASQUERADE rules, got %q", got)
	}
}

// Regression: docker emits `-s 172.x.0.0/16 ! -o br-xxx -j MASQUERADE`.
// The earlier loose implementation matched any `-o X` anywhere in args and
// latched onto the bridge name, reporting a false inferredOld.
func TestInferStaleIfaceIgnoresDockerStyleMasquerade(t *testing.T) {
	live := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{
			"-s", "172.22.0.0/16", "!", "-o", "br-b760fe75fd7d", "-j", "MASQUERADE",
		}},
	}
	if got := inferStaleIface(live, "eth0"); got != "" {
		t.Errorf("docker-style MASQUERADE with source/negation must not infer as stale, got %q", got)
	}
}

// Regression: horizon-shape MASQUERADE pinned to an old iface must still
// infer correctly after the tightening above.
func TestInferStaleIfaceMatchesBareHorizonShape(t *testing.T) {
	live := []Rule{
		// Docker-style first (must be skipped):
		{Table: "nat", Chain: "POSTROUTING", Args: []string{
			"-s", "172.22.0.0/16", "!", "-o", "br-b760fe75fd7d", "-j", "MASQUERADE",
		}},
		// Horizon-style old rule (should be inferred):
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "wlp3s0", "-j", "MASQUERADE"}},
	}
	if got := inferStaleIface(live, "eth0"); got != "wlp3s0" {
		t.Errorf("want wlp3s0, got %q", got)
	}
}

// wgFwdRules builds a minimal canonical WG-FORWARD expected list for a peer
// pair: 3 rules per lan-access peer (VPN allow, LAN allow, peer DROP) plus
// the trailing catch-all DROP.
func wgFwdRules(peerIPs ...string) []Rule {
	out := make([]Rule, 0, 3*len(peerIPs)+1)
	for _, ip := range peerIPs {
		out = append(out,
			Rule{Table: "filter", Chain: ForwardChainName, Args: []string{"-s", ip + "/32", "-d", "10.100.0.0/24", "-j", "ACCEPT"}},
			Rule{Table: "filter", Chain: ForwardChainName, Args: []string{"-s", ip + "/32", "-d", "192.168.1.0/24", "-j", "ACCEPT"}},
			Rule{Table: "filter", Chain: ForwardChainName, Args: []string{"-s", ip + "/32", "-j", "DROP"}},
		)
	}
	out = append(out, Rule{Table: "filter", Chain: ForwardChainName, Args: []string{"-j", "DROP"}})
	return out
}

func TestWGForwardDriftedAcceptsCorrectOrder(t *testing.T) {
	rules := wgFwdRules("10.100.0.2")
	if wgForwardDrifted(rules, rules) {
		t.Errorf("identical sequences must not be flagged as drifted")
	}
}

// Regression for the user-reported incident: after `wg-quick down/up` wiped
// WG-FORWARD, the reconciler re-added all rules via `iptables -I 1` while
// iterating expected forward, producing the chain in REVERSE order. The
// catch-all DROP ended up at position 1, silently blocking every VPN packet.
// Set-membership comparison can't see this — order check must.
func TestWGForwardDriftedDetectsReversedOrder(t *testing.T) {
	expected := wgFwdRules("10.100.0.2", "10.100.0.3")
	live := make([]Rule, len(expected))
	for i, r := range expected {
		live[len(expected)-1-i] = r
	}
	if !wgForwardDrifted(live, expected) {
		t.Errorf("reversed-order chain must be flagged as drifted (this is the wg-quick down/up regression)")
	}
}

func TestWGForwardDriftedDetectsMissingRule(t *testing.T) {
	expected := wgFwdRules("10.100.0.2")
	live := expected[1:] // drop the first rule
	if !wgForwardDrifted(live, expected) {
		t.Errorf("missing rule must be flagged as drifted")
	}
}

func TestWGForwardDriftedDetectsExtraRule(t *testing.T) {
	expected := wgFwdRules("10.100.0.2")
	live := append([]Rule{}, expected...)
	live = append(live, Rule{Table: "filter", Chain: ForwardChainName,
		Args: []string{"-s", "10.100.0.99/32", "-j", "ACCEPT"}})
	if !wgForwardDrifted(live, expected) {
		t.Errorf("extra rule must be flagged as drifted")
	}
}

func TestWGForwardDriftedBothEmpty(t *testing.T) {
	if wgForwardDrifted(nil, nil) {
		t.Errorf("both-empty must not be flagged as drifted")
	}
}

func TestFilterChainExtractsOnlyMatchingChain(t *testing.T) {
	rules := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}},
		{Table: "filter", Chain: "FORWARD", Args: []string{"-i", "wg0", "-j", ForwardChainName}},
		{Table: "filter", Chain: ForwardChainName, Args: []string{"-s", "10.100.0.2/32", "-j", "DROP"}},
		{Table: "filter", Chain: ForwardChainName, Args: []string{"-j", "DROP"}},
	}
	got := filterChain(rules, ForwardChainName)
	if len(got) != 2 {
		t.Fatalf("want 2 WG-FORWARD rules, got %d: %v", len(got), got)
	}
	for _, r := range got {
		if r.Table != "filter" || r.Chain != ForwardChainName {
			t.Errorf("unexpected rule in filtered set: %v", r)
		}
	}
}
