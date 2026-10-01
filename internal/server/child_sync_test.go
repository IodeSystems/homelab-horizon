package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/monitor"
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
