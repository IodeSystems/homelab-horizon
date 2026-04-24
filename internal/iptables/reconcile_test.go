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
