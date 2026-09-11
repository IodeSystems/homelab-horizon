package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

func pushCfg() *config.Config {
	return &config.Config{
		KioskURL:   "https://vpn.example.com",
		SSLEnabled: true,
		PublicIP:   "203.0.113.10",
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.invalid"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.1:80"}},
		},
	}
}

// report posts one push report as an agent would.
func report(t *testing.T, s *Server, token string, req probe.PushRequest) (*httptest.ResponseRecorder, probe.PushResponse) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/probe/report", strings.NewReader(string(body)))
	r.Host = "vpn.example.com"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.setupRoutes().ServeHTTP(w, r)

	var out probe.PushResponse
	_ = json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&out)
	return w, out
}

func mint(t *testing.T, s *Server) string {
	t.Helper()
	w := postRemote(t, s, s.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
	var out struct{ Token string }
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

// The whole point of push: the operator pastes nothing back. An agent
// installed from a command hz issued registers itself on first contact.
func TestFirstReportRegistersTheVantage(t *testing.T) {
	s := newTestServer(t, pushCfg())
	token := mint(t, s)

	now := time.Now().UTC()
	w, resp := report(t, s, token, probe.PushRequest{
		Vantage: "GCP", Version: "v-test", TargetsVersion: "",
		Results: []probe.Result{
			{Target: "api.example.invalid", Host: "api.example.invalid", Kind: probe.KindDNS,
				At: now, Status: probe.StatusOK, LatencyMS: 14},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("report returned %d: %s", w.Code, w.Body.String())
	}
	if resp.Accepted != 1 {
		t.Fatalf("accepted %d, want 1 — the agent drops exactly what hz acknowledges", resp.Accepted)
	}

	stored := s.cfg().RemoteProbes
	if len(stored) != 1 {
		t.Fatalf("expected the vantage to be registered, got %d", len(stored))
	}
	if stored[0].Name != "GCP" || !stored[0].IsPush() || stored[0].Token != token {
		t.Fatalf("registered entry = %+v", stored[0])
	}
	// No URL, because nothing dials it.
	if stored[0].URL != "" {
		t.Fatalf("a push vantage should carry no URL, got %q", stored[0].URL)
	}

	// The result reached the check rows.
	got := s.monitor.GetStatus("ext:GCP:dns:api.example.invalid")
	if got == nil || got.Vantage != "GCP" {
		t.Fatalf("result not folded into a check row: %+v", got)
	}

	// And the agent was handed the target set, since it held none.
	if resp.Targets == nil || len(resp.Targets.Targets) != 1 {
		t.Fatalf("hz should have sent the target set on first contact: %+v", resp.Targets)
	}
}

// Steady state: the agent holds the right version, so no set crosses the wire.
func TestReportOmitsTargetsWhenTheVersionMatches(t *testing.T) {
	s := newTestServer(t, pushCfg())
	token := mint(t, s)
	_, first := report(t, s, token, probe.PushRequest{Vantage: "GCP", Version: "v"})

	_, second := report(t, s, token, probe.PushRequest{
		Vantage: "GCP", Version: "v", TargetsVersion: first.TargetsVersion,
	})
	if second.Targets != nil {
		t.Fatal("an agent holding the current version should not be resent the set")
	}
	if second.TargetsVersion != first.TargetsVersion {
		t.Fatal("the version hz wants should be stable between reports")
	}
}

func TestReportRejectsUnknownTokens(t *testing.T) {
	s := newTestServer(t, pushCfg())

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"never issued", "0123456789abcdef0123456789abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := report(t, s, tc.token, probe.PushRequest{Vantage: "X"})
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", w.Code)
			}
		})
	}
	if len(s.cfg().RemoteProbes) != 0 {
		t.Fatal("a refused report registered a vantage anyway")
	}
}

// The token authenticates; the name is only a label. An agent must not be
// able to report as somebody else by claiming their name.
func TestReportIsIdentifiedByTokenNotName(t *testing.T) {
	s := newTestServer(t, pushCfg())
	token := mint(t, s)
	report(t, s, token, probe.PushRequest{Vantage: "GCP", Version: "v"})

	// Same token, different claimed name: still the vantage the token owns.
	w, _ := report(t, s, token, probe.PushRequest{
		Vantage: "somebody-else", Version: "v",
		Results: []probe.Result{{Target: "h", Host: "h", Kind: probe.KindDNS,
			At: time.Now().UTC(), Status: probe.StatusOK}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("report returned %d", w.Code)
	}
	if s.monitor.GetStatus("ext:somebody-else:dns:h") != nil {
		t.Fatal("results were filed under the name the agent claimed rather than the token's vantage")
	}
	if s.monitor.GetStatus("ext:GCP:dns:h") == nil {
		t.Fatal("results were not filed under the token's vantage")
	}
}

func TestRegistrationRefusesCollidingNames(t *testing.T) {
	cfg := pushCfg()
	cfg.RemoteProbes = []config.RemoteProbe{{Name: "GCP", Mode: config.ProbeModePush, Token: "other", Enabled: true}}
	s := newTestServer(t, cfg)

	w, _ := report(t, s, mint(t, s), probe.PushRequest{Vantage: "GCP", Version: "v"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 — two agents in one set of rows would interleave", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Fatalf("the error should say what to do: %s", w.Body.String())
	}
}

func TestRegistrationRefusesBadNames(t *testing.T) {
	for _, name := range []string{"", "   ", "has:colon", "has space"} {
		s := newTestServer(t, pushCfg())
		w, _ := report(t, s, mint(t, s), probe.PushRequest{Vantage: name, Version: "v"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("name %q returned %d, want 400", name, w.Code)
		}
	}
}

// A pull vantage must not silently accept reports: the operator configured
// hz to dial it, and quietly doing both would hide a misconfigured agent.
func TestPullVantageRefusesReports(t *testing.T) {
	cfg := pushCfg()
	cfg.RemoteProbes = []config.RemoteProbe{{
		Name: "dialled", Mode: config.ProbeModePull,
		URL: "https://198.51.100.7:8443", Token: "tok", Enabled: true,
	}}
	s := newTestServer(t, cfg)

	w, _ := report(t, s, "tok", probe.PushRequest{Vantage: "dialled", Version: "v"})
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
}

// Like the installer, ingest belongs on the vhost that faces outward.
func TestReportIsKioskOnly(t *testing.T) {
	s := newTestServer(t, pushCfg())
	r := httptest.NewRequest(http.MethodPost, "/api/v1/probe/report", strings.NewReader("{}"))
	r.Host = "hz.example.com"
	w := httptest.NewRecorder()
	s.setupRoutes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("admin vhost returned %d, want 404", w.Code)
	}
}

// A push vantage gets no poll loop — hz does not dial it.
func TestPushVantageStartsNoPollLoop(t *testing.T) {
	cfg := pushCfg()
	cfg.RemoteProbes = []config.RemoteProbe{
		{Name: "pushed", Mode: config.ProbeModePush, Token: "t", Enabled: true},
		{Name: "dialled", Mode: config.ProbeModePull, URL: "http://127.0.0.1:1", Token: "t", Enabled: true, Poll: 3600},
	}
	s := newTestServer(t, cfg)
	s.monitor.ReloadRemotes(s.cfg())
	defer s.monitor.Stop()

	running := s.monitor.RunningRemotesForTest()
	if running["pushed"] {
		t.Fatal("a push vantage must not get a poll loop")
	}
	if !running["dialled"] {
		t.Fatal("a pull vantage should still get one")
	}
}

// End to end with the real agent and the real Pusher: no operator step
// between installing the agent and the vantage appearing in hz.
func TestPushEndToEndWithARealAgent(t *testing.T) {
	s := newTestServer(t, pushCfg())
	token := mint(t, s)

	// hz, served on the kiosk hostname the agent will dial.
	hz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "vpn.example.com"
		s.setupRoutes().ServeHTTP(w, r)
	}))
	defer hz.Close()

	agent := probe.NewAgent("GCP", "v-e2e", token, "")
	pusher := &probe.Pusher{URL: hz.URL, Token: token, Timeout: 5 * time.Second}

	// Seed a result so the first report carries something.
	agent.Record([]probe.Result{{
		Target: "api.example.invalid", Host: "api.example.invalid",
		Kind: probe.KindHTTPS, At: time.Now().UTC(), Status: probe.StatusOK, LatencyMS: 88,
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// One cycle of the real loop: report, get targets, acknowledge.
	resp, err := pusher.Report(ctx, probe.PushRequest{
		Vantage: "GCP", Version: "v-e2e", TargetsVersion: agent.Targets().Version,
		Results: func() []probe.Result { r, _ := agent.Since(time.Time{}, 0); return r }(),
	})
	if err != nil {
		t.Fatalf("the agent could not report: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("accepted %d, want 1", resp.Accepted)
	}
	if resp.Targets == nil {
		t.Fatal("hz should have sent the target set to an agent holding none")
	}
	agent.SetTargets(*resp.Targets)

	// hz registered it, with no URL and no pin — nothing dials this agent.
	stored := s.cfg().RemoteProbes
	if len(stored) != 1 || !stored[0].IsPush() || stored[0].URL != "" || stored[0].PinSHA256 != "" {
		t.Fatalf("registered entry = %+v", stored)
	}

	// The result is a check row, and the agent holds hz's targets.
	if got := s.monitor.GetStatus("ext:GCP:https:api.example.invalid"); got == nil || got.Status != "ok" {
		t.Fatalf("check row = %+v", got)
	}
	if held := agent.Targets(); len(held.Targets) != 1 || held.Targets[0].Host != "api.example.invalid" {
		t.Fatalf("agent holds the wrong targets: %+v", held)
	}

	// Second report: nothing new, and the set is not resent.
	resp2, err := pusher.Report(ctx, probe.PushRequest{
		Vantage: "GCP", Version: "v-e2e", TargetsVersion: agent.Targets().Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp2.Targets != nil {
		t.Fatal("the set was resent to an agent already holding it")
	}
}
