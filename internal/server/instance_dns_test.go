package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// writeLogProvider is fakeProvider that counts writes, so "nothing was
// written" is a measured zero rather than an assumption.
type writeLogProvider struct {
	fakeProvider
	writes  []string // "SET name TYPE" / "DEL name TYPE"
	listErr error
}

func (p *writeLogProvider) ListRecords(z string) ([]dns.Record, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.fakeProvider.ListRecords(z)
}

func (p *writeLogProvider) SyncRecordSet(zoneID string, recs []dns.Record) (bool, error) {
	if len(recs) > 0 {
		p.writes = append(p.writes, "SET "+strings.TrimSuffix(recs[0].Name, ".")+" "+recs[0].Type)
	}
	return p.fakeProvider.SyncRecordSet(zoneID, recs)
}

func (p *writeLogProvider) DeleteRecord(zoneID, name, recType string) error {
	p.writes = append(p.writes, "DEL "+strings.TrimSuffix(name, ".")+" "+recType)
	return p.fakeProvider.DeleteRecord(zoneID, name, recType)
}

func (p *writeLogProvider) live(name, recType string) []string {
	var out []string
	for _, r := range p.records {
		if strings.TrimSuffix(r.Name, ".") == name && strings.EqualFold(r.Type, recType) {
			out = append(out, r.Value)
		}
	}
	return out
}

func (p *writeLogProvider) wrote(name string) bool {
	for _, w := range p.writes {
		if strings.Contains(w, " "+name+" ") {
			return true
		}
	}
	return false
}

const childIP = "203.0.113.20"

// dnsN4a is tlsN4a with N4h's grants — the child's TLS name and a subtree,
// another instance's subtree — a parent record of its own, and a fake
// provider.
func dnsN4a(t *testing.T) (n4aFixture, *writeLogProvider) {
	t.Helper()
	f, _ := tlsN4a(t)
	fp := &writeLogProvider{}
	fp.records = []dns.Record{{Name: "_dmarc.example.com", Type: "TXT", Value: "v=DMARC1; p=none", TTL: 300}}
	f.s.newDNSProvider = func(*config.DNSProviderConfig) (dns.Provider, error) { return fp, nil }
	if err := f.s.updateConfig(func(c *config.Config) {
		hs := append([]config.HZInstance(nil), c.HZInstances...)
		for i := range hs {
			switch hs[i].Name {
			case nestedInstance:
				hs[i].DNSGrant = []string{"loadtest.child.example.com", "*.hz.child.example.com"}
			case "other-hz":
				hs[i].DNSGrant = []string{"*.other.example.com"}
			}
		}
		c.HZInstances = hs
		zs := append([]config.Zone(nil), c.Zones...)
		for i := range zs {
			if zs[i].Name == "example.com" {
				zs[i].Records = []config.DNSRecord{{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"}}
			}
		}
		c.Zones = zs
		c.LastPublishedRecords = map[string][]string{driftKey("example.com", "_dmarc.example.com", "TXT"): {"v=DMARC1; p=none"}}
	}); err != nil {
		t.Fatal(err)
	}
	return f, fp
}

func (f n4aFixture) postDNS(token, instance string, req apitypes.InstanceDNSReq) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, apitypes.HZInstancesPath+"/"+instance+apitypes.HZInstanceDNSSuffix, bytes.NewReader(raw))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.RemoteAddr = "192.0.2.50:1234"
	w := httptest.NewRecorder()
	f.s.handler().ServeHTTP(w, r) // through the real mux and middleware
	return w
}

func dnsAnswer(t *testing.T, w *httptest.ResponseRecorder) apitypes.InstanceDNSResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("POST dns: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.InstanceDNSResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func statusOf(t *testing.T, resp apitypes.InstanceDNSResp, name string) apitypes.InstanceDNSRecordStatus {
	t.Helper()
	for _, r := range resp.Records {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no status for %s in %+v", name, resp.Records)
	return apitypes.InstanceDNSRecordStatus{}
}

func aRec(name string, values ...string) apitypes.InstanceDNSRecordReq {
	if len(values) == 0 {
		values = []string{childIP}
	}
	return apitypes.InstanceDNSRecordReq{Name: name, Type: "A", Values: values}
}

// Only that instance's token, on its own path. An admin token is not an
// instance token.
func TestInstanceDNSEndpointScope(t *testing.T) {
	f, _ := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	otherTok := f.mint(t, "other-hz")
	req := apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{aRec("app.hz.child.example.com")}}
	for _, tc := range []struct {
		name, token, instance string
		want                  int
	}{
		{"no token", "", nestedInstance, http.StatusUnauthorized},
		{"an admin token", f.pat, nestedInstance, http.StatusUnauthorized},
		{"another instance's token", otherTok, nestedInstance, http.StatusForbidden},
		{"its token, another instance's path", tok, "other-hz", http.StatusForbidden},
	} {
		if w := f.postDNS(tc.token, tc.instance, req); w.Code != tc.want {
			t.Errorf("%s: %d %q, want %d", tc.name, w.Code, errorOf(w), tc.want)
		}
	}
	if got := instanceRecordsIn(f.s.cfg(), nestedInstance); len(got) != 0 {
		t.Fatalf("a refused caller wrote records: %v", got)
	}
	r := httptest.NewRequest(http.MethodGet, apitypes.HZInstancesPath+"/"+nestedInstance+apitypes.HZInstanceDNSSuffix, nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	f.s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := f.postDNS(tok, nestedInstance, req); w.Code != http.StatusOK {
		t.Fatalf("own token: %d %s", w.Code, w.Body.String())
	}
}

func instanceRecordsIn(c *config.Config, instance string) []string {
	var out []string
	for i := range c.Zones {
		for _, r := range c.Zones[i].Records {
			if r.Instance == instance {
				out = append(out, r.Type+" "+c.Zones[i].Qualify(r.Name)+" "+r.Value)
			}
		}
	}
	return out
}

// Accepted records publish through the drift-safe path: published once,
// idempotent after, adopted when already live, and the drift guard still
// halts on an out-of-band change.
func TestInstanceDNSPublishesThroughTheDriftSafePath(t *testing.T) {
	f, fp := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	// Already live and identical, never published by hz: adopted, not written.
	fp.records = append(fp.records, dns.Record{Name: "adopt.hz.child.example.com", Type: "A", Value: childIP, TTL: 300})

	req := apitypes.InstanceDNSReq{PublicIP: childIP, Records: []apitypes.InstanceDNSRecordReq{
		aRec("app.hz.child.example.com"), aRec("loadtest.child.example.com"), aRec("adopt.hz.child.example.com"),
	}}
	resp := dnsAnswer(t, f.postDNS(tok, nestedInstance, req))
	for _, n := range []string{"app.hz.child.example.com", "loadtest.child.example.com", "adopt.hz.child.example.com"} {
		if st := statusOf(t, resp, n); st.Status != apitypes.InstanceDNSPublished {
			t.Errorf("%s: %+v", n, st)
		}
	}
	if got := fp.live("app.hz.child.example.com", "A"); len(got) != 1 || got[0] != childIP {
		t.Fatalf("live = %v", got)
	}
	if fp.wrote("adopt.hz.child.example.com") {
		t.Fatalf("an identical live record was rewritten: %v", fp.writes)
	}
	writes := len(fp.writes)
	if writes != 2 {
		t.Fatalf("writes = %v", fp.writes)
	}

	// The same set again: nothing written.
	dnsAnswer(t, f.postDNS(tok, nestedInstance, req))
	if len(fp.writes) != writes {
		t.Fatalf("an unchanged set wrote: %v", fp.writes[writes:])
	}

	// The parent's page shows them, attributed and published, and the last request.
	h, _ := f.s.cfg().FindHZInstance(nestedInstance)
	st := f.s.instanceDNSStatus(f.s.cfg(), h)
	if len(st.Records) != 3 || st.Records[0].Status != apitypes.InstanceDNSPublished || st.LastPublicIP != childIP || st.LastRequestAt == 0 {
		t.Fatalf("instance page: %+v", st)
	}

	// Out-of-band change: the parent's own sync halts all DNS, as for any record.
	for i := range fp.records {
		if fp.records[i].Name == "app.hz.child.example.com" {
			fp.records[i].Value = "198.51.100.66"
		}
	}
	if _, _, err := f.s.syncZoneRecords(f.s.newDNSSyncRun()); !errors.Is(err, errDNSDriftBlocked) {
		t.Fatalf("drift on an instance record did not halt sync: %v", err)
	}
	if d := f.s.cfg().DNSDriftDetail; d == nil || d.Name != "app.hz.child.example.com" {
		t.Fatalf("drift detail: %+v", d)
	}
	// The child is told why it is pending, and nothing is written.
	req.Records[0] = aRec("app.hz.child.example.com", "203.0.113.21")
	writes = len(fp.writes)
	resp = dnsAnswer(t, f.postDNS(tok, nestedInstance, req))
	if st := statusOf(t, resp, "app.hz.child.example.com"); st.Status != apitypes.InstanceDNSPending || !strings.Contains(st.Reason, "halted") {
		t.Fatalf("while blocked: %+v", st)
	}
	if len(fp.writes) != writes {
		t.Fatalf("wrote while blocked: %v", fp.writes[writes:])
	}
}

// Outside the grant is refused with the reason and written nowhere.
func TestInstanceDNSRefusesOutsideTheGrant(t *testing.T) {
	f, fp := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	resp := dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		aRec("app.hz.child.example.com"),
		aRec("evil.example.com"),
		aRec("app.other.example.com"), // another instance's grant
	}}))
	if st := statusOf(t, resp, "app.hz.child.example.com"); st.Status != apitypes.InstanceDNSPublished {
		t.Fatalf("inside: %+v", st)
	}
	for _, n := range []string{"evil.example.com", "app.other.example.com"} {
		st := statusOf(t, resp, n)
		if st.Status != apitypes.InstanceDNSRefused || !strings.Contains(st.Reason, "outside hz instance "+nestedInstance+"'s DNS grant") {
			t.Errorf("%s: %+v", n, st)
		}
		if fp.wrote(n) {
			t.Errorf("%s was written: %v", n, fp.writes)
		}
	}
	h, _ := f.s.cfg().FindHZInstance(nestedInstance)
	if st := f.s.instanceDNSStatus(f.s.cfg(), h); len(st.LastRefused) != 2 {
		t.Fatalf("the instance page does not show the refusals: %+v", st.LastRefused)
	}
}

// THE PARENT NEVER WRITES OUTSIDE THE GRANT, even when its config says to: an
// instance-owned record outside the grant, stored past validation, is skipped
// by every publish path.
func TestTheParentNeverWritesOutsideTheGrant(t *testing.T) {
	f, fp := dnsN4a(t)
	bad := *f.s.cfg()
	zs := append([]config.Zone(nil), bad.Zones...)
	for i := range zs {
		if zs[i].Name == "example.com" {
			zs[i].Records = append(append([]config.DNSRecord(nil), zs[i].Records...),
				config.DNSRecord{Name: "evil.example.com", Type: "A", Value: "203.0.113.66", Instance: nestedInstance},
				config.DNSRecord{Name: "ghost.example.com", Type: "A", Value: "203.0.113.67", Instance: "ghost-hz"},
				config.DNSRecord{Name: "app.hz.child.example.com", Type: "A", Value: childIP, Instance: nestedInstance},
			)
		}
	}
	bad.Zones = zs
	if err := bad.ValidateHZInstances(); err == nil {
		t.Fatal("instrument: the injected config validates, so this does not test the write-time guard")
	}
	f.s.config.Store(&bad) // past validation, as a hand-edited config.json would be

	if _, failed, err := f.s.syncZoneRecords(f.s.newDNSSyncRun()); err != nil || failed != 0 {
		t.Fatalf("sync: failed=%d err=%v", failed, err)
	}
	if _, failed, err := f.s.publishAllInstanceDNS(f.s.newDNSSyncRun()); err != nil || failed < 2 {
		t.Fatalf("instance publish: failed=%d err=%v", failed, err)
	}
	if fp.wrote("evil.example.com") || fp.wrote("ghost.example.com") {
		t.Fatalf("wrote outside the grant: %v", fp.writes)
	}
	if !fp.wrote("app.hz.child.example.com") {
		t.Fatalf("instrument: the record inside the grant was not written either, so the zero above proves nothing: %v", fp.writes)
	}
}

// The full set every time: a name left out is removed and retracted at the
// provider — only that instance's. The parent's own record and another
// instance's survive, though the set names neither.
func TestInstanceDNSFullSetRemoval(t *testing.T) {
	f, fp := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	otherTok := f.mint(t, "other-hz")
	dnsAnswer(t, f.postDNS(otherTok, "other-hz", apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{aRec("app.other.example.com", "203.0.113.30")}}))
	dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		aRec("a.hz.child.example.com"), aRec("b.hz.child.example.com"),
	}}))
	if len(fp.live("b.hz.child.example.com", "A")) != 1 {
		t.Fatal("instrument: b was never published, so its removal proves nothing")
	}
	resp := dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{aRec("a.hz.child.example.com")}}))
	if len(resp.Removed) != 1 || resp.Removed[0].Name != "b.hz.child.example.com" {
		t.Fatalf("removed = %+v", resp.Removed)
	}
	if got := fp.live("b.hz.child.example.com", "A"); len(got) != 0 {
		t.Fatalf("b is still live: %v", got)
	}
	if got := fp.live("a.hz.child.example.com", "A"); len(got) != 1 {
		t.Fatalf("a went too: %v", got)
	}
	if got := fp.live("app.other.example.com", "A"); len(got) != 1 {
		t.Fatalf("another instance's record went: %v", got)
	}
	if got := fp.live("_dmarc.example.com", "TXT"); len(got) != 1 {
		t.Fatalf("the parent's record went: %v", got)
	}
	for _, z := range f.s.cfg().Zones {
		if len(z.Tombstones) != 0 {
			t.Fatalf("a confirmed retraction left its tombstone: %+v", z.Tombstones)
		}
	}
	// An empty set: everything it owns goes; still nothing else.
	resp = dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{}))
	if len(resp.Removed) != 1 || len(fp.live("a.hz.child.example.com", "A")) != 0 || len(fp.live("app.other.example.com", "A")) != 1 {
		t.Fatalf("empty set: %+v live=%v", resp.Removed, fp.records)
	}
}

// The live hot fix: a record the parent declared by hand at the child's name.
// The identical value converts it, without a write; a different value is
// refused, naming the record, and nothing changes.
func TestInstanceDNSHotFixConversion(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		converts    bool
	}{
		{"identical converts", childIP, true},
		{"different is refused", "203.0.113.21", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fp := dnsN4a(t)
			if err := f.s.updateConfig(func(c *config.Config) {
				zs := append([]config.Zone(nil), c.Zones...)
				for i := range zs {
					if zs[i].Name == "example.com" {
						zs[i].Records = append(append([]config.DNSRecord(nil), zs[i].Records...),
							config.DNSRecord{Name: "loadtest.child.example.com", Type: "A", Value: childIP, Note: "hot fix"})
					}
				}
				c.Zones = zs
				lp := map[string][]string{}
				for k, v := range c.LastPublishedRecords {
					lp[k] = v
				}
				lp[driftKey("example.com", "loadtest.child.example.com", "A")] = []string{childIP}
				c.LastPublishedRecords = lp
			}); err != nil {
				t.Fatalf("the hot-fix shape does not save: %v", err)
			}
			fp.records = append(fp.records, dns.Record{Name: "loadtest.child.example.com", Type: "A", Value: childIP, TTL: 300})
			tok := f.mint(t, nestedInstance)
			resp := dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{aRec("loadtest.child.example.com", tc.value)}}))
			st := statusOf(t, resp, "loadtest.child.example.com")
			owner := ""
			for _, r := range f.s.cfg().GetZone("example.com").Records {
				if r.Name == "loadtest.child.example.com" {
					owner = r.Instance
				}
			}
			if tc.converts {
				if st.Status != apitypes.InstanceDNSPublished || !st.Converted || !strings.Contains(st.Reason, "converted") || owner != nestedInstance {
					t.Fatalf("conversion: %+v owner=%q", st, owner)
				}
			} else {
				if st.Status != apitypes.InstanceDNSRefused || !strings.Contains(st.Reason, "declares A loadtest.child.example.com -> "+childIP+" by hand") || owner != "" {
					t.Fatalf("refusal: %+v owner=%q", st, owner)
				}
			}
			if fp.wrote("loadtest.child.example.com") {
				t.Fatalf("the hot fix was rewritten: %v", fp.writes)
			}
		})
	}
}

// A parent admin sees instance-owned records and cannot edit them as theirs.
func TestAdminRecordEditsRefuseInstanceRecords(t *testing.T) {
	f, fp := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{aRec("app.hz.child.example.com")}}))
	writes := len(fp.writes)
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
		body any
		want string
	}{
		{"set on an owned record", f.s.handleAPIRecordSet, "/api/v1/zones/records/set",
			apitypes.DNSRecordSetRequest{Name: "app.hz.child.example.com", Type: "A", Values: []string{"198.51.100.1"}, ExpectedFrom: []string{childIP}}, "read-only here"},
		{"edit an owned record", f.s.handleAPIRecordEdit, "/api/v1/zones/records/edit",
			recordMutation{Name: "app.hz.child.example.com", Type: "A", Value: "198.51.100.1", OldValue: childIP, ExpectedFrom: []string{childIP}}, "read-only here"},
		{"delete an owned record", f.s.handleAPIRecordDelete, "/api/v1/zones/records/delete",
			recordMutation{Name: "app.hz.child.example.com", Type: "A", Value: childIP, ExpectedFrom: []string{childIP}}, "read-only here"},
		{"add inside the grant", f.s.handleAPIRecordAdd, "/api/v1/zones/records/add",
			recordMutation{Name: "new.hz.child.example.com", Type: "A", Value: "198.51.100.1"}, "inside hz instance " + nestedInstance + "'s DNS grant"},
	} {
		w := f.call(tc.h, http.MethodPost, tc.path, f.pat, tc.body)
		if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), tc.want) {
			t.Errorf("%s: %d %q", tc.name, w.Code, errorOf(w))
		}
	}
	if len(fp.writes) != writes {
		t.Fatalf("an admin edit wrote: %v", fp.writes[writes:])
	}
	// The records view says whose it is.
	w := f.call(f.s.handleAPIZoneRecords, http.MethodGet, "/api/v1/zones/records?zone=example.com", f.pat, nil)
	var zr apitypes.ZoneRecordsResponse
	_ = json.NewDecoder(w.Body).Decode(&zr)
	found := false
	for _, r := range zr.Records {
		if strings.TrimSuffix(r.Name, ".") == "app.hz.child.example.com" {
			found = r.Owner == config.RecordOwnerInstance && r.Instance == nestedInstance
		}
	}
	for _, d := range zr.Declared {
		if d.Name == "app.hz.child.example.com" && d.Instance != nestedInstance {
			found = false
		}
	}
	if !found {
		t.Fatalf("the records view does not attribute it: %+v / %+v", zr.Records, zr.Declared)
	}
}

// Narrowing the grant on the parent removes what lies outside it, now.
func TestNarrowingTheGrantRetracts(t *testing.T) {
	f, fp := dnsN4a(t)
	tok := f.mint(t, nestedInstance)
	dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		aRec("app.hz.child.example.com"), aRec("loadtest.child.example.com"),
	}}))
	grant := []string{"loadtest.child.example.com"}
	name := nestedInstance
	if w := f.call(f.s.handleAPIHZInstanceSet, http.MethodPost, "/api/v1/hz-instances/set", f.pat, apitypes.HZInstanceSetReq{Name: name, DNSGrant: &grant}); w.Code != http.StatusOK {
		t.Fatalf("set grant: %d %s", w.Code, w.Body.String())
	}
	if got := fp.live("app.hz.child.example.com", "A"); len(got) != 0 {
		t.Fatalf("still live after the grant was narrowed: %v", got)
	}
	if got := fp.live("loadtest.child.example.com", "A"); len(got) != 1 {
		t.Fatalf("the record still inside the grant went: %v", got)
	}
	if got := fp.live("_dmarc.example.com", "TXT"); len(got) != 1 {
		t.Fatalf("narrowing an instance's grant took this hz's own record: %v", got)
	}
}
