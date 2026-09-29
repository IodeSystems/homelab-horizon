package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// hz's half of the segment tunnels: what desiredFor serves, for machines hz is
// not on — and what it must never serve.

// tunnelEstate is one segment with a hub (gw-hub) and two spokes, every member
// keyed by a key MINTED THE WAY A BOX MINTS IT (agent.SegmentKeyStore), so each
// test holds the private halves hz must never see. None of the three is the
// machine hz runs on.
type tunnelEstate struct {
	cfg      *config.Config
	privates map[string]string // machine -> private key, as on that box
}

func newTunnelEstate(t *testing.T) tunnelEstate {
	t.Helper()
	e := tunnelEstate{privates: map[string]string{}}
	members := []config.SegmentMember{
		{Machine: "gw-hub", Address: "10.42.0.1", Hub: true, Endpoint: "gw.example.invalid:51830"},
		{Machine: "app-1", Address: "10.42.0.11"},
		{Machine: "app-2", Address: "10.42.0.12"},
	}
	for i := range members {
		store := agent.SegmentKeyStore{Dir: filepath.Join(t.TempDir(), members[i].Machine)}
		pub, _, err := store.EnsureKey("iode-net")
		if err != nil {
			t.Fatal(err)
		}
		priv, err := os.ReadFile(store.Path("iode-net"))
		if err != nil {
			t.Fatal(err)
		}
		members[i].PublicKey = pub
		e.privates[members[i].Machine] = strings.TrimSpace(string(priv))
	}
	e.cfg = &config.Config{
		Projects: []config.Project{{Name: "iode"}},
		Machines: []config.Machine{
			{Name: "gw-hub", Segments: []string{"iode-net"}},
			{Name: "app-1", Segments: []string{"iode-net"}},
			{Name: "app-2", Segments: []string{"iode-net"}},
		},
		Segments: []config.Segment{{
			Name: "iode-net", Project: "iode", CIDR: "10.42.0.0/24", Interface: "wg-iode", Members: members,
		}},
	}
	if err := e.cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return e
}

func (e tunnelEstate) pub(machine string) string {
	seg, _ := e.cfg.FindSegment("iode-net")
	m, _ := seg.Member(machine)
	return m.PublicKey
}

func onlyTunnel(t *testing.T, d *agent.Desired) agent.SegmentTunnel {
	t.Helper()
	if d.Segments == nil || len(d.Segments.Tunnels) != 1 {
		t.Fatalf("want exactly one segment tunnel, got %+v", d.Segments)
	}
	return d.Segments.Tunnels[0]
}

// A REMOTE MACHINE GETS ITS TUNNEL, built from records alone. The file is the
// pure renderer's output for exactly the projection's peers.
func TestAMachineHZIsNotOnGetsItsSegmentTunnel(t *testing.T) {
	e := newTunnelEstate(t)
	s := newTestServer(t, e.cfg)

	spoke := onlyTunnel(t, s.desiredFor("app-1"))
	if spoke.Interface != "wg-iode" || spoke.Address != "10.42.0.11/24" || spoke.Segment != "iode-net" {
		t.Fatalf("spoke tunnel = %+v", spoke)
	}
	if spoke.File.Path != agent.SegmentConfigPath("wg-iode") || spoke.File.Mode != 0o600 || !spoke.File.Secret {
		t.Fatalf("spoke file = path %s mode %o secret %v", spoke.File.Path, spoke.File.Mode, spoke.File.Secret)
	}
	wantSpoke := wireguard.RenderSegmentConfig(wireguard.SegmentInterface{
		Segment: "iode-net",
		Peers: []wireguard.SegmentPeer{{
			Name: "gw-hub", PublicKey: e.pub("gw-hub"), AllowedIPs: []string{"10.42.0.0/24"},
			Endpoint: "gw.example.invalid:51830", PersistentKeepalive: 25,
		}},
	})
	if spoke.File.Contents != wantSpoke {
		t.Errorf("spoke config:\n%s\nwant:\n%s", spoke.File.Contents, wantSpoke)
	}

	hub := onlyTunnel(t, s.desiredFor("gw-hub"))
	wantHub := wireguard.RenderSegmentConfig(wireguard.SegmentInterface{
		Segment: "iode-net", ListenPort: 51830,
		Peers: []wireguard.SegmentPeer{
			{Name: "app-1", PublicKey: e.pub("app-1"), AllowedIPs: []string{"10.42.0.11/32"}},
			{Name: "app-2", PublicKey: e.pub("app-2"), AllowedIPs: []string{"10.42.0.12/32"}},
		},
	})
	if hub.File.Contents != wantHub {
		t.Errorf("hub config:\n%s\nwant:\n%s", hub.File.Contents, wantHub)
	}
	if hub.Address != "10.42.0.1/24" {
		t.Errorf("hub address = %s", hub.Address)
	}
}

// NO PRIVATE KEY IN ANYTHING hz SERVES. Every box's private half exists (the
// fixture minted it the way a box does); none of them may appear in any
// machine's served payload or projection, and no tunnel file may carry a
// PrivateKey line at all.
func TestNoPrivateKeyIsInAnythingHZServesForASegment(t *testing.T) {
	e := newTunnelEstate(t)
	s := newTestServer(t, e.cfg)

	var served []string
	for _, m := range []string{"gw-hub", "app-1", "app-2"} {
		d := s.desiredFor(m)
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		served = append(served, string(b))
		for _, tun := range d.Segments.Tunnels {
			if strings.Contains(tun.File.Contents, "PrivateKey") {
				t.Errorf("%s's tunnel file has a PrivateKey line:\n%s", m, tun.File.Contents)
			}
		}

		w := httptest.NewRecorder()
		s.handleAPIMachineProjection(w, asAdmin(s, http.MethodGet, apitypes.MachineProjectionPath+"?machine="+m, ""))
		served = append(served, w.Body.String())
	}
	if !strings.Contains(strings.Join(served, "\n"), e.pub("app-1")) {
		t.Fatal("precondition: app-1's PUBLIC key is not in what hz serves, so a search for keys in it proves nothing")
	}
	for machine, priv := range e.privates {
		for _, body := range served {
			if strings.Contains(body, priv) {
				t.Fatalf("%s's PRIVATE segment key is in a payload hz serves", machine)
			}
		}
	}
}

// DENY-BY-DEFAULT FORWARDING STAYS (CLAUDE.md invariant 10). A segment client
// is given nothing that would let it forward between its segment interface
// and its other interfaces:
//
//   - no declared crossing (Forwards is empty, and nothing produces one);
//   - no firewall section and no generic files section (no FORWARD rule, no
//     ip_forward sysctl drop-in) for a machine hz is not on;
//   - a tunnel file with no wg-quick hooks, no route table, and no route
//     beyond the segment;
//   - the hub routes each spoke exactly its own /32, so a packet a spoke
//     relays from another network is dropped by WireGuard at the hub.
func TestASegmentClientIsGivenNothingThatForwards(t *testing.T) {
	e := newTunnelEstate(t)
	s := newTestServer(t, e.cfg)

	d := s.desiredFor("app-1")
	if d.Model == nil || len(d.Model.Forwards) != 0 {
		t.Fatalf("a crossing was declared for a segment client: %+v", d.Model)
	}
	if d.IPTables != nil || d.Files != nil || d.WireGuard != nil {
		t.Fatalf("a remote segment client was handed firewall, files or wg0 sections: iptables=%v files=%v wireguard=%v",
			d.IPTables != nil, d.Files != nil, d.WireGuard != nil)
	}
	body := onlyTunnel(t, d).File.Contents
	for _, banned := range []string{"PostUp", "PostDown", "PreUp", "Table", "FwMark", "0.0.0.0/0", "::/0", "iptables", "ip_forward"} {
		if strings.Contains(body, banned) {
			t.Errorf("the spoke's tunnel file contains %q:\n%s", banned, body)
		}
	}
	if !strings.Contains(body, "AllowedIPs = 10.42.0.0/24\n") || strings.Count(body, "AllowedIPs") != 1 {
		t.Errorf("the spoke routes more than the segment:\n%s", body)
	}

	hub := onlyTunnel(t, s.desiredFor("gw-hub")).File.Contents
	for _, line := range strings.Split(hub, "\n") {
		if strings.HasPrefix(line, "AllowedIPs = ") && !strings.HasSuffix(line, "/32") {
			t.Errorf("the hub accepts more than a spoke's own address from it: %q", line)
		}
	}
}

// A PEER WITH NO KEY HAS NO BLOCK. Unkey app-2: the hub's file peers with
// app-1 alone, and the projection it rides with names app-2 in a gap.
func TestAnUnkeyedSpokeIsLeftOutOfTheHubsFile(t *testing.T) {
	e := newTunnelEstate(t)
	for i := range e.cfg.Segments[0].Members {
		if e.cfg.Segments[0].Members[i].Machine == "app-2" {
			e.cfg.Segments[0].Members[i].PublicKey = ""
		}
	}
	s := newTestServer(t, e.cfg)
	d := s.desiredFor("gw-hub")
	body := onlyTunnel(t, d).File.Contents
	if strings.Contains(body, "# app-2") || strings.Count(body, "[Peer]") != 1 {
		t.Fatalf("the hub's file carries a block for the unkeyed spoke:\n%s", body)
	}
	gapped := false
	for _, g := range d.Model.Unresolved {
		if strings.Contains(g.Why, "iode-net/app-2") {
			gapped = true
		}
	}
	if !gapped {
		t.Fatalf("app-2 left the hub's file and no gap says why: %+v", d.Model.Unresolved)
	}
}

// hz's OWN VPN INTERFACE IS NEVER A SEGMENT TUNNEL. A segment declared on the
// gateway's wg0 would have the agent `wg syncconf` a segment config into it
// and drop every human VPN peer. hz renders nothing for it and says why.
func TestASegmentOnHZsOwnVPNInterfaceGetsNoTunnel(t *testing.T) {
	e := newTunnelEstate(t)
	local := LocalMachineName()
	e.cfg.WGInterface = "wg0"
	e.cfg.WGConfigPath = filepath.Join(t.TempDir(), "absent-wg0.conf")
	e.cfg.Segments[0].Interface = "wg0"
	for i := range e.cfg.Machines {
		if e.cfg.Machines[i].Name == "gw-hub" {
			e.cfg.Machines[i].Name = local
		}
	}
	for i := range e.cfg.Segments[0].Members {
		if e.cfg.Segments[0].Members[i].Machine == "gw-hub" {
			e.cfg.Segments[0].Members[i].Machine = local
		}
	}
	if err := e.cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	s := newTestServer(t, e.cfg)

	d := s.desiredFor(local)
	if d.Segments != nil {
		t.Fatalf("hz rendered a segment tunnel onto its own VPN interface: %+v", d.Segments)
	}
	found := false
	for _, g := range d.Model.Unresolved {
		if strings.Contains(g.Why, "hz's own VPN interface") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no gap says why the segment has no tunnel: %+v", d.Model.Unresolved)
	}

	// A remote spoke on the same segment still gets its tunnel: wg0 is only
	// hz's on hz's box.
	if spoke := s.desiredFor("app-1"); spoke.Segments == nil {
		t.Fatal("a remote spoke lost its tunnel because the hub's interface is called wg0 on the hub")
	}
}

// A hub endpoint that is not host:port gives no listen port hz can stand
// behind, so no tunnel — and a gap, not a tunnel on a random port.
func TestAHubWithAnEndpointThatIsNotHostPortGetsNoTunnel(t *testing.T) {
	e := newTunnelEstate(t)
	e.cfg.Segments[0].Members[0].Endpoint = "gw.example.invalid"
	s := newTestServer(t, e.cfg)
	d := s.desiredFor("gw-hub")
	if d.Segments != nil {
		t.Fatalf("a tunnel was rendered with no usable listen port: %+v", d.Segments)
	}
	found := false
	for _, g := range d.Model.Unresolved {
		if strings.Contains(g.Why, "not host:port") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no gap names the endpoint: %+v", d.Model.Unresolved)
	}
}
