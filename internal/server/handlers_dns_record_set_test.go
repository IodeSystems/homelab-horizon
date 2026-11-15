package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// countingProvider is fakeProvider plus a count of the writes that reached it,
// so a test can tell "adopted, nothing written" from "published".
type countingProvider struct {
	*fakeProvider
	writes  int
	deletes int
}

func (c *countingProvider) SyncRecordSet(zoneID string, recs []dns.Record) (bool, error) {
	c.writes++
	return c.fakeProvider.SyncRecordSet(zoneID, recs)
}

func (c *countingProvider) SyncRecord(zoneID string, r dns.Record) (bool, error) {
	return c.SyncRecordSet(zoneID, []dns.Record{r})
}

func (c *countingProvider) DeleteRecord(zoneID, name, recType string) error {
	c.deletes++
	return c.fakeProvider.DeleteRecord(zoneID, name, recType)
}

const sesZone = "iodesystems.com"

func recordServer(t *testing.T, live ...dns.Record) (*Server, *countingProvider) {
	t.Helper()
	cfg := &config.Config{Zones: []config.Zone{{
		Name: sesZone, ZoneID: "Z1",
		DNSProvider: &config.DNSProviderConfig{Type: config.DNSProviderRoute53},
	}}}
	s := newTestServer(t, cfg)
	fp := &countingProvider{fakeProvider: &fakeProvider{records: live}}
	s.newDNSProvider = func(*config.DNSProviderConfig) (dns.Provider, error) { return fp, nil }
	return s, fp
}

func setRecords(t *testing.T, s *Server, req apitypes.DNSRecordSetRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	s.handleAPIRecordSet(w, asAdmin(s, http.MethodPost, "/api/v1/zones/records/set", string(b)))
	return w
}

func deleteRecord(t *testing.T, s *Server, name, typ, value string, expectedFrom []string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"zone": sesZone, "name": name, "type": typ, "value": value, "expectedFrom": expectedFrom})
	w := httptest.NewRecorder()
	s.handleAPIRecordDelete(w, asAdmin(s, http.MethodPost, "/api/v1/zones/records/delete", string(b)))
	return w
}

func sortedLive(fp *countingProvider, name, typ string) []string {
	v := liveValues(fp.fakeProvider, name, typ)
	sort.Strings(v)
	return v
}

// The first use: three SES DKIM CNAMEs, zone derived from the name. Each is
// declared with its note and published; the set's baseline is recorded so the
// next sync reads it as hz's, not as a takeover.
func TestRecordSetPublishesDKIMCNAME(t *testing.T) {
	s, fp := recordServer(t)
	name := "abc123._domainkey.iodesystems.com"
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: name, Type: "cname", Values: []string{"abc123.dkim.amazonses.com."}, Note: "SES DKIM",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if got := sortedLive(fp, name, "CNAME"); len(got) != 1 || got[0] != "abc123.dkim.amazonses.com" {
		t.Fatalf("live = %v", got)
	}
	recs := s.cfg().Zones[0].Records
	if len(recs) != 1 || recs[0].Note != "SES DKIM" || recs[0].Value != "abc123.dkim.amazonses.com" {
		t.Fatalf("declared = %+v", recs)
	}
	if got := s.cfg().LastPublishedRecords[driftKey(sesZone, name, "CNAME")]; len(got) != 1 {
		t.Fatalf("baseline not recorded: %v", got)
	}

	// The scheduled sync then finds nothing to do and does not block.
	before := fp.writes
	if _, failed, err := s.syncZoneRecords(s.newDNSSyncRun()); err != nil || failed != 0 {
		t.Fatalf("sync after set: failed=%d err=%v", failed, err)
	}
	if fp.writes != before || s.dnsSyncBlocked() {
		t.Fatalf("sync rewrote or blocked: writes %d->%d blocked=%v", before, fp.writes, s.dnsSyncBlocked())
	}
}

// MX and TXT at a MAIL FROM subdomain: the change batch carries every value of
// the set, TXT unquoted (the adapter quotes), MX as "<pref> <host>".
func TestRecordSetMXAndTXT(t *testing.T) {
	s, fp := recordServer(t)
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: "mail.iodesystems.com", Type: "MX", Values: []string{"10 feedback-smtp.us-west-2.amazonses.com"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("MX: %d %s", w.Code, w.Body.String())
	}
	w = setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: "mail.iodesystems.com", Type: "TXT", Values: []string{"v=spf1 include:amazonses.com ~all"}, TTL: 600,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("TXT: %d %s", w.Code, w.Body.String())
	}
	if got := sortedLive(fp, "mail.iodesystems.com", "MX"); len(got) != 1 || got[0] != "10 feedback-smtp.us-west-2.amazonses.com" {
		t.Fatalf("MX live = %v", got)
	}
	if got := sortedLive(fp, "mail.iodesystems.com", "TXT"); len(got) != 1 || got[0] != "v=spf1 include:amazonses.com ~all" {
		t.Fatalf("TXT live = %v", got)
	}

	sets, errs := buildZoneRecordSets(s.cfg().Zones[0])
	if len(errs) != 0 || len(sets) != 2 {
		t.Fatalf("sets=%d errs=%v", len(sets), errs)
	}
	for _, set := range sets {
		r := set.Records[0]
		if r.Name != "mail.iodesystems.com" || r.ZoneID != "Z1" {
			t.Errorf("record %+v not qualified into Z1", r)
		}
		if r.Type == "TXT" && r.TTL != 600 {
			t.Errorf("TXT ttl = %d, want 600", r.TTL)
		}
	}
}

// A value live at the name that hz did not publish would be deleted by
// writing the set. Refused, and named.
func TestRecordSetRefusesForeignValue(t *testing.T) {
	foreign := dns.Record{Name: "iodesystems.com", Type: "TXT", Value: "google-site-verification=abc", ZoneID: "Z1"}
	s, fp := recordServer(t, foreign)
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: "iodesystems.com", Type: "TXT", Values: []string{"v=spf1 include:amazonses.com ~all"},
		ExpectedFrom: []string{foreign.Value},
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "google-site-verification=abc") {
		t.Fatalf("got %d %s, want 409 naming the foreign value", w.Code, w.Body.String())
	}
	if fp.writes != 0 || len(s.cfg().Zones[0].Records) != 0 {
		t.Fatalf("refused write still wrote: writes=%d declared=%v", fp.writes, s.cfg().Zones[0].Records)
	}
}

// Naming the live value adopts it: declared, and nothing written because the
// provider already holds exactly that — with the live TTL kept.
func TestRecordSetAdoptsWithoutWriting(t *testing.T) {
	live := dns.Record{Name: "x._domainkey.iodesystems.com", Type: "CNAME", Value: "x.dkim.amazonses.com", TTL: 1800, ZoneID: "Z1"}
	s, fp := recordServer(t, live)
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: live.Name, Type: "CNAME", Values: []string{"x.dkim.amazonses.com."}, ExpectedFrom: []string{live.Value},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	var resp apitypes.DNSRecordSetResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Changed || fp.writes != 0 {
		t.Fatalf("adoption wrote: changed=%v writes=%d", resp.Changed, fp.writes)
	}
	if got := s.cfg().Zones[0].Records; len(got) != 1 || got[0].TTL != 1800 {
		t.Fatalf("declared = %+v, want one record at the live TTL", got)
	}
}

func TestRecordSetValidation(t *testing.T) {
	cases := []struct {
		name string
		req  apitypes.DNSRecordSetRequest
		want string
	}{
		{"bad type", apitypes.DNSRecordSetRequest{Name: "a.iodesystems.com", Type: "SRV", Values: []string{"x"}}, "cannot be declared"},
		{"cname two values", apitypes.DNSRecordSetRequest{Name: "a.iodesystems.com", Type: "CNAME", Values: []string{"b.example.net", "c.example.net"}}, "exactly one"},
		{"outside every zone", apitypes.DNSRecordSetRequest{Name: "a.example.org", Type: "TXT", Values: []string{"x"}}, "no managed zone"},
		{"outside the named zone", apitypes.DNSRecordSetRequest{Zone: sesZone, Name: "a.example.org", Type: "TXT", Values: []string{"x"}}, "not in zone"},
		{"bad MX", apitypes.DNSRecordSetRequest{Name: "m.iodesystems.com", Type: "MX", Values: []string{"mail.example.net"}}, "preference"},
		{"quoted TXT", apitypes.DNSRecordSetRequest{Name: "t.iodesystems.com", Type: "TXT", Values: []string{`"v=spf1 -all"`}}, "quoted"},
		{"bad A", apitypes.DNSRecordSetRequest{Name: "h.iodesystems.com", Type: "A", Values: []string{"not-an-ip"}}, "IPv4"},
		{"no values", apitypes.DNSRecordSetRequest{Name: "h.iodesystems.com", Type: "A"}, "at least one value"},
		{"apex CNAME", apitypes.DNSRecordSetRequest{Name: sesZone, Type: "CNAME", Values: []string{"b.example.net"}}, "apex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fp := recordServer(t)
			w := setRecords(t, s, tc.req)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want 400 containing %q", w.Code, w.Body.String(), tc.want)
			}
			if fp.writes != 0 || len(s.cfg().Zones[0].Records) != 0 {
				t.Fatal("refused request changed state")
			}
		})
	}
}

// A CNAME may not share its name with another declared type.
func TestRecordSetRefusesCNAMESibling(t *testing.T) {
	s, _ := recordServer(t)
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "a.iodesystems.com", Type: "TXT", Values: []string{"hello"}}); w.Code != http.StatusOK {
		t.Fatalf("TXT: %d %s", w.Code, w.Body.String())
	}
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "a.iodesystems.com", Type: "CNAME", Values: []string{"b.example.net"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "coexist") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestRecordSetDriftGuard(t *testing.T) {
	s, fp := recordServer(t, dns.Record{Name: "a.iodesystems.com", Type: "TXT", Value: "changed", ZoneID: "Z1"})
	w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: "a.iodesystems.com", Type: "TXT", Values: []string{"changed", "new"}, ExpectedFrom: []string{"what-i-saw"},
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "drift") || fp.writes != 0 {
		t.Fatalf("got %d %s writes=%d", w.Code, w.Body.String(), fp.writes)
	}
}

// Delete takes only hz's value; a sibling someone else holds at the same
// (name, type) survives.
func TestRecordDeleteOnlyOwnValue(t *testing.T) {
	other := dns.Record{Name: "mail.iodesystems.com", Type: "MX", Value: "20 backup.example.net", ZoneID: "Z1"}
	s, fp := recordServer(t, other)
	// Adopt the foreign value and add hz's own, then let go of the foreign one
	// by declaring only hz's: that is the only way the set can become hz's.
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: "mail.iodesystems.com", Type: "MX", Values: []string{"10 feedback-smtp.us-west-2.amazonses.com"},
		ExpectedFrom: []string{other.Value},
	}); w.Code != http.StatusConflict {
		t.Fatalf("want the foreign MX to block a set write, got %d", w.Code)
	}

	// Declared via the per-value endpoint (as the UI does): the sibling rides along.
	b, _ := json.Marshal(map[string]any{"zone": sesZone, "name": "mail.iodesystems.com", "type": "MX",
		"value": "10 feedback-smtp.us-west-2.amazonses.com", "expectedFrom": []string{other.Value}})
	w := httptest.NewRecorder()
	s.handleAPIRecordAdd(w, asAdmin(s, http.MethodPost, "/api/v1/zones/records/add", string(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}

	w = deleteRecord(t, s, "mail.iodesystems.com", "MX", "10 feedback-smtp.us-west-2.amazonses.com",
		[]string{other.Value, "10 feedback-smtp.us-west-2.amazonses.com"})
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := sortedLive(fp, "mail.iodesystems.com", "MX"); len(got) != 1 || got[0] != other.Value {
		t.Fatalf("live after delete = %v, want only the foreign value", got)
	}
	if got := s.cfg().LastPublishedRecords[driftKey(sesZone, "mail.iodesystems.com", "MX")]; len(got) != 1 || got[0] != other.Value {
		t.Fatalf("baseline after delete = %v", got)
	}
}

// A declaration that never reached the provider is removed from config only:
// nothing is deleted at the provider, and no tombstone is left to retract a
// value that is not there.
func TestRecordDeleteDeclaredButNotLive(t *testing.T) {
	s, fp := recordServer(t)
	_ = s.updateConfig(func(c *config.Config) {
		c.Zones[0].Records = []config.DNSRecord{{Name: "t.iodesystems.com", Type: "TXT", Value: "never-published"}}
	})
	w := deleteRecord(t, s, "t.iodesystems.com", "TXT", "never-published", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(s.cfg().Zones[0].Records) != 0 || len(s.cfg().Zones[0].Tombstones) != 0 {
		t.Fatalf("records=%v tombstones=%v", s.cfg().Zones[0].Records, s.cfg().Zones[0].Tombstones)
	}
	if fp.writes != 0 || fp.deletes != 0 {
		t.Fatalf("provider touched: writes=%d deletes=%d", fp.writes, fp.deletes)
	}
}

// Delete then re-declare: the delete must leave the baseline at what is live,
// or the re-declare's first sync reads live {} vs last-published {old} as drift.
func TestRecordDeleteThenRedeclareDoesNotDrift(t *testing.T) {
	s, _ := recordServer(t)
	name := "a._domainkey.iodesystems.com"
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: name, Type: "CNAME", Values: []string{"a.dkim.amazonses.com"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := deleteRecord(t, s, name, "CNAME", "a.dkim.amazonses.com", []string{"a.dkim.amazonses.com"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	_ = s.updateConfig(func(c *config.Config) {
		c.Zones[0].Records = []config.DNSRecord{{Name: name, Type: "CNAME", Value: "b.dkim.amazonses.com"}}
	})
	if _, _, err := s.syncZoneRecords(s.newDNSSyncRun()); err != nil || s.dnsSyncBlocked() {
		t.Fatalf("re-declare sync: err=%v blocked=%v detail=%+v", err, s.dnsSyncBlocked(), s.cfg().DNSDriftDetail)
	}
}

// The list reports declared records with their note and whether they are live.
func TestZoneRecordsListsDeclared(t *testing.T) {
	s, _ := recordServer(t, dns.Record{Name: "other.iodesystems.com", Type: "TXT", Value: "x", ZoneID: "Z1"})
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "a.iodesystems.com", Type: "TXT", Values: []string{"hi"}, Note: "why"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	_ = s.updateConfig(func(c *config.Config) {
		c.Zones[0].Records = append(c.Zones[0].Records, config.DNSRecord{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"})
	})
	w := httptest.NewRecorder()
	s.handleAPIZoneRecords(w, asAdmin(s, http.MethodGet, "/api/v1/zones/records?zone="+sesZone, ""))
	var resp apitypes.ZoneRecordsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Declared) != 2 {
		t.Fatalf("declared = %+v", resp.Declared)
	}
	for _, d := range resp.Declared {
		switch d.Name {
		case "a.iodesystems.com":
			if !d.Live || d.Note != "why" {
				t.Errorf("a: %+v", d)
			}
		case "_dmarc.iodesystems.com":
			if d.Live {
				t.Errorf("_dmarc reported live: %+v", d)
			}
		default:
			t.Errorf("unexpected %+v", d)
		}
	}
	for _, r := range resp.Records {
		if r.Name == "a.iodesystems.com" && (r.Owner != config.RecordOwnerDeclared || r.Note != "why") {
			t.Errorf("live row: %+v", r)
		}
		if r.Name == "other.iodesystems.com" && r.Owner != config.RecordOwnerObserved {
			t.Errorf("foreign row: %+v", r)
		}
	}
}

// The SES us-west-2 set for iodesystems.com, published beside the zone's
// existing foreign records: those survive untouched, a sync afterwards writes
// nothing and does not block, and an attempt to declare SPF over the foreign
// apex TXT set is refused rather than upserted.
func TestRecordSetSESBesideForeignRecords(t *testing.T) {
	apexSPF := dns.Record{Name: sesZone, Type: "TXT", Value: "v=spf1 include:spf.messagingengine.com ?all", ZoneID: "Z1"}
	dmarc := dns.Record{Name: "_dmarc.iodesystems.com", Type: "TXT", Value: "v=DMARC1; p=none;", ZoneID: "Z1"}
	s, fp := recordServer(t, apexSPF, dmarc)

	for _, tok := range []string{"ozh6vxsiyhkdbinnzbevshxnnmlahivr", "ztro3ohpdebedhii4jcqgtvv222baryh", "eeoviqjcazm7kgpmo2rdmzfhhdmedbsv"} {
		if w := setRecords(t, s, apitypes.DNSRecordSetRequest{
			Name: tok + "._domainkey.iodesystems.com", Type: "CNAME", Values: []string{tok + ".dkim.amazonses.com"}, Note: "SES DKIM us-west-2",
		}); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tok, w.Code, w.Body.String())
		}
	}
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "bounce.iodesystems.com", Type: "MX", Values: []string{"10 feedback-smtp.us-west-2.amazonses.com"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "bounce.iodesystems.com", Type: "TXT", Values: []string{"v=spf1 include:amazonses.com ~all"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}

	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{
		Name: sesZone, Type: "TXT", Values: []string{"v=spf1 include:amazonses.com ~all"}, ExpectedFrom: []string{apexSPF.Value},
	}); w.Code != http.StatusConflict {
		t.Fatalf("SPF over the foreign apex TXT set: %d %s", w.Code, w.Body.String())
	}

	if got := liveValues(fp.fakeProvider, sesZone, "TXT"); len(got) != 1 || got[0] != apexSPF.Value {
		t.Fatalf("apex TXT = %v", got)
	}
	if got := liveValues(fp.fakeProvider, "_dmarc.iodesystems.com", "TXT"); len(got) != 1 || got[0] != dmarc.Value {
		t.Fatalf("_dmarc = %v", got)
	}
	if n := len(s.cfg().Zones[0].Records); n != 5 {
		t.Fatalf("declared %d records, want 5", n)
	}

	before := fp.writes
	if _, failed, err := s.syncZoneRecords(s.newDNSSyncRun()); err != nil || failed != 0 || s.dnsSyncBlocked() {
		t.Fatalf("sync: failed=%d err=%v blocked=%v", failed, err, s.dnsSyncBlocked())
	}
	if fp.writes != before {
		t.Fatalf("sync rewrote %d set(s) it had just published", fp.writes-before)
	}
}

// syncedRecordServer is recordServer with a synced baseline taken the way a
// real sync leaves it: Zone.GetDNSProvider fills dns_provider.zone_name on
// first use (a read that writes), and a real sync calls it before markSynced.
func syncedRecordServer(t *testing.T) *Server {
	t.Helper()
	s, _ := recordServer(t)
	_ = s.updateConfig(func(c *config.Config) { c.Zones[0].GetDNSProvider() })
	s.markSynced()
	return s
}

func zonePending(s *Server) []apitypes.PendingItem {
	var out []apitypes.PendingItem
	for _, it := range s.computePending().Items {
		if it.Kind == "zone" {
			out = append(out, it)
		}
	}
	return out
}

// The 2026-09-28 report: five `hz dns record add` printed "(published)" and
// `hz pending` still listed the zone's records as not synced. A set published
// straight to the provider is synced; the zone must not read as pending for it.
func TestRecordSetPublishedIsNotPending(t *testing.T) {
	s := syncedRecordServer(t)
	name := "abc._domainkey.iodesystems.com"
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: name, Type: "CNAME", Values: []string{"abc.dkim.amazonses.com"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "iodesystems.com", Type: "TXT", Values: []string{"v=spf1 include:amazonses.com ~all"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if items := zonePending(s); len(items) != 0 {
		t.Fatalf("published sets read as pending: %+v", items)
	}

	// Per-value delete publishes too: removing one is synced as well.
	if w := deleteRecord(t, s, name, "CNAME", "abc.dkim.amazonses.com", []string{"abc.dkim.amazonses.com"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if items := zonePending(s); len(items) != 0 {
		t.Fatalf("published delete reads as pending: %+v", items)
	}
}

// Only the published set is folded into the baseline. An edit in the same zone
// that nothing published stays pending, before and after the publish.
func TestRecordSetPublishLeavesOtherZoneEditsPending(t *testing.T) {
	s := syncedRecordServer(t)
	unsynced := config.DNSRecord{Name: "verify.iodesystems.com", Type: "TXT", Value: "not-yet-published"}
	_ = s.updateConfig(func(c *config.Config) { c.Zones[0].Records = append(c.Zones[0].Records, unsynced) })
	if items := zonePending(s); len(items) != 1 {
		t.Fatalf("precondition: the unsynced edit is not pending: %+v", items)
	}

	if w := setRecords(t, s, apitypes.DNSRecordSetRequest{Name: "mail.iodesystems.com", Type: "MX", Values: []string{"10 feedback-smtp.us-west-2.amazonses.com"}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	items := zonePending(s)
	if len(items) != 1 {
		t.Fatalf("the unsynced edit is no longer pending: %+v", items)
	}
	f := items[0].Fields
	if len(f) != 1 || f[0].Path != "records" || strings.Contains(f[0].Before, "not-yet-published") ||
		!strings.Contains(f[0].After, "not-yet-published") || !strings.Contains(f[0].Before, "feedback-smtp") {
		t.Fatalf("want the baseline to hold the published MX and not the unsynced TXT: %+v", f)
	}
}
