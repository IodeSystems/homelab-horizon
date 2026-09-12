package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitPath = "/etc/systemd/system/hz-probe.service"

const (
	updateUnitPath  = "/etc/systemd/system/hz-probe-update.service"
	updateTimerPath = "/etc/systemd/system/hz-probe-update.timer"
)

// updateUnitTemplate runs the update as root.
//
// Deliberately not hardened the way the agent is: this one has to write
// /usr/local/bin and restart a service, which is the whole reason it is a
// separate unit instead of something the agent does. It runs for a few
// seconds a day and does nothing if the versions match.
const updateUnitTemplate = `[Unit]
Description=hz-probe - pull the build hz holds and restart if it differs
Documentation=https://github.com/iodesystems/homelab-horizon
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=__EXEC__
`

// updateTimerTemplate schedules it.
//
// Daily with a randomised delay: a fleet of vantages installed from the same
// command would otherwise all ask at the same second, and the only thing
// that achieves is a spike against the one host they all report to.
const updateTimerTemplate = `[Unit]
Description=hz-probe daily update check

[Timer]
OnCalendar=daily
RandomizedDelaySec=2h
Persistent=true

[Install]
WantedBy=timers.target
`

// unitTemplate is the systemd unit install writes.
//
// It runs unprivileged. The agent makes outbound DNS and HTTPS requests and
// listens on one port; none of that needs root, and this host sits on the
// public internet. The token and key stay root-owned at 0600 and reach the
// DynamicUser through systemd's credentials directory (%d), so nothing under
// /etc has to be made readable to run without privileges.
//
// Placeholders are __UPPER__ rather than printf verbs: the unit itself
// contains %d, and a Sprintf over this text would eat it.
const unitTemplate = `[Unit]
Description=hz-probe - outside-in vantage for homelab-horizon
Documentation=https://github.com/iodesystems/homelab-horizon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=__EXEC__
Restart=on-failure
RestartSec=5

DynamicUser=yes
StateDirectory=hz-probe
NoNewPrivileges=true
AmbientCapabilities=
__CREDENTIALS__
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
`

// generateUnit renders the unit for these flags.
//
// TLS credentials are included only when the certificate is actually there:
// a LoadCredential naming a missing file makes the service fail to start, so
// a unit written before the certificate exists would be worse than one that
// serves plain HTTP and says so.
func generateUnit(f *serveFlags, execPath string) string {
	creds := []string{"LoadCredential=token:" + f.tokenFile}
	args := []string{
		execPath, "serve",
		"--vantage", f.vantageName(),
		"--token-file", "%d/token",
		"--state", f.statePath,
	}

	// Push mode needs no listener, no certificate and no open port — it only
	// dials out. Emitting the listen and TLS flags anyway would suggest the
	// agent is reachable, which is the confusion this mode exists to remove.
	if f.pushMode() {
		args = append(args, "--push-to", f.pushTo)
		return render(args, creds, execPath)
	}

	args = append(args, "--listen", f.listen)
	if fileExists(f.tlsCert) && fileExists(f.tlsKey) {
		creds = append(creds,
			"LoadCredential=cert.pem:"+f.tlsCert,
			"LoadCredential=key.pem:"+f.tlsKey)
		args = append(args, "--tls-cert", "%d/cert.pem", "--tls-key", "%d/key.pem")
	} else {
		// Explicitly empty, in the --flag=value form: a bare "--tls-cert" with
		// an empty argument would let flag parsing swallow the next flag as
		// its value. serve then takes the plain-HTTP branch and says so.
		args = append(args, "--tls-cert=", "--tls-key=")
	}
	return render(args, creds, execPath)
}

// render fills the unit template.
func render(args, creds []string, _ string) string {

	return strings.NewReplacer(
		"__EXEC__", strings.Join(args, " "),
		"__CREDENTIALS__", strings.Join(creds, "\n"),
	).Replace(unitTemplate)
}

func runShowSystemd(args []string) error {
	fs := flag.NewFlagSet("show-systemd", flag.ExitOnError)
	var f serveFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Print(generateUnit(&f, execPath()))
	return nil
}

// execPath is the binary the unit should run: the one being executed now, so
// install wires up the copy the operator actually placed on the host.
func execPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/usr/local/bin/hz-probe"
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return exe
	}
	return abs
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var f serveFlags
	f.register(fs)
	dryRun := fs.Bool("dry-run", false, "print what install would do, change nothing")
	noStart := fs.Bool("no-start", false, "write and enable the unit, but do not start it")
	brief := fs.Bool("brief", false, "skip the trailing remote_probes block (the installer prints its own)")
	noAutoUpdate := fs.Bool("no-auto-update", false, "do not install the daily update timer")
	if err := fs.Parse(args); err != nil {
		return err
	}

	unit := generateUnit(&f, execPath())

	if *dryRun {
		fmt.Println("DRY RUN: no changes made.")
		fmt.Printf("Would mint a token at %s if absent\n", f.tokenFile)
		fmt.Printf("Would write %s:\n\n%s", unitPath, unit)
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to install the systemd unit")
	}

	minted, err := ensureToken(f.tokenFile)
	if err != nil {
		return err
	}
	if minted {
		fmt.Printf("Minted a token at %s\n", f.tokenFile)
	} else {
		fmt.Printf("Using the existing token at %s\n", f.tokenFile)
	}

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing the unit: %w", err)
	}
	fmt.Printf("Created %s\n", unitPath)

	// Validate before enabling. A unit that systemd will refuse is better
	// caught here than at the next reboot of a host you are not sitting at.
	if out, err := exec.Command("systemd-analyze", "verify", unitPath).CombinedOutput(); err != nil {
		fmt.Printf("Warning: systemd-analyze verify reported:\n%s\n", strings.TrimSpace(string(out)))
	}

	// The update timer, for push installs. In pull mode hz does not hold the
	// agent's token, so the unattended download has nothing to authenticate
	// with — those are updated by re-running the install command.
	if !*noAutoUpdate && f.pushMode() {
		if err := writeUpdateUnits(&f); err != nil {
			return err
		}
		fmt.Printf("Created %s and %s\n", updateUnitPath, updateTimerPath)
	}

	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "hz-probe"); err != nil {
		return err
	}
	if !*noAutoUpdate && f.pushMode() {
		if err := run("systemctl", "enable", "--now", "hz-probe-update.timer"); err != nil {
			// Not fatal: the agent works, it just will not update itself.
			// Failing the whole install over the timer would be worse.
			fmt.Printf("Warning: could not enable the update timer: %v\n", err)
		} else {
			fmt.Println("Daily update check enabled (hz-probe-update.timer).")
		}
	}

	if *noStart {
		fmt.Println("Enabled. Start it with: systemctl start hz-probe")
	} else {
		if err := run("systemctl", "restart", "hz-probe"); err != nil {
			return err
		}
		// A zero exit from restart is not the same as a service that stayed
		// up: a unit that starts and immediately dies satisfies restart and
		// then fails. Ask what state it is actually in, and say so — the
		// alternative is an operator who has to guess, which is exactly what
		// happened the first time this ran for real.
		switch state := serviceState(); {
		case state == "active":
			fmt.Println("Service hz-probe is active.")
			fmt.Println("Follow it with: journalctl -u hz-probe -f")
		case strings.HasPrefix(state, "unknown"):
			// systemctl could not be asked. Odd, but not evidence of
			// failure, and refusing to finish over it would strand an
			// install that is otherwise complete.
			fmt.Printf("Could not read the service state (%s).\n", state)
			fmt.Println("Check it with: systemctl status hz-probe --no-pager")
		default:
			fmt.Printf("Service hz-probe is %s, which is not what it should be.\n", state)
			fmt.Println("Look at:")
			fmt.Println("  systemctl status hz-probe --no-pager")
			fmt.Println("  journalctl -u hz-probe -n 50 --no-pager")
			return fmt.Errorf("hz-probe did not stay running (state: %s)", state)
		}
	}

	if *brief {
		return nil
	}

	if f.pushMode() {
		fmt.Printf("\nReporting to %s as %q.\n", f.pushTo, f.vantageName())
		fmt.Println("Nothing to paste back — it registers itself on the first report.")
		fmt.Println("If it does not appear in hz within a minute or two:")
		fmt.Println("  journalctl -u hz-probe -n 50 --no-pager")
		return nil
	}

	tok, err := os.ReadFile(f.tokenFile)
	if err != nil {
		return err
	}
	fmt.Printf("\nPut this in hz's config under \"remote_probes\":\n\n")
	fmt.Printf("  {\n")
	fmt.Printf("    \"name\": %q,\n", f.vantageName())
	fmt.Printf("    \"url\": \"https://<this-host>%s\",\n", f.listen)
	fmt.Printf("    \"token\": %q,\n", strings.TrimSpace(string(tok)))
	fmt.Printf("    \"enabled\": true")
	if fileExists(f.tlsCert) {
		if pin, err := fingerprintOf(f.tlsCert); err == nil {
			fmt.Printf(",\n    \"pin_sha256\": %q", pin)
		}
	} else {
		fmt.Printf("\n  }\n\nNo certificate at %s — the token is crossing the network in cleartext.\n", f.tlsCert)
		fmt.Printf("Fix it with: hz-probe gen-cert && hz-probe install\n")
		return nil
	}
	fmt.Printf("\n  }\n")
	return nil
}

// writeUpdateUnits writes the root-side updater and its timer.
func writeUpdateUnits(f *serveFlags) error {
	args := []string{
		execPath(), "update",
		"--token-file", f.tokenFile,
		"--push-to", f.pushTo,
	}
	unit := strings.NewReplacer("__EXEC__", strings.Join(args, " ")).Replace(updateUnitTemplate)
	if err := os.WriteFile(updateUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing the update unit: %w", err)
	}
	if err := os.WriteFile(updateTimerPath, []byte(updateTimerTemplate), 0o644); err != nil {
		return fmt.Errorf("writing the update timer: %w", err)
	}
	return nil
}

// ensureToken mints a token if the file is not already there, and reports
// whether it minted one. An existing token is never replaced: rotating it
// silently would break the hz that already holds it.
func ensureToken(path string) (bool, error) {
	if fileExists(path) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return false, fmt.Errorf("generating a token: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(buf)+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("writing the token: %w", err)
	}
	return true, nil
}

// serviceState is systemd's own word for what the unit is doing. Unknown
// rather than a guess when systemctl cannot be asked.
func serviceState() string {
	out, err := exec.Command("systemctl", "is-active", "hz-probe").Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		if err != nil {
			return "unknown (" + err.Error() + ")"
		}
		return "unknown"
	}
	return state
}

// run executes a command, surfacing its output when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
