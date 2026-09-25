package server

import (
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// masqPlaceholderIface and masqPlaceholder stand in for the iface token when
// comparing two PostUp lines that should differ only by which iface they NAT
// through. The placeholder form is produced by the same rewrite that performs
// the real heal (wireguard.RetargetMasquerade), so "these two lines differ only
// by interface" is decided by one regex and not by a second copy of it.
const (
	masqPlaceholderIface = "IFACE"
	masqPlaceholder      = "-o " + masqPlaceholderIface + " -j MASQUERADE"
)

// priorChainPostUp is the frozen PostUp template from the horizon version that
// had WG-FORWARD but no WG-INPUT — i.e. the one whose MFA jail could be walked
// around by addressing the gateway itself. Pinned here (rather than derived)
// because a migration has to recognize the *old* shape exactly; the current
// shape lives in wireguard.ExpectedPostUp and will keep moving.
const priorChainPostUp = "iptables -N WG-FORWARD 2>/dev/null || true; " +
	"iptables -I FORWARD 1 -i %i -j WG-FORWARD; " +
	"iptables -I FORWARD 2 -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; " +
	"iptables -t nat -I POSTROUTING 1 " + masqPlaceholder

// reconcileIPTables is the single-entry self-heal for on-host state that drifts
// when the LAN interface changes. It runs at startup and on every tick of
// startHealthCheck (60s), and handles FIVE drifts — the count and the numbering
// below are the ones the code's own section headers use, because a description
// that disagrees with its headers is how "axes 2/3" came to mean three
// different things in three documents:
//
//  1. LocalInterface IP (dnsmasq binds here + maps localhost services):
//     on change, updateConfig + dns.WriteConfig + dns.Reload.
//  2. Default-route iface name or LAN CIDR (iptables MASQUERADE + WG-FORWARD
//     pin to these): classify + auto-delete stale rules + auto-add missing
//     expected rules, then persist LastLocalIface/LastLanCIDR. Also heal
//     wg0.conf's PostUp/PostDown MASQUERADE so a reboot comes up clean.
//  3. First-run bootstrap (LastLocalIface empty): auto-infer the stale iface
//     from a live `-o X -j MASQUERADE` where X isn't the current default, and
//     proceed as if LastLocalIface were that X. Done inside iptables.Reconcile
//     and reported back as Report.InferredOld, which is why axes 2 and 3 share
//     one block here and one section on the wire.
//  4. Legacy bypass PostUp: hosts upgraded from a horizon version that wrote
//     `-I FORWARD 1 -i %i -j ACCEPT` in PostUp had per-peer policy silently
//     bypassed. Detect that pattern in wg0.conf and migrate to the modern
//     chain-based form, removing the live bypass rules in the same pass.
//  5. WG-INPUT jump migration: hosts predating the INPUT-side jail get the
//     current template re-emitted.
//
// WHAT IS ALREADY THE AGENT'S, AND WHAT IS NOT. Axes 2/3's rule work and its
// wg0.conf heal are both DECLARED to hz-agent today — the rule sets as
// IPTablesSection (handlers_agent.go) and the healed file as the WireGuard
// section's contents (wg_masquerade.go) — and hz keeps doing both here until
// item 12 step 5. Axis 1's observe+persist stays in hz for good. Axes 4 and 5
// are one-shot migrations awaiting the "has every box passed that version"
// answer (privilege-audit.md §8, question 9) and are the only reason this file
// shells iptables directly.
//
// THE PERSIST IN AXIS 2 IS PART OF AXIS 2, not part of axis 1's observe+persist.
// iptables.StaleRules derives the whole old-interface rule set from
// LastLocalIface, so advancing it without healing turns the stale rules into
// unknown ones and nothing ever deletes them. See wg_masquerade.go's header.
//
// Failures are logged but don't stop the loop — a transient iptables lock or
// missing binary on first boot shouldn't prevent subsequent passes.
func (s *Server) reconcileIPTables() {
	cfg := s.cfg()

	newIface := s.defaultIface()
	if newIface == "" {
		// No default route — link is probably down. Skip; next tick will
		// try again once the link comes back.
		return
	}
	newLanCIDR := config.GetLocalNetworkCIDR(newIface)
	newLocalIP := cfg.DetectLocalInterface()

	// ---- Axis 1: LocalInterface (IP) ----
	if newLocalIP != "" && newLocalIP != cfg.LocalInterface {
		slog.Info("iptables-sync: LocalInterface changed", "old", cfg.LocalInterface, "new", newLocalIP)
		if err := s.updateConfig(func(c *config.Config) { c.LocalInterface = newLocalIP }); err != nil {
			slog.Warn("iptables-sync: persist LocalInterface failed", "err", err)
		}
		if err := s.dns.WriteConfig(); err != nil {
			slog.Warn("iptables-sync: dns WriteConfig failed", "err", err)
		} else if err := s.dns.Reload(); err != nil {
			slog.Warn("iptables-sync: dns Reload failed", "err", err)
		}
	}

	// ---- Axis 4: legacy bypass PostUp migration ----
	// Older horizon emitted `iptables -I FORWARD 1 -i %i -j ACCEPT` in PostUp,
	// which short-circuits FORWARD before WG-FORWARD jumps fire — per-peer
	// profile/jail/DROP rules are silently bypassed. Detect that exact pattern
	// and rewrite wg0.conf to the modern chain-based form, then drop the live
	// bypass + legacy `-m state` return rule so Reconcile (below) installs the
	// chain jump and `-m conntrack` return on this same pass.
	//
	// Detection is conservative: bypass token AND no WG-FORWARD reference. A
	// custom admin PostUp that already mentions WG-FORWARD is left untouched.
	if isLegacyBypassPostUp(s.wg.GetPostUp()) {
		slog.Info("iptables-sync: migrating legacy bypass PostUp to chain-based form", "iface", newIface)
		if err := s.wg.UpdateInterfaceRules(wireguard.ExpectedPostUp(newIface), wireguard.ExpectedPostDown(newIface)); err != nil {
			slog.Warn("iptables-sync: migrate wg0.conf failed", "err", err)
		}
		// Strip live legacy rules. Loop because the bypass and the state-form
		// return can each have duplicates from prior reconcile dup-inserts.
		// `iptables -D` returns non-zero when no match remains — that's our
		// loop terminator.
		for i := 0; i < 16; i++ {
			if err := exec.Command("iptables", "-D", "FORWARD", "-i", cfg.WGInterface, "-j", "ACCEPT").Run(); err != nil {
				break
			}
		}
		for i := 0; i < 16; i++ {
			if err := exec.Command("iptables", "-D", "FORWARD", "-o", cfg.WGInterface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run(); err != nil {
				break
			}
		}
	}

	// ---- Axis 5: WG-INPUT jump migration ----
	// Hosts installed before the MFA jail covered the INPUT path have a
	// wg0.conf that only sets up WG-FORWARD. Re-emit the current template so
	// a reboot brings the INPUT jump up with the interface; the live rule
	// itself is installed by Reconcile below, on this same pass.
	if isPriorChainPostUp(s.wg.GetPostUp()) {
		slog.Info("iptables-sync: adding WG-INPUT jump to wg0.conf", "iface", newIface)
		if err := s.wg.UpdateInterfaceRules(wireguard.ExpectedPostUp(newIface), wireguard.ExpectedPostDown(newIface)); err != nil {
			slog.Warn("iptables-sync: WG-INPUT wg0.conf migration failed", "err", err)
		}
	}

	// ---- Axis 2 & 3: iface name / LAN CIDR drift + iptables classify+heal ----
	peers := make([]iptables.PeerInput, 0, len(s.wg.GetPeers()))
	for _, p := range s.wg.GetPeers() {
		peers = append(peers, iptables.PeerInput{
			Name:       p.Name,
			AllowedIPs: p.AllowedIPs,
		})
	}

	serverWGIP := ""
	if addr := s.wg.GetAddress(); addr != "" {
		serverWGIP = strings.TrimSpace(strings.Split(addr, "/")[0])
	}
	listenPort := ""
	if addr := cfg.ListenAddr; addr != "" {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			listenPort = p
		}
	}

	expected := iptables.ExpectedRules(iptables.Inputs{
		WGInterface:  cfg.WGInterface,
		OutIface:     newIface,
		VPNRange:     cfg.VPNRange,
		LanCIDR:      newLanCIDR,
		Peers:        peers,
		ServerWGIP:   serverWGIP,
		ListenPort:   listenPort,
		JailedPeers:  cfg.GetJailedPeers(),
		HAProxyPorts: cfg.HAProxyJailPorts(),
		Profiles:     cfg.VPNProfiles,

		Forwards:      iptables.ForwardsFromConfig(cfg),
		ReservedPorts: cfg.ForwardReservedPorts(),

		BannedIPs: activeBanIPs(cfg.IPBans, time.Now().Unix()),
	})
	stale := iptables.StaleRules(cfg, peers, serverWGIP, listenPort)

	live, err := iptables.LiveRules()
	if err != nil {
		slog.Warn("iptables-sync: LiveRules failed", "err", err)
		return
	}

	report := iptables.Reconcile(live, expected, stale, cfg.BlessedIPTablesRules,
		newIface, cfg.LastLocalIface)

	if len(report.Deleted) > 0 || len(report.Added) > 0 || report.InferredOld != "" {
		slog.Info("iptables-sync: reconciled",
			"deleted", len(report.Deleted), "added", len(report.Added),
			"inferred_old", report.InferredOld, "summary", report.Summary)
	}
	for _, e := range report.Errors {
		slog.Warn("iptables-sync: reconcile error", "err", e)
	}

	// Persist new last-seen values so the next pass (and a reboot) have
	// the right baseline. Persist even if nothing needed deletion — the
	// current iface/CIDR becomes the new "last good."
	ifaceChanged := cfg.LastLocalIface != newIface
	cidrChanged := cfg.LastLanCIDR != newLanCIDR
	if ifaceChanged || cidrChanged {
		if err := s.updateConfig(func(c *config.Config) {
			c.LastLocalIface = newIface
			c.LastLanCIDR = newLanCIDR
		}); err != nil {
			slog.Warn("iptables-sync: persist last-iface/cidr failed", "err", err)
		}
	}

	// Heal wg0.conf's MASQUERADE clause, so the rule PostUp installs at the
	// next `wg-quick up` is the one this box actually needs. hz's half of the
	// hand-over in wg_masquerade.go; the agent is served the same healed file
	// and both take the rewrite from wireguard.HealMasqueradeIface.
	//
	// NOT GATED ON ifaceChanged any more. The question is whether the FILE
	// names the current interface, not whether hz noticed a change on this
	// pass — and the two are different whenever the persist above failed
	// (updateConfig only warns), whenever hz restarted between the change and
	// the persist, and on any box whose wg0.conf was edited by hand. The call
	// is idempotent: a file that already agrees is not rewritten at all.
	if healed, err := s.wg.HealMasquerade(newIface); err != nil {
		slog.Warn("iptables-sync: heal wg0.conf MASQUERADE failed", "err", err)
	} else if healed {
		slog.Info("iptables-sync: wg0.conf now NATs through the current interface", "iface", newIface)
	}
}

// isLegacyBypassPostUp reports whether postUp matches the legacy template that
// emitted `iptables -I FORWARD 1 -i %i -j ACCEPT`. That single rule short-
// circuits FORWARD for all wg-incoming traffic, defeating WG-FORWARD policy.
// Detection requires both the bypass token and the absence of any WG-FORWARD
// reference, so a custom admin PostUp that already uses the chain is not
// misidentified.
func isLegacyBypassPostUp(postUp string) bool {
	return strings.Contains(postUp, "-i %i -j ACCEPT") && !strings.Contains(postUp, "WG-FORWARD")
}

// isPriorChainPostUp reports whether postUp is exactly the pre-WG-INPUT
// horizon template, ignoring which iface it NATs through.
//
// Exact-match rather than "mentions WG-FORWARD but not WG-INPUT": an admin who
// hand-rolled a PostUp around WG-FORWARD owns that line, and silently
// rewriting it would throw away their rules. They lose the INPUT-side jail
// until they adopt the new template — visible in the IPTables tab as a missing
// expected rule, which is the honest failure mode.
func isPriorChainPostUp(postUp string) bool {
	if strings.Contains(postUp, iptables.InputChainName) {
		return false
	}
	return wireguard.RetargetMasquerade(strings.TrimSpace(postUp), masqPlaceholderIface) == priorChainPostUp
}
