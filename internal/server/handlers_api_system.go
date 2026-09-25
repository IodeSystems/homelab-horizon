package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
	"github.com/iodesystems/homelab-horizon/internal/system"
)

// handleAPISystemHealth returns per-component facts about the on-host software
// stack: is wg installed, is haproxy configured, is dnsmasq running, are the
// systemd units enabled, is IP forwarding on, etc. Data for the SystemTab
// dashboard — does not probe downstream services (see /api/v1/checks for that).
func (s *Server) handleAPISystemHealth(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	cfg := s.cfg()
	resp := apitypes.SystemHealthResponse{}

	// Range collision advice. Not a component and not fixable from here —
	// the remedy is renumbering a network — but it belongs on this page
	// because the symptom shows up somewhere else entirely: a remote worker
	// whose own network shares hz's LAN range loses the VPN, and hz's DNS
	// keeps answering with addresses that resolve to their side of it.
	lanAdvice, vpnAdvice := cfg.AdviseNetworks()
	if lanAdvice.Risk != config.RiskNone {
		resp.LANAdvice = &apitypes.CIDRAdviceResp{
			Range: lanAdvice.Range, Risk: lanAdvice.Risk,
			Reason: lanAdvice.Reason, Suggest: lanAdvice.Suggest,
		}
	}
	if vpnAdvice.Risk != config.RiskNone {
		resp.VPNAdvice = &apitypes.CIDRAdviceResp{
			Range: vpnAdvice.Range, Risk: vpnAdvice.Risk,
			Reason: vpnAdvice.Reason, Suggest: vpnAdvice.Suggest,
		}
	}

	// Services publishing at an address this host does not hold. Belongs
	// beside the range advice for the same reason: hz is doing exactly what
	// it was configured to do, the configuration is what went stale, and
	// nothing else in hz would ever say so.
	for _, w := range cfg.PinnedIPWarnings() {
		resp.PinnedIPs = append(resp.PinnedIPs, apitypes.PinnedIPResp{
			Service: w.Service, Domains: w.Domains, Pinned: w.Pinned,
			HostIP: w.HostIP, Redundant: w.Redundant,
		})
	}

	// IP forwarding — a system-wide prereq for WG to route. Read sysctl
	// directly rather than relying on the wg package so this shows up even
	// if WireGuard isn't installed yet.
	if data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		resp.IPForwarding = strings.TrimSpace(string(data)) == "1"
		if !resp.IPForwarding {
			resp.IPForwardingError = "sysctl net.ipv4.ip_forward is 0"
		}
	} else {
		resp.IPForwardingError = err.Error()
	}

	// horizon systemd unit — admins can confirm the service is set to survive
	// reboots and is currently up. File path matches the installer.
	if _, err := os.Stat("/etc/systemd/system/homelab-horizon.service"); err == nil {
		resp.HorizonUnitInstalled = true
	}
	resp.HorizonEnabled = systemdIsEnabled("homelab-horizon")
	resp.HorizonRunning = systemdIsActive("homelab-horizon")

	// WireGuard.
	wg := apitypes.ComponentHealth{Name: "wireguard"}
	wg.Installed = binaryOnPath("wg")
	if _, err := os.Stat(cfg.WGConfigPath); err == nil {
		wg.ConfigExists = true
	}
	wgIface := cfg.WGInterface
	wg.Enabled = systemdIsEnabled("wg-quick@" + wgIface)
	// "Running" for wg is iface-up (checked via `wg show <iface>`), not a
	// systemd unit — wg-quick exits immediately after bringing the iface up.
	if wg.Installed {
		sysStatus := s.wg.CheckSystem(cfg.VPNRange)
		wg.Running = sysStatus.InterfaceUp
		wg.Extras = map[string]any{
			"interface_up":  sysStatus.InterfaceUp,
			"ip_forwarding": sysStatus.IPForwarding,
			"masquerading":  sysStatus.Masquerading,
		}
		if sysStatus.InterfaceError != "" {
			wg.Errors = append(wg.Errors, "interface: "+sysStatus.InterfaceError)
		}
		if sysStatus.ForwardingError != "" {
			wg.Errors = append(wg.Errors, "forwarding: "+sysStatus.ForwardingError)
		}
		if sysStatus.MasqError != "" {
			wg.Errors = append(wg.Errors, "masquerade: "+sysStatus.MasqError)
		}
	}
	resp.Components = append(resp.Components, wg)

	// HAProxy.
	hap := apitypes.ComponentHealth{Name: "haproxy"}
	hap.Installed = binaryOnPath("haproxy")
	hapStatus := s.haproxy.GetStatus()
	hap.ConfigExists = hapStatus.ConfigExists
	hap.Running = hapStatus.Running
	hap.Enabled = systemdIsEnabled("haproxy")
	hap.Version = hapStatus.Version
	if hapStatus.Error != "" {
		hap.Errors = append(hap.Errors, hapStatus.Error)
	}
	// Logging sub-check: a chrooted haproxy cannot reach syslog if rsyslogd's
	// apparmor profile lacks `attach_disconnected`, or if /var/log/haproxy.log
	// does not exist.
	//
	// hz READS this and does not repair it. POST /api/v1/haproxy/fix-logging
	// used to, by piping an hz-built shell string through systemd-run to escape
	// hz's own sandbox; it moved to `sudo homelab-horizon fix-haproxy-logging`
	// and the card names that command (plan/design/privilege-audit.md §7 A,
	// "keep the diagnosis card").
	//
	// FOUR states cross the wire, not two booleans. The old pair reported
	// `logging_apparmor_ok: true` for any read error — so a profile hz could
	// not read looked identical to one that was correct, which is this repo's
	// founding bug (CLAUDE.md §2) in miniature and is the state hz will
	// actually be in after item 12 de-roots it.
	if hap.Installed {
		extras, errs := haproxyLoggingWire(system.DiagnoseHAProxyLoggingHere())
		hap.Extras = extras
		hap.Errors = append(hap.Errors, errs...)
	}
	resp.Components = append(resp.Components, hap)

	// dnsmasq.
	dns := apitypes.ComponentHealth{Name: "dnsmasq"}
	dns.Installed = binaryOnPath("dnsmasq")
	dnsStatus := s.dns.Status()
	dns.ConfigExists = dnsStatus.ConfigExists
	dns.Running = dnsStatus.Running
	dns.Enabled = dnsStatus.Enabled
	if dnsStatus.Error != "" {
		dns.Errors = append(dns.Errors, dnsStatus.Error)
	}
	if len(dnsStatus.MissingInterfaces) > 0 {
		dns.Extras = map[string]any{"missing_interfaces": dnsStatus.MissingInterfaces}
		dns.Errors = append(dns.Errors, "config missing interfaces: "+strings.Join(dnsStatus.MissingInterfaces, ", "))
	}
	// Cross-check: with bind-dynamic dnsmasq only listens on IPs owned by its
	// configured interfaces. If local_interface points at an IP that lives on
	// a different NIC than any in dnsmasq_interfaces, dnsmasq runs fine but
	// silently ignores requests on local_interface — invisible to MissingInterfaces.
	dnsAllIfaces := append([]string{cfg.WGInterface}, cfg.DNSMasqInterfaces...)
	bindCheck := dnsmasq.CheckLocalBind(cfg.LocalInterface, dnsAllIfaces)
	if !bindCheck.OK {
		if dns.Extras == nil {
			dns.Extras = map[string]any{}
		}
		dns.Extras["local_bind"] = map[string]any{
			"local_ip":       bindCheck.LocalIP,
			"bound_ips":      bindCheck.BoundIPs,
			"owning_iface":   bindCheck.OwningIface,
			"configured_ifs": bindCheck.ConfiguredIfs,
		}
		msg := "local_interface " + bindCheck.LocalIP + " is not on any dnsmasq interface (" + strings.Join(bindCheck.ConfiguredIfs, ", ") + ")"
		if bindCheck.OwningIface != "" {
			msg += " — IP lives on " + bindCheck.OwningIface
		}
		dns.Errors = append(dns.Errors, msg)
	}
	// Configuration coherence is one question; whether anything is listening is
	// another. With bind-dynamic dnsmasq starts happily and binds addresses as
	// they appear, so a correct config can still be answering on nothing.
	if cfg.DNSMasqEnabled && cfg.LocalInterface != "" {
		answering := dnsmasq.Answers(net.JoinHostPort(cfg.LocalInterface, "53"))
		if dns.Extras == nil {
			dns.Extras = map[string]any{}
		}
		dns.Extras["answers_on_local_interface"] = answering
		if !answering && dnsStatus.Running {
			dns.Errors = append(dns.Errors,
				"dnsmasq is running but not answering on "+cfg.LocalInterface+
					":53 — services bound to localhost will not resolve for VPN clients")
		}
	}

	// Every address dnsmasq should answer on, and whether it forwards.
	//
	// Two failures hid behind the checks above. dnsmasq binds the WireGuard
	// address separately (bind-dynamic), so it can stop answering there while
	// the LAN address stays perfect — and VPN clients are then the only ones
	// with no DNS at all. And every name hz resolves elsewhere is one dnsmasq
	// serves from its own `address=` lines, which answer exactly as well with
	// every upstream unreachable.
	if cfg.DNSMasqEnabled && dnsStatus.Running {
		probe := cfg.DNSProbeName
		if strings.TrimSpace(probe) == "" {
			probe = dnsmasq.DefaultProbeName
		}
		if dns.Extras == nil {
			dns.Extras = map[string]any{}
		}
		dns.Extras["probe_name"] = probe

		if why := dnsmasq.ProbeConflict(probe, servedDomains(cfg)); why != "" {
			dns.Extras["probe_conflict"] = why
			dns.Errors = append(dns.Errors, "forwarding check cannot run: "+why)
		} else {
			type listener struct {
				Addr     string   `json:"addr"`
				Role     string   `json:"role"`
				Answers  bool     `json:"answers"`
				Forwards bool     `json:"forwards"`
				IPs      []string `json:"ips,omitempty"`
				Err      string   `json:"err,omitempty"`
			}
			var listeners []listener
			for _, l := range []struct{ ip, role string }{
				{cfg.LocalInterface, "local_interface"},
				{cfg.GetWGGatewayIP(), "vpn"},
			} {
				if strings.TrimSpace(l.ip) == "" {
					continue
				}
				addr := net.JoinHostPort(l.ip, "53")
				answers := dnsmasq.Answers(addr)
				fwd := dnsmasq.Forwards(addr, probe)
				listeners = append(listeners, listener{
					Addr: addr, Role: l.role, Answers: answers,
					Forwards: fwd.OK, IPs: fwd.IPs, Err: fwd.Err,
				})
				switch {
				case !answers && l.role == "vpn":
					dns.Errors = append(dns.Errors,
						"dnsmasq is not answering on the VPN address "+addr+
							" — VPN clients have no DNS at all")
				case answers && !fwd.OK:
					dns.Errors = append(dns.Errors,
						"dnsmasq answers on "+addr+" but cannot resolve "+probe+
							" ("+fwd.Err+") — internal names still work, everything else fails")
				}
			}
			dns.Extras["listeners"] = listeners
		}
	}

	resp.Components = append(resp.Components, dns)

	// Let's Encrypt. "Installed" here means acme account configured, not a
	// binary — lego is compiled in.
	le := apitypes.ComponentHealth{Name: "letsencrypt"}
	if cfg.SSLEnabled {
		leMgr := letsencrypt.New(letsencrypt.Config{
			Domains:        cfg.DeriveSSLDomains(),
			CertDir:        cfg.SSLCertDir,
			HAProxyCertDir: cfg.SSLHAProxyCertDir,
		})
		leStatus := leMgr.GetStatus()
		le.Installed = leStatus.LegoAvailable
		// "Running" doesn't really apply — LE is request-driven. Report true
		// when all configured domains have a cert present.
		// Parallel to leStatus.Domains — same order, lets us pull ExtraSANs
		// from the original DomainConfig (DomainStatus doesn't carry them).
		sslDomains := cfg.DeriveSSLDomains()
		allHaveCerts := len(leStatus.Domains) > 0
		perDomain := make([]map[string]any, 0, len(leStatus.Domains))
		for i, d := range leStatus.Domains {
			if !d.CertExists {
				allHaveCerts = false
			}
			// NeedsRenewal decodes the cert and checks NotAfter vs the 30d
			// window used by the startup renewal sweep. Surfacing it in the
			// health payload lets the UI flag certs that'll need attention
			// soon even though the cert still exists.
			needsRenewal := false
			if d.CertExists {
				needsRenewal = leMgr.NeedsRenewal(
					leDomainFromStatus(d, cfg),
					certRenewalDays,
				)
			}
			var sans []string
			if i < len(sslDomains) {
				sans = sslDomains[i].ExtraSANs
			}
			perDomain = append(perDomain, map[string]any{
				"domain":        d.Domain,
				"sans":          sans,
				"cert_exists":   d.CertExists,
				"expiry_info":   d.ExpiryInfo,
				"provider":      d.ProviderType,
				"needs_renewal": needsRenewal,
			})
			if needsRenewal {
				le.Errors = append(le.Errors, d.Domain+": expires within "+strings.TrimSpace(d.ExpiryInfo))
			}
		}
		le.Running = allHaveCerts
		le.Extras = map[string]any{"domains": perDomain}

	} else {
		le.Extras = map[string]any{"disabled": true}
	}
	resp.Components = append(resp.Components, le)

	// Audit logging and admin exposure — the two PCI controls that are
	// properties of the host rather than of a service, so they get their own
	// card rather than being buried under one.
	facts := s.hostFacts.snapshot()
	// Installed/Running carry the sense the other cards give them: the journal
	// exists everywhere systemd does, and "running" here means it is actually
	// retaining what 10.5.1 asks for.
	audit := apitypes.ComponentHealth{
		Name:         "audit",
		Installed:    true,
		ConfigExists: facts.measured,
		Enabled:      facts.journalPersistent,
		Running:      facts.journalPersistent && facts.journalRetention >= 365*24*time.Hour,
	}
	switch {
	case !facts.measured:
		audit.Errors = append(audit.Errors, "not measured yet")
	case !facts.journalPersistent:
		audit.Errors = append(audit.Errors,
			"the journal is volatile: every log is lost at the next reboot")
	case facts.journalRetention == 0:
		audit.Errors = append(audit.Errors,
			"no retention limit is set, so logs rotate on size alone")
	case facts.journalRetention < 365*24*time.Hour:
		audit.Errors = append(audit.Errors, fmt.Sprintf(
			"logs are kept for %d days; PCI DSS 10.5.1 wants 365",
			int(facts.journalRetention.Hours()/24)))
	}
	if !cfg.AdminBoundToLoopback() {
		// No fixer for this one on purpose: rebinding the listener can cut off
		// whoever is reading the message, and the safe route depends on their
		// HAProxy vhost rather than on anything hz can decide.
		audit.Errors = append(audit.Errors, fmt.Sprintf(
			"the admin interface listens on %s, so it is reachable in cleartext off this host "+
				"(PCI DSS 2.2.7). Try it first as a start option — restart hz with "+
				"--listen 127.0.0.1:8080, which reverts on the next restart — then set "+
				"listen_addr in config.json once the HTTPS vhost is confirmed working.",
			cfg.EffectiveListenAddr()))
	}
	audit.Extras = map[string]any{
		"persistent":     facts.journalPersistent,
		"retention_days": int(facts.journalRetention.Hours() / 24),
		"requirement":    "10.5.1",
		// 2.2.7 rides along on the same card: both are about how this box is
		// administered and audited, and neither belongs to a service.
		"admin_loopback_only": cfg.AdminBoundToLoopback(),
		"listen_addr":         cfg.EffectiveListenAddr(),
		// Surfaced separately so the page can say "for this run" rather than
		// implying the file was changed.
		"listen_override": cfg.ListenOverride(),
	}
	resp.Components = append(resp.Components, audit)

	resp.PublicIP = cfg.PublicIP

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// binaryOnPath reports whether a named executable is found via $PATH.
func binaryOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// systemdIsActive reports `systemctl is-active <unit>` = active.
func systemdIsActive(unit string) bool {
	return exec.Command("systemctl", "is-active", unit).Run() == nil
}

// systemdIsEnabled reports `systemctl is-enabled <unit>` = enabled.
// Returns false for "static", "masked", "disabled", or any failure.
func systemdIsEnabled(unit string) bool {
	out, err := exec.Command("systemctl", "is-enabled", unit).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "enabled"
}

// leDomainFromStatus rebuilds the DomainConfig that produced a DomainStatus —
// the status struct doesn't carry enough to call NeedsRenewal, so we re-look
// it up from the derived domain list. Returns zero value if no match.
func leDomainFromStatus(ds letsencrypt.DomainStatus, cfg *config.Config) letsencrypt.DomainConfig {
	for _, d := range cfg.DeriveSSLDomains() {
		if d.Domain == ds.Domain {
			return d
		}
	}
	return letsencrypt.DomainConfig{}
}

// haproxyLoggingWire turns the diagnosis into what the card reads.
//
// A FUNCTION, not four lines inline in the handler, because it is the only part
// of this that can be tested without the host: DiagnoseHAProxyLoggingHere reads
// /etc/apparmor.d, so the handler itself measures whatever box the test runs on.
// Extracting it is what lets "unknown does not render as fine" be pinned — it
// was NOT, and a control that turned the unknown state back into LogOK reddened
// two tests in other packages while every test in this one stayed green.
//
// The states reach the UI as strings, not booleans. The pair this replaced —
// logging_apparmor_ok / logging_file_exists — had no way to say "I could not
// read it", so it said "true".
func haproxyLoggingWire(d system.HAProxyLogging) (map[string]any, []string) {
	extras := map[string]any{
		"logging_apparmor":    string(d.AppArmor),
		"logging_file":        string(d.LogFile),
		"logging_fix_command": system.FixHAProxyLoggingCommand,
	}
	// Only the two states that are actually a problem become errors.
	// LogNotApplicable carries a sentence too ("nothing confines rsyslogd on
	// this host"), and that is an explanation rather than a fault — it travels
	// in Extras and must not redden the card.
	var errs []string
	for _, f := range []struct {
		state  system.LogCheck
		detail string
	}{
		{d.AppArmor, d.AppArmorDetail},
		{d.LogFile, d.LogFileDetail},
	} {
		if f.state == system.LogBroken || f.state == system.LogUnknown {
			errs = append(errs, "logging: "+f.detail)
		}
	}
	return extras, errs
}

// servedDomains is every name dnsmasq answers from hz's own config. The
// forwarding probe must not be one of them.
func servedDomains(cfg *config.Config) []string {
	var out []string
	for _, svc := range cfg.Services {
		out = append(out, svc.Domains...)
	}
	for _, z := range cfg.Zones {
		out = append(out, z.Name)
	}
	return out
}
