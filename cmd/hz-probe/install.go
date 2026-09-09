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
		"--listen", f.listen,
		"--vantage", f.vantageName(),
		"--token-file", "%d/token",
		"--state", f.statePath,
	}
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

	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "hz-probe"); err != nil {
		return err
	}
	if *noStart {
		fmt.Println("Enabled. Start it with: systemctl start hz-probe")
	} else {
		if err := run("systemctl", "restart", "hz-probe"); err != nil {
			return err
		}
		fmt.Println("Started. Follow it with: journalctl -u hz-probe -f")
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

// run executes a command, surfacing its output when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
