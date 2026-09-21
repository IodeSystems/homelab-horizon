package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitPath = "/etc/systemd/system/hz-agent.service"

// unitTemplate is the systemd unit install writes.
//
// It runs as root, unlike hz-probe's, and there is no way around that: the
// whole reason this binary exists is to hold the privilege hz is giving up.
// What it does NOT do is start applying the moment it is enabled —
// ExecStart deliberately carries no --apply, so an enabled, started hz-agent
// computes a diff and logs it. Turning on the writing is a separate, visible
// edit to this file.
//
// There is no [Install] WantedBy section either. A unit with one can be
// enabled with `systemctl enable`; without it, `enable` fails and says so.
// That is the difference between "somebody decided" and "somebody
// autocompleted", and it can be removed in one commit when item 12 flips.
//
// Placeholders are __UPPER__ rather than printf verbs, matching hz-probe's
// installer: a unit file can contain %-escapes of its own and a Sprintf over
// this text would eat them.
const unitTemplate = `[Unit]
Description=hz-agent - the privileged on-box half of homelab-horizon
Documentation=https://github.com/iodesystems/homelab-horizon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=__EXEC__
Restart=on-failure
RestartSec=10

# Root, because applying means writing /etc and reloading services. It is
# doing nothing with that privilege: the apply flag is absent from ExecStart
# above, so this unit computes a diff and logs it.
User=root

NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true
ProtectKernelModules=true
LockPersonality=true
MemoryDenyWriteExecute=true

# There is deliberately no install section here, so "systemctl enable" has
# nothing to hook the unit onto and refuses. hz still owns this box. See
# plan/architecture.md phase 4, item 12 — that is the commit where the agent
# takes over, and where this paragraph goes away.
`

// generateUnit renders the unit for these flags.
//
// --apply is never emitted, whatever the caller passed. install accepts the
// flag set for uniformity, and an installer that could write an applying unit
// would make the inert default one typo deep.
func generateUnit(f *agentFlags, execPath string) string {
	args := []string{
		execPath, "run",
		"--hz", f.hzURL,
		"--token-file", f.tokenFile,
		"--interval", f.interval.String(),
	}
	if f.machine != "" {
		args = append(args, "--machine", f.machine)
	}
	return strings.NewReplacer("__EXEC__", strings.Join(args, " ")).Replace(unitTemplate)
}

// execPath is the binary the unit should run: the one being executed now, so
// install wires up the copy the operator actually placed on the host.
func execPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/usr/local/bin/hz-agent"
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return exe
	}
	return abs
}

func runShowSystemd(args []string) error {
	fs := flag.NewFlagSet("show-systemd", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Print(generateUnit(&f, execPath()))
	return nil
}

// runInstall writes the unit, behind a Geteuid check, and stops there.
//
// The third instance of the house pattern (cmd/hz-probe/install.go,
// cmd/homelab-horizon's install), with one difference that is the point of
// this whole item: hz-probe's installer enables and starts its unit. This one
// does neither. hz is still root on this box and still applying its own
// config; an agent that started reconciling haproxy beside it would be two
// processes fighting over the gateway.
//
// So installing this on the live gateway writes one file and changes nothing
// that runs.
//
// It does one more thing than it used to: it ENROLS the machine first, because
// an agent with no credential cannot poll at all (plan/privilege-audit.md
// §1.1) and an installer that leaves a unit unable to authenticate is the
// half-done shape that hid the bug. Enrolment is still inert — a credential
// buys a READ of the desired state; applying needs the three separate
// decisions above.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	dryRun := fs.Bool("dry-run", false, "print what install would do, change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	unit := generateUnit(&f, execPath())

	if *dryRun {
		fmt.Println("DRY RUN: no changes made.")
		fmt.Printf("Would enrol this machine with hz: mint a credential into %s\n", f.tokenFile)
		fmt.Printf("and record its hash in %s.\n\n", f.hzCredentials)
		fmt.Printf("Would write %s:\n\n%s", unitPath, unit)
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to install the systemd unit")
	}

	// The credential first, so a machine is never left with a unit it cannot
	// authenticate with. Safe to re-run: an enrolment that already matches is
	// left alone. It prints paths, never the secret.
	if err := enroll(&f, false, os.Stdout); err != nil {
		return fmt.Errorf("enrolling this machine: %w", err)
	}
	fmt.Println()

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing the unit: %w", err)
	}
	fmt.Printf("Created %s\n", unitPath)

	// Validate before leaving it there. A unit systemd will refuse is better
	// caught now than by whoever enables it later.
	if out, err := exec.Command("systemd-analyze", "verify", unitPath).CombinedOutput(); err != nil {
		fmt.Printf("Note: systemd-analyze verify reported:\n%s\n", strings.TrimSpace(string(out)))
	}

	// daemon-reload so systemd knows the file exists. It does not enable,
	// start or otherwise run anything.
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		fmt.Printf("Note: systemctl daemon-reload said:\n%s\n", strings.TrimSpace(string(out)))
	}

	fmt.Println()
	fmt.Println("The unit is NOT enabled and NOT started, on purpose.")
	fmt.Println("hz still runs as root and still applies its own config; two processes")
	fmt.Println("reconciling the same haproxy is how the gateway breaks.")
	fmt.Println()
	fmt.Println("Nothing on this machine changed. To see what the agent would do:")
	fmt.Printf("  %s diff --hz %s\n", execPath(), f.hzURL)
	fmt.Println()
	fmt.Println("The unit has no [Install] section, so 'systemctl enable hz-agent' will")
	fmt.Println("refuse until the commit that hands ownership over (architecture.md,")
	fmt.Println("phase 4 item 12) adds one.")
	return nil
}
