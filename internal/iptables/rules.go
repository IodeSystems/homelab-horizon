// Package iptables owns the horizon-managed iptables rule set: generating what
// the current config wants (ExpectedRules), generating what the *previous*
// config wanted (StaleRules, used to find drift), and a canonical form for
// set comparison in the classifier.
//
// The scope of "what horizon manages" is deliberately narrow — only the three
// chains it touches:
//   - nat POSTROUTING  (a single MASQUERADE rule pinned to the default iface)
//   - filter FORWARD   (jump to WG-FORWARD + stateful return traffic)
//   - filter WG-FORWARD (per-peer profile rules + default drop)
//
// Other iptables state on the host is none of horizon's business — the
// classifier treats it as "unknown" and leaves it alone unless the admin
// explicitly removes it via the IPTables tab.
package iptables

import (
	"strings"

	"homelab-horizon/internal/config"
)

// ForwardChainName is the chain horizon inserts per-peer profile rules into.
// Kept as a constant so both generators and callers use the same spelling.
const ForwardChainName = "WG-FORWARD"

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
	WGInterface string            // "wg0"
	OutIface    string            // default-route iface, e.g. "eth0"
	VPNRange    string            // "10.100.0.0/24"
	LanCIDR     string            // "192.168.1.0/24" — may be empty
	Peers       []PeerInput       // per-peer facts (IP, profile, MFA jail status)
	ServerWGIP  string            // "10.100.0.1" — for MFA jail rule
	ListenPort  string            // horizon's HTTP port, for MFA jail rule
	JailedPeers map[string]bool   // peer name → currently MFA-jailed
	Profiles    map[string]string // peer name → profile
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

	rules := make([]Rule, 0, 8+3*len(in.Peers))

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

	// WG-FORWARD body: per-peer rules + default drop.
	for _, p := range in.Peers {
		ip := p.IP
		if ip == "" {
			ip = peerIP(p.AllowedIPs)
		}
		if ip == "" {
			continue
		}

		// MFA jail takes precedence over profile — jailed peer can only
		// reach horizon's own HTTP port on the server WG IP.
		if in.JailedPeers[p.Name] && in.ServerWGIP != "" && in.ListenPort != "" {
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
				Args:  []string{"-s", ip + "/32", "-d", in.ServerWGIP + "/32", "-p", "tcp", "--dport", in.ListenPort, "-j", "ACCEPT"},
			})
			rules = append(rules, Rule{
				Table: "filter",
				Chain: ForwardChainName,
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
		WGInterface: cfg.WGInterface,
		OutIface:    cfg.LastLocalIface,
		VPNRange:    cfg.VPNRange,
		LanCIDR:     cfg.LastLanCIDR,
		Peers:       peers,
		ServerWGIP:  serverWGIP,
		ListenPort:  listenPort,
		JailedPeers: cfg.GetJailedPeers(),
		Profiles:    cfg.VPNProfiles,
	}
	return ExpectedRules(in)
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
