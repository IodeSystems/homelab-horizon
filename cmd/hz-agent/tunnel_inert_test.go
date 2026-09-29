package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// tunnelSpy fails the test if anything is reloaded or brought up.
type tunnelSpy struct{ t *testing.T }

func (s tunnelSpy) HAProxy(*agent.HAProxySection) error { s.t.Error("haproxy reloaded"); return nil }
func (s tunnelSpy) DNSMasq(*agent.DNSMasqSection) error { s.t.Error("dnsmasq reloaded"); return nil }
func (s tunnelSpy) WireGuard(*agent.WireGuardSection) error {
	s.t.Error("wireguard reloaded")
	return nil
}
func (s tunnelSpy) Units(*agent.FilesSection) error { s.t.Error("units poked"); return nil }
func (s tunnelSpy) RestartUnit(string) error        { s.t.Error("unit restarted"); return nil }
func (s tunnelSpy) IPTables(*agent.IPTablesSection, []iptables.Rule) (iptables.Report, error) {
	s.t.Error("firewall reconciled")
	return iptables.Report{}, nil
}
func (s tunnelSpy) SegmentTunnel(dec agent.TunnelDecision) error {
	s.t.Errorf("a report-only agent brought %s up", dec.Tunnel.Interface)
	return nil
}

// WITHOUT --apply A SEGMENT TUNNEL IS REPORTED AND NOTHING ELSE. The agent is
// inert on the live gateway (plan Tier 0) and a segment section must not be
// the thing that changes that: no interface is created, no config is written.
func TestAReportOnlyAgentBringsNoTunnelUp(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "wg-iode.conf")
	d := &agent.Desired{
		Machine: "app-1",
		Segments: &agent.SegmentsSection{Tunnels: []agent.SegmentTunnel{{
			Segment: "iode-net", Interface: "wg-iode", Address: "10.42.0.11/24",
			File: agent.File{Path: conf, Mode: 0o600, Contents: "[Interface]\n"},
		}}},
	}
	payload := filepath.Join(dir, "desired.json")
	b, _ := json.Marshal(d)
	if err := os.WriteFile(payload, b, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &agentFlags{
		from: payload, machine: "app-1", interval: time.Second,
		tokenFile:    filepath.Join(dir, "token"),
		testReloader: tunnelSpy{t},
	}
	// The box is enrolled (it holds its key), so the tunnel is a CREATE the
	// plan wants — "nothing happened" is only evidence of inertness if
	// something was pending.
	if _, _, err := f.segmentKeys().EnsureKey("iode-net"); err != nil {
		t.Fatal(err)
	}
	plan := agent.Compute(d, f.observer().Observe(d))
	pendingTunnel := false
	for _, c := range plan.Pending() {
		if c.Subsystem == agent.SubsystemSegments && c.Target == "wg-iode" && c.Kind == agent.KindCreate {
			pendingTunnel = true
		}
	}
	if !pendingTunnel {
		t.Fatalf("precondition: the plan does not want to create wg-iode, so this proves nothing: %+v", plan.Changes)
	}

	onePass(context.Background(), f, f.source(), f.observer(), "")

	if _, err := os.Stat(conf); err == nil {
		t.Fatal("a report-only pass wrote the segment tunnel's config")
	}
}
