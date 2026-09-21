package dnsmasq

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// This file installs and drives a systemd unit, and that is a *different*
// privilege from the one apply.go needs.
//
// apply.go writes two files under /etc/dnsmasq.d. This file writes
// /etc/systemd/system/dnsmasq.service, runs daemon-reload, and starts,
// restarts and reset-fails a unit — via systemd-run, specifically to escape
// hz's own ProtectSystem=strict sandbox. Anything that can do this can install
// a unit that runs anything as root; the file-writing half cannot. Keeping the
// two apart is the point of the separation, so hz-agent can expose them as
// separate operations rather than one "manage dnsmasq" capability.
//
// Status() reads the unit's state and lives here too — same sandbox escape is
// not needed to read, but the systemctl surface is one thing to keep together.

// Reload applies configuration changes by restarting dnsmasq.
//
// A restart rather than SIGHUP because hz's records live in a conf-file
// include, and SIGHUP only re-reads /etc/hosts and addn-hosts files — it would
// clear the cache and change nothing.
//
// The failure counter is cleared first, and that is not defensive noise: hz
// restarts dnsmasq on every record change, and systemd's StartLimitBurst stops
// honouring starts after a handful in quick succession. An operator adding
// three records in a row would leave the LAN with no resolver at all, needing a
// manual reset-failed to recover — a self-inflicted outage on the box whose job
// is resolving names.
func (d *DNSMasq) Reload() error {
	if err := d.ensureServiceUnit(); err != nil {
		return err
	}
	d.clearStartLimit()
	return systemctlWithJournal("restart", "dnsmasq")
}

func (d *DNSMasq) Start() error {
	if err := d.ensureServiceUnit(); err != nil {
		return err
	}
	d.clearStartLimit()
	return systemctlWithJournal("start", "dnsmasq")
}

// clearStartLimit resets systemd's restart rate limiter for dnsmasq.
//
// Best-effort: a failure here means the start below reports the real problem,
// and reset-failed on a healthy unit is a no-op.
func (d *DNSMasq) clearStartLimit() {
	_ = systemctlWithJournal("reset-failed", "dnsmasq")
}

// ensureServiceUnit creates dnsmasq.service if it doesn't exist.
// Ubuntu's dnsmasq package uses sysv init scripts and relies on
// systemd-sysv-generator, which may not run when installed via
// a transient systemd-run service.
func (d *DNSMasq) ensureServiceUnit() error {
	servicePath := "/etc/systemd/system/dnsmasq.service"

	// If a system-provided unit exists (not ours), use it
	existingContent := ""
	if out, err := exec.Command("systemctl", "cat", "dnsmasq.service").CombinedOutput(); err == nil {
		if !strings.Contains(string(out), "managed by homelab-horizon") {
			return nil
		}
		existingContent = string(out)
	}

	// Check dnsmasq binary exists
	dnsmasqBin, err := exec.LookPath("dnsmasq")
	if err != nil {
		return fmt.Errorf("dnsmasq binary not found — install it first")
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=dnsmasq - managed by homelab-horizon
After=network.target

[Service]
Type=simple
ExecStart=%s -k -C %s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, dnsmasqBin, d.configPath)

	// Skip if content hasn't changed
	if strings.Contains(existingContent, strings.TrimSpace(serviceContent)) {
		return nil
	}

	// Create/update our service unit
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("cat > %s && systemctl daemon-reload", servicePath))
	cmd.Stdin = strings.NewReader(serviceContent)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create dnsmasq.service: %v — %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// systemctlWithJournal runs a systemctl command and, on failure, pulls recent
// journal lines for the unit so the caller gets a useful error message.
// Uses systemd-run to escape ProtectSystem=strict sandbox.
func systemctlWithJournal(action, unit string) error {
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"systemctl", action, unit)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Grab last 10 journal lines for context
		journal := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
			"journalctl", "-u", unit, "-n", "10", "--no-pager", "-o", "cat")
		journalOut, _ := journal.CombinedOutput()
		detail := strings.TrimSpace(string(out))
		if len(journalOut) > 0 {
			detail += "\n" + strings.TrimSpace(string(journalOut))
		}
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s %s failed: %s", action, unit, detail)
	}
	return nil
}

type Status struct {
	Running           bool
	Enabled           bool
	ConfigExists      bool
	Error             string
	MissingInterfaces []string // Configured interfaces not found in dnsmasq config
}

func (d *DNSMasq) Status() Status {
	status := Status{}

	cmd := exec.Command("systemctl", "is-active", "dnsmasq")
	if err := cmd.Run(); err == nil {
		status.Running = true
	}

	cmd = exec.Command("systemctl", "is-enabled", "dnsmasq")
	if err := cmd.Run(); err == nil {
		status.Enabled = true
	}

	if _, err := os.Stat(d.configPath); err == nil {
		status.ConfigExists = true
	}

	// Check if all configured interfaces are present in the config file
	if status.ConfigExists {
		status.MissingInterfaces = d.checkMissingInterfaces()
	}

	return status
}
