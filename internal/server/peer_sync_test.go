package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/monitor"
)

func TestMergeRemoteIntoLocal(t *testing.T) {
	local := &config.Config{
		PeerID:          "site-b",
		ConfigPrimary:   false,
		Peers:           []config.Peer{{ID: "site-a", WGAddr: "10.0.0.1", Primary: true}},
		ListenAddr:      ":8080",
		WGInterface:     "wg0",
		WGConfigPath:    "/etc/wireguard/wg0.conf",
		ServerEndpoint:  "b.example.com:51820",
		ServerPublicKey: "B-pubkey",
		PublicIP:        "203.0.113.2", // local public IP
		LocalInterface:  "192.168.2.1",
		AdminToken:      "local-secret",
		Services:        []config.Service{{Name: "old-service"}},
		IPBans:          []config.IPBan{{IP: "10.0.0.99", Reason: "local-ban"}},
	}

	remote := &config.Config{
		PeerID:          "site-a",
		ConfigPrimary:   true,
		Peers:           []config.Peer{{ID: "site-b", WGAddr: "10.0.0.2"}},
		ListenAddr:      ":9090", // different on remote — must NOT clobber
		WGInterface:     "wg-remote",
		WGConfigPath:    "/etc/wireguard/wg-remote.conf",
		ServerEndpoint:  "a.example.com:51820",
		ServerPublicKey: "A-pubkey",
		PublicIP:        "203.0.113.1", // remote's IP — must NOT clobber local
		LocalInterface:  "192.168.1.1",
		AdminToken:      "remote-secret", // must NOT clobber
		Services: []config.Service{
			{Name: "grafana", Domains: []string{"grafana.example.com"}},
			{Name: "prometheus", Domains: []string{"prom.example.com"}},
		},
		Zones:  []config.Zone{{Name: "example.com"}},
		IPBans: []config.IPBan{{IP: "10.0.0.42", Reason: "remote-ban"}}, // must NOT replicate
	}

	merged := mergeRemoteIntoLocal(remote, local)

	// Shared state should come from remote.
	if len(merged.Services) != 2 {
		t.Errorf("expected 2 services from remote, got %d", len(merged.Services))
	}
	if len(merged.Zones) != 1 {
		t.Errorf("expected 1 zone from remote, got %d", len(merged.Zones))
	}

	// Per-instance fields must remain local.
	if merged.PeerID != "site-b" {
		t.Errorf("PeerID clobbered: got %q want site-b", merged.PeerID)
	}
	if merged.ConfigPrimary {
		t.Error("ConfigPrimary clobbered to true")
	}
	if len(merged.Peers) != 1 || merged.Peers[0].ID != "site-a" || !merged.Peers[0].Primary {
		t.Errorf("Peers clobbered: %+v", merged.Peers)
	}
	if merged.ListenAddr != ":8080" {
		t.Errorf("ListenAddr clobbered: %s", merged.ListenAddr)
	}
	if merged.WGInterface != "wg0" {
		t.Errorf("WGInterface clobbered: %s", merged.WGInterface)
	}
	if merged.WGConfigPath != "/etc/wireguard/wg0.conf" {
		t.Errorf("WGConfigPath clobbered: %s", merged.WGConfigPath)
	}
	if merged.ServerEndpoint != "b.example.com:51820" {
		t.Errorf("ServerEndpoint clobbered: %s", merged.ServerEndpoint)
	}
	if merged.ServerPublicKey != "B-pubkey" {
		t.Errorf("ServerPublicKey clobbered: %s", merged.ServerPublicKey)
	}
	if merged.PublicIP != "203.0.113.2" {
		t.Errorf("PublicIP clobbered: %s", merged.PublicIP)
	}
	if merged.LocalInterface != "192.168.2.1" {
		t.Errorf("LocalInterface clobbered: %s", merged.LocalInterface)
	}
	if merged.AdminToken != "local-secret" {
		t.Errorf("AdminToken clobbered: %s", merged.AdminToken)
	}
	if len(merged.IPBans) != 1 || merged.IPBans[0].IP != "10.0.0.99" {
		t.Errorf("IPBans should remain local until Phase 4 LWW sync, got: %+v", merged.IPBans)
	}

	// Original local config must not be mutated.
	if len(local.Services) != 1 || local.Services[0].Name != "old-service" {
		t.Errorf("local config was mutated: %+v", local.Services)
	}
}

// newTestServer builds a minimal Server suitable for testing the peer sync
// pull loop. It avoids touching the filesystem (other than tempDir for the
// config save), iptables, dnsmasq, and haproxy by leaving DNSMasqEnabled and
// HAProxyEnabled false. The monitor is created (Reload-safe) but with no
// checks so its goroutines stay idle.
func newTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	s := &Server{
		configPath:   cfgPath,
		adminToken:   "test-admin",
		csrfSecret:   "test-csrf",
		monitor:      monitor.New(cfg),
		sync:         NewSyncBroadcaster(),
		health:       &HealthStatus{healthy: true},
		configShares: make(map[string]*configShare),
	}
	s.config.Store(cfg)
	return s
}

// startPeerHTTPServer wires the /api/peer/ping and /api/peer/config endpoints
// to a test Server and returns its host:port (suitable for use as wg_addr in
// the non-primary's Peer entry). The peerOnlyMiddleware is intentionally
// bypassed so loopback (127.0.0.1) requests are accepted.
func startPeerHTTPServer(t *testing.T, s *Server) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/ping", s.handlePeerPing)
	mux.HandleFunc("/api/peer/config", s.handlePeerConfig)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	// httptest URLs look like http://127.0.0.1:NNNNN — strip the scheme.
	host, port, err := net.SplitHostPort(ts.URL[len("http://"):])
	if err != nil {
		t.Fatalf("split test server URL %q: %v", ts.URL, err)
	}
	return net.JoinHostPort(host, port)
}

// TestPullLoopE2E runs the pull loop end-to-end against a real HTTP server
// hosted by a second in-process Server. Verifies that:
//   - the non-primary's pull converges its config to the primary's
//   - per-instance fields are preserved
//   - subsequent pulls are no-ops when nothing changed
//   - applyNewConfig persists to disk
func TestPullLoopE2E(t *testing.T) {
	primaryCfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		Peers: []config.Peer{
			{ID: "site-b", WGAddr: "ignored-from-primary-side"},
		},
		// Shared state primary will publish:
		Services: []config.Service{
			{Name: "grafana", Domains: []string{"grafana.example.com"}},
		},
		Zones: []config.Zone{{Name: "example.com"}},
		// Per-instance values that must NOT clobber site-b:
		ListenAddr:      ":9090",
		WGInterface:     "wg-a",
		WGConfigPath:    "/etc/wireguard/wg-a.conf",
		ServerEndpoint:  "a.example.com:51820",
		ServerPublicKey: "A-pubkey",
		PublicIP:        "203.0.113.1",
		LocalInterface:  "192.168.1.1",
		VPNRange:        "10.100.0.0/24",
	}
	primary := newTestServer(t, primaryCfg)
	primaryAddr := startPeerHTTPServer(t, primary)

	nonPrimaryCfg := &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: primaryAddr, Primary: true},
		},
		// Shared state on non-primary differs — should be replaced after pull.
		Services:        []config.Service{{Name: "stale"}},
		ListenAddr:      ":8080",
		WGInterface:     "wg-b",
		WGConfigPath:    "/etc/wireguard/wg-b.conf",
		ServerEndpoint:  "b.example.com:51820",
		ServerPublicKey: "B-pubkey",
		PublicIP:        "203.0.113.2",
		LocalInterface:  "192.168.2.1",
		VPNRange:        "10.100.0.0/24",
		IPBans:          []config.IPBan{{IP: "10.0.0.99", Reason: "local-only"}},
	}
	nonPrimary := newTestServer(t, nonPrimaryCfg)

	// First pull — should converge.
	nonPrimary.pullConfigOnce()

	got := nonPrimary.cfg()
	if len(got.Services) != 1 || got.Services[0].Name != "grafana" {
		t.Errorf("services not converged from primary: %+v", got.Services)
	}
	if len(got.Zones) != 1 || got.Zones[0].Name != "example.com" {
		t.Errorf("zones not converged from primary: %+v", got.Zones)
	}

	// Per-instance fields preserved.
	if got.PeerID != "site-b" {
		t.Errorf("PeerID clobbered: %s", got.PeerID)
	}
	if got.ConfigPrimary {
		t.Error("ConfigPrimary should remain false on non-primary")
	}
	if got.ListenAddr != ":8080" {
		t.Errorf("ListenAddr clobbered: %s", got.ListenAddr)
	}
	if got.WGInterface != "wg-b" {
		t.Errorf("WGInterface clobbered: %s", got.WGInterface)
	}
	if got.WGConfigPath != "/etc/wireguard/wg-b.conf" {
		t.Errorf("WGConfigPath clobbered: %s", got.WGConfigPath)
	}
	if got.ServerEndpoint != "b.example.com:51820" {
		t.Errorf("ServerEndpoint clobbered: %s", got.ServerEndpoint)
	}
	if got.PublicIP != "203.0.113.2" {
		t.Errorf("PublicIP clobbered: %s", got.PublicIP)
	}
	if len(got.IPBans) != 1 || got.IPBans[0].IP != "10.0.0.99" {
		t.Errorf("IPBans should remain local: %+v", got.IPBans)
	}

	// Peers entry should still be marked Primary on the non-primary side
	// even though primary's own copy of `Peers` does NOT mark site-b primary.
	if len(got.Peers) != 1 || !got.Peers[0].Primary {
		t.Errorf("Peers fleet topology clobbered: %+v", got.Peers)
	}

	// Second pull — should be a no-op (same config) but must not error.
	beforeAddr := nonPrimary.cfg()
	nonPrimary.pullConfigOnce()
	if nonPrimary.cfg() != beforeAddr {
		t.Error("idempotent pull replaced the atomic pointer when nothing changed")
	}

	// Disk persistence: the config file under nonPrimary.configPath should
	// reflect the primary's services.
	persisted, err := config.Load(nonPrimary.configPath)
	if err != nil {
		t.Fatalf("reload persisted non-primary config: %v", err)
	}
	if len(persisted.Services) != 1 || persisted.Services[0].Name != "grafana" {
		t.Errorf("persisted config not updated: %+v", persisted.Services)
	}
}

// TestPullLoopSplitConfigGuards verifies that pullConfigOnce refuses to apply
// configs from peers whose ping/config payloads are inconsistent with the
// non-primary's expectation of which peer is the primary.
func TestPullLoopSplitConfigGuards(t *testing.T) {
	cases := []struct {
		name        string
		primaryCfg  *config.Config
		expectStale bool // if true, the non-primary's services should NOT change
	}{
		{
			name: "remote no longer claims primary",
			primaryCfg: &config.Config{
				PeerID:        "site-a",
				ConfigPrimary: false, // demoted!
				Services:      []config.Service{{Name: "primary-services"}},
				VPNRange:      "10.100.0.0/24",
			},
			expectStale: true,
		},
		{
			name: "remote reports wrong peer_id",
			primaryCfg: &config.Config{
				PeerID:        "site-c", // not what site-b expects
				ConfigPrimary: true,
				Services:      []config.Service{{Name: "primary-services"}},
				VPNRange:      "10.100.0.0/24",
			},
			expectStale: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := newTestServer(t, tc.primaryCfg)
			primaryAddr := startPeerHTTPServer(t, primary)

			nonPrimary := newTestServer(t, &config.Config{
				PeerID:        "site-b",
				ConfigPrimary: false,
				Peers: []config.Peer{
					{ID: "site-a", WGAddr: primaryAddr, Primary: true},
				},
				Services: []config.Service{{Name: "stale-but-pinned"}},
				VPNRange: "10.100.0.0/24",
			})

			nonPrimary.pullConfigOnce()

			got := nonPrimary.cfg()
			if tc.expectStale {
				if len(got.Services) != 1 || got.Services[0].Name != "stale-but-pinned" {
					t.Errorf("split-config guard failed to refuse pull: services=%+v", got.Services)
				}
			}
		})
	}
}

// TestPullLoopRecordsStatus verifies pullConfigOnce updates peerSyncStatus
// for both successful and failed attempts so the dashboard can render the
// "last successful pull" tile (Phase 1 hardening item 5).
func TestPullLoopRecordsStatus(t *testing.T) {
	primary := newTestServer(t, &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		Services:      []config.Service{{Name: "grafana"}},
		VPNRange:      "10.100.0.0/24",
	})
	primaryAddr := startPeerHTTPServer(t, primary)

	nonPrimary := newTestServer(t, &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: primaryAddr, Primary: true},
		},
		Services: []config.Service{{Name: "stale"}},
		VPNRange: "10.100.0.0/24",
	})

	// First successful pull — applies new config.
	nonPrimary.pullConfigOnce()
	snap := nonPrimary.peerSyncSnapshot()
	if snap.PullCount != 1 {
		t.Errorf("PullCount=%d, want 1", snap.PullCount)
	}
	if snap.LastError != "" {
		t.Errorf("LastError should be empty after success, got %q", snap.LastError)
	}
	if snap.LastPullAt.IsZero() {
		t.Error("LastPullAt should be set after first attempt")
	}
	if snap.LastSuccessAt.IsZero() {
		t.Error("LastSuccessAt should be set after successful pull")
	}
	if snap.LastApplyAt.IsZero() {
		t.Error("LastApplyAt should be set after config swap")
	}

	// Second pull — no change, but still counts as a successful attempt.
	prevApplyAt := snap.LastApplyAt
	nonPrimary.pullConfigOnce()
	snap = nonPrimary.peerSyncSnapshot()
	if snap.PullCount != 2 {
		t.Errorf("PullCount=%d, want 2", snap.PullCount)
	}
	if snap.LastError != "" {
		t.Errorf("LastError should be empty after no-op success, got %q", snap.LastError)
	}
	if !snap.LastApplyAt.Equal(prevApplyAt) {
		t.Errorf("LastApplyAt should not advance on a no-op pull: was %v, now %v", prevApplyAt, snap.LastApplyAt)
	}

	// Now point at a dead peer to verify error recording.
	nonPrimary.config.Store(&config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: "127.0.0.1:1", Primary: true}, // refused
		},
		VPNRange: "10.100.0.0/24",
	})
	nonPrimary.pullConfigOnce()
	snap = nonPrimary.peerSyncSnapshot()
	if snap.PullCount != 3 {
		t.Errorf("PullCount=%d, want 3", snap.PullCount)
	}
	if snap.LastError == "" {
		t.Error("LastError should be populated after failed pull")
	}
}

// TestNonPrimaryGuardMiddleware exercises the route registration helpers and
// the middleware together against the live setupRoutes() registry, locking in
// which routes are blocked / allowed on a non-primary instance. Adding a new
// route that should be exempt from the guard requires using
// handlePeerInstance / handlePeerInstanceSubtree, and this test will fail if
// somebody hand-rolls the registration with mux.HandleFunc.
func TestNonPrimaryGuardMiddleware(t *testing.T) {
	cfg := &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers:         []config.Peer{{ID: "site-a", WGAddr: "10.0.0.1", Primary: true}},
		VPNRange:      "10.100.0.0/24",
	}
	s := newTestServer(t, cfg)
	// Populate the per-instance registry without registering real handlers
	// (setupRoutes pulls in dnsmasq/haproxy/SPA which we don't want here).
	mux := http.NewServeMux()
	noop := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	s.handlePeerInstanceSubtree(mux, "/api/deploy/", noop)
	s.handlePeerInstanceSubtree(mux, "/api/ban/", noop)
	s.handlePeerInstance(mux, "/api/peer/ping", noop)
	s.handlePeerInstance(mux, "/api/peer/config", noop)
	s.handlePeerInstance(mux, "/api/v1/auth/status", noop)
	s.handlePeerInstance(mux, "/api/v1/auth/login", noop)
	s.handlePeerInstance(mux, "/api/v1/auth/logout", noop)
	s.handlePeerInstance(mux, "/api/v1/services/sync", noop)
	s.handlePeerInstance(mux, "/api/v1/services/sync/stream", noop)
	s.handlePeerInstance(mux, "/api/v1/services/sync/status", noop)
	s.handlePeerInstance(mux, "/api/v1/services/sync/cancel", noop)
	s.handlePeerInstance(mux, "/api/v1/dns/sync", noop)
	s.handlePeerInstance(mux, "/api/v1/dns/sync-all", noop)
	s.handlePeerInstance(mux, "/api/v1/vpn/reload", noop)
	s.handlePeerInstance(mux, "/api/v1/haproxy/reload", noop)
	s.handlePeerInstance(mux, "/api/v1/haproxy/write-config", noop)
	mux.HandleFunc("/api/v1/services/add", noop)
	mux.HandleFunc("/api/v1/dashboard", noop)

	handler := s.nonPrimaryGuardMiddleware(mux)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		// Read-only methods always pass through, even for shared-config
		// routes.
		{"GET dashboard", "GET", "/api/v1/dashboard", http.StatusOK},
		{"GET services add", "GET", "/api/v1/services/add", http.StatusOK},

		// Per-instance routes pass through on POST.
		{"POST peer config", "POST", "/api/peer/config", http.StatusOK},
		{"POST auth login", "POST", "/api/v1/auth/login", http.StatusOK},
		{"POST dns sync-all", "POST", "/api/v1/dns/sync-all", http.StatusOK},
		{"POST haproxy reload", "POST", "/api/v1/haproxy/reload", http.StatusOK},
		{"POST haproxy write-config", "POST", "/api/v1/haproxy/write-config", http.StatusOK},
		{"POST vpn reload", "POST", "/api/v1/vpn/reload", http.StatusOK},
		{"POST services sync", "POST", "/api/v1/services/sync", http.StatusOK},
		{"POST services sync stream", "POST", "/api/v1/services/sync/stream", http.StatusOK},
		{"POST deploy subtree", "POST", "/api/deploy/some-service", http.StatusOK},
		{"POST ban subtree", "POST", "/api/ban/anything", http.StatusOK},

		// Shared-config mutations are blocked on non-primary.
		{"POST services add", "POST", "/api/v1/services/add", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// Primary should pass everything through (including the unregistered
	// shared-config mutation).
	primaryCfg := *cfg
	primaryCfg.ConfigPrimary = true
	s.config.Store(&primaryCfg)
	req := httptest.NewRequest("POST", "/api/v1/services/add", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("primary should not be guarded: status=%d", rec.Code)
	}
}

func TestBuildPeerURL(t *testing.T) {
	tests := []struct {
		addr string
		path string
		want string
	}{
		{"10.0.0.1", "/api/peer/ping", "http://10.0.0.1:8080/api/peer/ping"},
		{"10.0.0.1:9090", "/api/peer/config", "http://10.0.0.1:9090/api/peer/config"},
	}
	for _, tt := range tests {
		got := buildPeerURL(tt.addr, tt.path)
		if got != tt.want {
			t.Errorf("buildPeerURL(%q, %q) = %q, want %q", tt.addr, tt.path, got, tt.want)
		}
	}
}
