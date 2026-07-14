package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// fakeProvider is an in-memory dns.Provider whose live record set the test
// controls, so the full drift lifecycle can be exercised without a real
// provider or network.
type fakeProvider struct {
	records []dns.Record // live state (FQDN names)
}

func (f *fakeProvider) Name() string                   { return "fake" }
func (f *fakeProvider) ListZones() ([]dns.Zone, error) { return nil, nil }
func (f *fakeProvider) ListRecords(string) ([]dns.Record, error) {
	return append([]dns.Record(nil), f.records...), nil
}
func (f *fakeProvider) GetRecord(string, string, string) (*dns.Record, error) { return nil, nil }
func (f *fakeProvider) CreateRecord(string, dns.Record) error                 { return nil }
func (f *fakeProvider) UpdateRecord(string, dns.Record) error                 { return nil }

func (f *fakeProvider) DeleteRecord(_, name, recType string) error {
	var kept []dns.Record
	for _, r := range f.records {
		if strings.TrimSuffix(r.Name, ".") == strings.TrimSuffix(name, ".") && strings.EqualFold(r.Type, recType) {
			continue
		}
		kept = append(kept, r)
	}
	f.records = kept
	return nil
}

func (f *fakeProvider) SyncRecord(zoneID string, r dns.Record) (bool, error) {
	return f.SyncRecordSet(zoneID, []dns.Record{r})
}

func (f *fakeProvider) SyncRecordSet(zoneID string, recs []dns.Record) (bool, error) {
	if len(recs) == 0 {
		return false, nil
	}
	_ = f.DeleteRecord(zoneID, recs[0].Name, recs[0].Type)
	f.records = append(f.records, recs...)
	return true, nil
}

// TestDNSDriftLifecycle drives the real publish path end to end: seed baseline,
// idempotent re-publish, out-of-band change -> drift -> block + sync-back, HTTP
// block gate refuses sync, clear resumes.
func TestDNSDriftLifecycle(t *testing.T) {
	cfg := &config.Config{Zones: []config.Zone{{Name: "example.com", ZoneID: "z1"}}}
	s := newTestServer(t, cfg)
	zone := s.cfg().Zones[0]
	fp := &fakeProvider{}
	key := driftKey("example.com", "hz.example.com", "TXT")
	desired := []dns.Record{{Name: "hz.example.com", Type: "TXT", Value: "v1", ZoneID: "z1"}}

	// 1. First publish seeds baseline, publishes, does not block.
	changed, err := s.newDNSSyncRun().publish(fp, zone, "hz.example.com", "TXT", desired)
	if err != nil || !changed {
		t.Fatalf("first publish: changed=%v err=%v", changed, err)
	}
	if s.dnsSyncBlocked() {
		t.Fatal("must not be blocked after a clean publish")
	}
	if got := s.cfg().LastPublishedRecords[key]; !valueSetsEqual(got, []string{"v1"}) {
		t.Fatalf("baseline = %v, want [v1]", got)
	}
	if live, _ := fp.ListRecords("z1"); len(live) != 1 || live[0].Value != "v1" {
		t.Fatalf("provider live = %v, want [v1]", live)
	}

	// 2. Re-publishing identical desired is a no-op (live == desired).
	if changed, err := s.newDNSSyncRun().publish(fp, zone, "hz.example.com", "TXT", desired); err != nil || changed {
		t.Fatalf("idempotent publish: changed=%v err=%v", changed, err)
	}

	// 3. Out-of-band change at the provider.
	fp.records = []dns.Record{{Name: "hz.example.com", Type: "TXT", Value: "EVIL"}}

	// 4. Next sync detects drift, aborts, blocks, records detail, syncs baseline back.
	_, err = s.newDNSSyncRun().publish(fp, zone, "hz.example.com", "TXT", desired)
	if !errors.Is(err, errDNSDriftBlocked) {
		t.Fatalf("expected errDNSDriftBlocked, got %v", err)
	}
	if !s.dnsSyncBlocked() {
		t.Fatal("must be blocked after drift")
	}
	d := s.cfg().DNSDriftDetail
	if d == nil || !valueSetsEqual(d.Expected, []string{"v1"}) || !valueSetsEqual(d.Live, []string{"EVIL"}) {
		t.Fatalf("drift detail = %+v, want expected=[v1] live=[EVIL]", d)
	}
	if got := s.cfg().LastPublishedRecords[key]; !valueSetsEqual(got, []string{"EVIL"}) {
		t.Errorf("baseline after drift = %v, want synced-back [EVIL]", got)
	}
	if live, _ := fp.ListRecords("z1"); len(live) != 1 || live[0].Value != "EVIL" {
		t.Errorf("provider must be untouched on drift, got %v", live)
	}

	// 5. Block gate: sync-all and record mutation refuse with 409 while blocked.
	admin := &http.Cookie{Name: "session", Value: s.signCookie("admin")}
	gate := map[string]http.HandlerFunc{
		"/api/v1/dns/sync-all":      s.handleAPISyncAllDNS,
		"/api/v1/zones/records/add": s.handleAPIRecordAdd,
	}
	for path, h := range gate {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s while blocked = %d, want 409", path, rec.Code)
		}
	}

	// 6. Clear resumes sync.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dns/drift/clear", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	s.handleAPIClearDNSDrift(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d, want 200", rec.Code)
	}
	if s.dnsSyncBlocked() || s.cfg().DNSDriftDetail != nil {
		t.Error("must be unblocked with detail cleared after clear")
	}
}

// TestMergeRemoteIntoLocalPinsDriftState guards the HA invariant: drift block +
// published-baseline are local-only and must never be inherited from a peer.
func TestMergeRemoteIntoLocalPinsDriftState(t *testing.T) {
	local := &config.Config{
		LastPublishedRecords: map[string][]string{"example.com|a|A": {"1.1.1.1"}},
		DNSDriftBlocked:      true,
		DNSDriftDetail:       &config.DNSDriftInfo{Zone: "example.com", Name: "a", Type: "A"},
	}
	remote := &config.Config{
		LastPublishedRecords: map[string][]string{"other|x|TXT": {"z"}},
		DNSDriftBlocked:      false,
	}
	out := mergeRemoteIntoLocal(remote, local)

	if !out.DNSDriftBlocked {
		t.Error("drift block must remain local, not be cleared by the peer")
	}
	if out.DNSDriftDetail == nil || out.DNSDriftDetail.Zone != "example.com" {
		t.Error("drift detail must remain local")
	}
	if !valueSetsEqual(out.LastPublishedRecords["example.com|a|A"], []string{"1.1.1.1"}) {
		t.Error("local published-baseline must be preserved")
	}
	if _, ok := out.LastPublishedRecords["other|x|TXT"]; ok {
		t.Error("must not inherit the peer's published-baseline")
	}
}
