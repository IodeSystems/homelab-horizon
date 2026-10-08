package config

import (
	"strings"
	"testing"
	"time"
)

// liveChildConfig is the nested hz of 2026-10-08: a service with external_dns
// on a name in a zone with no dns_provider. The parent publishes that name for
// it (ParentDNSAsks); the child's own sync tried to as well, and failed every
// cycle.
func liveChildConfig() *Config {
	return &Config{
		PublicIP:            "192.0.2.35",
		PublicIPLastChecked: time.Now().Unix(),
		Upstream:            &UpstreamHZ{URL: "http://10.100.0.1:8080", TokenFile: "/x", Instance: "loadtest", Domain: "loadtest.example.com"},
		Zones:               []Zone{{Name: "loadtest.redline.example.com"}},
		Services: []Service{{
			Name:        "redline-loadtest",
			Domains:     []string{"loadtest.redline.example.com"},
			ExternalDNS: &ExternalDNS{},
		}},
	}
}

func TestNestedHzDoesNotPublishWhatItAsksTheParentFor(t *testing.T) {
	c := liveChildConfig()

	asks, unasked := c.ParentDNSAsks()
	if unasked != "" || len(asks) != 1 || asks[0].Name != "loadtest.redline.example.com" {
		t.Fatalf("precondition: the child must ask the parent for the name; asks=%v unasked=%q", asks, unasked)
	}

	for _, zoneID := range []string{"", "Z1"} { // a legacy zone_id does not make the child the publisher
		c.Zones[0].ZoneID = zoneID
		records, skipped := c.DeriveRoute53Plan()
		if len(records) != 0 {
			t.Errorf("zone_id %q: the child would write %v itself; the parent publishes it", zoneID, records)
		}
		if len(skipped) != 1 || !skipped[0].Delegated || skipped[0].Name != "loadtest.redline.example.com" || skipped[0].Reason == "" {
			t.Errorf("zone_id %q: skipped = %+v, want the name skipped as delegated, with a reason", zoneID, skipped)
		}
	}
}

// A parent's legacy zone (zone_id, no dns_provider) is still written by the
// AWS CLI with ambient credentials; only a zone with neither is skipped.
func TestParentLegacyZoneIDStillPublishes(t *testing.T) {
	c := liveChildConfig()
	c.Upstream = nil
	c.Zones[0].ZoneID = "Z1"
	records, skipped := c.DeriveRoute53Plan()
	if len(records) != 1 || len(skipped) != 0 {
		t.Errorf("records=%v skipped=%+v, want the legacy zone published", records, skipped)
	}
}

// A nested hz still publishes a name in a zone it has a provider for: that
// name is not asked of the parent, so nobody else would.
func TestNestedHzStillPublishesItsOwnProviderZone(t *testing.T) {
	c := liveChildConfig()
	c.Zones[0].DNSProvider = &DNSProviderConfig{Type: DNSProviderRoute53, AWSProfile: "p"}
	if asks, _ := c.ParentDNSAsks(); len(asks) != 0 {
		t.Fatalf("precondition: nothing asked of the parent, got %v", asks)
	}
	records, skipped := c.DeriveRoute53Plan()
	if len(records) != 1 || len(skipped) != 0 {
		t.Errorf("records=%v skipped=%+v, want the name published here", records, skipped)
	}
}

// A parent has nobody to hand the name to. A provider-less zone is not
// attempted, and is reported, not delegated, with a reason that names the zone.
func TestParentProviderlessZoneIsReportedWithAReason(t *testing.T) {
	c := liveChildConfig()
	c.Upstream = nil

	records, skipped := c.DeriveRoute53Plan()
	if len(records) != 0 {
		t.Errorf("records = %v, want none: the zone has no provider", records)
	}
	if len(skipped) != 1 || skipped[0].Delegated {
		t.Fatalf("skipped = %+v, want one, not delegated", skipped)
	}
	if !strings.Contains(skipped[0].Reason, "loadtest.redline.example.com") || !strings.Contains(skipped[0].Reason, "no dns_provider and no zone_id") {
		t.Errorf("reason = %q, want it to name the zone and the missing dns_provider", skipped[0].Reason)
	}
}

// The VPN-only UI name is never asked of the parent, so skipping it as
// "delegated" would be false.
func TestNestedHzUINameIsNotCalledDelegated(t *testing.T) {
	c := liveChildConfig()
	c.Zones = []Zone{{Name: "loadtest.example.com"}}
	c.Services[0].Domains = []string{"hz.loadtest.example.com"}

	_, skipped := c.DeriveRoute53Plan()
	if len(skipped) != 1 || skipped[0].Delegated || skipped[0].Reason == "" {
		t.Errorf("skipped = %+v, want one, not delegated, with a reason", skipped)
	}
}
