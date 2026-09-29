package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// THE BOOT PATH, from the daemon's side: an armed agent brings its segment
// tunnels up from the last-known-good BEFORE its first poll, with hz
// unreachable; a report-only agent brings nothing up.

// order records every tunnel action and every poll, in the order they came.
type order struct{ events []string }

type orderReloader struct{ o *order }

func (r orderReloader) HAProxy(*agent.HAProxySection) error     { return nil }
func (r orderReloader) DNSMasq(*agent.DNSMasqSection) error     { return nil }
func (r orderReloader) WireGuard(*agent.WireGuardSection) error { return nil }
func (r orderReloader) Units(*agent.FilesSection) error         { return nil }
func (r orderReloader) RestartUnit(string) error                { return nil }
func (r orderReloader) IPTables(*agent.IPTablesSection, []iptables.Rule) (iptables.Report, error) {
	return iptables.Report{}, nil
}
func (r orderReloader) SegmentTunnel(dec agent.TunnelDecision) error {
	r.o.events = append(r.o.events, string(dec.Action)+" "+dec.Tunnel.Interface)
	return nil
}

// unreachable is hz behind the tunnel that is not up yet: every poll fails.
type unreachable struct{ o *order }

func (u unreachable) Fetch(context.Context, string) (*agent.Desired, string, bool, error) {
	u.o.events = append(u.o.events, "poll")
	return nil, "", false, errors.New("dial tcp 10.42.0.1:8080: connect: no route to host")
}

// bootFlags is an armed or report-only agent whose state lives in a temp
// dir, holding its key for iode-net and a record naming wg-iode, on a box
// whose kernel has no interfaces (it just rebooted).
func bootFlags(t *testing.T, apply bool, r agent.Reloader) *agentFlags {
	t.Helper()
	dir := t.TempDir()
	f := &agentFlags{
		machine: "app-1", interval: time.Millisecond, once: true, apply: apply,
		tokenFile:    filepath.Join(dir, "etc", "token"),
		statePath:    filepath.Join(dir, "var", "generations.json"),
		testReloader: r,
		testLinks: func(names []string) map[string]agent.LinkState {
			out := map[string]agent.LinkState{}
			for _, n := range names {
				out[n] = agent.LinkState{}
			}
			return out
		},
	}
	if _, _, err := f.segmentKeys().EnsureKey("iode-net"); err != nil {
		t.Fatal(err)
	}
	rec := agent.TunnelRecord{
		Machine: "app-1", Fingerprint: "0123456789abcdef", AppliedAt: "2023-09-29T00:00:00Z",
		Segments: &agent.SegmentsSection{Tunnels: []agent.SegmentTunnel{{
			Segment: "iode-net", Interface: "wg-iode", Address: "10.42.0.11/24",
			File: agent.File{Path: filepath.Join(dir, "etc", "segments", "wg-iode.conf"), Mode: 0o600,
				Contents: "[Interface]\n\n[Peer]\nPublicKey = 8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=\nAllowedIPs = 10.42.0.0/24\n"},
		}}},
	}
	if err := f.tunnelStore().Save(rec); err != nil {
		t.Fatal(err)
	}
	return f
}

// hz IS UNREACHABLE AND THE TUNNEL COMES UP ANYWAY, BEFORE THE FIRST POLL.
// The order is the property: a box that reaches hz through wg-iode can only
// poll once wg-iode is up.
func TestAnArmedAgentBootsItsTunnelsBeforeItsFirstPoll(t *testing.T) {
	o := &order{}
	f := bootFlags(t, true, orderReloader{o})
	if err := runLoop(context.Background(), f, unreachable{o}, f.observer()); err != nil {
		t.Fatal(err)
	}
	if len(o.events) < 2 || o.events[0] != "create wg-iode" || o.events[1] != "poll" {
		t.Fatalf("events = %v, want the tunnel created and THEN a poll", o.events)
	}
	rec, err := f.tunnelStore().Load()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Created["wg-iode"] != "iode-net" {
		t.Errorf("the booted interface is not recorded as the agent's: %+v", rec.Created)
	}
}

// REPORT-ONLY BOOTS NOTHING. The record is there and the tunnel is down; the
// agent is not armed, so no interface is touched (tunnelSpy fails the test on
// any reload or tunnel action).
func TestAReportOnlyAgentBootsNothing(t *testing.T) {
	o := &order{}
	f := bootFlags(t, false, tunnelSpy{t})
	if err := runLoop(context.Background(), f, unreachable{o}, f.observer()); err != nil {
		t.Fatal(err)
	}
	if len(o.events) != 1 || o.events[0] != "poll" {
		t.Fatalf("events = %v, want one poll and nothing else", o.events)
	}
}
