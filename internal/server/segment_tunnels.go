package server

import (
	"fmt"
	"net"
	"strconv"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/projection"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// segmentTunnels builds a machine's segment tunnel section from its projection.
//
// EVERY INPUT IS A RECORD, so this runs for every machine hz declares, not
// only the one hz is on: the interface is the segment's, the address is the
// member entry's, and the peers — public keys and DERIVED AllowedIPs — are
// the projection's. The machine's own private key is on the machine and not
// here; no argument or field could carry it.
//
// A membership that did not resolve, or resolved without an address, gets no
// tunnel: the projection already names it in a segments gap. A tunnel this
// function refuses to build for another reason gets a gap of its own.
//
// localWG is hz's own human-VPN interface on the machine hz is on, "" for any
// other machine.
//
// nil when there is no tunnel, which the agent reads as "hz renders none
// here" — never as "remove the ones that exist" (agent.Desired.Segments).
func segmentTunnels(mc *projection.MachineConfig, cfg *config.Config, localWG string) *agent.SegmentsSection {
	var tunnels []agent.SegmentTunnel
	for _, ps := range mc.Segments {
		if !ps.Resolved || ps.Address == "" {
			continue
		}
		seg, ok := cfg.FindSegment(ps.Name)
		if !ok {
			continue
		}
		self, ok := seg.Member(mc.Machine)
		if !ok {
			continue
		}

		// THE GATEWAY'S wg0 IS NOT A SEGMENT INTERFACE. hz maintains that
		// file in place (internal/wireguard) and serves it as the WireGuard
		// section; a second config for the same interface, loaded with
		// `wg syncconf`, would replace every human VPN peer on it.
		if localWG != "" && seg.Interface == localWG {
			mc.AddGapReason(projection.SectionSegments, projection.ReasonUnmodelled,
				"segment "+seg.Name+" lands on "+seg.Interface+", which is hz's own VPN interface on this machine (WGInterface)."+
					" hz renders no segment tunnel for it: loading a segment config into "+seg.Interface+" would replace every VPN peer hz keeps there."+
					" Give the segment its own interface (`hz segment set "+seg.Name+" --interface <iface>`).")
			continue
		}

		_, ipnet, err := net.ParseCIDR(seg.CIDR)
		if err != nil {
			// ValidateSegments refuses this on Save; a config nobody saved
			// can still reach here.
			mc.AddGapReason(projection.SectionSegments, projection.ReasonUnmodelled,
				"segment "+seg.Name+" has range "+seg.CIDR+", which is not a network, so hz renders no tunnel for it: "+err.Error())
			continue
		}
		ones, _ := ipnet.Mask.Size()

		port, err := listenPort(self.Endpoint)
		if err != nil {
			mc.AddGapReason(projection.SectionSegments, projection.ReasonUnmodelled,
				"segment "+seg.Name+" gives "+mc.Machine+" endpoint "+strconv.Quote(self.Endpoint)+", which is not host:port ("+err.Error()+"),"+
					" so hz renders no tunnel for it — the listen port is the endpoint's port, and a hub on a random port cannot be dialled.")
			continue
		}

		peers := make([]wireguard.SegmentPeer, 0, len(ps.Peers))
		for _, p := range ps.Peers {
			sp := wireguard.SegmentPeer{
				Name: p.Name, PublicKey: p.PublicKey,
				AllowedIPs: p.AllowedIPs, Endpoint: p.Endpoint,
			}
			if !self.Hub {
				// A spoke dials and is usually behind NAT; the keepalive is
				// what lets the hub answer it. The value the human VPN's
				// client configs already use (GenerateClientConfig).
				sp.PersistentKeepalive = 25
			}
			peers = append(peers, sp)
		}

		tunnels = append(tunnels, agent.SegmentTunnel{
			Segment:   seg.Name,
			Interface: seg.Interface,
			Address:   fmt.Sprintf("%s/%d", ps.Address, ones),
			File: agent.File{
				Path: agent.SegmentConfigPath(seg.Interface),
				Mode: 0o600,
				Contents: wireguard.RenderSegmentConfig(wireguard.SegmentInterface{
					Segment: seg.Name, ListenPort: port, Peers: peers,
				}),
				Secret: true,
			},
		})
	}
	if len(tunnels) == 0 {
		return nil
	}
	return &agent.SegmentsSection{Tunnels: tunnels}
}

// listenPort is the port of this member's own endpoint, or 0 when it declares
// none (a spoke, which dials out). The endpoint is where the member is reached
// from OUTSIDE; hz assumes the box listens on that same port, which is false
// behind a port-translating NAT.
func listenPort(endpoint string) (int, error) {
	if endpoint == "" {
		return 0, nil
	}
	_, p, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q is not 1-65535", p)
	}
	return n, nil
}
