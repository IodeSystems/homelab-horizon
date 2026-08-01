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

// A name hz has never published, already holding someone else's value, must not
// be silently overwritten just because no baseline exists. Adding a service for
// a domain already pointed somewhere else is a conflict, not a first publish.
func TestFirstPublishOverForeignRecordBlocks(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "shop.example.com", Type: "A", Value: "203.0.113.9", ZoneID: "z1"},
	}}
	desired := []dns.Record{{Name: "shop.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"}}

	changed, err := s.newDNSSyncRun().publish(fp, s.cfg().Zones[0], "shop.example.com", "A", desired)
	if err == nil {
		t.Fatal("expected the takeover to be refused")
	}
	if changed {
		t.Error("nothing should have been written")
	}
	if got := liveValues(fp, "shop.example.com", "A"); len(got) != 1 || got[0] != "203.0.113.9" {
		t.Errorf("foreign record was modified: %v", got)
	}
	if !s.dnsSyncBlocked() {
		t.Error("a takeover must halt sync like any other conflict")
	}
	if d := s.cfg().DNSDriftDetail; d == nil || d.Reason != config.DNSConflictTakeover {
		t.Errorf("reason = %+v, want %q so the notice can say which conflict it is",
			d, config.DNSConflictTakeover)
	}
}

// Clearing the block adopts the name: the baseline was synced back to live, so
// the next publish is an ordinary intentional change.
func TestClearedTakeoverThenPublishes(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "shop.example.com", Type: "A", Value: "203.0.113.9", ZoneID: "z1"},
	}}
	desired := []dns.Record{{Name: "shop.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"}}

	_, _ = s.newDNSSyncRun().publish(fp, s.cfg().Zones[0], "shop.example.com", "A", desired)
	if err := s.updateConfig(func(c *config.Config) {
		c.DNSDriftBlocked = false
		c.DNSDriftDetail = nil
	}); err != nil {
		t.Fatal(err)
	}

	changed, err := s.newDNSSyncRun().publish(fp, s.cfg().Zones[0], "shop.example.com", "A", desired)
	if err != nil || !changed {
		t.Fatalf("after clearing: changed=%v err=%v", changed, err)
	}
	if got := liveValues(fp, "shop.example.com", "A"); len(got) != 1 || got[0] != "198.51.100.10" {
		t.Errorf("live = %v, want the adopted name overwritten", got)
	}
}

// An unclaimed name that already holds exactly what hz wants is adopted in
// silence — there is nothing to conflict over.
func TestFirstPublishMatchingValueIsAdoptedSilently(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	s := newTestServer(t, cfg)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "same.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"},
	}}
	desired := []dns.Record{{Name: "same.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"}}

	changed, err := s.newDNSSyncRun().publish(fp, s.cfg().Zones[0], "same.example.com", "A", desired)
	if err != nil {
		t.Fatalf("adopting a matching record must not block: %v", err)
	}
	if changed {
		t.Error("no write was needed")
	}
	if s.dnsSyncBlocked() {
		t.Error("must not block when live already equals desired")
	}
}

// Cancelling withdraws the intent without restoring anything: the record stays
// live, and hz stops claiming it.
func TestCancelTombstoneStopsRetraction(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "keep.example.com", Type: "A", Value: "198.51.100.10"},
	}
	s := newTestServer(t, cfg)

	if err := s.updateConfig(func(c *config.Config) {
		c.DropTombstone("example.com", "keep.example.com", "A", "198.51.100.10")
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.cfg().Zones[0].Tombstones; len(got) != 0 {
		t.Fatalf("tombstone not withdrawn: %v", got)
	}

	// With the intent gone, a sync must leave the record alone.
	fp := &fakeProvider{records: []dns.Record{
		{Name: "keep.example.com", Type: "A", Value: "198.51.100.10", ZoneID: "z1"},
	}}
	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil || failed != 0 || retracted != 0 {
		t.Fatalf("retracted=%d failed=%d err=%v", retracted, failed, err)
	}
	if got := liveValues(fp, "keep.example.com", "A"); len(got) != 1 {
		t.Errorf("record was removed despite the cancellation: %v", got)
	}
}

// A cancelled record is no longer hz's: the delete already dropped it from
// Zone.Records, so it reclassifies as observed rather than lingering as owned.
func TestCancelledRecordReclassifiesAsObserved(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	s := newTestServer(t, cfg)
	if got := s.cfg().ClassifyRecord("example.com", "orphan.example.com", "A", "198.51.100.10"); got != config.RecordOwnerObserved {
		t.Errorf("owner = %q, want %q", got, config.RecordOwnerObserved)
	}
}
