package config

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// The `upstream` VPN profile: a nested hz (a Machine with an HZ marker) reaching
// its PARENT — this hz — as a client of the existing human VPN, wg0
// (plan/plan.md Tier 1b, "Child → parent reachability", N1b).
//
// WHAT IT ADMITS, and why exactly that. The child needs hz's API and nothing
// else. hz's API on the gateway's WG address is hz's OWN listener — ListenAddr,
// or, when that is loopback or a LAN IP, the VPN listener at its port
// (server/vpn_listener.go); plain HTTP inside the tunnel — which is the same port the MFA jail already
// admits as "horizon direct" (internal/iptables/rules.go jailAllows). HAProxy's
// ports are NOT admitted: HAProxy fronts every vhost on the gateway, LAN
// backends included, so admitting 443 would hand the child the LAN at L7. DNS is
// not admitted either: the child is given the gateway's IP, not a name, so it
// needs no resolver, and its client config carries no DNS line.
//
// THE DIRECTION IS UNCHANGED (CLAUDE.md invariant 1): the child dials; this hz
// never does. The rules drop everything else the child sends, and nothing
// addressed from here to the child is admitted back.

// ValidPeerProfile reports a routing profile hz knows. "" is not one: callers
// default an empty request to ProfileLanAccess before asking.
func ValidPeerProfile(p string) bool {
	switch p {
	case ProfileLanAccess, ProfileFullTunnel, ProfileVPNOnly, ProfileUpstream:
		return true
	}
	return false
}

// PeerProfileNames is every profile ValidPeerProfile accepts, for an error
// message.
const PeerProfileNames = "lan-access, full-tunnel, vpn-only or upstream"

// UpstreamLinkOf returns the machine whose nested-hz marker names this VPN
// client (MachineHZ.VPNClient), or "" when none does.
func (c *Config) UpstreamLinkOf(peer string) string {
	for _, m := range c.Machines {
		if m.HZ != nil && m.HZ.VPNClient != "" && m.HZ.VPNClient == peer {
			return m.Name
		}
	}
	return ""
}

// CheckPeerProfileChange refuses a profile a VPN client may not take. For the
// add, edit and set-profile handlers, which must refuse BEFORE wg0.conf is
// touched — a refusal from Save after it would leave wg0.conf and the config
// disagreeing.
//
//   - an unknown profile is refused (the add and edit paths used to store
//     any string, which the renderer then read as lan-access);
//   - a client a nested hz is linked to must stay `upstream`: widening it
//     would widen what that hz can reach without anyone looking at the link;
//   - a VPN admin cannot be `upstream`: a VPN admin is authenticated to hz's
//     API by its source address alone (Server.isVPNAdmin), and hz's API is the
//     one thing an upstream client can reach.
func (c *Config) CheckPeerProfileChange(peer, profile string) error {
	if !ValidPeerProfile(profile) {
		return fmt.Errorf("invalid profile %q: must be %s", profile, PeerProfileNames)
	}
	if m := c.UpstreamLinkOf(peer); m != "" && profile != ProfileUpstream {
		return fmt.Errorf("%s is the VPN client of nested hz %s, so it must stay %q — anything wider widens what that hz reaches. Remove %s (cascade removes this client too) or clear its hz marker first",
			peer, m, ProfileUpstream, m)
	}
	if profile == ProfileUpstream && c.IsVPNAdminName(peer) {
		return fmt.Errorf("%s is a VPN admin, and a VPN admin is signed in to hz's API by its address alone — which is the one thing an upstream client can reach. Remove its admin flag first", peer)
	}
	return nil
}

// IsVPNAdminName reports a client named in VPNAdmins.
func (c *Config) IsVPNAdminName(peer string) bool {
	for _, a := range c.VPNAdmins {
		if a == peer {
			return true
		}
	}
	return false
}

// RenameUpstreamClient carries a nested hz's link across a rename of its VPN
// client. Without it the link would name a client that no longer exists and the
// next Save would be refused.
func (c *Config) RenameUpstreamClient(oldName, newName string) {
	if oldName == newName {
		return
	}
	machines := append([]Machine(nil), c.Machines...)
	changed := false
	for i, m := range machines {
		if m.HZ != nil && m.HZ.VPNClient == oldName {
			hz := *m.HZ
			hz.VPNClient = newName
			machines[i].HZ = &hz
			changed = true
		}
	}
	if changed {
		c.Machines = machines
	}
}

// validateUpstreamLinks checks every MachineHZ.VPNClient: it names a VPN client
// hz holds (WGPeers, the snapshot of wg0.conf), that client's profile is
// `upstream`, and no two machines share one.
func (c *Config) validateUpstreamLinks() error {
	peers := make(map[string]bool, len(c.WGPeers))
	for _, p := range c.WGPeers {
		peers[p.Name] = true
	}
	owner := map[string]string{}
	names := make([]string, 0, len(c.Machines))
	for _, m := range c.Machines {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		m, _ := c.FindMachine(name)
		if m.HZ == nil || m.HZ.VPNClient == "" {
			continue
		}
		client := m.HZ.VPNClient
		if !peers[client] {
			return fmt.Errorf("machine %q names VPN client %q as its link to this hz, and there is no such client", m.Name, client)
		}
		if p := c.GetPeerProfile(client); p != ProfileUpstream {
			return fmt.Errorf("machine %q names VPN client %q as its link to this hz, and that client's profile is %q, not %q — a nested hz reaches only the parent's API", m.Name, client, p, ProfileUpstream)
		}
		if other, dup := owner[client]; dup {
			return fmt.Errorf("machines %q and %q both name VPN client %q — one client is one nested hz", other, m.Name, client)
		}
		owner[client] = m.Name
	}
	return nil
}

// ParentAPIURL is the address a nested hz uses to reach this hz over its
// upstream VPN client: hz's own listener at the gateway's WG address.
//
// serverWGIP is the address on wg0 (what the rules admit, as -d). The port is
// ListenAddr's, derived exactly as every iptables.Inputs.ListenPort is
// (net.SplitHostPort(cfg.ListenAddr)), so the URL names the port the rule
// admits — not a second answer that could disagree with it.
//
// Plain http: hz's listener speaks only HTTP, and the hop is inside WireGuard,
// encrypted and authenticated by the client's key. https would mean HAProxy,
// whose ports the upstream profile deliberately does not admit (vpn_upstream.go
// header).
//
// Refused when that URL could not answer: no WG address, no port, or hz bound
// to an address that is neither every interface nor the WG address itself and
// no VPN listener there — then the rule would admit a port nothing listens on
// at that address.
//
// vpnListener is the address hz's second listener is BOUND to right now
// (server.vpnListener.Bound), "" when there is none. It counts only when it is
// exactly <serverWGIP>:<port>, the address and port the rule admits.
func (c *Config) ParentAPIURL(serverWGIP, vpnListener string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(serverWGIP))
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("hz does not know the gateway's WireGuard address (got %q), so there is no address an upstream client could reach it on", serverWGIP)
	}
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil || port == "" {
		return "", fmt.Errorf("hz's listen address %q has no port, so the upstream rule cannot name the port hz's API is on", c.ListenAddr)
	}
	host, effPort, err := net.SplitHostPort(strings.TrimSpace(c.EffectiveListenAddr()))
	if err != nil {
		return "", fmt.Errorf("hz's effective listen address %q does not parse: %v", c.EffectiveListenAddr(), err)
	}
	if effPort != port {
		return "", fmt.Errorf("hz listens on port %s but its config says %s — the upstream rule admits the config's port, so it would admit one hz is not on", effPort, port)
	}
	host = strings.Trim(host, "[]")
	if vpnListener != "" && vpnListener == net.JoinHostPort(ip.String(), port) {
		return "http://" + vpnListener, nil
	}
	switch host {
	case "", "0.0.0.0", "::", "*":
		// every interface, wg0 included
	default:
		if h := net.ParseIP(host); h == nil || !h.Equal(ip) {
			return "", errors.New("hz is bound to " + c.EffectiveListenAddr() + ", which is not the gateway's WireGuard address " + ip.String() +
				", and hz's VPN listener is not bound at " + net.JoinHostPort(ip.String(), port) +
				" — an upstream client could reach nothing there. Turn vpn_listen on (and drop --no-vpn-listen), or bind hz to " +
				net.JoinHostPort(ip.String(), port))
		}
	}
	return "http://" + net.JoinHostPort(ip.String(), port), nil
}
