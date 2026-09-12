package config

import "testing"

func TestPinnedIPWarnings(t *testing.T) {
	cfg := &Config{
		PublicIP: "97.113.72.46",
		Services: []Service{
			// Tracks the host automatically — the shape that keeps working.
			{Name: "auto", Domains: []string{"auto.example.com"},
				ExternalDNS: &ExternalDNS{}},
			// Pinned to a former address of this connection. The production bug.
			{Name: "stale", Domains: []string{"stale.example.com"},
				ExternalDNS: &ExternalDNS{IPs: []string{"97.113.67.44"}}},
			// Pinned to the current address: works today, breaks on renumber.
			{Name: "redundant", Domains: []string{"redundant.example.com"},
				ExternalDNS: &ExternalDNS{IPs: []string{"97.113.72.46"}}},
			// A deliberate multi-address record including this host.
			{Name: "roundrobin", Domains: []string{"rr.example.com"},
				ExternalDNS: &ExternalDNS{IPs: []string{"97.113.72.46", "198.51.100.7"}}},
			// No external DNS at all.
			{Name: "none", Domains: []string{"none.example.com"}},
			// The legacy single-IP field must be judged too.
			{Name: "legacy", Domains: []string{"legacy.example.com"},
				ExternalDNS: &ExternalDNS{IP: "203.0.113.9"}},
		},
	}

	got := map[string]PinnedIPWarning{}
	for _, w := range cfg.PinnedIPWarnings() {
		got[w.Service] = w
	}

	if _, ok := got["auto"]; ok {
		t.Error("a service tracking the host's IP must not warn")
	}
	if _, ok := got["none"]; ok {
		t.Error("a service with no external DNS must not warn")
	}
	if _, ok := got["roundrobin"]; ok {
		t.Error("a multi-address record including this host is deliberate, not a mistake")
	}

	stale, ok := got["stale"]
	if !ok {
		t.Fatal("a pin to an address this host does not hold must warn")
	}
	if stale.Redundant {
		t.Error("a pin to a different address is not redundant")
	}
	if stale.HostIP != "97.113.72.46" {
		t.Errorf("HostIP = %q, want the host's address so the message can name it", stale.HostIP)
	}

	red, ok := got["redundant"]
	if !ok {
		t.Fatal("a pin to the current address should still be surfaced — it breaks on renumber")
	}
	if !red.Redundant {
		t.Error("a pin matching this host should be marked redundant, not broken")
	}

	if _, ok := got["legacy"]; !ok {
		t.Error("the deprecated single-IP field must be judged too")
	}
}

// With no public IP there is nothing to compare against, so every pin would
// look wrong. Silence beats noise.
func TestPinnedIPWarningsNeedAHostIP(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{Name: "pinned", ExternalDNS: &ExternalDNS{IPs: []string{"203.0.113.9"}}},
		},
	}
	if got := cfg.PinnedIPWarnings(); len(got) != 0 {
		t.Fatalf("expected no advice without a host IP, got %+v", got)
	}
}
