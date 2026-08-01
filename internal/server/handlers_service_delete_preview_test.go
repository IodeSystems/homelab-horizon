package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// deleteTestServer builds a config exercising every orphan classification at
// once: an exact SubZone owned by one service, an exact SubZone two services
// share, a domain covered only by a wildcard, and a service publishing an
// external record.
func deleteTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		configPath: filepath.Join(t.TempDir(), "hz.yml"),
		adminToken: "test-admin-token",
	}
	s.config.Store(&config.Config{
		PublicIPOverride: "203.0.113.1",
		Zones: []config.Zone{{
			Name:     "example.com",
			ZoneID:   "Z1",
			SSL:      &config.ZoneSSL{Enabled: true, Email: "ops@example.com"},
			SubZones: []string{"solo", "shared", "*.vpn"},
		}},
		Services: []config.Service{
			{
				Name:        "solo",
				Domains:     []string{"solo.example.com"},
				InternalDNS: &config.InternalDNS{IP: "192.168.1.10"},
				ExternalDNS: &config.ExternalDNS{IPs: []string{"198.51.100.7"}, TTL: 300},
			},
			{Name: "shared-a", Domains: []string{"shared.example.com"}},
			{Name: "shared-b", Domains: []string{"shared.example.com"}},
			{Name: "wild", Domains: []string{"box.vpn.example.com"}},
		},
	})
	return s
}

func previewFor(t *testing.T, s *Server, name string) apitypes.ServiceDeletePreviewResponse {
	t.Helper()
	for i := range s.cfg().Services {
		if s.cfg().Services[i].Name == name {
			return s.previewServiceDelete(&s.cfg().Services[i])
		}
	}
	t.Fatalf("service %q not in test config", name)
	return apitypes.ServiceDeletePreviewResponse{}
}

func findOrphan(orphans []apitypes.ServiceDeleteOrphan, kind string) *apitypes.ServiceDeleteOrphan {
	for i := range orphans {
		if orphans[i].Kind == kind {
			return &orphans[i]
		}
	}
	return nil
}

// A SubZone nothing else needs is the case that bit us in production: it stays
// on the zone as a cert SAN for a host with no backend.
func TestPreviewFlagsUnsharedSubZone(t *testing.T) {
	got := previewFor(t, deleteTestServer(t), "solo")

	https := findOrphan(got.Orphans, apitypes.OrphanKindHTTPS)
	if https == nil {
		t.Fatal("expected an https orphan for solo.example.com")
	}
	if https.Action != apitypes.OrphanActionDelete {
		t.Errorf("action = %q, want %q", https.Action, apitypes.OrphanActionDelete)
	}
	if https.SubZone != "solo" {
		t.Errorf("subZone = %q, want %q", https.SubZone, "solo")
	}
}

// The published record outlives the delete because the DNS sync is upsert-only.
func TestPreviewFlagsExternalDNS(t *testing.T) {
	got := previewFor(t, deleteTestServer(t), "solo")

	ext := findOrphan(got.Orphans, apitypes.OrphanKindExternalDNS)
	if ext == nil {
		t.Fatal("expected an external-dns orphan")
	}
	if ext.Action != apitypes.OrphanActionDelete {
		t.Errorf("action = %q, want %q", ext.Action, apitypes.OrphanActionDelete)
	}
	if len(ext.Values) != 1 || ext.Values[0] != "198.51.100.7" {
		t.Errorf("values = %v, want [198.51.100.7]", ext.Values)
	}
	if ext.Zone != "example.com" {
		t.Errorf("zone = %q, want example.com", ext.Zone)
	}
}

// dnsmasq is rewritten wholesale each sync, so it needs no decision.
func TestPreviewInternalDNSNeedsNoDecision(t *testing.T) {
	got := previewFor(t, deleteTestServer(t), "solo")

	in := findOrphan(got.Orphans, apitypes.OrphanKindInternalDNS)
	if in == nil {
		t.Fatal("expected an internal-dns entry")
	}
	if in.Action != apitypes.OrphanActionAuto {
		t.Errorf("action = %q, want %q", in.Action, apitypes.OrphanActionAuto)
	}
}

// Deleting one of two services on the same domain must not offer to strip the
// SubZone the other still depends on.
func TestPreviewKeepsSubZoneAnotherServiceUses(t *testing.T) {
	got := previewFor(t, deleteTestServer(t), "shared-a")

	https := findOrphan(got.Orphans, apitypes.OrphanKindHTTPS)
	if https == nil {
		t.Fatal("expected an https entry for shared.example.com")
	}
	if https.Action != apitypes.OrphanActionKeep {
		t.Errorf("action = %q, want %q — shared-b still needs it", https.Action, apitypes.OrphanActionKeep)
	}
}

// Wildcard coverage belongs to the zone, not the service; removing it would
// drop HTTPS for every sibling under the wildcard.
func TestPreviewNeverProposesRemovingWildcardCoverage(t *testing.T) {
	got := previewFor(t, deleteTestServer(t), "wild")

	https := findOrphan(got.Orphans, apitypes.OrphanKindHTTPS)
	if https == nil {
		t.Fatal("expected an https entry for box.vpn.example.com")
	}
	if https.Action != apitypes.OrphanActionKeep {
		t.Errorf("action = %q, want %q", https.Action, apitypes.OrphanActionKeep)
	}
	if https.SubZone != "" {
		t.Errorf("subZone = %q, want empty — the domain has no SubZone of its own", https.SubZone)
	}
}

// A service with no HTTPS and no external DNS strands nothing, so the delete
// must stay a plain delete with no decision to make.
func TestPreviewCleanServiceHasNoActionableOrphans(t *testing.T) {
	s := deleteTestServer(t)
	s.config.Store(&config.Config{
		Zones:    []config.Zone{{Name: "example.com", ZoneID: "Z1"}},
		Services: []config.Service{{Name: "plain", Domains: []string{"plain.example.com"}}},
	})

	got := previewFor(t, s, "plain")
	for _, o := range got.Orphans {
		if o.Action == apitypes.OrphanActionDelete {
			t.Errorf("unexpected actionable orphan: %+v", o)
		}
	}
}

func postDeletePreview(t *testing.T, s *Server, name string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(apitypes.ServiceDeletePreviewRequest{Name: name})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/services/delete/preview", bytes.NewReader(body))
	if auth {
		r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	}
	w := httptest.NewRecorder()
	s.handleAPIDeleteServicePreview(w, r)
	return w
}

func TestDeletePreviewEndpoint(t *testing.T) {
	s := deleteTestServer(t)

	if got := postDeletePreview(t, s, "solo", false).Code; got != http.StatusUnauthorized {
		t.Errorf("unauthenticated preview = %d, want 401", got)
	}
	if got := postDeletePreview(t, s, "nope", true).Code; got != http.StatusNotFound {
		t.Errorf("unknown service = %d, want 404", got)
	}

	w := postDeletePreview(t, s, "solo", true)
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out apitypes.ServiceDeletePreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Service != "solo" {
		t.Errorf("service = %q, want solo", out.Service)
	}
	if len(out.Orphans) != 3 {
		t.Errorf("orphans = %d, want 3 (https + external-dns + internal-dns)", len(out.Orphans))
	}
}
