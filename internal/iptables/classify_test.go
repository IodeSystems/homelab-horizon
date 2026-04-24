package iptables

import (
	"testing"
)

func TestClassifyExpectedTakesPrecedence(t *testing.T) {
	live := []Rule{
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}},
	}
	expected := live
	stale := []Rule{}
	blessed := []string{live[0].Canonical()} // also blessed

	got := Classify(live, expected, stale, blessed)
	if got[0].State != StateExpected {
		t.Errorf("expected > blessed precedence broken: got %q", got[0].State)
	}
}

func TestClassifyStaleAndUnknown(t *testing.T) {
	live := []Rule{
		// Matches stale set:
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}},
		// Nothing matches this:
		{Table: "filter", Chain: "FORWARD", Args: []string{"-s", "10.99.99.99/32", "-j", "ACCEPT"}},
	}
	stale := []Rule{live[0]}
	got := Classify(live, nil, stale, nil)

	if got[0].State != StateStale {
		t.Errorf("expected stale, got %q", got[0].State)
	}
	if got[1].State != StateUnknown {
		t.Errorf("expected unknown, got %q", got[1].State)
	}
}

func TestClassifyBlessed(t *testing.T) {
	live := []Rule{
		{Table: "filter", Chain: "FORWARD", Args: []string{"-i", "br0", "-j", "ACCEPT"}},
	}
	blessed := []string{live[0].Canonical()}
	got := Classify(live, nil, nil, blessed)
	if got[0].State != StateBlessed {
		t.Errorf("expected blessed, got %q", got[0].State)
	}
}

func TestSummarize(t *testing.T) {
	classified := []ClassifiedRule{
		{State: StateExpected},
		{State: StateExpected},
		{State: StateStale},
		{State: StateBlessed},
		{State: StateUnknown},
		{State: StateUnknown},
		{State: StateUnknown},
	}
	s := SummarizeClassified(classified)
	if s.Expected != 2 || s.Stale != 1 || s.Blessed != 1 || s.Unknown != 3 {
		t.Errorf("summary mismatch: %+v", s)
	}
}

func TestParseIptablesSave(t *testing.T) {
	// Minimal iptables-save-style output with mixed chains.
	sample := `# Generated
*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
-A POSTROUTING -o eth0 -j MASQUERADE
-A POSTROUTING -s 10.50.0.0/24 -j MASQUERADE
-A OUTPUT -j ACCEPT
COMMIT
`
	got := parseIptablesSave(sample, "nat", []string{"POSTROUTING"})
	if len(got) != 2 {
		t.Fatalf("want 2 POSTROUTING rules, got %d: %v", len(got), got)
	}
	if got[0].Canonical() != "nat|POSTROUTING|-o eth0 -j MASQUERADE" {
		t.Errorf("first rule wrong: %q", got[0].Canonical())
	}
	if got[1].Canonical() != "nat|POSTROUTING|-s 10.50.0.0/24 -j MASQUERADE" {
		t.Errorf("second rule wrong: %q", got[1].Canonical())
	}
}

func TestParseIptablesSaveFilterWithWGForward(t *testing.T) {
	sample := `*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:WG-FORWARD - [0:0]
-A FORWARD -i wg0 -j WG-FORWARD
-A WG-FORWARD -s 10.100.0.42/32 -d 192.168.1.0/24 -j ACCEPT
-A WG-FORWARD -j DROP
-A INPUT -j ACCEPT
COMMIT
`
	got := parseIptablesSave(sample, "filter", []string{"FORWARD", "WG-FORWARD"})
	if len(got) != 3 {
		t.Fatalf("want 3 rules, got %d: %v", len(got), got)
	}
	// INPUT rule should NOT appear.
	for _, r := range got {
		if r.Chain == "INPUT" {
			t.Errorf("INPUT rule leaked into result: %v", r)
		}
	}
}
