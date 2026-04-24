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
