package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// pendingServer answers the record-set calls of `hz dns record add`, a sync,
// and the pending list, and counts reads of the pending list.
func pendingServer(t *testing.T, pending apitypes.PendingChanges, reads *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = enc.Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/zones":
			_ = enc.Encode([]apitypes.ZoneResp{{Name: "iodesystems.com"}})
		case "/api/v1/zones/records":
			_ = enc.Encode(apitypes.ZoneRecordsResponse{Zone: "iodesystems.com"})
		case "/api/v1/zones/records/set":
			var req apitypes.DNSRecordSetRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = enc.Encode(apitypes.DNSRecordSetResponse{OK: true, Zone: req.Zone, Name: req.Name, Type: req.Type, Values: req.Values, Changed: true})
		case "/api/v1/services/sync":
			_ = enc.Encode(apitypes.TriggerSyncResponse{Started: true})
		case "/api/v1/sync/pending":
			*reads++
			_ = enc.Encode(pending)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var addArgs = []string{"record", "add", "--name", "a._domainkey.iodesystems.com", "--type", "CNAME", "--value", "a.dkim.amazonses.com"}

// The 2026-09-28 report: `hz dns record add` said "(published)" five times and
// nothing said that other changes were still staged. A mutating command now
// ends with the pending list.
func TestMutatingCommandReportsPending(t *testing.T) {
	reads := 0
	srv := pendingServer(t, apitypes.PendingChanges{HasPending: true, Count: 2, Items: []apitypes.PendingItem{
		{Kind: "zone", Name: "iodesystems.com", Change: "modified", Fields: []apitypes.FieldChange{{Path: "records"}}},
		{Kind: "service", Name: "bintag", Change: "modified", Fields: []apitypes.FieldChange{{Path: "domains"}}},
	}}, &reads)
	var stderr bytes.Buffer
	if err := run(newClient(srv.URL, "t"), "dns", addArgs, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "2 pending change(s) — run 'hz sync' to publish ('hz pending' for detail):\n" +
		"  modified service  bintag (domains)\n" +
		"  modified zone     iodesystems.com (records)\n"
	if stderr.String() != want {
		t.Fatalf("stderr =\n%s\nwant\n%s", stderr.String(), want)
	}
}

func TestMutatingCommandReportsNothingPending(t *testing.T) {
	reads := 0
	srv := pendingServer(t, apitypes.PendingChanges{Items: []apitypes.PendingItem{}}, &reads)
	var stderr bytes.Buffer
	if err := run(newClient(srv.URL, "t"), "dns", addArgs, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "Nothing pending.\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// A read-only command says nothing, and neither does a sync that was started
// but not waited for: its pending list is mid-change.
func TestReadOnlyAndUnwaitedSyncDoNotReport(t *testing.T) {
	reads := 0
	srv := pendingServer(t, apitypes.PendingChanges{HasPending: true, Count: 1,
		Items: []apitypes.PendingItem{{Kind: "zone", Name: "iodesystems.com", Change: "modified"}}}, &reads)
	var stderr bytes.Buffer
	if err := run(newClient(srv.URL, "t"), "sync", nil, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run(newClient(srv.URL, "t"), "dns", []string{"record", "list", "iodesystems.com"}, &stderr); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("reads=%d stderr=%q", reads, stderr.String())
	}
}
