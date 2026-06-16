package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestBuildZoneRecordSets(t *testing.T) {
	zone := config.Zone{
		Name:   "example.com",
		ZoneID: "Z123",
		Records: []config.DNSRecord{
			{Name: "app.example.com", Type: "txt", Value: "google-site-verification=abc"},
			{Name: "app.example.com", Type: "TXT", Value: "v=spf1 -all", TTL: 600},
			{Name: "alias.example.com", Type: "CNAME", Value: "target.example.com"},
		},
	}

	sets, errs := buildZoneRecordSets(zone)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(sets) != 2 {
		t.Fatalf("want 2 record sets, got %d", len(sets))
	}

	// First set: both TXT values for app.example.com, grouped, type normalized.
	txt := sets[0].Records
	if len(txt) != 2 {
		t.Fatalf("want 2 TXT values grouped, got %d", len(txt))
	}
	for _, r := range txt {
		if r.Type != "TXT" {
			t.Errorf("type not normalized: %q", r.Type)
		}
		if r.ZoneID != "Z123" {
			t.Errorf("zone id not propagated: %q", r.ZoneID)
		}
	}
	if txt[0].TTL != 300 {
		t.Errorf("want default TTL 300, got %d", txt[0].TTL)
	}
	if txt[1].TTL != 600 {
		t.Errorf("want explicit TTL 600, got %d", txt[1].TTL)
	}

	// Second set: the CNAME.
	if got := sets[1].Records[0].Type; got != "CNAME" {
		t.Errorf("want CNAME set, got %q", got)
	}
}

func TestBuildZoneRecordSetsValidation(t *testing.T) {
	zone := config.Zone{
		Name:   "example.com",
		ZoneID: "Z123",
		Records: []config.DNSRecord{
			{Name: "", Type: "TXT", Value: "x"},                 // missing name
			{Name: "ok.example.com", Type: "", Value: "x"},      // missing type
			{Name: "ok.example.com", Type: "TXT", Value: ""},    // missing value
			{Name: "ok.example.com", Type: "TXT", Value: "good"}, // valid
		},
	}

	sets, errs := buildZoneRecordSets(zone)
	if len(errs) != 3 {
		t.Fatalf("want 3 validation errors, got %d (%v)", len(errs), errs)
	}
	if len(sets) != 1 {
		t.Fatalf("want 1 valid record set, got %d", len(sets))
	}
	if sets[0].Records[0].Value != "good" {
		t.Errorf("unexpected surviving record: %+v", sets[0].Records[0])
	}
}
