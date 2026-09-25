package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// Fixer endpoints for on-host system state. These restore the "fix this"
// buttons that lived on the old Go-template setup page (ripped in 807364b).
// Every endpoint is POST + admin-only; responses are JSON {ok:true} on
// success and {error:"..."} on failure.
//
// NOTHING HERE BUILDS A SHELL STRING, and there is no longer a helper that
// could. `systemdRun` lived at the top of this file — a variadic
// `systemd-run --pipe --wait --service-type=oneshot <anything>` that let an
// HTTP handler run a command as root outside hz's own sandbox — and it is
// gone, along with its four call sites. Three were in the haproxy fix-logging
// handler; the fourth was that handler's inline `bash -c "cat > …"`, which a
// grep for the helper's name would have missed.
//
// The two handlers that needed it moved to the binaries that own the files:
// `sudo hz-agent wg-create-config` writes wg0.conf, `sudo homelab-horizon
// fix-haproxy-logging` repairs haproxy's logging. Both had been "move" items
// on item 12's readiness checklist (plan/design/privilege-audit.md §7 A) and
// the checklist named them as systemdRun's last two callers — which was stale:
// wg/create-config never called it, so moving fix-logging alone is what
// retired it.
//
// The remaining fixers here take TYPED arguments the whole way down — an
// iptables rule, a sysctl, a systemd unit name, all constants or config
// values, never a string assembled for a shell. `shell_guard_test.go` fails
// the build if that stops being true.

func (s *Server) writeFixOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}

// POST /api/v1/system/fix/ip-forwarding — sysctl net.ipv4.ip_forward=1
func (s *Server) handleAPISystemFixIPForwarding(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
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
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
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
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if err := wireguard.SetupForwardChain(s.cfg().WGInterface, s.wgChainOpts()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/wg-rules — regenerate PostUp/PostDown in wg0.conf
// for the current default-route iface, then bounce the interface so new rules
// take effect.
func (s *Server) handleAPISystemFixWGRules(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
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

// aptAuditEntry is one line in the apt-audit.log JSONL file. Horizon has a
// single admin token so there's no per-user attribution — SourceIP is the
// closest we get to "who asked for this."
//
// HISTORICAL as of 2026-09-22. POST /system/install/package was the only writer
// and is gone (privilege-classification.md §3.1 #8) — packages are installed by
// `homelab-horizon install-deps`, which journals rather than writing here. The
// read stays because the file is the only record of what the button installed
// while it existed, and answering "was it ever pressed on this box" is the
// question §3.1 said to read it for. Nothing appends to it any more, so a box
// provisioned after this commit will have none.
type aptAuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Package   string    `json:"package"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Output    string    `json:"output,omitempty"`
	SourceIP  string    `json:"source_ip,omitempty"`
}

// aptAuditPath is the JSONL log file next to config.json; one entry per line.
// Read-only now — see aptAuditEntry. Read via GET /api/v1/system/apt-audit.
func (s *Server) aptAuditPath() string {
	return filepath.Join(filepath.Dir(s.configPath), "apt-audit.log")
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
	defer func() { _ = f.Close() }()

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
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if records := s.cfg().DeriveDNSRecords(); len(records) > 0 {
		if err := s.dns.SetRecords(records); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "mappings: "+err.Error())
			return
		}
	}
	s.writeFixOK(w)
}

// POST /api/v1/dnsmasq/reload — systemctl reload dnsmasq. Writes config
// first so the reload picks up any drift.
func (s *Server) handleAPIDNSMasqReload(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write-config: "+err.Error())
		return
	}
	if records := s.cfg().DeriveDNSRecords(); len(records) > 0 {
		if err := s.dns.SetRecords(records); err != nil {
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

// POST /api/v1/dnsmasq/fix-interfaces — adds the NIC that actually owns
// local_interface IP to dnsmasq_interfaces, then rewrites + reloads dnsmasq.
// Catches the case where local_interface points at one NIC but dnsmasq is
// configured for a different NIC (so it silently ignores requests on the
// expected IP).
func (s *Server) handleAPIDNSMasqFixInterfaces(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	cfg := s.cfg()
	if cfg.LocalInterface == "" {
		writeJSONError(w, http.StatusBadRequest, "local_interface is empty — nothing to fix")
		return
	}

	currentIfs := append([]string{cfg.WGInterface}, cfg.DNSMasqInterfaces...)
	check := dnsmasq.CheckLocalBind(cfg.LocalInterface, currentIfs)
	if check.OK {
		s.writeFixOK(w)
		return
	}
	if check.OwningIface == "" {
		writeJSONError(w, http.StatusInternalServerError,
			"no interface owns local_interface "+cfg.LocalInterface+" — set local_interface to an IP that exists on this host")
		return
	}

	newDNSIfs := append([]string{}, cfg.DNSMasqInterfaces...)
	for _, name := range newDNSIfs {
		if name == check.OwningIface {
			s.writeFixOK(w)
			return
		}
	}
	newDNSIfs = append(newDNSIfs, check.OwningIface)

	if err := s.updateConfig(func(c *config.Config) {
		c.DNSMasqInterfaces = newDNSIfs
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}

	s.dns.UpdateInterfaces(append([]string{s.cfg().WGInterface}, newDNSIfs...))

	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write-config: "+err.Error())
		return
	}
	if records := s.cfg().DeriveDNSRecords(); len(records) > 0 {
		if err := s.dns.SetRecords(records); err != nil {
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
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if err := s.dns.WriteConfig(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write-config: "+err.Error())
		return
	}
	if records := s.cfg().DeriveDNSRecords(); len(records) > 0 {
		if err := s.dns.SetRecords(records); err != nil {
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

// POST /api/v1/system/fix/log-retention — make the journal survive reboots and
// keep a year of it (PCI DSS 10.5.1).
//
// Written as a drop-in rather than an edit to journald.conf: the main file is
// a package-managed default full of commented examples, and rewriting it means
// owning a merge with every future upgrade. A drop-in is additive, obvious in
// `systemd-analyze cat-config`, and removable by deleting one file.
func (s *Server) handleAPISystemFixLogRetention(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	// journald creates nothing itself: with Storage=persistent and no
	// directory it silently stays volatile, so the directory comes first.
	if err := s.fs.MkdirAll("/var/log/journal", 0o755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create /var/log/journal: "+err.Error())
		return
	}

	const dropInDir = "/etc/systemd/journald.conf.d"
	if err := s.fs.MkdirAll(dropInDir, 0o755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create "+dropInDir+": "+err.Error())
		return
	}

	// SystemMaxUse is set alongside retention because the two bounds are
	// independent: without a size cap a year of logs can fill the disk, and
	// filling the disk on a gateway takes far more than logging with it.
	const conf = `# Written by homelab-horizon for PCI DSS 10.5.1.
# Twelve months of audit history, with a size cap so a year of logs cannot
# fill the disk this gateway runs on.
[Journal]
Storage=persistent
MaxRetentionSec=1year
SystemMaxUse=2G
`
	if err := s.fs.WriteFile(dropInDir+"/99-homelab-horizon.conf", []byte(conf), 0o644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not write the drop-in: "+err.Error())
		return
	}

	if err := s.runner.Run(r.Context(), "systemctl", "restart", "systemd-journald"); err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"config written, but journald did not restart: "+err.Error())
		return
	}

	// Re-measure now rather than waiting for the health tick, so the card the
	// operator is looking at reflects what they just did.
	s.hostFacts.refresh()

	slog.Info("journal made persistent with a year of retention", "by", s.adminActor(r))
	s.writeFixOK(w)
}
