package server

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/iptables"
	"homelab-horizon/internal/wireguard"
)

// masqIfaceRe matches the iface token in an iptables MASQUERADE clause embedded
// in a wg0.conf PostUp/PostDown line. Used to rewrite wg0.conf when the
// default-route iface changes — the regex approach works even when we don't
// know the old iface name (first-reconcile bootstrap case).
var masqIfaceRe = regexp.MustCompile(`-o \S+ -j MASQUERADE`)

// reconcileIPTables is the single-entry self-heal for on-host state that drifts
// when the LAN interface changes. It runs at startup and on every tick of
// startHealthCheck (60s), and handles four independent drifts:
//
//  1. LocalInterface IP (dnsmasq binds here + maps localhost services):
//     on change, updateConfig + dns.WriteConfig + dns.Reload.
//  2. Default-route iface name or LAN CIDR (iptables MASQUERADE + WG-FORWARD
//     pin to these): on change, classify + auto-delete stale rules + auto-add
//     missing expected rules, then persist LastLocalIface/LastLanCIDR. Also
//     rewrite wg0.conf PostUp/PostDown so a reboot comes up clean.
//  3. First-run bootstrap (LastLocalIface empty): auto-infer the stale iface
//     from a live `-o X -j MASQUERADE` where X isn't the current default, and
//     proceed as if LastLocalIface were that X.
//  4. Legacy bypass PostUp: hosts upgraded from a horizon version that wrote
//     `-I FORWARD 1 -i %i -j ACCEPT` in PostUp had per-peer policy silently
//     bypassed. Detect that pattern in wg0.conf and migrate to the modern
//     chain-based form, removing the live bypass rules in the same pass.
//
// Failures are logged but don't stop the loop — a transient iptables lock or
// missing binary on first boot shouldn't prevent subsequent passes.
func (s *Server) reconcileIPTables() {
	cfg := s.cfg()

	newIface := config.DetectDefaultInterface()
	if newIface == "" {
		// No default route — link is probably down. Skip; next tick will
		// try again once the link comes back.
		return
	}
	newLanCIDR := config.GetLocalNetworkCIDR(newIface)
	newLocalIP := cfg.DetectLocalInterface()

	// ---- Axis 1: LocalInterface (IP) ----
	if newLocalIP != "" && newLocalIP != cfg.LocalInterface {
		fmt.Printf("[iptables-sync] LocalInterface: %s -> %s\n", cfg.LocalInterface, newLocalIP)
		if err := s.updateConfig(func(c *config.Config) { c.LocalInterface = newLocalIP }); err != nil {
			fmt.Printf("[iptables-sync] persist LocalInterface: %v\n", err)
		}
		if err := s.dns.WriteConfig(); err != nil {
			fmt.Printf("[iptables-sync] dns WriteConfig: %v\n", err)
		} else if err := s.dns.Reload(); err != nil {
			fmt.Printf("[iptables-sync] dns Reload: %v\n", err)
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
		fmt.Printf("[iptables-sync] migrating legacy bypass PostUp to chain-based form (iface=%s)\n", newIface)
		if err := s.wg.UpdateInterfaceRules(wireguard.ExpectedPostUp(newIface), wireguard.ExpectedPostDown(newIface)); err != nil {
			fmt.Printf("[iptables-sync] migrate wg0.conf: %v\n", err)
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
		WGInterface: cfg.WGInterface,
		OutIface:    newIface,
		VPNRange:    cfg.VPNRange,
		LanCIDR:     newLanCIDR,
		Peers:       peers,
		ServerWGIP:  serverWGIP,
		ListenPort:  listenPort,
		JailedPeers: cfg.GetJailedPeers(),
		Profiles:    cfg.VPNProfiles,
	})
	stale := iptables.StaleRules(cfg, peers, serverWGIP, listenPort)

	live, err := iptables.LiveRules()
	if err != nil {
		fmt.Printf("[iptables-sync] LiveRules: %v\n", err)
		return
	}

	report := iptables.Reconcile(live, expected, stale, cfg.BlessedIPTablesRules,
		newIface, cfg.LastLocalIface)

	if len(report.Deleted) > 0 || len(report.Added) > 0 || report.InferredOld != "" {
		fmt.Printf("[iptables-sync] deleted=%d added=%d inferredOld=%q summary=%+v\n",
			len(report.Deleted), len(report.Added), report.InferredOld, report.Summary)
	}
	for _, e := range report.Errors {
		fmt.Printf("[iptables-sync] err: %s\n", e)
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
			fmt.Printf("[iptables-sync] persist last-iface/cidr: %v\n", err)
		}
	}

	// Rewrite wg0.conf PostUp/PostDown if the iface changed. Uses a regex
	// on the MASQUERADE clause so it works even when we inferred the old
	// iface (or don't know it at all) — we don't need the old name, just
	// "find the -o X -j MASQUERADE and swap X for the new iface."
	if ifaceChanged {
		oldUp := s.wg.GetPostUp()
		oldDown := s.wg.GetPostDown()
		repl := "-o " + newIface + " -j MASQUERADE"
		newUp := masqIfaceRe.ReplaceAllString(oldUp, repl)
		newDown := masqIfaceRe.ReplaceAllString(oldDown, repl)
		if newUp != oldUp || newDown != oldDown {
			if err := s.wg.UpdateInterfaceRules(newUp, newDown); err != nil {
				fmt.Printf("[iptables-sync] rewrite wg0.conf PostUp/Down: %v\n", err)
			}
		}
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
