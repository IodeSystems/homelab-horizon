package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/autoheal"
	"homelab-horizon/internal/config"
	"homelab-horizon/internal/wireguard"
)

// Fixer endpoints for on-host system state. These restore the "fix this"
// buttons that lived on the old Go-template setup page (ripped in 807364b).
// Every endpoint is POST + admin-only; responses are JSON {ok:true} on
// success and {error:"..."} on failure.

func (s *Server) writeFixOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}

// systemdRun executes a command inside a transient one-shot unit so it escapes
// horizon's own ProtectSystem=strict sandbox. Mirrors the pattern the ripped
// handlers used. The command's combined stdout/stderr is returned for errors.
func systemdRun(args ...string) (string, error) {
	cmd := exec.Command("systemd-run", append([]string{"--pipe", "--wait", "--service-type=oneshot"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

// POST /api/v1/system/fix/ip-forwarding — sysctl net.ipv4.ip_forward=1
func (s *Server) handleAPISystemFixIPForwarding(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := wireguard.EnableIPForwarding(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/masquerade — iptables POSTROUTING -j MASQUERADE for
// the current default-route interface. Idempotent: if the rule already exists
// the command errors, but we check first via CheckSystem and skip adding.
func (s *Server) handleAPISystemFixMasquerade(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := wireguard.AddMasqueradeRule(s.cfg().VPNRange); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/wg-forward-chain — (re)install the WG-FORWARD chain
// with per-peer profile rules based on current config + peers + LAN CIDR.
func (s *Server) handleAPISystemFixWGForwardChain(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	cfg := s.cfg()
	lanCIDR := config.GetLocalNetworkCIDR(config.DetectDefaultInterface())
	peers := s.wg.GetPeers()
	if err := wireguard.SetupForwardChain(cfg.WGInterface, peers, cfg.VPNProfiles, cfg.VPNRange, lanCIDR); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/wg-rules — regenerate PostUp/PostDown in wg0.conf
// for the current default-route iface, then bounce the interface so new rules
// take effect.
func (s *Server) handleAPISystemFixWGRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	outIface := config.DetectDefaultInterface()
	if outIface == "" {
		outIface = "eth0"
	}
	postUp := wireguard.ExpectedPostUp(outIface)
	postDown := wireguard.ExpectedPostDown(outIface)
	if err := s.wg.UpdateInterfaceRules(postUp, postDown); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update rules: "+err.Error())
		return
	}
	if err := s.wg.InterfaceDown(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "restart down: "+err.Error())
		return
	}
	if err := s.wg.InterfaceUp(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "restart up: "+err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/wg/create-config — generate a fresh [Interface] wg0.conf with
// a new keypair + PostUp/PostDown for the current default iface. Only used
// on a truly fresh install; persists the server's public key into config.
func (s *Server) handleAPIWGCreateConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	cfg := s.cfg()

	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "keygen: "+err.Error())
		return
	}

	serverIP := strings.Split(cfg.VPNRange, "/")[0]
	serverIP = strings.TrimSuffix(serverIP, ".0") + ".1"

	outIface := config.DetectDefaultInterface()
	if outIface == "" {
		outIface = "eth0"
	}

	wgConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = 51820
PostUp = %s
PostDown = %s
`, privKey, serverIP, wireguard.ExpectedPostUp(outIface), wireguard.ExpectedPostDown(outIface))

	// /etc is sandboxed from horizon's own systemd unit — shell out through
	// systemd-run to escape ProtectSystem=strict.
	script := fmt.Sprintf("mkdir -p /etc/wireguard && chmod 700 /etc/wireguard && cat > %s && chmod 600 %s",
		cfg.WGConfigPath, cfg.WGConfigPath)
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot", "bash", "-c", script)
	cmd.Stdin = strings.NewReader(wgConfig)
	if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "write config: "+cmdErr.Error()+" — "+strings.TrimSpace(string(out)))
		return
	}

	if err := s.updateConfig(func(c *config.Config) { c.ServerPublicKey = pubKey }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save public key: "+err.Error())
		return
	}
	_ = s.wg.Load()

	s.writeFixOK(w)
}

// POST /api/v1/system/install/horizon-unit — writes the systemd unit that
// supervises horizon itself into /etc/systemd/system/homelab-horizon.service
// and runs daemon-reload so the new unit is visible.
func (s *Server) handleAPISystemInstallHorizonUnit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	binaryPath := "/usr/local/bin/homelab-horizon"
	if execPath, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(execPath); err == nil {
			binaryPath = abs
		}
	}
	configPath := s.configPath
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	content := config.GenerateServiceFile(binaryPath, configPath)

	const servicePath = "/etc/systemd/system/homelab-horizon.service"
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("cat > %s", servicePath))
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write unit: "+err.Error()+" — "+strings.TrimSpace(string(out)))
		return
	}
	if out, err := systemdRun("systemctl", "daemon-reload"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "daemon-reload: "+err.Error()+" — "+out)
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/enable/horizon — systemctl enable homelab-horizon
func (s *Server) handleAPISystemEnableHorizon(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if out, err := systemdRun("systemctl", "enable", "homelab-horizon"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error()+" — "+out)
		return
	}
	s.writeFixOK(w)
}

// aptAuditEntry is one line in the apt-audit.log JSONL file. Horizon has a
// single admin token so there's no per-user attribution — SourceIP is the
// closest we get to "who asked for this."
type aptAuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Package   string    `json:"package"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Output    string    `json:"output,omitempty"`
	SourceIP  string    `json:"source_ip,omitempty"`
}

// aptAuditPath is the JSONL log file next to config.json; one entry per line,
// append-only. Read via GET /api/v1/system/apt-audit (latest last, tail).
func (s *Server) aptAuditPath() string {
	return filepath.Join(filepath.Dir(s.configPath), "apt-audit.log")
}

// recordAptAudit appends one JSONL line. Best-effort: errors logged but not
// surfaced — the install itself already returned, and the audit being incomplete
// shouldn't break the response to the admin.
func (s *Server) recordAptAudit(entry aptAuditEntry) {
	f, err := os.OpenFile(s.aptAuditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apt-audit open: %v\n", err)
		return
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		fmt.Fprintf(os.Stderr, "apt-audit write: %v\n", err)
	}
}

// POST /api/v1/system/install/package — admin installs one of the apt
// packages horizon knows about. Whitelisted in autoheal.KnownPackages so
// this endpoint can't be coerced into running arbitrary apt commands.
// Every invocation is journaled to apt-audit.log (next to config.json)
// with timestamp, admin, output, and error — readable via /apt-audit.
func (s *Server) handleAPISystemInstallPackage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	var body struct {
		Package string `json:"package"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	if body.Package == "" {
		writeJSONError(w, http.StatusBadRequest, "package is required")
		return
	}

	output, err := autoheal.InstallPackage(body.Package)
	entry := aptAuditEntry{
		Timestamp: time.Now().UTC(),
		Package:   body.Package,
		Success:   err == nil,
		Output:    output,
		SourceIP:  s.getClientIP(r),
	}
	if err != nil {
		entry.Error = err.Error()
	}
	s.recordAptAudit(entry)

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"package": body.Package,
		"output":  output,
	})
}

// GET /api/v1/system/apt-audit — returns the last ~N entries of the
// apt-audit.log JSONL file. Newest first in the response.
func (s *Server) handleAPISystemAptAudit(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	f, err := os.Open(s.aptAuditPath())
	if err != nil {
		if os.IsNotExist(err) {
			// First-run case: no installs ever performed.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []aptAuditEntry{}})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	entries := []aptAuditEntry{}
	dec := json.NewDecoder(f)
	for {
		var entry aptAuditEntry
		if err := dec.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			// Skip malformed line — best-effort.
			continue
		}
		entries = append(entries, entry)
	}

	// Reverse: newest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

// POST /api/v1/dnsmasq/write-config — regenerate dnsmasq.conf from current
// config + service-derived DNS mappings. Does not reload; use /reload after.
func (s *Server) handleAPIDNSMasqWriteConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mappings := s.cfg().DeriveDNSMappings(); len(mappings) > 0 {
		if err := s.dns.SetMappings(mappings); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "mappings: "+err.Error())
			return
		}
	}
	s.writeFixOK(w)
}

// POST /api/v1/dnsmasq/reload — systemctl reload dnsmasq. Writes config
// first so the reload picks up any drift.
func (s *Server) handleAPIDNSMasqReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write-config: "+err.Error())
		return
	}
	if mappings := s.cfg().DeriveDNSMappings(); len(mappings) > 0 {
		if err := s.dns.SetMappings(mappings); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "mappings: "+err.Error())
			return
		}
	}
	if err := s.dns.Reload(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "reload: "+err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/dnsmasq/start — systemctl start dnsmasq. Internally ensures
// the service unit file exists (creates it if missing) before starting.
// Writes config first so the service comes up with horizon's settings.
func (s *Server) handleAPIDNSMasqStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write-config: "+err.Error())
		return
	}
	if mappings := s.cfg().DeriveDNSMappings(); len(mappings) > 0 {
		if err := s.dns.SetMappings(mappings); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "mappings: "+err.Error())
			return
		}
	}
	if err := s.dns.Start(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/haproxy/fix-logging — fixes the two common reasons HAProxy
// logs get silently dropped when the daemon is chrooted:
//  1. rsyslogd apparmor profile missing `attach_disconnected` flag.
//  2. /var/log/haproxy.log doesn't exist (rsyslog can't create it after
//     dropping privileges).
// Collects errors rather than bailing on the first — each sub-fix is
// independent and partial success is useful.
func (s *Server) handleAPIHAProxyFixLogging(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	var errs []string

	const profilePath = "/etc/apparmor.d/usr.sbin.rsyslogd"
	if data, err := os.ReadFile(profilePath); err == nil {
		content := string(data)
		if !strings.Contains(content, "attach_disconnected") {
			fixed := strings.Replace(content,
				"profile rsyslogd /usr/sbin/rsyslogd {",
				"profile rsyslogd /usr/sbin/rsyslogd flags=(attach_disconnected) {", 1)
			if fixed == content {
				errs = append(errs, "could not find apparmor profile declaration to patch")
			} else {
				cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
					"bash", "-c", fmt.Sprintf("cat > %s", profilePath))
				cmd.Stdin = strings.NewReader(fixed)
				if out, err := cmd.CombinedOutput(); err != nil {
					errs = append(errs, "write apparmor profile: "+err.Error()+" — "+strings.TrimSpace(string(out)))
				} else {
					if out, err := systemdRun("apparmor_parser", "-r", profilePath); err != nil {
						errs = append(errs, "reload apparmor: "+err.Error()+" — "+out)
					}
				}
			}
		}
	}

	if _, err := os.Stat("/var/log/haproxy.log"); os.IsNotExist(err) {
		script := "touch /var/log/haproxy.log && chown syslog:adm /var/log/haproxy.log && chmod 640 /var/log/haproxy.log"
		if out, err := systemdRun("bash", "-c", script); err != nil {
			errs = append(errs, "create log file: "+err.Error()+" — "+out)
		}
	}

	// Best-effort rsyslog bounce — not fatal if it fails.
	_, _ = systemdRun("systemctl", "restart", "rsyslog")

	if len(errs) > 0 {
		writeJSONError(w, http.StatusInternalServerError, strings.Join(errs, "; "))
		return
	}
	s.writeFixOK(w)
}
