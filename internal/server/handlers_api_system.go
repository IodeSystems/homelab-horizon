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
	// Logging sub-check: chrooted haproxy can't reach syslog if rsyslogd's
	// apparmor profile lacks `attach_disconnected` or /var/log/haproxy.log
	// doesn't exist. Both are fixable via /api/v1/haproxy/fix-logging.
	if hap.Installed {
		apparmorOK, apparmorReason := checkHAProxyApparmor()
		logFileOK := fileExists("/var/log/haproxy.log")
		hap.Extras = map[string]any{
			"logging_apparmor_ok": apparmorOK,
			"logging_file_exists": logFileOK,
		}
		if !apparmorOK {
			hap.Errors = append(hap.Errors, "logging: "+apparmorReason)
		}
		if !logFileOK {
			hap.Errors = append(hap.Errors, "logging: /var/log/haproxy.log missing (rsyslog drops privileges before it can create it)")
		}
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
			cfg.ListenAddr))
	}
	audit.Extras = map[string]any{
		"persistent":     facts.journalPersistent,
		"retention_days": int(facts.journalRetention.Hours() / 24),
		"requirement":    "10.5.1",
		// 2.2.7 rides along on the same card: both are about how this box is
		// administered and audited, and neither belongs to a service.
		"admin_loopback_only": cfg.AdminBoundToLoopback(),
		"listen_addr":         cfg.ListenAddr,
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

// checkHAProxyApparmor detects whether the rsyslogd apparmor profile has the
// `attach_disconnected` flag — without it, rsyslog denies messages from the
// chrooted HAProxy because the kernel presents the socket as a disconnected
// path and the logs are silently dropped. Returns (true, "") on hosts with no
// apparmor profile (not applicable).
func checkHAProxyApparmor() (bool, string) {
	const profilePath = "/etc/apparmor.d/usr.sbin.rsyslogd"
	data, err := os.ReadFile(profilePath)
	if err != nil {
		// No apparmor profile → not applicable to this host.
		return true, ""
	}
	if strings.Contains(string(data), "attach_disconnected") {
		return true, ""
	}
	return false, "rsyslogd apparmor profile is missing attach_disconnected flag — HAProxy logs are silently dropped"
}
