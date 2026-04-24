package server

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/iptables"
)

// masqIfaceRe matches the iface token in an iptables MASQUERADE clause embedded
// in a wg0.conf PostUp/PostDown line. Used to rewrite wg0.conf when the
// default-route iface changes — the regex approach works even when we don't
// know the old iface name (first-reconcile bootstrap case).
var masqIfaceRe = regexp.MustCompile(`-o \S+ -j MASQUERADE`)

// reconcileIPTables is the single-entry self-heal for on-host state that drifts
// when the LAN interface changes. It runs at startup and on every tick of
// startHealthCheck (60s), and handles three independent drifts:
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
