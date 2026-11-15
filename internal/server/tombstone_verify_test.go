package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// silentNoopDeleteProvider is the live failure of 2026-10-08 as a fake: the
// provider answers a delete with success and keeps serving the record
// (libdns/route53 v1.6.2 given an empty-Data wildcard).
type silentNoopDeleteProvider struct{ fakeProvider }

func (f *silentNoopDeleteProvider) DeleteRecord(string, string, string) error { return nil }

func kioskTombstoneServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Zones: []config.Zone{tombstoneZone()}}
	cfg.Zones[0].DNSProvider = &config.DNSProviderConfig{Type: config.DNSProviderRoute53}
	cfg.Zones[0].Tombstones = []config.DNSTombstone{
		{Name: "kiosk.hz.loadtest.example.com", Type: "A", Value: "192.0.2.35", Reason: "instance domain set"},
	}
	return newTestServer(t, cfg)
}

func kioskLive() []dns.Record {
	return []dns.Record{{Name: "kiosk.hz.loadtest.example.com", Type: "A", Value: "192.0.2.35", TTL: 300, ZoneID: "z1"}}
}

// A delete the provider accepts and does not perform is NOT a retraction. The
// tombstone stays pending, the log says LOUD that it is not confirmed, never
// "retracted", and the zone view carries "retraction not confirmed".
func TestTombstoneSilentNoopDeleteStaysPending(t *testing.T) {
	s := kioskTombstoneServer(t)
	fp := &silentNoopDeleteProvider{fakeProvider{records: kioskLive()}}
	logs := captureLogs(t)

	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if retracted != 0 || failed != 1 {
		t.Errorf("retracted=%d failed=%d, want 0 and 1", retracted, failed)
	}
	ts := s.cfg().Zones[0].Tombstones
	if len(ts) != 1 {
		t.Fatalf("tombstone dropped although the record is still live: %v", ts)
	}
	if !strings.Contains(ts[0].NotConfirmed, "192.0.2.35") {
		t.Errorf("NotConfirmed = %q, want the reason naming the value still served", ts[0].NotConfirmed)
	}
	out := logs.String()
	if !strings.Contains(out, "NOT confirmed") || !strings.Contains(out, "level=ERROR") {
		t.Errorf("log does not say the retraction is not confirmed:\n%s", out)
	}
	if strings.Contains(out, `msg="tombstone retracted`) {
		t.Errorf("log claims a retraction the provider did not perform:\n%s", out)
	}
	// The baseline is not advanced, so the next sync retries the write
	// rather than reading the still-live record as drift.
	if got := s.cfg().LastPublishedRecords[driftKey("example.com", "kiosk.hz.loadtest.example.com", "A")]; len(got) != 1 {
		t.Errorf("baseline = %v, want the still-live value so the retry is an intentional change", got)
	}

	// Retried next sync, and still not confirmed — still pending.
	if r, f, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0]); err != nil || r != 0 || f != 1 {
		t.Errorf("second sync: retracted=%d failed=%d err=%v", r, f, err)
	}

	// The DNS UI sees it.
	admin := &http.Cookie{Name: "session", Value: s.signCookie("admin")}
	s.newDNSProvider = func(*config.DNSProviderConfig) (dns.Provider, error) { return fp, nil }
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/records?zone=example.com", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	s.handleAPIZoneRecords(rec, req)
	var resp apitypes.ZoneRecordsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode zone records (%d): %v", rec.Code, err)
	}
	if len(resp.Tombstones) != 1 || resp.Tombstones[0].NotConfirmed == "" || !resp.Tombstones[0].StillLive {
		t.Errorf("zone view tombstones = %+v, want one, still live, not confirmed", resp.Tombstones)
	}
}

// A provider that deletes: confirmed by read-back, logged, forgotten.
func TestTombstoneWorkingDeleteIsConfirmed(t *testing.T) {
	s := kioskTombstoneServer(t)
	fp := &fakeProvider{records: kioskLive()}
	logs := captureLogs(t)

	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil || retracted != 1 || failed != 0 {
		t.Fatalf("retracted=%d failed=%d err=%v", retracted, failed, err)
	}
	if ts := s.cfg().Zones[0].Tombstones; len(ts) != 0 {
		t.Errorf("tombstone not cleared after a confirmed delete: %v", ts)
	}
	if !strings.Contains(logs.String(), "confirmed by read-back") {
		t.Errorf("log does not say the retraction was confirmed:\n%s", logs.String())
	}
}

// The provider's names may come back with the trailing dot or in another case;
// the read-back must not call a real delete unconfirmed over the spelling.
func TestTombstoneConfirmationIgnoresNameForm(t *testing.T) {
	s := kioskTombstoneServer(t)
	fp := &fakeProvider{records: []dns.Record{
		{Name: "kiosk.hz.loadtest.example.com.", Type: "A", Value: "192.0.2.35", ZoneID: "z1"},
		{Name: "Other.Example.com.", Type: "A", Value: "192.0.2.36", ZoneID: "z1"},
	}}
	retracted, failed, err := s.retractZoneTombstones(s.newDNSSyncRun(), fp, s.cfg().Zones[0])
	if err != nil || retracted != 1 || failed != 0 {
		t.Fatalf("retracted=%d failed=%d err=%v", retracted, failed, err)
	}
}
