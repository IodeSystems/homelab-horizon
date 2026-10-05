package config

import (
	"strings"
	"testing"
)

// The range a parent proposes for a child's human VPN never overlaps the
// parent's own VPN range (where the child's hz-upstream address lives — the
// overlap that broke the first bootstrap), its LAN, or a fleet site's range.
func TestProposeChildVPNRange(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		avoid        []string
		want         string
	}{
		{"the next /24 after the parent's", "10.100.0.0/24", nil, "10.101.0.0/24"},
		{"skips the LAN", "10.100.0.0/24", []string{"10.101.0.0/16"}, "10.102.0.0/24"},
		{"skips a fleet site and an upstream address", "10.100.0.0/24", []string{"10.101.0.0/24", "10.102.0.9"}, "10.103.0.0/24"},
		{"a wide parent range is avoided whole", "10.100.0.0/14", nil, "10.104.0.0/24"},
		{"wraps past 10.255", "10.255.0.0/24", nil, "10.1.0.0/24"},
		{"a parent outside 10/8 starts at 10.100", "192.168.50.0/24", nil, "10.100.0.0/24"},
	} {
		got, err := ProposeChildVPNRange(tc.parent, tc.avoid)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		// The property first, then the exact answer: a proposal that
		// overlaps fails here whatever the expected value says.
		for _, a := range append([]string{tc.parent}, tc.avoid...) {
			if CheckChildVPNRange(got, []string{a}) != nil {
				t.Errorf("%s: OVERLAP — %s overlaps %s", tc.name, got, a)
			}
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %s", tc.name, got, tc.want)
		}
	}
	// Deterministic: the same parent proposes the same range, so a re-run keeps the box's wg0.
	a, _ := ProposeChildVPNRange("10.100.0.0/24", []string{"192.168.1.0/24"})
	b, _ := ProposeChildVPNRange("10.100.0.0/24", []string{"192.168.1.0/24"})
	if a != b {
		t.Fatalf("two proposals differ: %s %s", a, b)
	}
	if _, err := ProposeChildVPNRange("10.100.0.0/24", []string{"10.0.0.0/8"}); err == nil || !strings.Contains(err.Error(), "explicitly") {
		t.Fatalf("all of 10/8 taken: %v", err)
	}
}

// An explicit range obeys the same rules, and each refusal names its cause.
func TestCheckChildVPNRange(t *testing.T) {
	avoid := []string{"10.100.0.0/24", "192.168.1.0/24"}
	for r, want := range map[string]string{
		"10.101.0.0/24":  "",
		"10.100.0.0/24":  "overlaps 10.100.0.0/24",
		"10.100.0.0/16":  "overlaps 10.100.0.0/24",
		"192.168.1.0/25": "overlaps 192.168.1.0/24",
		"10.101.0.1/24":  "not a network address",
		"10.0.0.0/8":     "/16 to /29",
		"fd00::/64":      "not an IPv4 CIDR",
		"nonsense":       "not an IPv4 CIDR",
	} {
		err := CheckChildVPNRange(r, avoid)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: refused: %v", r, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", r, err, want)
		}
	}
}

// The kiosk name joins the instance's TLS domains and its DNS grant, and every
// rule of both holds for it: a covering grant entry is not duplicated (an
// exact entry beside a covering "*." one is an overlap), and a name this hz
// cannot issue or publish is refused with the rule.
func TestAddInstanceKiosk(t *testing.T) {
	c := dnsFixture()
	changed, err := c.AddInstanceKiosk("child", "kiosk.hz.loadtest.example.com")
	if err != nil || !changed {
		t.Fatalf("kiosk in the zone, under the grant: %v %v", changed, err)
	}
	h, _ := c.FindHZInstance("child")
	if h.Domains[len(h.Domains)-1] != "kiosk.hz.loadtest.example.com" || len(h.DNSGrant) != 2 {
		t.Fatalf("domains %v grant %v — the covering *.hz.loadtest grant must not gain an exact twin", h.Domains, h.DNSGrant)
	}
	if again, err := c.AddInstanceKiosk("child", "KIOSK.hz.loadtest.example.com."); err != nil || again {
		t.Fatalf("a second add of the same name: %v %v", again, err)
	}

	// No covering entry: the grant gains exactly the kiosk name.
	c = dnsFixture()
	if _, err := c.AddInstanceKiosk("child", "kiosk.loadtest.redline.example.com"); err != nil {
		t.Fatal(err)
	}
	h, _ = c.FindHZInstance("child")
	if !GrantCovers(h.DNSGrant, "kiosk.loadtest.redline.example.com") || len(h.DNSGrant) != 3 {
		t.Fatalf("grant %v", h.DNSGrant)
	}
	if err := c.ValidateInstanceDomains(); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateInstanceDNS(); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"kiosk.example.invalid":   "outside every zone",
		"kiosk.example.net":       "no dns_provider",
		"kiosk.example.org":       "different zones",
		"redline.example.com":     "also a domain of service redline",
		"*.hz.loadtest.example":   "no wildcard",
		"Bad_Name.example.com":    "not a lower-case hostname",
		"kiosk.deleg.example.com": "delegated",
	} {
		c := dnsFixture()
		c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "deleg", Type: "NS", Value: "ns1.example.org"})
		before, _ := c.FindHZInstance("child")
		if _, err := c.AddInstanceKiosk("child", name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
		after, _ := c.FindHZInstance("child")
		if strings.Join(after.Domains, ",") != strings.Join(before.Domains, ",") || strings.Join(after.DNSGrant, ",") != strings.Join(before.DNSGrant, ",") {
			t.Errorf("%s: a refused kiosk changed the instance: %v %v", name, after.Domains, after.DNSGrant)
		}
	}
}
