package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
)

// The delegation the operator decided on 2026-10-01: the office hz owns
// iodesystems.com and publishes only the NS for the redline prod hz's zone.
var redlineNS = []DNSRecord{
	{Name: "loadtest.redline", Type: "NS", Value: "ns-1.awsdns-01.org."},
	{Name: "loadtest.redline.iodesystems.com", Type: "NS", Value: "ns-2.awsdns-02.com"},
}

func withRecords(extra ...DNSRecord) []DNSRecord {
	return append(append([]DNSRecord(nil), redlineNS...), extra...)
}

func TestValidateRecordsNS(t *testing.T) {
	cases := []struct {
		name    string
		records []DNSRecord
		want    string // "" = valid
	}{
		{"delegation of two nameservers", withRecords(), ""},
		{"one nameserver is a set", redlineNS[:1], ""},
		{"a record at the parent name is still ours", withRecords(
			DNSRecord{Name: "redline", Type: "A", Value: "192.0.2.1"}), ""},
		{"a sibling is still ours", withRecords(
			DNSRecord{Name: "other.redline", Type: "TXT", Value: "x"}), ""},
		{"apex by @", []DNSRecord{{Name: "@", Type: "NS", Value: "ns.example.net"}}, "zone apex"},
		{"apex by zone name", []DNSRecord{{Name: "iodesystems.com.", Type: "NS", Value: "ns.example.net"}}, "zone apex"},
		{"another type at the delegation point", withRecords(
			DNSRecord{Name: "loadtest.redline", Type: "TXT", Value: "x"}), "holds only NS"},
		{"a record below the delegation", withRecords(
			DNSRecord{Name: "api.loadtest.redline.iodesystems.com", Type: "A", Value: "192.0.2.1"}), "below a delegated name"},
		{"an NS below the delegation", withRecords(
			DNSRecord{Name: "deeper.loadtest.redline", Type: "NS", Value: "ns.example.net"}), "below a delegated name"},
		{"nameserver not a hostname", []DNSRecord{{Name: "sub", Type: "NS", Value: "192.0.2.1:53/x"}}, "not a hostname"},
		{"nameserver needing glue", []DNSRecord{{Name: "sub", Type: "NS", Value: "ns1.sub.iodesystems.com"}}, "glue"},
		{"duplicate nameserver (dot-insensitive)", withRecords(
			DNSRecord{Name: "loadtest.redline", Type: "NS", Value: "ns-1.awsdns-01.org"}), "twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z := Zone{Name: "iodesystems.com", Records: tc.records}
			err := z.ValidateRecords()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// Every refusal names the delegation and where its nameservers are, so the
// operator can tell which hz owns the name.
func TestDelegationRefusalsNameTheDelegation(t *testing.T) {
	z := Zone{Name: "iodesystems.com", Records: withRecords(
		DNSRecord{Name: "x.loadtest.redline", Type: "TXT", Value: "x"})}
	err := z.ValidateRecords()
	if err == nil {
		t.Fatal("record below a delegation accepted")
	}
	for _, want := range []string{"loadtest.redline.iodesystems.com is delegated to", "ns-1.awsdns-01.org", "ns-2.awsdns-02.com", "belong to that zone's owner"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestDelegations(t *testing.T) {
	z := Zone{Name: "iodesystems.com", Records: withRecords(
		DNSRecord{Name: "a", Type: "NS", Value: "ns.example.net"},
		DNSRecord{Name: "a", Type: "TXT", Value: "ignored"},
	)}
	got := z.Delegations()
	if len(got) != 2 || got[0].Name != "a.iodesystems.com" || got[1].Name != "loadtest.redline.iodesystems.com" {
		t.Fatalf("delegations = %+v", got)
	}
	if ns := got[1].NameServers; len(ns) != 2 || ns[0] != "ns-1.awsdns-01.org" || ns[1] != "ns-2.awsdns-02.com" {
		t.Fatalf("nameservers = %v, want canonical, declared order", ns)
	}
	for name, want := range map[string]bool{
		"loadtest.redline.iodesystems.com":     true,
		"LOADTEST.redline.iodesystems.com.":    true,
		"api.loadtest.redline.iodesystems.com": true,
		"redline.iodesystems.com":              false,
		"xloadtest.redline.iodesystems.com":    false,
	} {
		if _, ok := z.DelegationCovering(name); ok != want {
			t.Errorf("DelegationCovering(%s) = %v, want %v", name, ok, want)
		}
	}
}

func delegatedConfig() *Config {
	return &Config{
		Zones: []Zone{{Name: "iodesystems.com", ZoneID: "Z1", Records: withRecords(),
			SubZones: []string{"", "*", "redline", "*.redline"}}},
		Services: []Service{{Name: "redline-staging", Domains: []string{"redline.iodesystems.com"}}},
	}
}

// The live office config's shape — a service at the parent of the delegation
// and the parent's own certificate names — validates with the delegation
// declared and without it.
func TestExistingShapeStillValidates(t *testing.T) {
	c := delegatedConfig()
	if err := c.ValidateDelegations(); err != nil {
		t.Fatalf("with delegation: %v", err)
	}
	c.Zones[0].Records = nil
	if err := c.ValidateDelegations(); err != nil {
		t.Fatalf("without delegation: %v", err)
	}
	if err := Save(filepath.Join(t.TempDir(), "c.json"), delegatedConfig()); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func TestServiceDomainBelowDelegationRefused(t *testing.T) {
	for _, domain := range []string{
		"loadtest.redline.iodesystems.com",
		"api.loadtest.redline.iodesystems.com",
		"*.loadtest.redline.iodesystems.com",
	} {
		t.Run(domain, func(t *testing.T) {
			c := delegatedConfig()
			svc := Service{Name: "rogue", Domains: []string{domain}}
			err := c.ValidateService(&svc)
			if err == nil || !strings.Contains(err.Error(), "loadtest.redline.iodesystems.com is delegated to") {
				t.Fatalf("ValidateService err = %v", err)
			}
			c.Services = append(c.Services, svc)
			err = Save(filepath.Join(t.TempDir(), "c.json"), c)
			if err == nil || !strings.Contains(err.Error(), "service rogue cannot use domain") {
				t.Fatalf("Save err = %v", err)
			}
		})
	}
}

// One hz may own both the parent and the delegated zone. A name below the
// delegation then belongs to the child zone, which delegates nothing.
func TestServiceInConfiguredChildZoneAllowed(t *testing.T) {
	c := delegatedConfig()
	c.Zones = append(c.Zones, Zone{Name: "loadtest.redline.iodesystems.com", ZoneID: "Z2"})
	c.Services = append(c.Services, Service{Name: "child", Domains: []string{"api.loadtest.redline.iodesystems.com"}})
	if err := c.ValidateDelegations(); err != nil {
		t.Fatalf("service in the child zone refused: %v", err)
	}
}

func TestSubZoneBelowDelegationRefused(t *testing.T) {
	for _, sub := range []string{"loadtest.redline", "*.loadtest.redline", "x.loadtest.redline"} {
		t.Run(sub, func(t *testing.T) {
			c := delegatedConfig()
			c.Zones[0].SubZones = append(c.Zones[0].SubZones, sub)
			err := c.ValidateDelegations()
			if err == nil || !strings.Contains(err.Error(), "DNS-01 challenge") ||
				!strings.Contains(err.Error(), "is delegated to") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDeriveDNSRecordsForwardsDelegation(t *testing.T) {
	c := delegatedConfig()
	var fwd []string
	for _, r := range c.DeriveDNSRecords() {
		if r.ForwardUpstream {
			fwd = append(fwd, r.Name)
		}
	}
	if len(fwd) != 1 || fwd[0] != "loadtest.redline.iodesystems.com" {
		t.Fatalf("forwarded = %v", fwd)
	}
	// Config to bytes: the line dnsmasq reads, beside the parent's wildcard.
	hosts := dnsmasq.RenderHosts(dnsmasq.HostsInput{Records: c.DeriveDNSRecords()})
	if !strings.Contains(hosts, "server=/loadtest.redline.iodesystems.com/#\n") {
		t.Fatalf("rendered hosts lack the exclusion:\n%s", hosts)
	}

	c.Zones[0].Records = nil
	for _, r := range c.DeriveDNSRecords() {
		if r.ForwardUpstream {
			t.Fatalf("no delegation, but forwarded %s", r.Name)
		}
	}
	if hosts := dnsmasq.RenderHosts(dnsmasq.HostsInput{Records: c.DeriveDNSRecords()}); strings.Contains(hosts, "server=") {
		t.Fatalf("no delegation, but a server= line:\n%s", hosts)
	}
}

// Apex NS stays the provider's and is never inventory. A hand-made delegation
// below the apex is someone else's and is ingested; one hz declared is hz's.
func TestShouldIngestNS(t *testing.T) {
	c := delegatedConfig()
	if c.ShouldIngest("iodesystems.com", "iodesystems.com", "NS") {
		t.Error("apex NS ingested")
	}
	if c.ShouldIngest("iodesystems.com", "iodesystems.com", "SOA") {
		t.Error("SOA ingested")
	}
	if c.ShouldIngest("iodesystems.com", "loadtest.redline.iodesystems.com", "NS") {
		t.Error("hz's own declared NS ingested as observed")
	}
	if !c.ShouldIngest("iodesystems.com", "byhand.iodesystems.com", "NS") {
		t.Error("a hand-made delegation was hidden from the inventory")
	}
}
