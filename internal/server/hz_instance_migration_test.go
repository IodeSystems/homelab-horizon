package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// B3 against TODAY'S LIVE SHAPE: the gateway's nested hz is machine
// `redline-loadtest`, owned by redline, with `hz: {url, vpn_client,
// token_sha256}`, and rung redline/loadtest names it as its upstream. The
// placeholders are RFC 5737 / private; the token is minted here so its digest
// is the one in the file.
const liveInstanceToken = "hzi_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func liveB2Config(extraMachine string) string {
	return `{
  "listen_addr": ":8080",
  "public_ip_override": "192.0.2.1",
  "vpn_range": "10.100.0.0/24",
  "projects": [{"name": "redline"}],
  "environments": [
    {"project": "redline", "name": "staging", "posture": "staging"},
    {"project": "redline", "name": "loadtest", "posture": "prod", "from": "staging", "upstream": "redline-loadtest"}
  ],
  "machines": [
    {"name": "gw-1"},
    {"name": "redline-loadtest", "project": "redline"` + extraMachine + `,
     "hz": {"url": "http://10.100.0.3:8080", "vpn_client": "redline-loadtest-upstream", "token_sha256": "` + tokenDigest(liveInstanceToken) + `"}}
  ],
  "wg_peers": [{"name": "redline-loadtest-upstream", "public_key": "` + attributionKeys[0] + `", "allowed_ips": "10.100.0.3/32"}],
  "vpn_profiles": {"redline-loadtest-upstream": "upstream"},
  "vpn_projects": {"redline-loadtest-upstream": "redline"}
}`
}

// bootLive writes the live config to disk and starts hz on it the way the
// binary does: config.Load, then NewWithConfig (dry run: no system writes).
func bootLive(t *testing.T, raw string, enrolled bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw = strings.Replace(raw, `"listen_addr"`, `"wg_config_path": "`+filepath.Join(dir, "wg0.conf")+`",
  "listen_addr"`, 1)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if enrolled {
		if err := (agent.CredentialStore{Path: path + agent.CredentialsSuffix}).Enroll("redline-loadtest", "box-secret"); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the live pre-B3 config does not load: %v", err)
	}
	s, err := NewWithConfig(cfg, path, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(filepath.Join(dir, "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.users = store
	return s, path
}

func desiredAs(s *Server, token, env string) *httptest.ResponseRecorder {
	q := url.Values{"project": {"redline"}, "environment": {env}}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deploys/desired?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleAPIDeployDesired(w, r)
	return w
}

// The instance token the live child holds keeps working across the upgrade,
// with no re-bootstrap: it authenticates desired and the forwarded report for
// redline/loadtest, and is still refused for a rung placed here. The machine
// that carried the marker held nothing else and is gone; the instance is on
// disk with the same digest.
func TestTheLiveInstanceTokenStillWorksAfterB3(t *testing.T) {
	s, path := bootLive(t, liveB2Config(""), false)

	if who, ok := s.instanceCaller(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatalf("positive control: no token authenticated as %q", who)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+liveInstanceToken)
	if who, ok := s.instanceCaller(r); !ok || who != "redline-loadtest" {
		t.Fatalf("the live instance token no longer authenticates: %q %v", who, ok)
	}
	// desired: authorised (404 "nothing applied" is the answer for a rung
	// with no apply yet — the auth passed); another rung is 403.
	if w := desiredAs(s, liveInstanceToken, "loadtest"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "nothing applied") {
		t.Fatalf("desired for redline/loadtest: %d %s", w.Code, w.Body.String())
	}
	if w := desiredAs(s, liveInstanceToken, "staging"); w.Code != http.StatusForbidden {
		t.Fatalf("desired for a rung placed here: %d", w.Code)
	}
	// The forwarded report: the token is the caller, scoped to its rung.
	env, _ := s.cfg().LookupEnvironment("redline", "loadtest")
	if who, status, msg := s.instanceForRung(r, env); status != 0 || who != "redline-loadtest" {
		t.Fatalf("report scope for redline/loadtest: %d %s", status, msg)
	}
	f := deployFixture{s: s}
	rep := apitypes.DeployReportReq{Project: "redline", Environment: "staging", App: "redline", Version: "1.0.0",
		ArtifactSHA256: deployShaA, Host: "lt-1", ForwardedFor: "service:redline-loadtest"}
	if w := f.call(s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", liveInstanceToken, rep); w.Code != http.StatusForbidden {
		t.Fatalf("a forwarded report for a rung placed here: %d %s", w.Code, w.Body.String())
	}
	if w := desiredAs(s, "hzi_not-the-token", "loadtest"); w.Code != http.StatusUnauthorized {
		t.Fatalf("positive control: a wrong instance token got %d", w.Code)
	}
	rep.Environment = "loadtest"
	if w := f.call(s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", liveInstanceToken, rep); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"recorded":true`) {
		t.Fatalf("the forwarded report for redline/loadtest: %d %s", w.Code, w.Body.String())
	}

	cfg := s.cfg()
	if _, ok := cfg.FindMachine("redline-loadtest"); ok {
		t.Fatal("the carrier machine is still declared")
	}
	if _, ok := cfg.FindMachine("gw-1"); !ok {
		t.Fatal("an unrelated machine was removed")
	}
	disk, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := disk.FindHZInstance("redline-loadtest")
	if !ok || h.TokenSHA256 != tokenDigest(liveInstanceToken) || h.VPNClient != "redline-loadtest-upstream" || h.URL != "http://10.100.0.3:8080" || h.Project != "redline" {
		t.Fatalf("instance on disk: %+v %v", h, ok)
	}
	if _, ok := disk.FindMachine("redline-loadtest"); ok {
		t.Fatal("the carrier machine is still on disk")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"hz":`) {
		t.Fatalf("the saved config still has a machine hz marker:\n%s", raw)
	}

	// The Instances page still shows it as a nested row, with its link; it is
	// not on Machines.
	n := nestedRow(t, s, "redline-loadtest")
	if !n.HasToken || n.VPNClient != "redline-loadtest-upstream" {
		t.Fatalf("instances row: %+v", n)
	}
	for _, m := range listMachinesAPI(t, s) {
		if m.Name == "redline-loadtest" {
			t.Fatal("the instance appears on /api/v1/machines")
		}
	}
}

// A carrier with an agent enrolment is a real box: kept, with the instance
// migrated beside it. A carrier with a note is kept for the same reason.
func TestALiveCarrierThatIsABoxIsKept(t *testing.T) {
	for label, tc := range map[string]struct {
		extra    string
		enrolled bool
	}{
		"enrolled": {"", true},
		"note":     {`, "note": "also the office build box"`, false},
	} {
		s, path := bootLive(t, liveB2Config(tc.extra), tc.enrolled)
		if _, ok := s.cfg().FindMachine("redline-loadtest"); !ok {
			t.Fatalf("%s: the carrier was removed", label)
		}
		if h, ok := s.cfg().FindHZInstance("redline-loadtest"); !ok || h.TokenSHA256 != tokenDigest(liveInstanceToken) {
			t.Fatalf("%s: instance %+v %v", label, h, ok)
		}
		if err := config.Save(path, s.cfg()); err != nil {
			t.Fatalf("%s: the kept estate does not save: %v", label, err)
		}
	}
}

// A pre-B3 backup restores through the same migration: the instance comes
// back with its digest, and the carrier settles against the credential store.
func TestARestoredPreB3BackupMigrates(t *testing.T) {
	s, path := bootLive(t, liveB2Config(""), false)
	// The restore handler decodes with plain json.Unmarshal (handlers_backup.go).
	cfg := &config.Config{}
	if err := json.Unmarshal([]byte(liveB2Config("")), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.MigrateLegacyMachineHZ(); err != nil {
		t.Fatal(err)
	}
	removed, _ := settleLegacyHZCarriers(cfg, s.configPath)
	if len(removed) != 1 || removed[0] != "redline-loadtest" {
		t.Fatalf("restore settle removed %v", removed)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	disk, _ := config.Load(path)
	if h, ok := disk.FindHZInstance("redline-loadtest"); !ok || h.TokenSHA256 != tokenDigest(liveInstanceToken) {
		t.Fatalf("restored instance: %+v %v", h, ok)
	}
}
