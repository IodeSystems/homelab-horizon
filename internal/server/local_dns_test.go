package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func dnsServer(t *testing.T, records ...config.LocalDNSRecord) *Server {
	t.Helper()
	// updateConfig persists, so it needs somewhere to write. A temp file keeps
	// these tests exercising the real save path rather than a stub.
	s := &Server{
		adminToken: "test-admin-token",
		configPath: filepath.Join(t.TempDir(), "config.json"),
	}
	s.config.Store(&config.Config{LocalDNSRecords: records})
	return s
}

func del(t *testing.T, s *Server, name string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/dns/local?name="+name, nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPILocalDNS(w, r)
	return w.Code
}

func names(s *Server) []string {
	var out []string
	for _, r := range s.cfg().LocalDNSRecords {
		out = append(out, r.Name)
	}
	return out
}

// The bug this exists for: updateConfig copies Config SHALLOWLY, so filtering
// records with slice[:0] writes into the backing array the live config still
// points at. Two deletes in a row then dropped a third record nobody named —
// observed on a live box, where it removed the only record that mattered.
func TestDeletingRecordsDoesNotDisturbOthers(t *testing.T) {
	s := dnsServer(t,
		config.LocalDNSRecord{Name: "desktop", IP: "192.168.1.76"},
		config.LocalDNSRecord{Name: "desktop.lan", IP: "192.168.1.76"},
		config.LocalDNSRecord{Name: "desktop.example.com", IP: "192.168.1.76"},
	)

	if code := del(t, s, "desktop.lan"); code != http.StatusOK {
		t.Fatalf("first delete = %d", code)
	}
	if code := del(t, s, "desktop.example.com"); code != http.StatusOK {
		t.Fatalf("second delete = %d", code)
	}

	got := names(s)
	if len(got) != 1 || got[0] != "desktop" {
		t.Fatalf("after deleting two of three, records = %v, want [desktop]", got)
	}
}

// The live config must not be mutated by a delete that has not been stored yet:
// a reader holding the previous pointer should still see the old set.
func TestDeleteLeavesThePreviousConfigIntact(t *testing.T) {
	s := dnsServer(t,
		config.LocalDNSRecord{Name: "a", IP: "10.0.0.1"},
		config.LocalDNSRecord{Name: "b", IP: "10.0.0.2"},
		config.LocalDNSRecord{Name: "c", IP: "10.0.0.3"},
	)
	before := s.cfg() // a reader's snapshot

	if code := del(t, s, "b"); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}

	if len(before.LocalDNSRecords) != 3 {
		t.Fatalf("the snapshot lost entries: %d", len(before.LocalDNSRecords))
	}
	for i, want := range []string{"a", "b", "c"} {
		if before.LocalDNSRecords[i].Name != want {
			t.Fatalf("snapshot was rewritten in place: %v", before.LocalDNSRecords)
		}
	}
}

func TestDeleteUnknownRecordIs404(t *testing.T) {
	s := dnsServer(t, config.LocalDNSRecord{Name: "desktop", IP: "192.168.1.76"})
	if code := del(t, s, "nope"); code != http.StatusNotFound {
		t.Fatalf("deleting an unknown record = %d, want 404", code)
	}
	if got := names(s); len(got) != 1 {
		t.Fatalf("a failed delete changed the set: %v", got)
	}
}

// Upsert replaces rather than appending a duplicate.
func TestUpsertReplacesByName(t *testing.T) {
	s := dnsServer(t, config.LocalDNSRecord{Name: "desktop", IP: "192.168.1.76"})

	body := `{"name":"DESKTOP","ip":"192.168.1.99"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/dns/local", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPILocalDNS(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("upsert = %d: %s", w.Code, w.Body)
	}
	records := s.cfg().LocalDNSRecords
	if len(records) != 1 {
		t.Fatalf("case-different name created a duplicate: %v", names(s))
	}
	if records[0].IP != "192.168.1.99" {
		t.Fatalf("address not updated: %s", records[0].IP)
	}
}
