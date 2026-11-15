package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/monitor"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// The skip rules in child_sync.go: what a sync — and hz's other automatic
// paths — must not do on a nested hz.

// recSyncLogger records what a sync says, in order.
type recSyncLogger struct {
	lines []string
	done  []bool
}

func (l *recSyncLogger) Info(m string)    { l.lines = append(l.lines, "info: "+m) }
func (l *recSyncLogger) Success(m string) { l.lines = append(l.lines, "success: "+m) }
func (l *recSyncLogger) Warning(m string) { l.lines = append(l.lines, "warning: "+m) }
func (l *recSyncLogger) Error(m string)   { l.lines = append(l.lines, "error: "+m) }
func (l *recSyncLogger) Step(m string)    { l.lines = append(l.lines, "step: "+m) }
func (l *recSyncLogger) Done(ok bool)     { l.done = append(l.done, ok) }

func (l *recSyncLogger) text() string { return strings.Join(l.lines, "\n") }

// childTestConfig is a seeded child as the bootstrap leaves it: an upstream
// block, the defaults for everything else (dnsmasq ON, HAProxy and SSL off),
// and every dnsmasq path inside the test's own directory.
func childTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	dir := t.TempDir()
	cfg.DNSMasqConfigPath = filepath.Join(dir, "wg-vpn.conf")
	cfg.DNSMasqHostsPath = filepath.Join(dir, "wg-hosts.conf")
	cfg.UsersDB = filepath.Join(dir, "hz.db")
	cfg.Upstream = &config.UpstreamHZ{
		URL: "http://10.100.0.1:8080", TokenFile: filepath.Join(dir, "upstream.token"),
		Rungs: []config.UpstreamRung{{Project: "redline", Environment: "loadtest"}},
	}
	return cfg
}

// RULE: a child never starts dnsmasq. Not running → skipped (say why); running
// → hz's as on any hz. A parent is unaffected.
func TestChildDNSMasqRule(t *testing.T) {
	for _, tc := range []struct {
		child, running, skip bool
	}{
		{child: true, running: false, skip: true},
		{child: true, running: true, skip: false},
		{child: false, running: false, skip: false},
		{child: false, running: true, skip: false},
	} {
		if got := childDNSMasqSkip(tc.child, tc.running) != ""; got != tc.skip {
			t.Errorf("child=%v running=%v: skip %v, want %v", tc.child, tc.running, got, tc.skip)
		}
	}
}

// The same rule at startup: a child with dnsmasq installed but stopped is not
// started and its config is not written; the row is "disabled", so a child
// does not boot DEGRADED for it. A parent in the same state IS started.
func TestStartupDoesNotStartDNSMasqOnAChild(t *testing.T) {
	obs := subsystemObservation{DNSEnabled: true, DNSBinaryOnPath: true, DNSRunning: false}

	obs.DNSChild = true
	p := planDNSMasq(obs)
	if p.Start || p.WriteConfig || p.Skip == "" {
		t.Fatalf("child, stopped: %+v — hz must not start dnsmasq", p)
	}
	if st := skipStatus(p.Skip); st != monitor.StatusDisabled {
		t.Fatalf("child skip classified %s, want disabled", st)
	}

	obs.DNSRunning = true
	if p := planDNSMasq(obs); p.Skip != "" || p.Start {
		t.Fatalf("child, running: %+v — left as it is", p)
	}

	obs.DNSChild, obs.DNSRunning = false, false
	if p := planDNSMasq(obs); !p.Start {
		t.Fatalf("parent, stopped: %+v — the rule must not reach a parent", p)
	}
}

// The sync itself on a child whose dnsmasq is stopped: no file written (so no
// unit, no restart — those come after the write) and the reason in the log.
func TestAChildSyncSkipsAStoppedDNSMasq(t *testing.T) {
	cfg := childTestConfig(t)
	s := newTestServer(t, cfg)
	probed := 0
	s.dnsRunningProbe = func() bool { probed++; return false }

	log := &recSyncLogger{}
	s.runSyncInternal(log, nil)

	if probed == 0 {
		t.Fatal("instrument: the sync never asked whether dnsmasq runs")
	}
	if _, err := os.Stat(cfg.DNSMasqConfigPath); !os.IsNotExist(err) {
		t.Fatalf("the sync wrote %s on a child with dnsmasq stopped (%v)", cfg.DNSMasqConfigPath, err)
	}
	if !strings.Contains(log.text(), childDNSMasqSkipPrefix) {
		t.Fatalf("the skip is not said:\n%s", log.text())
	}
	if strings.Contains(log.text(), "Syncing DNSMasq") {
		t.Fatalf("the dnsmasq step ran:\n%s", log.text())
	}
}

// RULE: a child's own WireGuard settings may not name the upstream tunnel —
// refused at Save and at start, so hz never manages hz-upstream. The seed's
// defaults (wg0) pass.
func TestAChildMayNotManageItsUpstreamTunnel(t *testing.T) {
	ok := childTestConfig(t)
	if err := ok.UpstreamTunnelCollision(); err != nil {
		t.Fatalf("a seeded child (wg0 defaults) is refused: %v", err)
	}
	if ok.WGInterface == childUpstreamIface || strings.Contains(ok.WGConfigPath, childUpstreamIface) {
		t.Fatalf("instrument: the defaults name the tunnel: %s %s", ok.WGInterface, ok.WGConfigPath)
	}

	byIface := childTestConfig(t)
	byIface.WGInterface = childUpstreamIface
	byPath := childTestConfig(t)
	byPath.WGConfigPath = "/etc/wireguard/" + childUpstreamIface + ".conf"
	for name, cfg := range map[string]*config.Config{"wg_interface": byIface, "wg_config_path": byPath} {
		if err := config.Save(filepath.Join(t.TempDir(), "config.json"), cfg); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: Save accepted it (%v)", name, err)
		}
		if _, err := NewWithConfig(cfg, filepath.Join(t.TempDir(), "config.json"), true, "test"); err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Errorf("%s: hz started (%v)", name, err)
		}
	}

	// A PARENT may call its interface anything.
	parent := childTestConfig(t)
	parent.Upstream = nil
	parent.WGInterface = childUpstreamIface
	if err := parent.UpstreamTunnelCollision(); err != nil {
		t.Fatalf("a parent named its wg interface %s and was refused: %v", childUpstreamIface, err)
	}
}

// RULE (N4g): a child with a human VPN owns wg0 as any hz does — a client
// added through its API lands in wg0.conf, and a sync, the masquerade heal and
// that client edit leave hz-upstream.conf byte-for-byte as wg-quick has it.
// A child whose wg settings name hz-upstream never starts, so nothing can.
func TestAChildWithAHumanVPNRendersWG0AndNeverTouchesItsUpstream(t *testing.T) {
	dir := t.TempDir()
	const upstreamConf = "[Interface]\nPrivateKey = cGFzc3dvcmQ=\nAddress = 10.100.0.7/32\n\n[Peer]\nPublicKey = " + bootServerPK +
		"\nEndpoint = vpn.example.com:51820\nAllowedIPs = 10.100.0.1/32\nPersistentKeepalive = 25\n"
	upPath := filepath.Join(dir, childUpstreamIface+".conf")
	wgPath := filepath.Join(dir, "wg0.conf")
	if err := os.WriteFile(upPath, []byte(upstreamConf), 0o600); err != nil {
		t.Fatal(err)
	}
	wg0 := wireguard.RenderServerInterface(wireguard.ServerInterface{PrivateKey: "cGFzc3dvcmQ=", Address: "10.101.0.1/24",
		ListenPort: config.ChildHumanVPNPort, PostUp: wireguard.ExpectedPostUp("eth0"), PostDown: wireguard.ExpectedPostDown("eth0")})
	if err := os.WriteFile(wgPath, []byte(wg0), 0o600); err != nil {
		t.Fatal(err)
	}
	untouched := func(when string) {
		t.Helper()
		got, err := os.ReadFile(upPath)
		if err != nil || string(got) != upstreamConf {
			t.Fatalf("%s: %s changed (%v):\n%s", when, upPath, err, got)
		}
	}

	cfg := childTestConfig(t)
	cfg.WGConfigPath = wgPath
	cfg.VPNRange, cfg.DNS, cfg.ServerEndpoint = "10.101.0.0/24", "10.101.0.1", bootKioskName+":51820"
	cfg.ServerPublicKey = bootServerPK
	s := newTestServer(t, cfg)
	s.dnsRunningProbe = func() bool { return false }
	s.wg = wireguard.NewConfig(wgPath, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	s.wgReload = func() error { return nil }

	// The client is added the way `homelab-horizon vpn add` asks for it: the
	// whole handler chain, the bootstrap's local admin token, no cookie.
	store, err := db.Open(cfg.UsersDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.users = store
	tokFile := filepath.Join(dir, "admin.token")
	if _, err := CreateConsoleAdmin(context.Background(), store, childAdminUser, tokFile); err != nil {
		t.Fatal(err)
	}
	tok, _ := os.ReadFile(tokFile)
	raw, _ := json.Marshal(apitypes.PeerAddReq{Name: "carl-1", Profile: config.ProfileVPNOnly})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/vpn/peers/add", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	var added apitypes.AddPeerResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &added) != nil {
		t.Fatalf("the console's client add: %d %s", w.Code, w.Body.String())
	}
	body, _ := os.ReadFile(wgPath)
	if !strings.Contains(string(body), "# carl-1") || !strings.Contains(string(body), "AllowedIPs = 10.101.0.2/32") {
		t.Fatalf("the client is not in wg0.conf:\n%s", body)
	}
	if !strings.Contains(added.Config, "Endpoint = "+bootKioskName+":51820") || !strings.Contains(added.Config, "AllowedIPs = 10.101.0.0/24") {
		t.Fatalf("the client config does not point at the child's kiosk and range:\n%s", added.Config)
	}
	untouched("after a client add")

	s.runSyncInternal(&recSyncLogger{}, nil)
	untouched("after a sync")
	if _, err := s.wg.HealMasquerade("ens9"); err != nil {
		t.Fatal(err)
	}
	untouched("after the masquerade heal")
	if body, _ := os.ReadFile(wgPath); !strings.Contains(string(body), "-o ens9 -j MASQUERADE") {
		t.Fatalf("instrument: the heal did not reach wg0.conf:\n%s", body)
	}

	// The tunnel's own file named as hz's: refused at start, before any
	// write. Were it started, a client add would append to the tunnel.
	bad := childTestConfig(t)
	bad.WGConfigPath = upPath
	bad.PublicIPOverride = "192.0.2.1"
	srv, err := NewWithConfig(bad, filepath.Join(t.TempDir(), "config.json"), true, "test")
	if err == nil {
		_ = srv.wg.AddPeer("intruder", boxKey, "10.100.0.9/32")
	}
	untouched("after a start with wg_config_path naming it")
	if err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("a child whose wg_config_path is the upstream tunnel started (%v)", err)
	}
}
