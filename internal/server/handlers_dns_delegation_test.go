package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

const delegated = "loadtest.redline.iodesystems.com"

var childNS = []string{"ns-1.awsdns-01.org.", "ns-2.awsdns-02.com"}

func declareDelegation(t *testing.T, s *Server) {
	t.Helper()
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: delegated, Type: "NS", Values: childNS, Note: "redline-prod-hz"})
	if w.Code != http.StatusOK {
		t.Fatalf("declare NS: %d %s", w.Code, w.Body.String())
	}
}

// An NS set is published whole, recorded as hz's baseline, left alone by the
// next sync, and an out-of-band change to it halts DNS like any other set.
func TestNSDelegationPublishesAndDriftChecks(t *testing.T) {
	s, fp := recordServer(t)
	declareDelegation(t, s)

	want := []string{"ns-1.awsdns-01.org", "ns-2.awsdns-02.com"}
	if got := sortedLive(fp, delegated, "NS"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live NS = %v, want %v", got, want)
	}
	base := append([]string(nil), s.cfg().LastPublishedRecords[driftKey(sesZone, delegated, "NS")]...)
	sort.Strings(base)
	if strings.Join(base, ",") != strings.Join(want, ",") {
		t.Fatalf("baseline = %v, want %v", base, want)
	}

	before := fp.writes
	if _, failed, err := s.syncZoneRecords(s.newDNSSyncRun()); err != nil || failed != 0 {
		t.Fatalf("sync: failed=%d err=%v", failed, err)
	}
	if fp.writes != before || s.dnsSyncBlocked() {
		t.Fatalf("sync rewrote or blocked an in-sync NS set: writes %d->%d blocked=%v", before, fp.writes, s.dnsSyncBlocked())
	}

	// Someone repoints the delegation by hand.
	for i := range fp.records {
		if fp.records[i].Type == "NS" && fp.records[i].Value == "ns-2.awsdns-02.com" {
			fp.records[i].Value = "ns.hijack.example"
		}
	}
	_, _, err := s.syncZoneRecords(s.newDNSSyncRun())
	if !errors.Is(err, errDNSDriftBlocked) || !s.dnsSyncBlocked() {
		t.Fatalf("out-of-band NS change: err=%v blocked=%v", err, s.dnsSyncBlocked())
	}
	if d := s.cfg().DNSDriftDetail; d == nil || d.Type != "NS" || d.Name != delegated {
		t.Fatalf("drift detail = %+v", d)
	}
	if got := sortedLive(fp, delegated, "NS"); !containsValue(got, "ns.hijack.example") {
		t.Fatalf("provider must be untouched on drift, got %v", got)
	}
}

func TestNSDelegationRefusalsOnTheSetEndpoint(t *testing.T) {
	cases := []struct {
		name string
		req  apitypes.DNSRecordSetRequest
		want string
	}{
		{"apex NS", apitypes.DNSRecordSetRequest{Name: sesZone, Type: "NS", Values: []string{"ns.example.net"}}, "zone apex"},
		{"record below the delegation", apitypes.DNSRecordSetRequest{Name: "api." + delegated, Type: "A", Values: []string{"192.0.2.1"}}, "below a delegated name"},
		{"other type at the delegation", apitypes.DNSRecordSetRequest{Name: delegated, Type: "TXT", Values: []string{"x"}}, "holds only NS"},
		{"nameserver not a hostname", apitypes.DNSRecordSetRequest{Name: "x.iodesystems.com", Type: "NS", Values: []string{"not a host"}}, "not a hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fp := recordServer(t)
			declareDelegation(t, s)
			writes, records := fp.writes, len(s.cfg().Zones[0].Records)
			w := setRecords(t, s, tc.req)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want 400 containing %q", w.Code, w.Body.String(), tc.want)
			}
			if fp.writes != writes || len(s.cfg().Zones[0].Records) != records {
				t.Fatal("refused request changed state")
			}
		})
	}
}

func addRecord(t *testing.T, s *Server, name, typ, value string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"zone": sesZone, "name": name, "type": typ, "value": value, "expectedFrom": []string{}})
	w := httptest.NewRecorder()
	s.handleAPIRecordAdd(w, asAdmin(s, http.MethodPost, "/api/v1/zones/records/add", string(b)))
	return w
}

// The UI's per-value path holds the same delegation rules as the set endpoint.
func TestNSDelegationOnThePerValuePath(t *testing.T) {
	s, fp := recordServer(t)
	if w := addRecord(t, s, sesZone, "NS", "ns.example.net"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "zone apex") {
		t.Fatalf("apex NS: %d %s", w.Code, w.Body.String())
	}
	if w := addRecord(t, s, delegated, "NS", "ns-1.awsdns-01.org."); w.Code != http.StatusOK {
		t.Fatalf("NS below apex: %d %s", w.Code, w.Body.String())
	}
	if got := sortedLive(fp, delegated, "NS"); len(got) != 1 || got[0] != "ns-1.awsdns-01.org" {
		t.Fatalf("live NS = %v", got)
	}
	writes := fp.writes
	if w := addRecord(t, s, "www."+delegated, "A", "192.0.2.1"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "below a delegated name") {
		t.Fatalf("record below the delegation: %d %s", w.Code, w.Body.String())
	}
	if fp.writes != writes {
		t.Fatal("refused add reached the provider")
	}
}

func TestSubZoneBelowDelegationRefused(t *testing.T) {
	s, _ := recordServer(t)
	declareDelegation(t, s)
	b, _ := json.Marshal(map[string]string{"zone": sesZone, "subzone": "*.loadtest.redline"})
	w := httptest.NewRecorder()
	s.handleAPIAddSubZone(w, asAdmin(s, http.MethodPost, "/api/v1/zones/subzone", string(b)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "is delegated to") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg().Zones[0].SubZones) != 0 {
		t.Fatalf("sub_zones = %v", s.cfg().Zones[0].SubZones)
	}
}

func TestServiceDomainBelowDelegationRefused(t *testing.T) {
	s, _ := recordServer(t)
	declareDelegation(t, s)
	b, _ := json.Marshal(apitypes.ServiceRequest{Name: "rogue", Domains: []string{"api." + delegated}})
	w := httptest.NewRecorder()
	s.handleAPIAddService(w, asAdmin(s, http.MethodPost, "/api/v1/services/add", string(b)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "is delegated to") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if s.cfg().GetService("rogue") != nil {
		t.Fatal("refused service was stored")
	}
}
