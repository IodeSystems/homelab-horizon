package monitor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

func probeCfg() *config.Config {
	return &config.Config{
		PublicIP:   "203.0.113.10",
		SSLEnabled: true,
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.com"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.1:9000"}},
			{Name: "docs", Domains: []string{"docs.example.com", "api.example.com"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.2:9000"}},
			{Name: "wild", Domains: []string{"*.example.com"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.3:9000"}},
			{Name: "unproxied", Domains: []string{"nope.example.com"}},
		},
	}
}

// The agent gets public names and a public IP. Nothing else about the network
// may cross the wire — that is the reason the design is safe to run on a
// rented host.
func TestPublicTargetsSendOnlyPublicFacts(t *testing.T) {
	m := New(probeCfg())
	got := m.publicTargets()

	if len(got) != 2 {
		t.Fatalf("expected 2 targets (deduped, no wildcard, no unproxied), got %+v", got)
	}
	// Sorted, so the version is stable across restarts.
	if got[0].Host != "api.example.com" || got[1].Host != "docs.example.com" {
		t.Fatalf("targets not sorted/deduped: %+v", got)
	}
	for _, tgt := range got {
		if len(tgt.ExpectIPs) != 1 || tgt.ExpectIPs[0] != "203.0.113.10" {
			t.Fatalf("target %s should expect the public IP, got %+v", tgt.Host, tgt.ExpectIPs)
		}
		if len(tgt.Kinds) != 2 {
			t.Fatalf("with SSL on, expected dns+https, got %+v", tgt.Kinds)
		}
	}
}

func TestPublicTargetsSkipHTTPSWithoutSSL(t *testing.T) {
	cfg := probeCfg()
	cfg.SSLEnabled = false
	got := New(cfg).publicTargets()
	if len(got) == 0 {
		t.Fatal("expected targets")
	}
	for _, tgt := range got {
		if len(tgt.Kinds) != 1 || tgt.Kinds[0] != probe.KindDNS {
			t.Fatalf("without SSL there is nothing to probe over HTTPS, got %+v", tgt.Kinds)
		}
	}
}

func TestTargetSetVersionIsStable(t *testing.T) {
	m := New(probeCfg())
	rp := config.RemoteProbe{Name: "vps"}
	if a, b := m.remoteTargetSet(rp).Version, m.remoteTargetSet(rp).Version; a != b {
		t.Fatalf("version must not change between calls (%s vs %s) or the agent re-fetches forever", a, b)
	}
}

// One poll cycle end to end, against a real agent over HTTP.
func TestPollRemoteFoldsResultsIntoChecks(t *testing.T) {
	agent := probe.NewAgent("vps-nyc", "test", "tok", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	base := time.Now().UTC().Add(-time.Minute)
	agent.SetTargets(probe.TargetSet{})
	agentRecord(t, agent, []probe.Result{
		{Target: "api.example.com", Host: "api.example.com", Kind: probe.KindDNS, At: base, Status: probe.StatusOK, LatencyMS: 12, Detail: "203.0.113.10"},
		{Target: "api.example.com", Host: "api.example.com", Kind: probe.KindHTTPS, At: base.Add(time.Second), Status: probe.StatusFailed, LatencyMS: 5001, Error: "connection refused"},
	})

	m := New(probeCfg())
	rp := config.RemoteProbe{Name: "vps-nyc", URL: srv.URL, Token: "tok", Enabled: true}
	client := &probe.Client{URL: srv.URL, Token: "tok"}

	since := m.pollRemote(context.Background(), client, rp, time.Time{})
	if !since.Equal(base.Add(time.Second)) {
		t.Fatalf("watermark = %v, expected the newest result's timestamp %v", since, base.Add(time.Second))
	}

	byName := map[string]CheckStatus{}
	for _, c := range m.GetStatuses() {
		byName[c.Name] = c
	}

	dns, ok := byName["ext:vps-nyc:dns:api.example.com"]
	if !ok {
		t.Fatalf("no DNS row for the remote vantage; got %v", byName)
	}
	if dns.Status != StatusOK || dns.Vantage != "vps-nyc" || dns.Target != "api.example.com" {
		t.Fatalf("DNS row = %+v", dns)
	}

	https, ok := byName["ext:vps-nyc:https:api.example.com"]
	if !ok {
		t.Fatal("no HTTPS row for the remote vantage")
	}
	if https.Status != StatusFailed || https.LastError != "connection refused" {
		t.Fatalf("HTTPS row = %+v", https)
	}

	// The agent itself is a row, so an agent that stops answering is visible
	// rather than leaving every target frozen on its last known status.
	agentRow, ok := byName["ext:vps-nyc:agent"]
	if !ok {
		t.Fatal("no row for the agent itself")
	}
	if agentRow.Status != StatusOK {
		t.Fatalf("agent row = %+v, expected ok", agentRow)
	}

	// History carries the reported latency, not the time hz spent polling.
	hist := m.GetHistory("ext:vps-nyc:https:api.example.com")
	if len(hist) != 1 || hist[0].Latency != 5001 {
		t.Fatalf("history = %+v, expected the agent's own latency", hist)
	}

	// A second poll with the watermark returns nothing new and must not
	// duplicate history.
	m.pollRemote(context.Background(), client, rp, since)
	if got := len(m.GetHistory("ext:vps-nyc:https:api.example.com")); got != 1 {
		t.Fatalf("re-poll duplicated history: %d entries", got)
	}
}

func TestPollRemoteMarksAnUnreachableAgent(t *testing.T) {
	srv := httptest.NewServer(probe.NewAgent("vps", "test", "tok", "").Handler())
	url := srv.URL
	srv.Close() // the agent is now gone

	m := New(probeCfg())
	rp := config.RemoteProbe{Name: "vps", URL: url, Token: "tok", Enabled: true}
	client := &probe.Client{URL: url, Token: "tok", Timeout: 2 * time.Second}

	since := m.pollRemote(context.Background(), client, rp, time.Time{})
	if !since.IsZero() {
		t.Fatal("a failed poll must not advance the watermark, or results are skipped")
	}

	got := m.GetStatus("ext:vps:agent")
	if got == nil || got.Status != StatusFailed {
		t.Fatalf("unreachable agent = %+v, expected a failed row", got)
	}
	if got.LastError == "" {
		t.Fatal("the agent row must carry why hz could not reach it")
	}
}

func TestExternalChecksAreNotToggleable(t *testing.T) {
	m := New(probeCfg())
	if !isAutoGen("ext:vps:dns:api.example.com") {
		t.Fatal("ext: rows must count as auto-generated so they cannot be deleted")
	}
	m.upsertExternal(externalRow{
		name: "ext:vps:dns:api.example.com", checkType: "dns", target: "api.example.com",
		vantage: "vps", status: StatusOK, at: time.Now(), interval: 60,
	})
	// UpdateConfig persists disabled auto-checks; a remote row is enabled and
	// must not end up in that list.
	m.UpdateConfig()
	for _, name := range m.cfg().DisabledAutoChecks {
		if name == "ext:vps:dns:api.example.com" {
			t.Fatal("a remote row leaked into disabled_auto_checks")
		}
	}
}

// agentRecord buffers results on an agent through its public surface.
func agentRecord(t *testing.T, a *probe.Agent, rs []probe.Result) {
	t.Helper()
	a.Record(rs)
}

// The narrow reload exists so editing one vantage does not cost the history
// of every unrelated check. Reload (the blunt one) clears everything.
func TestReloadRemotesKeepsLocalHistory(t *testing.T) {
	cfg := probeCfg()
	m := New(cfg)
	defer m.Stop()

	// A local check with history, standing in for everything the blunt
	// reload would throw away.
	m.mu.Lock()
	m.statuses["svc:api"] = &CheckStatus{Name: "svc:api", Status: StatusOK}
	m.history["svc:api"] = []CheckResult{{Timestamp: time.Now(), Status: StatusOK, Latency: 3}}
	m.mu.Unlock()

	next := *cfg
	next.RemoteProbes = []config.RemoteProbe{
		{Name: "vps", Mode: config.ProbeModePull, URL: "http://127.0.0.1:1", Token: "t", Enabled: true, Poll: 3600},
	}
	m.ReloadRemotes(&next)

	if got := len(m.GetHistory("svc:api")); got != 1 {
		t.Fatalf("local history was cleared by a remote-only reload: %d entries", got)
	}
	if m.GetStatus("svc:api") == nil {
		t.Fatal("local check status was dropped by a remote-only reload")
	}
}

func TestReloadRemotesLifecycle(t *testing.T) {
	base := probeCfg()
	m := New(base)
	defer m.Stop()

	withProbes := func(probes ...config.RemoteProbe) *config.Config {
		c := *base
		c.RemoteProbes = probes
		return &c
	}

	// A poll interval long enough that no loop actually fires during the
	// test; this is about lifecycle, not polling.
	a := config.RemoteProbe{Name: "a", Mode: config.ProbeModePull, URL: "http://127.0.0.1:1", Token: "t", Enabled: true, Poll: 3600}
	b := config.RemoteProbe{Name: "b", Mode: config.ProbeModePull, URL: "http://127.0.0.1:2", Token: "t", Enabled: true, Poll: 3600}

	m.ReloadRemotes(withProbes(a, b))
	if got := m.runningRemotes(); len(got) != 2 {
		t.Fatalf("expected 2 loops, got %v", got)
	}

	// Seed state and a row for each, so we can see what a stop takes with it.
	for _, name := range []string{"a", "b"} {
		m.upsertExternal(externalRow{
			name: externalPrefix + name + ":dns:api.example.com", checkType: "dns",
			target: "api.example.com", vantage: name, status: StatusOK, at: time.Now(), interval: 60,
		})
	}

	// An untouched entry keeps its loop; b is removed.
	m.ReloadRemotes(withProbes(a))
	running := m.runningRemotes()
	if len(running) != 1 || !running["a"] {
		t.Fatalf("expected only a to survive, got %v", running)
	}
	if m.GetStatus("ext:b:dns:api.example.com") != nil {
		t.Fatal("a removed vantage left its check row behind, which reads as current forever")
	}
	if m.GetStatus("ext:a:dns:api.example.com") == nil {
		t.Fatal("an untouched vantage lost its check row")
	}

	// Disabling stops the loop without deleting the config entry.
	disabled := a
	disabled.Enabled = false
	m.ReloadRemotes(withProbes(disabled))
	if got := m.runningRemotes(); len(got) != 0 {
		t.Fatalf("a disabled vantage kept polling: %v", got)
	}
}

func TestSameRemoteProbe(t *testing.T) {
	base := config.RemoteProbe{
		Name: "a", Mode: config.ProbeModePull, URL: "u", Token: "t", Enabled: true,
		Poll: 60, Probe: 60, Timeout: 10, PinSHA256: "p", Resolvers: []string{"1.1.1.1:53"},
	}
	if !sameRemoteProbe(base, base) {
		t.Fatal("an entry must equal itself, or every reload restarts everything")
	}

	mutate := map[string]func(*config.RemoteProbe){
		"url":       func(r *config.RemoteProbe) { r.URL = "other" },
		"token":     func(r *config.RemoteProbe) { r.Token = "other" },
		"enabled":   func(r *config.RemoteProbe) { r.Enabled = false },
		"poll":      func(r *config.RemoteProbe) { r.Poll = 30 },
		"probe":     func(r *config.RemoteProbe) { r.Probe = 30 },
		"timeout":   func(r *config.RemoteProbe) { r.Timeout = 5 },
		"pin":       func(r *config.RemoteProbe) { r.PinSHA256 = "other" },
		"resolvers": func(r *config.RemoteProbe) { r.Resolvers = []string{"8.8.8.8:53"} },
		"resolver count": func(r *config.RemoteProbe) {
			r.Resolvers = append(r.Resolvers, "8.8.8.8:53")
		},
	}
	for name, fn := range mutate {
		t.Run(name, func(t *testing.T) {
			changed := base
			fn(&changed)
			if sameRemoteProbe(base, changed) {
				t.Fatalf("a change to %s must force a restart — the loop reads it once at start", name)
			}
		})
	}
}

// runningRemotes is the set of vantages with a live poll loop.
func (m *Monitor) runningRemotes() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]bool, len(m.remoteCancel))
	for name := range m.remoteCancel {
		out[name] = true
	}
	return out
}

// A reload before Start must still bring vantages up. Treating "config
// unchanged" as "already running" left them silently unpolled.
func TestReloadRemotesStartsLoopsThatAreNotRunning(t *testing.T) {
	cfg := probeCfg()
	cfg.RemoteProbes = []config.RemoteProbe{
		{Name: "a", Mode: config.ProbeModePull, URL: "http://127.0.0.1:1", Token: "t", Enabled: true, Poll: 3600},
	}
	m := New(cfg)
	defer m.Stop()

	// Note: no Start(). The config already lists the vantage, so before and
	// after are identical — the case that used to start nothing.
	m.ReloadRemotes(cfg)

	if got := m.runningRemotes(); !got["a"] {
		t.Fatalf("expected a poll loop for an unchanged-but-unstarted vantage, got %v", got)
	}
}

// A mode change has to restart: the two modes are different code paths, and
// leaving the old loop running would have hz dialling an agent that is now
// pushing to it.
func TestModeChangeRestarts(t *testing.T) {
	pull := config.RemoteProbe{Name: "a", Mode: config.ProbeModePull, URL: "u", Token: "t", Enabled: true}
	push := pull
	push.Mode = config.ProbeModePush
	if sameRemoteProbe(pull, push) {
		t.Fatal("a mode change must force a restart")
	}
}

// Push vantages have no poll loop, so nothing would notice one going quiet.
// The watchdog is what replaces reachability testing in that mode.
func TestPushWatchdogMarksSilentVantages(t *testing.T) {
	cfg := probeCfg()
	cfg.RemoteProbes = []config.RemoteProbe{
		{Name: "quiet", Mode: config.ProbeModePush, Token: "t", Enabled: true, Probe: 1},
	}
	m := New(cfg)
	defer m.Stop()

	rp := cfg.RemoteProbes[0]

	// Never reported is its own state — "not yet" is not "broken".
	m.sweepPushVantages()
	if got := m.GetStatus("ext:quiet:agent"); got != nil {
		t.Fatalf("a vantage that has never reported must not be marked failed: %+v", got)
	}

	// One report, then silence past three intervals.
	m.AcceptPushedResults(rp, "quiet", "v-test", []probe.Result{
		{Target: "h", Host: "h", Kind: probe.KindDNS, At: time.Now().UTC(), Status: StatusOK},
	})
	if got := m.GetStatus("ext:quiet:agent"); got == nil || got.Status != StatusOK {
		t.Fatalf("agent row after a report = %+v, want ok", got)
	}

	m.mu.Lock()
	m.remoteStates["quiet"].LastPoll = time.Now().Add(-time.Hour)
	m.mu.Unlock()

	m.sweepPushVantages()
	got := m.GetStatus("ext:quiet:agent")
	if got == nil || got.Status != StatusFailed {
		t.Fatalf("a vantage silent for an hour should be failed, got %+v", got)
	}
	if !strings.Contains(got.LastError, "no report") {
		t.Fatalf("the error should say it stopped reporting: %q", got.LastError)
	}
}

// The accepted count is the agent's watermark: over-count loses results,
// under-count duplicates them.
func TestAcceptedCountMatchesWhatWasFolded(t *testing.T) {
	cfg := probeCfg()
	m := New(cfg)
	defer m.Stop()
	rp := config.RemoteProbe{Name: "v", Mode: config.ProbeModePush, Token: "t", Enabled: true}

	base := time.Now().UTC()
	results := []probe.Result{
		{Target: "a", Host: "a", Kind: probe.KindDNS, At: base, Status: StatusOK},
		{Target: "a", Host: "a", Kind: probe.KindHTTPS, At: base.Add(time.Second), Status: StatusFailed, Error: "boom"},
	}
	if n := m.AcceptPushedResults(rp, "v", "v-test", results); n != len(results) {
		t.Fatalf("accepted %d of %d", n, len(results))
	}
	if m.GetStatus("ext:v:dns:a") == nil || m.GetStatus("ext:v:https:a") == nil {
		t.Fatal("both results should have become check rows")
	}
	if got := len(m.GetHistory("ext:v:https:a")); got != 1 {
		t.Fatalf("history has %d entries, want 1", got)
	}
}

// An outside vantage cannot resolve an internal-only name, so asking it to
// try produces a failure that means nothing. Found in production: 42 of 75
// rows were red for names working exactly as configured.
func TestPublicTargetsSkipInternalOnlyServices(t *testing.T) {
	cfg := &config.Config{
		SSLEnabled: true,
		PublicIP:   "203.0.113.10",
		Services: []config.Service{
			{Name: "public", Domains: []string{"www.example.com"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.1:80"}},
			{Name: "internal", Domains: []string{"admin.example.com"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.2:80", InternalOnly: true}},
		},
	}
	got := New(cfg).publicTargets()

	if len(got) != 1 {
		t.Fatalf("expected only the public service, got %+v", got)
	}
	if got[0].Host != "www.example.com" {
		t.Fatalf("kept the wrong target: %+v", got)
	}
}
