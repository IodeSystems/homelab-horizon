package main

import (
	"fmt"
	"io"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Dependency reporting and the one explicit way to install them.
//
// `homelab-horizon install` used to print "Installation complete!" and three
// next steps on a box that had neither haproxy nor dnsmasq nor wireguard-tools.
// That message is the first thing an operator reads and it was a lie: starting
// the service at step 1 produced three failures and an `active` unit.
//
// Two rules shape what replaced it:
//
//   - hz never installs packages implicitly. Not at boot, not as a side effect
//     of installing a systemd unit. A surprise apt-get on a live gateway can
//     restart a daemon carrying traffic at a moment nobody chose.
//   - The install path must therefore be something a human or a provisioning
//     script asks for BY NAME, and must not require the web UI. You cannot
//     click a button in the admin UI of a gateway whose dependencies are
//     missing, and a UI button is not scriptable.
//
// Hence `install-deps` as a sibling verb to `install` and `check` (the CLI is
// flat verbs), `install --with-deps` for a single provisioning step, and
// `--dry-run` — already persistent, already meaning "show what would be done"
// — as the report-only mode. No new flag vocabulary.

// reportDependencies writes the dependency state to w and returns what is
// missing. It reads the config to know which optional subsystems are in scope:
// a box with HAProxy switched off is not missing haproxy.
func reportDependencies(w io.Writer, cfg *config.Config) []autoheal.Dependency {
	missing := autoheal.Missing(cfg)
	if len(missing) == 0 {
		_, _ = fmt.Fprintln(w, "Dependencies: all present.")
		return nil
	}

	_, _ = fmt.Fprintf(w, "Dependencies: %d MISSING\n", len(missing))
	for _, d := range missing {
		_, _ = fmt.Fprintf(w, "  %-26s (package %s)\n", d.Name, d.Package)
		_, _ = fmt.Fprintf(w, "  %-26s %s\n", "", d.Purpose)
	}
	return missing
}

// dependencyFixHint is the sentence that follows a missing-dependency report.
// One string, so the install summary and the install-deps failure cannot
// suggest two different commands.
const dependencyFixHint = "Install them with: sudo homelab-horizon install-deps"

// runInstallDeps is the `install-deps` verb.
func runInstallDeps(configPath string, dryRun bool) error {
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	missing := reportDependencies(os.Stdout, cfg)
	if len(missing) == 0 {
		return nil
	}

	if dryRun {
		fmt.Println()
		fmt.Println("DRY RUN: nothing was installed.")
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to install packages (try: sudo homelab-horizon install-deps)")
	}

	fmt.Println()
	fmt.Println("Installing...")
	if err := autoheal.InstallMissing(cfg); err != nil {
		return err
	}

	// Re-observe rather than assert success: apt can report 0 and still leave
	// a binary absent (a package that exists under another name on this
	// release, a partially configured dpkg state). "Installed" is a claim
	// about the box, so read the box.
	if still := autoheal.Missing(cfg); len(still) > 0 {
		fmt.Println()
		reportDependencies(os.Stdout, cfg)
		return fmt.Errorf("%d dependencies are still missing after installing", len(still))
	}
	fmt.Println("All dependencies present.")
	return nil
}
