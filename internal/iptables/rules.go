// Package iptables owns the horizon-managed iptables rule set: generating what
// the current config wants (ExpectedRules), generating what the *previous*
// config wanted (StaleRules, used to find drift), and a canonical form for
// set comparison in the classifier.
//
// The scope of "what horizon manages" is deliberately narrow — only the chains
// it touches:
//   - nat POSTROUTING  (a single MASQUERADE rule pinned to the default iface)
//   - filter FORWARD   (jump to WG-FORWARD + stateful return traffic)
//   - filter WG-FORWARD (per-peer profile rules + default drop)
//   - filter INPUT     (jump to WG-INPUT, for wg-incoming traffic only)
//   - filter WG-INPUT  (MFA jail rules for traffic to the gateway itself)
//
// Other iptables state on the host is none of horizon's business — the
// classifier treats it as "unknown" and leaves it alone unless the admin
// explicitly removes it via the IPTables tab. In particular, INPUT is read
// back only for rules that jump to WG-INPUT: a typical host has a pile of
// unrelated INPUT rules (ufw, docker) and horizon must not claim them.
package iptables

import (
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// ForwardChainName is the chain horizon inserts per-peer profile rules into.
// Kept as a constant so both generators and callers use the same spelling.
const ForwardChainName = "WG-FORWARD"

// InputChainName is the chain that enforces the MFA jail for traffic addressed
// to the gateway itself.
//
// WG-FORWARD cannot do this job. Packets from a peer to the server's own wg0
// address are locally delivered, so they traverse INPUT and never reach
// FORWARD — a jailed peer could otherwise reach every daemon listening on the
// gateway (dnsmasq, sshd, and critically HAProxy, which would then originate
// LAN-bound connections *from the gateway* and sidestep WG-FORWARD entirely).
//
// The chain holds rules for jailed peers only, and has no catch-all DROP:
// unjailed peers fall through it untouched. With MFA off it is empty, and the
// INPUT jump is a no-op hash lookup.
const InputChainName = "WG-INPUT"

// jailDNSPort is opened to the gateway for jailed peers. Clients are handed
// the gateway as their resolver in the WG config, so with DNS blocked the
// captive portal is unreachable by name and the whole tunnel reads as "broken"
// rather than "locked" — a support call instead of a login. It widens the jail
// by exactly one on-box resolver.
const jailDNSPort = "53"

// Rule is one iptables rule in a stable, table/chain-aware form. Args is the
// rule body (everything that would appear after `-A <chain>` on the command
// line), split for easier programmatic manipulation.
//
// Canonical returns a deterministic string suitable for set membership — two
// rules with semantically equivalent specs produce the same string regardless
// of how they were constructed.
type Rule struct {
	Table string   // "nat", "filter"
	Chain string   // "POSTROUTING", "FORWARD", "WG-FORWARD"
	Args  []string // rule body, e.g. {"-o", "eth0", "-j", "MASQUERADE"}
}

// Canonical returns "<table>|<chain>|<space-joined-args>". The format is for
// internal comparison only; it's not valid iptables syntax. Stable across
// Go versions — arg order is preserved as provided because iptables treats
// arg order as significant for matching.
//
// Equivalence normalization: legacy `-m state --state X` and modern
// `-m conntrack --ctstate X` are semantically identical (the kernel treats
// them the same; iptables-nft transparently converts one to the other on
// some distros). Canonical collapses both to the conntrack form so the
// classifier sees them as the same rule and doesn't dup-insert when the
// emitted form and the saved form disagree.
func (r Rule) Canonical() string {
	return r.Table + "|" + r.Chain + "|" + strings.Join(canonicalizeArgs(r.Args), " ")
}

// canonicalizeArgs rewrites equivalent iptables match modules to a single
// canonical form for comparison. Currently handles state→conntrack only;
// extend here if other modules show similar legacy/modern divergence.
func canonicalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		// `-m state --state X` → `-m conntrack --ctstate X`
		if args[i] == "-m" && i+3 < len(args) && args[i+1] == "state" && args[i+2] == "--state" {
			out = append(out, "-m", "conntrack", "--ctstate", args[i+3])
			i += 3
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// String returns a human-readable form matching how iptables-save prints
// rules, useful for logging and UI display:
//
//	-t nat -A POSTROUTING -o eth0 -j MASQUERADE
func (r Rule) String() string {
	return "-t " + r.Table + " -A " + r.Chain + " " + strings.Join(r.Args, " ")
}

// Inputs carries the variable inputs that drive rule generation. Pulled out of
// *config.Config so that StaleRules can substitute LastLocalIface / LastLanCIDR
// without mutating the live config.
type Inputs struct {
	WGInterface string          // "wg0"
	OutIface    string          // default-route iface, e.g. "eth0"
	VPNRange    string          // "10.100.0.0/24"
	LanCIDR     string          // "192.168.1.0/24" — may be empty
	Peers       []PeerInput     // per-peer facts (IP, profile, MFA jail status)
	ServerWGIP  string          // "10.100.0.1" — for MFA jail rule
	ListenPort  string          // horizon's HTTP port, for MFA jail rule
	JailedPeers map[string]bool // peer name → currently MFA-jailed

	// HAProxyPorts are the gateway's HAProxy bind ports (80/443). Jailed peers
	// are allowed to reach them so HAProxy can apply the L7 half of the jail —
	// which vhost they asked for is a question only it can answer. Empty when
	// HAProxy is disabled, in which case the jail stays purely L3.
	HAProxyPorts []string
	Profiles     map[string]string // peer name → profile
}

// PeerInput is the subset of a WG peer we need to emit forward rules. Keeping
// this type here (instead of reusing wireguard.Peer) avoids a cycle since
// wireguard will eventually call into this package.
type PeerInput struct {
	Name       string
	IP         string // /32 address, e.g. "10.100.0.42"
	AllowedIPs string // raw AllowedIPs string if IP is empty we can parse it
}

// ExpectedRules generates the rule set horizon wants to see installed, given
// the inputs. Order within the returned slice is deterministic but doesn't
// match runtime insertion order — the classifier compares by canonical form,
// not by position.
//
// Returns an empty slice for trivially-empty inputs (no WG interface). Callers
// should handle that case however they prefer (e.g. skip reconciliation).
func ExpectedRules(in Inputs) []Rule {
	if in.WGInterface == "" {
		return nil
	}

	rules := make([]Rule, 0, 9+5*len(in.Peers))

	// nat POSTROUTING: one MASQUERADE rule pinned to the default iface.
	if in.OutIface != "" {
		rules = append(rules, Rule{
			Table: "nat",
			Chain: "POSTROUTING",
			Args:  []string{"-o", in.OutIface, "-j", "MASQUERADE"},
		})
	}

	// filter FORWARD: jump to WG-FORWARD + stateful return traffic.
	rules = append(rules, Rule{
		Table: "filter",
		Chain: "FORWARD",
		Args:  []string{"-i", in.WGInterface, "-j", ForwardChainName},
	})
	rules = append(rules, Rule{
		Table: "filter",
		Chain: "FORWARD",
		Args:  []string{"-o", in.WGInterface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	})

	// filter INPUT: jump to WG-INPUT for wg-incoming traffic. Emitted
	// unconditionally (not only when peers are jailed) so the enforcement
	// point is already in place when MFA is switched on, and so the rule
	// doesn't flap in and out on every jail transition.
	rules = append(rules, Rule{
		Table: "filter",
		Chain: "INPUT",
		Args:  []string{"-i", in.WGInterface, "-j", InputChainName},
	})

	// WG-FORWARD / WG-INPUT bodies: per-peer rules + default drop.
	for _, p := range in.Peers {
		ip := p.IP
		if ip == "" {
			ip = peerIP(p.AllowedIPs)
		}
		if ip == "" {
			continue
		}

		// MFA jail takes precedence over profile. Enforced on both paths:
		// WG-FORWARD drops everything transiting the gateway, WG-INPUT drops
		// everything addressed to the gateway except the portal (and DNS).
		//
		// Fails open when we can't name the portal: emitting the DROPs
		// without the matching ACCEPTs would strand the peer with no way to
		// reach the very page that un-jails it.
		if in.JailedPeers[p.Name] && in.ServerWGIP != "" && in.ListenPort != "" {
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
				Args:  []string{"-s", ip + "/32", "-j", "DROP"},
			})
			for _, allow := range jailAllows(in.ListenPort, in.HAProxyPorts) {
				args := append([]string{"-s", ip + "/32", "-d", in.ServerWGIP + "/32"}, allow...)
				rules = append(rules, Rule{
					Table: "filter",
					Chain: InputChainName,
					Args:  append(args, "-j", "ACCEPT"),
				})
			}
			rules = append(rules, Rule{
				Table: "filter",
				Chain: InputChainName,
				Args:  []string{"-s", ip + "/32", "-j", "DROP"},
			})
			continue
		}

		profile := in.Profiles[p.Name]
		if profile == "" {
			profile = "lan-access"
		}

		switch profile {
		case "full-tunnel":
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
				Args:  []string{"-s", ip + "/32", "-j", "ACCEPT"},
			})
		case "vpn-only":
			if in.VPNRange != "" {
				rules = append(rules, Rule{
					Table: "filter",
					Chain: ForwardChainName,
					Args:  []string{"-s", ip + "/32", "-d", in.VPNRange, "-j", "ACCEPT"},
				})
			}
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
				Args:  []string{"-s", ip + "/32", "-j", "DROP"},
			})
		default: // lan-access
			if in.VPNRange != "" {
				rules = append(rules, Rule{
					Table: "filter",
					Chain: ForwardChainName,
					Args:  []string{"-s", ip + "/32", "-d", in.VPNRange, "-j", "ACCEPT"},
				})
			}
			if in.LanCIDR != "" {
				rules = append(rules, Rule{
					Table: "filter",
					Chain: ForwardChainName,
					Args:  []string{"-s", ip + "/32", "-d", in.LanCIDR, "-j", "ACCEPT"},
				})
			}
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
				Args:  []string{"-s", ip + "/32", "-j", "DROP"},
			})
		}
	}

	// Default drop — anything not matched above.
	rules = append(rules, Rule{
		Table: "filter",
		Chain: ForwardChainName,
		Args:  []string{"-j", "DROP"},
	})

	return rules
}

// StaleRules returns what horizon *would have* generated under the previous
// iface/CIDR, for use by the classifier to identify drift. Returns empty if
// neither LastLocalIface nor LastLanCIDR is persisted (nothing to diff against).
//
// Substitutes only the network coordinates — peers, profiles, VPN range, and
// server WG IP come from the current config because they're unchanged by an
// interface swap. The only thing an iface/CIDR change rewrites is MASQUERADE's
// `-o` and lan-access's `-d <LanCIDR>`.
func StaleRules(cfg *config.Config, peers []PeerInput, serverWGIP, listenPort string) []Rule {
	if cfg.LastLocalIface == "" && cfg.LastLanCIDR == "" {
		return nil
	}
	in := Inputs{
		WGInterface:  cfg.WGInterface,
		OutIface:     cfg.LastLocalIface,
		VPNRange:     cfg.VPNRange,
		LanCIDR:      cfg.LastLanCIDR,
		Peers:        peers,
		ServerWGIP:   serverWGIP,
		ListenPort:   listenPort,
		JailedPeers:  cfg.GetJailedPeers(),
		HAProxyPorts: cfg.HAProxyJailPorts(),
		Profiles:     cfg.VPNProfiles,
	}
	return ExpectedRules(in)
}

// jailAllows returns the destination-port matchers a jailed peer is permitted
// to reach on the gateway, in rule order: horizon direct, HAProxy (where the
// L7 jail then decides which vhost), DNS.
//
// Shared by the generator here and internal/wireguard's immediate-apply path so
// the two can't drift — a jailed peer allowed through one and not the other is
// either a lockout or a hole, depending on which way it drifts.
func jailAllows(listenPort string, haproxyPorts []string) [][]string {
	allows := [][]string{{"-p", "tcp", "--dport", listenPort}}
	for _, p := range haproxyPorts {
		if p == "" || p == listenPort {
			continue
		}
		allows = append(allows, []string{"-p", "tcp", "--dport", p})
	}
	return append(allows,
		[]string{"-p", "udp", "--dport", jailDNSPort},
		[]string{"-p", "tcp", "--dport", jailDNSPort},
	)
}

// peerIP pulls the first /32 IP from an AllowedIPs string, falling back to
// whatever's before the first `/` on the first entry.
func peerIP(allowedIPs string) string {
	for _, part := range strings.Split(allowedIPs, ",") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, "/32") {
			return strings.TrimSuffix(part, "/32")
		}
	}
	parts := strings.Split(strings.TrimSpace(allowedIPs), "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
