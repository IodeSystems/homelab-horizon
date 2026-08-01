package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

func tombstoneZone() config.Zone {
	return config.Zone{Name: "example.com", ZoneID: "z1"}
}

func liveValues(fp *fakeProvider, name, recType string) []string {
	var out []string
	recs, _ := fp.ListRecords("z1")
	for _, r := range recs {
		if r.Name == name && r.Type == recType {
			out = append(out, r.Value)
		}
	}
	return out
}

// The core promise: a tombstoned record is actually removed at the provider,
// and the tombstone is then forgotten.
func TestTombstoneRetractsAndClears(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "gone.example.com", Type: "A", Value: "198.51.100.10"},
	}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "gone.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"},
	}}

	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil || failed != 0 || retracted != 1 {
		t.Fatalf("retracted=%d failed=%d err=%v", retracted, failed, err)
	}
	if got := liveValues(fp, "gone.example.com", "A"); len(got) != 0 {
		t.Errorf("record still live at provider: %v", got)
	}
	if got := s.cfg().Zones[0].Tombstones; len(got) != 0 {
		t.Errorf("tombstone not cleared after confirmation: %v", got)
	}
}

// A round-robin set is one record per value. Retracting must take only the
// tombstoned value — the old delete-by-(name,type) would have removed both.
func TestTombstoneLeavesSiblingValues(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "rr.example.com", Type: "A", Value: "198.51.100.10"},
	}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "rr.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"},
		{Name: "rr.example.com", Type: "A", Value: "198.51.100.11", ZoneID: "z1"},
	}}

	if _, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0]); err != nil || failed != 0 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	got := liveValues(fp, "rr.example.com", "A")
	if len(got) != 1 || got[0] != "198.51.100.11" {
		t.Errorf("live = %v, want only the untombstoned sibling [198.51.100.11]", got)
	}
}

// Already gone counts as satisfied — a hand-deleted record, or a retraction
// that succeeded and died before clearing its tombstone, must not wedge sync.
func TestTombstoneAlreadyGoneIsCleared(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "vanished.example.com", Type: "A", Value: "198.51.100.10"},
	}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{} // nothing live

	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil || failed != 0 || retracted != 1 {
		t.Fatalf("retracted=%d failed=%d err=%v", retracted, failed, err)
	}
	if got := s.cfg().Zones[0].Tombstones; len(got) != 0 {
		t.Errorf("tombstone survived a confirmed-absent record: %v", got)
	}
}

// Re-declaring a record is a later intent than the pending deletion. Without
// the interlock, publish and retract fight and the record flaps every sync.
func TestTombstoneSupersededByLiveDeclaration(t *testing.T) {
	cfg := &config.Config{
		Zones: []config.Zone{tombstoneZone()},
		Services: []config.Service{{
			Name:        "back",
			Domains:     []string{"back.example.com"},
			ExternalDNS: &config.ExternalDNS{IPs: []string{"198.51.100.10"}},
		}},
	}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "back.example.com", Type: "A", Value: "198.51.100.10"},
	}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "back.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"},
	}}

	if _, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0]); err != nil || failed != 0 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	if got := liveValues(fp, "back.example.com", "A"); len(got) != 1 {
		t.Errorf("live = %v, want the re-declared record left alone", got)
	}
	if got := s.cfg().Zones[0].Tombstones; len(got) != 0 {
		t.Errorf("superseded tombstone should be dropped, got: %v", got)
	}
}

// Ingest is the other half: adopt what hz doesn't own so it's visible, but
// never adopt hz's own output, and never touch delegation records.
func TestIngestAdoptsForeignRecordsOnly(t *testing.T) {
	cfg := &config.Config{
		Zones: []config.Zone{tombstoneZone()},
		Services: []config.Service{{
			Name:        "mine",
			Domains:     []string{"mine.example.com"},
			ExternalDNS: &config.ExternalDNS{IPs: []string{"198.51.100.10"}},
		}},
	}
	cfg.Zones[0].Records = []config.DNSRecord{
		{Name: "example.com", Type: "TXT", Value: "v=spf1 -all"},
	}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "leaving.example.com", Type: "A", Value: "198.51.100.99"},
	}
	s := newTestServer(t, cfg)

	fp := &fakeProvider{records: []dns.Record{
		{Name: "mine.example.com", Type: "A", Value: "198.51.100.10"},    // derived — hz owns
		{Name: "example.com", Type: "TXT", Value: "v=spf1 -all"},         // declared — hz owns
		{Name: "leaving.example.com", Type: "A", Value: "198.51.100.99"}, // retracting
		{Name: "example.com", Type: "NS", Value: "ns-1.awsdns.com"},      // delegation
		{Name: "example.com", Type: "SOA", Value: "ns-1.awsdns.com ..."}, // delegation
		{Name: "example.com", Type: "MX", Value: "10 mail.example.com"},  // foreign
		{Name: "shop.example.com", Type: "CNAME", Value: "x.shopify.com"},
	}}

	got, err := s.ingestZoneRecords(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, r := range got {
		seen[r.Type+" "+r.Name] = true
	}
	for _, want := range []string{"MX example.com", "CNAME shop.example.com"} {
		if !seen[want] {
			t.Errorf("expected to ingest %q, got %v", want, seen)
		}
	}
	for _, unwanted := range []string{
		"A mine.example.com",    // hz derives it
		"TXT example.com",       // hz declares it
		"A leaving.example.com", // pending retraction, not foreign
		"NS example.com",        // never
		"SOA example.com",       // never
	} {
		if seen[unwanted] {
			t.Errorf("must not ingest %q", unwanted)
		}
	}
}
