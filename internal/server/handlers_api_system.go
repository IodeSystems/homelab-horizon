package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/letsencrypt"
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
		allHaveCerts := len(leStatus.Domains) > 0
		perDomain := make([]map[string]any, 0, len(leStatus.Domains))
		for _, d := range leStatus.Domains {
			if !d.CertExists {
				allHaveCerts = false
			}
			perDomain = append(perDomain, map[string]any{
				"domain":      d.Domain,
				"cert_exists": d.CertExists,
				"expiry_info": d.ExpiryInfo,
				"provider":    d.ProviderType,
			})
		}
		le.Running = allHaveCerts
		le.Extras = map[string]any{"domains": perDomain}
	} else {
		le.Extras = map[string]any{"disabled": true}
	}
	resp.Components = append(resp.Components, le)

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
