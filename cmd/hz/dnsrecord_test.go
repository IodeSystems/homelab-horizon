package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func TestZoneForLongestSuffix(t *testing.T) {
	zones := []apitypes.ZoneResp{{Name: "iodesystems.com"}, {Name: "dev.iodesystems.com"}, {Name: "veliode.com"}}
	cases := map[string]string{
		"a._domainkey.iodesystems.com.": "iodesystems.com",
		"x.dev.iodesystems.com":         "dev.iodesystems.com",
		"iodesystems.com":               "iodesystems.com",
	}
	for name, want := range cases {
		if got, err := zoneFor(zones, name, ""); err != nil || got != want {
			t.Errorf("%s -> %q, %v; want %s", name, got, err, want)
		}
	}
	if _, err := zoneFor(zones, "a.example.org", ""); err == nil || !strings.Contains(err.Error(), "not in any managed zone") {
		t.Errorf("outside every zone: %v", err)
	}
	if _, err := zoneFor(zones, "a.veliode.com", "iodesystems.com"); err == nil {
		t.Error("name outside the named zone accepted")
	}
	// "notiodesystems.com" ends with the zone's text but is not under it.
	if _, err := zoneFor(zones, "notiodesystems.com", ""); err == nil {
		t.Error("suffix without a label boundary matched")
	}
}

func TestPlanRemovalTouchesOnlyDeclared(t *testing.T) {
	rs := recordSet{
		live: []apitypes.DNSRecordResp{
			{Value: "10 hz.example.net", Owner: "declared"},
			{Value: "20 theirs.example.net", Owner: "observed"},
		},
		declared: []apitypes.DeclaredDNSRecordResp{
			{Value: "10 hz.example.net", Live: true},
			{Value: "30 pending.example.net", Live: false},
		},
	}
	plan, keep, err := planRemoval(rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || !plan[0].live || plan[1].live {
		t.Fatalf("plan = %+v", plan)
	}
	if len(keep) != 1 || keep[0] != "20 theirs.example.net" {
		t.Fatalf("keep = %v", keep)
	}

	if _, _, err := planRemoval(rs, []string{"20 theirs.example.net"}); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("asking to remove a foreign value: %v", err)
	}
	foreignOnly := recordSet{live: []apitypes.DNSRecordResp{{Value: "x", Owner: "observed"}}}
	if _, _, err := planRemoval(foreignOnly, nil); err == nil || !strings.Contains(err.Error(), "did not declare") {
		t.Fatalf("undeclared set: %v", err)
	}
}

// add keeps what is already declared, sends the live set as expectedFrom, and
// derives the zone from the name.
func TestDNSRecordAddSendsUnionAndExpectedFrom(t *testing.T) {
	var got apitypes.DNSRecordSetRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = enc.Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/zones":
			_ = enc.Encode([]apitypes.ZoneResp{{Name: "iodesystems.com"}})
		case "/api/v1/zones/records":
			_ = enc.Encode(apitypes.ZoneRecordsResponse{
				Zone:     "iodesystems.com",
				Records:  []apitypes.DNSRecordResp{{Name: "mail.iodesystems.com", Type: "MX", Value: "10 a.example.net", Owner: "declared"}},
				Declared: []apitypes.DeclaredDNSRecordResp{{Name: "mail.iodesystems.com", Type: "MX", Value: "10 a.example.net", Live: true}},
			})
		case "/api/v1/zones/records/set":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = enc.Encode(apitypes.DNSRecordSetResponse{OK: true, Zone: got.Zone, Name: got.Name, Type: got.Type, Values: got.Values, Changed: true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, "test-token")

	err := runDNS(c, []string{"record", "add", "--name", "Mail.iodesystems.com.", "--type", "mx",
		"--value", "20 b.example.net", "--note", "SES MAIL FROM"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Zone != "iodesystems.com" || got.Name != "mail.iodesystems.com" || got.Type != "MX" || got.Note != "SES MAIL FROM" {
		t.Fatalf("request = %+v", got)
	}
	if strings.Join(got.Values, "|") != "10 a.example.net|20 b.example.net" {
		t.Fatalf("values = %v", got.Values)
	}
	if strings.Join(got.ExpectedFrom, "|") != "10 a.example.net" {
		t.Fatalf("expectedFrom = %v", got.ExpectedFrom)
	}
}
