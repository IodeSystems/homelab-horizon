package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// None of these reach a CA: each stops before ObtainCertificate.

func TestSSLTestGateIsOnePerZonePerCooldown(t *testing.T) {
	var g sslTestGate
	now := time.Unix(1_800_000_000, 0)
	if w := g.claim("example.com", now); w != 0 {
		t.Fatalf("first claim waits %s", w)
	}
	if w := g.claim("example.com", now.Add(time.Minute)); w != sslTestCooldown-time.Minute {
		t.Errorf("second claim a minute later waits %s, want %s", w, sslTestCooldown-time.Minute)
	}
	if w := g.claim("example.org", now.Add(time.Minute)); w != 0 {
		t.Errorf("another zone waits %s, want 0", w)
	}
	if w := g.claim("example.com", now.Add(sslTestCooldown)); w != 0 {
		t.Errorf("a claim after the cooldown waits %s, want 0", w)
	}
}

func testIssueServer(t *testing.T) (*Server, string) {
	certDir := t.TempDir()
	s := newTestServer(t, &config.Config{
		SSLCertDir:        certDir,
		SSLHAProxyCertDir: filepath.Join(certDir, "haproxy"),
		Zones: []config.Zone{
			// SSL on, but no DNS provider: TestIssue refuses before the CA.
			{Name: "example.com", ZoneID: "Z1", SubZones: []string{""}, SSL: &config.ZoneSSL{Enabled: true, Email: "ops@example.com"}},
			{Name: "example.org", ZoneID: "Z2"},
		},
	})
	return s, certDir
}

func postTestIssue(t *testing.T, s *Server, zone string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPISSLTestIssue(w, asAdmin(s, http.MethodPost, "/api/v1/ssl/test-issue", `{"zone":"`+zone+`"}`))
	return w
}

func TestSSLTestIssueRefusesWhatItCannotTest(t *testing.T) {
	s, _ := testIssueServer(t)
	if w := postTestIssue(t, s, "example.net"); w.Code != http.StatusNotFound {
		t.Errorf("unknown zone = %d %s, want 404", w.Code, w.Body.String())
	}
	if w := postTestIssue(t, s, "example.org"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "requests no certificate") {
		t.Errorf("zone without SSL = %d %s, want 400", w.Code, w.Body.String())
	}
}

// A failed attempt still spends the zone's turn, and writes nothing — not
// even the staging account, since it stops before the CA.
func TestSSLTestIssueWritesNothingAndCoolsDown(t *testing.T) {
	s, certDir := testIssueServer(t)
	w := postTestIssue(t, s, "example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("test-issue = %d %s", w.Code, w.Body.String())
	}
	var got apitypes.SSLTestIssueResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !strings.Contains(got.Error, "no DNS provider") {
		t.Errorf("answer = %+v, want a refusal naming the missing DNS provider", got)
	}
	entries, err := os.ReadDir(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cert dir holds %d entries after a test issue, want none", len(entries))
	}

	w = postTestIssue(t, s, "example.com")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "try again in") {
		t.Errorf("second test-issue = %d %s, want 429 with the wait", w.Code, w.Body.String())
	}
}
