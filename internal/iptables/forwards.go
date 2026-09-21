package iptables

// This file is part of the PURE half of the package: the layer-4 port-forward
// rules. `net` appears below for ParseIP/ParseCIDR only — string parsing, no
// socket, no resolver — which seam_test.go enforces rather than trusting.

import (
	"net"
	"strconv"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Chains holding layer-4 port forwards (config.Forward). All three are wholly
// horizon-owned and rebuilt atomically, like WG-FORWARD. The only rules
// horizon adds to built-in chains for forwards are one jump each, in
// ForwardJumpRules.
//
// Dedicated chains are the point: a forward is removed by rebuilding a chain
// nobody else writes to, never by editing PREROUTING, POSTROUTING or FORWARD
// around other tools' rules (Docker, ufw).
const (
	PreroutingChainName  = "HZ-PREROUTING"  // nat: DNAT to the backend
	PostroutingChainName = "HZ-POSTROUTING" // nat: MASQUERADE the forwarded flow
	ForwardsChainName    = "HZ-FORWARD"     // filter: accept the forwarded flow
)

// ForwardInput is one forward with its backend already split. Built from
// config by ForwardsFromConfig.
type ForwardInput struct {
	Service     string
	Proto       string // "udp" | "tcp"
	Port        int    // public port on the gateway
	BackendIP   string
	BackendPort int
}

// ForwardsFromConfig flattens every service's forwards. Entries whose backend
// does not parse are dropped here; everything else is re-checked by the
// generator, which does not trust config validation to have run.
func ForwardsFromConfig(cfg *config.Config) []ForwardInput {
	var out []ForwardInput
	for _, svc := range cfg.Services {
		for _, f := range svc.Forwards {
			ip, port, ok := f.BackendIPPort()
			if !ok {
				continue
			}
			out = append(out, ForwardInput{
				Service:     svc.Name,
				Proto:       f.Proto,
				Port:        f.Port,
				BackendIP:   ip,
				BackendPort: port,
			})
		}
	}
	return out
}

// ForwardJumpRules are the jumps from the built-in chains into the forward
// chains — the only forward rules outside horizon's own chains.
//
//   - PREROUTING jumps only for packets addressed to this host
//     (`--dst-type LOCAL`), so transit traffic is never rewritten. It covers
//     every way a client reaches the gateway: from the internet via the
//     router's port forward, from the LAN, and from VPN peers, which resolve
//     service names to the gateway through split-horizon DNS.
//   - POSTROUTING and FORWARD jump unconditionally; the chain bodies match
//     only conntrack-DNATed flows to a configured backend, so everything else
//     returns straight away.
//
// Emitted only while at least one forward exists, and always present in the
// stale set, so removing the last forward removes the jumps as well.
func ForwardJumpRules() []Rule {
	return []Rule{
		{Table: "nat", Chain: "PREROUTING", Args: []string{"-m", "addrtype", "--dst-type", "LOCAL", "-j", PreroutingChainName}},
		{Table: "nat", Chain: "POSTROUTING", Args: []string{"-j", PostroutingChainName}},
		{Table: "filter", Chain: "FORWARD", Args: []string{"-j", ForwardsChainName}},
	}
}

// forwardRules generates the rules for Inputs.Forwards: the jumps, then the
// three chain bodies. Per forward (udp 4433 -> 192.168.1.76:4433, out eth0):
//
//	-t nat    -A HZ-PREROUTING  -p udp --dport 4433 -j DNAT --to-destination 192.168.1.76:4433
//	-t nat    -A HZ-POSTROUTING -d 192.168.1.76/32 -o eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE
//	-t filter -A HZ-FORWARD     -d 192.168.1.76/32 -i eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT
//	-t filter -A HZ-FORWARD     -s 192.168.1.76/32 -o eth0 -p udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT
//
// Why each one:
//   - MASQUERADE: the backend shares the LAN with the router, so without it
//     replies go straight to the router with the backend's address and the
//     router's conntrack drops them. The backend sees the gateway as the
//     client address.
//   - ACCEPT `-i <out>`: traffic from VPN peers arrives on the WireGuard
//     interface and is left to WG-FORWARD, which ends in DROP, so profiles
//     and the MFA jail still decide what a peer can reach. Their replies are
//     covered by the existing `-o wg0 RELATED,ESTABLISHED` rule.
//   - `--ctstate DNAT` on all three: only flows this chain DNATed match, so a
//     LAN host that happens to route through the gateway gets nothing extra.
//
// Args are written in iptables-save's order (-s -d -i -o -p, then matches in
// insertion order) so Canonical matches the readback without further rewrites.
//
// Safety: the generator re-checks every forward itself and skips — rather than
// emits — anything unsafe, whether or not config validation ran: a port in
// config.ForwardAlwaysReserved (22, 53, 80, 443), in in.ReservedPorts, equal
// to horizon's or HAProxy's port; a protocol other than udp/tcp; a backend that
// is not an IPv4 address inside in.LanCIDR. It returns nothing without an out
// interface and LAN CIDR, which is fail-closed.
func forwardRules(in Inputs) []Rule {
	if in.OutIface == "" || in.LanCIDR == "" || len(in.Forwards) == 0 {
		return nil
	}
	_, lan, err := net.ParseCIDR(in.LanCIDR)
	if err != nil {
		return nil
	}

	var pre, post, fwd []Rule
	seenPort := map[string]bool{}
	seenRule := map[string]bool{}
	add := func(dst *[]Rule, r Rule) {
		if c := r.Canonical(); !seenRule[c] {
			seenRule[c] = true
			*dst = append(*dst, r)
		}
	}

	for _, f := range in.Forwards {
		if !forwardAllowed(in, lan, f) {
			continue
		}
		key := f.Proto + "/" + strconv.Itoa(f.Port)
		if seenPort[key] {
			continue // first one wins; validation rejects the duplicate
		}
		seenPort[key] = true

		port := strconv.Itoa(f.Port)
		bport := strconv.Itoa(f.BackendPort)
		backend := f.BackendIP + "/32"

		add(&pre, Rule{Table: "nat", Chain: PreroutingChainName, Args: []string{
			"-p", f.Proto, "--dport", port,
			"-j", "DNAT", "--to-destination", f.BackendIP + ":" + bport,
		}})
		add(&post, Rule{Table: "nat", Chain: PostroutingChainName, Args: []string{
			"-d", backend, "-o", in.OutIface, "-p", f.Proto, "--dport", bport,
			"-m", "conntrack", "--ctstate", "DNAT", "-j", "MASQUERADE",
		}})
		add(&fwd, Rule{Table: "filter", Chain: ForwardsChainName, Args: []string{
			"-d", backend, "-i", in.OutIface, "-p", f.Proto, "--dport", bport,
			"-m", "conntrack", "--ctstate", "DNAT", "-j", "ACCEPT",
		}})
		add(&fwd, Rule{Table: "filter", Chain: ForwardsChainName, Args: []string{
			"-s", backend, "-o", in.OutIface, "-p", f.Proto, "--sport", bport,
			"-m", "conntrack", "--ctstate", "DNAT", "-j", "ACCEPT",
		}})
	}
	if len(pre) == 0 {
		return nil
	}

	out := ForwardJumpRules()
	out = append(out, pre...)
	out = append(out, post...)
	return append(out, fwd...)
}

// forwardAllowed is the generator's own safety check. See forwardRules.
func forwardAllowed(in Inputs, lan *net.IPNet, f ForwardInput) bool {
	if f.Proto != "udp" && f.Proto != "tcp" {
		return false
	}
	if f.Port < 1 || f.Port > 65535 || f.BackendPort < 1 || f.BackendPort > 65535 {
		return false
	}
	if forwardPortReserved(in, f.Port) {
		return false
	}
	ip := net.ParseIP(f.BackendIP)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return lan.Contains(ip)
}

// forwardPortReserved reports whether a public port belongs to the gateway.
func forwardPortReserved(in Inputs, port int) bool {
	if _, ok := config.ForwardAlwaysReserved(port); ok {
		return true
	}
	if _, ok := in.ReservedPorts[port]; ok {
		return true
	}
	p := strconv.Itoa(port)
	if p == in.ListenPort {
		return true
	}
	for _, hp := range in.HAProxyPorts {
		if p == hp {
			return true
		}
	}
	return false
}
