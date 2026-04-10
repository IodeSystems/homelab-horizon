package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
	// IPBans are now shared state (Phase 4 LWW sync) — they come from remote.
	if len(merged.IPBans) != 1 || merged.IPBans[0].IP != "10.0.0.42" {
		t.Errorf("IPBans should come from remote (shared state), got: %+v", merged.IPBans)
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
	// IPBans are now shared state (Phase 4 LWW sync) — pulled from primary.
	// The primary config has no bans, so non-primary's local ban is replaced.
	if len(got.IPBans) != 0 {
		t.Errorf("IPBans should match primary (empty): %+v", got.IPBans)
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

func TestIsAllowedPeer(t *testing.T) {
	// With peers configured: only exact wg_addr hosts are allowed.
	s := newTestServer(t, &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		Peers: []config.Peer{
			{ID: "site-b", WGAddr: "10.0.0.2"},
			{ID: "site-c", WGAddr: "10.0.0.3:9090"},
		},
		VPNRange: "10.0.0.0/24",
	})

	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.2", true},  // exact match, no port in config
		{"10.0.0.3", true},  // host extracted from host:port config
		{"10.0.0.4", false}, // in VPN CIDR but not a configured peer
		{"192.168.1.1", false},
	}
	for _, tc := range cases {
		got := s.isAllowedPeer(tc.ip)
		if got != tc.want {
			t.Errorf("isAllowedPeer(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}

	// Without peers configured: falls back to VPN CIDR.
	s2 := newTestServer(t, &config.Config{
		PeerID:   "standalone",
		VPNRange: "10.0.0.0/24",
	})
	if !s2.isAllowedPeer("10.0.0.5") {
		t.Error("no peers configured: VPN CIDR address should be allowed")
	}
	if s2.isAllowedPeer("192.168.1.1") {
		t.Error("no peers configured: non-VPN address should be rejected")
	}
}

// TestPeerOnlyMiddlewareRejectsUnlistedPeer is the integration test for
// the tightened peer auth (Phase 2 item 2). It wires up the middleware
// with an HTTP handler and asserts that an in-CIDR-but-not-listed peer
// gets 403.
func TestPeerOnlyMiddlewareRejectsUnlistedPeer(t *testing.T) {
	s := newTestServer(t, &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		Peers: []config.Peer{
			{ID: "site-b", WGAddr: "10.0.0.2"},
		},
		VPNRange: "10.0.0.0/24",
	})

	handler := s.peerOnlyMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Listed peer — allowed.
	req := httptest.NewRequest("GET", "/api/peer/ping", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("listed peer: got %d, want 200", rec.Code)
	}

	// In CIDR but not listed — rejected.
	req = httptest.NewRequest("GET", "/api/peer/ping", nil)
	req.RemoteAddr = "10.0.0.99:12345"
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("unlisted in-CIDR peer: got %d, want 403", rec.Code)
	}
}

func TestAlivePeers(t *testing.T) {
	// Stand up two peers as HTTP servers.
	peerB := newTestServer(t, &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		VPNRange:      "10.100.0.0/24",
	})
	addrB := startPeerHTTPServer(t, peerB)

	peerC := newTestServer(t, &config.Config{
		PeerID:        "site-c",
		ConfigPrimary: false,
		VPNRange:      "10.100.0.0/24",
	})
	addrC := startPeerHTTPServer(t, peerC)

	// site-a knows about site-b (reachable) and site-c (reachable) and
	// site-d (dead — unreachable loopback port).
	siteA := newTestServer(t, &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		Peers: []config.Peer{
			{ID: "site-b", WGAddr: addrB},
			{ID: "site-c", WGAddr: addrC},
			{ID: "site-d", WGAddr: "127.0.0.1:1"}, // dead
		},
		VPNRange: "10.100.0.0/24",
	})

	alive := siteA.alivePeers()

	// Should include self + two reachable peers, sorted.
	want := []string{"site-a", "site-b", "site-c"}
	if len(alive) != len(want) {
		t.Fatalf("alivePeers() = %v, want %v", alive, want)
	}
	for i := range want {
		if alive[i] != want[i] {
			t.Errorf("alivePeers()[%d] = %q, want %q", i, alive[i], want[i])
		}
	}
}

func TestAlivePeersStandalone(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	alive := s.alivePeers()
	if alive != nil {
		t.Errorf("standalone instance should return nil, got %v", alive)
	}
}

func TestHandlePeerCert(t *testing.T) {
	// Set up a cert on disk in the expected layout.
	certDir := t.TempDir()
	domainDir := filepath.Join(certDir, "live", "example.com")
	if err := os.MkdirAll(domainDir, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(domainDir, "fullchain.pem"), []byte("CERT-DATA"), 0600)
	os.WriteFile(filepath.Join(domainDir, "privkey.pem"), []byte("KEY-DATA"), 0600)

	cfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		SSLCertDir:    certDir,
		VPNRange:      "10.100.0.0/24",
	}
	s := newTestServer(t, cfg)

	// Wire up handler without peer middleware (testing the handler logic,
	// not the auth — that's covered by TestPeerOnlyMiddlewareRejectsUnlistedPeer).
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/cert/", s.handlePeerCert)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Fetch existing cert.
	resp, err := http.Get(ts.URL + "/api/peer/cert/*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var cr PeerCertResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if cr.Domain != "*.example.com" {
		t.Errorf("domain = %q, want *.example.com", cr.Domain)
	}
	if cr.Cert != "CERT-DATA" {
		t.Errorf("cert = %q, want CERT-DATA", cr.Cert)
	}
	if cr.Key != "KEY-DATA" {
		t.Errorf("key = %q, want KEY-DATA", cr.Key)
	}

	// Missing domain returns 404.
	resp2, err := http.Get(ts.URL + "/api/peer/cert/*.nonexistent.com")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("missing cert: status = %d, want 404", resp2.StatusCode)
	}

	// Empty domain returns 400.
	resp3, err := http.Get(ts.URL + "/api/peer/cert/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("empty domain: status = %d, want 400", resp3.StatusCode)
	}
}

// startPeerHTTPServerWithCerts is like startPeerHTTPServer but also registers
// the /api/peer/cert/ endpoint so cert-pull tests can fetch certs.
func startPeerHTTPServerWithCerts(t *testing.T, s *Server) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/ping", s.handlePeerPing)
	mux.HandleFunc("/api/peer/config", s.handlePeerConfig)
	mux.HandleFunc("/api/peer/cert/", s.handlePeerCert)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	host, port, err := net.SplitHostPort(ts.URL[len("http://"):])
	if err != nil {
		t.Fatalf("split test server URL %q: %v", ts.URL, err)
	}
	return net.JoinHostPort(host, port)
}

// TestCertOwnershipShiftOnPeerDown is the Phase 2 end-to-end test (item 7).
// It sets up two peers (site-a, site-b), each with a cert dir. We verify:
//   1. With both alive, certOwner deterministically assigns each domain to one.
//   2. The non-owner peer pulls the cert from the owner.
//   3. When the owner dies (server shut down), ownership shifts to the
//      surviving peer, which now takes the "renew" code path instead of "pull".
func TestCertOwnershipShiftOnPeerDown(t *testing.T) {
	// Create cert dirs and seed site-a with a cert.
	certDirA := t.TempDir()
	domainDir := filepath.Join(certDirA, "live", "example.com")
	os.MkdirAll(domainDir, 0700)
	os.WriteFile(filepath.Join(domainDir, "fullchain.pem"), []byte("CERT-A"), 0600)
	os.WriteFile(filepath.Join(domainDir, "privkey.pem"), []byte("KEY-A"), 0600)

	certDirB := t.TempDir()
	haproxyDirB := t.TempDir()

	// Stand up site-a as an HTTP server.
	cfgA := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		SSLEnabled:    true,
		SSLCertDir:    certDirA,
		VPNRange:      "10.100.0.0/24",
	}
	srvA := newTestServer(t, cfgA)
	addrA := startPeerHTTPServerWithCerts(t, srvA)

	// Configure site-b to know about site-a.
	cfgB := &config.Config{
		PeerID:          "site-b",
		ConfigPrimary:   false,
		SSLEnabled:      true,
		SSLCertDir:      certDirB,
		SSLHAProxyCertDir: haproxyDirB,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: addrA, Primary: true},
		},
		VPNRange: "10.100.0.0/24",
	}
	srvB := newTestServer(t, cfgB)

	// Also update site-a to know about site-b (for alivePeers symmetry).
	// site-b doesn't have an HTTP server for this test — site-a doesn't
	// need to reach it for the ownership logic to work.
	cfgA.Peers = []config.Peer{
		{ID: "site-b", WGAddr: "127.0.0.1:1"}, // unreachable is fine for this test
	}
	srvA.config.Store(cfgA)

	// --- Phase 1: Both alive, determine ownership ---
	aliveFromB := srvB.alivePeers()
	if len(aliveFromB) != 2 {
		t.Fatalf("expected 2 alive peers from site-b, got %v", aliveFromB)
	}

	domain := "*.example.com"
	owner := certOwner(domain, aliveFromB)
	t.Logf("owner of %s with both alive: %s", domain, owner)

	// Verify both peers agree on the owner (deterministic).
	// site-a sees site-b as dead (port 1), but for the determinism check
	// we use the same alive list.
	ownerAgain := certOwner(domain, aliveFromB)
	if owner != ownerAgain {
		t.Errorf("ownership not deterministic: %s vs %s", owner, ownerAgain)
	}

	// --- Phase 2: Non-owner pulls cert from owner ---
	if owner == "site-a" {
		// site-b is the non-owner and should pull from site-a.
		err := srvB.pullCertFromPeer(domain, "site-a", cfgB)
		if err != nil {
			t.Fatalf("pullCertFromPeer: %v", err)
		}
		// Verify cert landed on site-b's disk.
		cert, _ := os.ReadFile(filepath.Join(certDirB, "live", "example.com", "fullchain.pem"))
		if string(cert) != "CERT-A" {
			t.Errorf("pulled cert = %q, want CERT-A", cert)
		}
	} else {
		// site-b owns the domain — it would renew (can't test actual LE
		// issuance here, but the code path is exercised). The pull path
		// is already covered by TestPullCertFromPeer.
		t.Log("site-b owns domain — pull path not exercised in this branch (covered by TestPullCertFromPeer)")
	}

	// --- Phase 3: site-a goes down, ownership shifts ---
	// Simulate site-a dying by pointing site-b's peer entry to a dead port.
	cfgBDead := *cfgB
	cfgBDead.Peers = []config.Peer{
		{ID: "site-a", WGAddr: "127.0.0.1:1", Primary: true}, // dead
	}
	srvB.config.Store(&cfgBDead)

	aliveAfterDeath := srvB.alivePeers()
	if len(aliveAfterDeath) != 1 || aliveAfterDeath[0] != "site-b" {
		t.Fatalf("after site-a death, expected [site-b], got %v", aliveAfterDeath)
	}

	ownerAfterDeath := certOwner(domain, aliveAfterDeath)
	if ownerAfterDeath != "site-b" {
		t.Errorf("after site-a death, owner = %q, want site-b (sole survivor)", ownerAfterDeath)
	}

	// With site-b as the sole survivor and owner, the renewal sweep would
	// take the "renew" code path (not "pull"). We can't invoke actual LE
	// here, but we've verified that:
	// - alivePeers correctly excludes the dead peer
	// - certOwner shifts to the surviving peer
	// - The sweep logic branches on owner == self (tested structurally)
	t.Log("ownership shifted to site-b — renewal path would execute")
}

func TestPullCertFromPeer(t *testing.T) {
	// Set up the owner peer with a cert on disk.
	ownerCertDir := t.TempDir()
	domainDir := filepath.Join(ownerCertDir, "live", "example.com")
	os.MkdirAll(domainDir, 0700)
	os.WriteFile(filepath.Join(domainDir, "fullchain.pem"), []byte("OWNER-CERT"), 0600)
	os.WriteFile(filepath.Join(domainDir, "privkey.pem"), []byte("OWNER-KEY"), 0600)

	ownerCfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		SSLCertDir:    ownerCertDir,
		VPNRange:      "10.100.0.0/24",
	}
	owner := newTestServer(t, ownerCfg)

	// Start HTTP server with the cert endpoint (bypass peer auth for test).
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/ping", owner.handlePeerPing)
	mux.HandleFunc("/api/peer/config", owner.handlePeerConfig)
	mux.HandleFunc("/api/peer/cert/", owner.handlePeerCert)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ownerAddr := ts.URL[len("http://"):]

	// Set up the pulling peer with empty cert dirs.
	pullerCertDir := t.TempDir()
	pullerHADir := t.TempDir()
	pullerCfg := &config.Config{
		PeerID:          "site-b",
		ConfigPrimary:   false,
		SSLCertDir:      pullerCertDir,
		SSLHAProxyCertDir: pullerHADir,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: ownerAddr, Primary: true},
		},
		VPNRange: "10.100.0.0/24",
	}
	puller := newTestServer(t, pullerCfg)

	// Pull the cert.
	err := puller.pullCertFromPeer("*.example.com", "site-a", pullerCfg)
	if err != nil {
		t.Fatalf("pullCertFromPeer: %v", err)
	}

	// Verify cert written to disk.
	cert, err := os.ReadFile(filepath.Join(pullerCertDir, "live", "example.com", "fullchain.pem"))
	if err != nil {
		t.Fatalf("read pulled cert: %v", err)
	}
	if string(cert) != "OWNER-CERT" {
		t.Errorf("cert = %q, want OWNER-CERT", cert)
	}

	key, err := os.ReadFile(filepath.Join(pullerCertDir, "live", "example.com", "privkey.pem"))
	if err != nil {
		t.Fatalf("read pulled key: %v", err)
	}
	if string(key) != "OWNER-KEY" {
		t.Errorf("key = %q, want OWNER-KEY", key)
	}

	// Verify HAProxy combined cert written.
	combined, err := os.ReadFile(filepath.Join(pullerHADir, "example.com.pem"))
	if err != nil {
		t.Fatalf("read haproxy cert: %v", err)
	}
	if string(combined) != "OWNER-CERTOWNER-KEY" {
		t.Errorf("haproxy combined = %q, want OWNER-CERTOWNER-KEY", combined)
	}

	// Pulling from unknown peer returns error.
	err = puller.pullCertFromPeer("*.example.com", "site-unknown", pullerCfg)
	if err == nil {
		t.Error("expected error for unknown peer")
	}
}

func TestMergeBansLWW(t *testing.T) {
	local := []config.IPBan{
		{IP: "1.1.1.1", CreatedAt: 100, Reason: "local-old"},
		{IP: "2.2.2.2", CreatedAt: 200, Reason: "local-only"},
	}
	remote := []config.IPBan{
		{IP: "1.1.1.1", CreatedAt: 150, Reason: "remote-newer"},
		{IP: "3.3.3.3", CreatedAt: 300, Reason: "remote-only"},
	}

	merged := mergeBansLWW(local, remote)

	byIP := make(map[string]config.IPBan)
	for _, b := range merged {
		byIP[b.IP] = b
	}

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged bans, got %d: %+v", len(merged), merged)
	}

	// 1.1.1.1: remote is newer (150 > 100)
	if byIP["1.1.1.1"].Reason != "remote-newer" {
		t.Errorf("1.1.1.1 should be remote-newer, got %q", byIP["1.1.1.1"].Reason)
	}

	// 2.2.2.2: local-only
	if byIP["2.2.2.2"].Reason != "local-only" {
		t.Errorf("2.2.2.2 should be local-only, got %q", byIP["2.2.2.2"].Reason)
	}

	// 3.3.3.3: remote-only
	if byIP["3.3.3.3"].Reason != "remote-only" {
		t.Errorf("3.3.3.3 should be remote-only, got %q", byIP["3.3.3.3"].Reason)
	}
}

func TestBanSyncViaPeerState(t *testing.T) {
	// Primary has a ban.
	primaryCfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		VPNRange:      "10.100.0.0/24",
		IPBans: []config.IPBan{
			{IP: "5.5.5.5", CreatedAt: 1000, Reason: "primary-ban"},
		},
	}
	primary := newTestServer(t, primaryCfg)

	// Start HTTP server with state endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/ping", primary.handlePeerPing)
	mux.HandleFunc("/api/peer/config", primary.handlePeerConfig)
	mux.HandleFunc("/api/peer/state", primary.handlePeerState)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	primaryAddr := ts.URL[len("http://"):]

	// Non-primary has a different ban.
	nonPrimaryCfg := &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: primaryAddr, Primary: true},
		},
		VPNRange: "10.100.0.0/24",
		IPBans: []config.IPBan{
			{IP: "6.6.6.6", CreatedAt: 900, Reason: "local-ban"},
		},
	}
	nonPrimary := newTestServer(t, nonPrimaryCfg)

	// Run ban sync once.
	nonPrimary.banSyncOnce()

	got := nonPrimary.cfg()
	if len(got.IPBans) != 2 {
		t.Fatalf("expected 2 merged bans, got %d: %+v", len(got.IPBans), got.IPBans)
	}

	byIP := make(map[string]config.IPBan)
	for _, b := range got.IPBans {
		byIP[b.IP] = b
	}
	if _, ok := byIP["5.5.5.5"]; !ok {
		t.Error("missing primary ban 5.5.5.5")
	}
	if _, ok := byIP["6.6.6.6"]; !ok {
		t.Error("missing local ban 6.6.6.6")
	}
}

func TestWGPeersReplicateViaPullLoop(t *testing.T) {
	// Primary has WG peers in config.
	primaryCfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		VPNRange:      "10.100.0.0/24",
		WGPeers: []config.WGPeer{
			{Name: "alice", PublicKey: "alice-pubkey-abc", AllowedIPs: "10.100.0.2/32"},
			{Name: "bob", PublicKey: "bob-pubkey-xyz", AllowedIPs: "10.100.0.3/32"},
		},
	}
	primary := newTestServer(t, primaryCfg)
	primaryAddr := startPeerHTTPServer(t, primary)

	// Non-primary starts with no WG peers.
	nonPrimaryCfg := &config.Config{
		PeerID:        "site-b",
		ConfigPrimary: false,
		Peers: []config.Peer{
			{ID: "site-a", WGAddr: primaryAddr, Primary: true},
		},
		VPNRange: "10.100.0.0/24",
		WGPeers:  nil, // empty
	}
	nonPrimary := newTestServer(t, nonPrimaryCfg)

	// Pull should converge WGPeers.
	nonPrimary.pullConfigOnce()

	got := nonPrimary.cfg()
	if len(got.WGPeers) != 2 {
		t.Fatalf("expected 2 WG peers after pull, got %d", len(got.WGPeers))
	}
	if got.WGPeers[0].Name != "alice" || got.WGPeers[1].Name != "bob" {
		t.Errorf("WG peers not replicated correctly: %+v", got.WGPeers)
	}

	// Verify persisted to disk.
	persisted, err := config.Load(nonPrimary.configPath)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if len(persisted.WGPeers) != 2 {
		t.Errorf("persisted WGPeers count = %d, want 2", len(persisted.WGPeers))
	}
}

func TestCertOwner(t *testing.T) {
	peers := []string{"site-a", "site-b", "site-c"}

	// Deterministic: same inputs always produce same output.
	owner1 := certOwner("*.example.com", peers)
	owner2 := certOwner("*.example.com", peers)
	if owner1 != owner2 {
		t.Errorf("certOwner not deterministic: %q vs %q", owner1, owner2)
	}

	// Result is always one of the alive peers.
	for _, domain := range []string{"*.example.com", "*.other.io", "grafana.test"} {
		owner := certOwner(domain, peers)
		found := false
		for _, p := range peers {
			if p == owner {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("certOwner(%q) = %q, not in peers %v", domain, owner, peers)
		}
	}

	// When a peer dies, ownership shifts deterministically.
	ownerFull := certOwner("*.example.com", []string{"site-a", "site-b", "site-c"})
	ownerReduced := certOwner("*.example.com", []string{"site-a", "site-c"})
	// We don't assert which peer owns it, just that the function returns
	// a valid peer from the reduced set.
	found := false
	for _, p := range []string{"site-a", "site-c"} {
		if p == ownerReduced {
			found = true
		}
	}
	if !found {
		t.Errorf("certOwner with reduced peers returned %q, not in [site-a, site-c]", ownerReduced)
	}
	_ = ownerFull // used for documentation, not assertion

	// Empty peers returns empty string.
	if got := certOwner("*.example.com", nil); got != "" {
		t.Errorf("certOwner with nil peers = %q, want empty", got)
	}

	// Distribution: with enough domains, both peers should get at least one.
	twopeers := []string{"alpha", "beta"}
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		d := fmt.Sprintf("domain-%d.example.com", i)
		counts[certOwner(d, twopeers)]++
	}
	if counts["alpha"] == 0 || counts["beta"] == 0 {
		t.Errorf("poor distribution: %v", counts)
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
