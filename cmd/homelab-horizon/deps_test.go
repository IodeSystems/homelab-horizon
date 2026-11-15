package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The finding: `homelab-horizon install` printed "Installation complete!" and
// three next steps on a box with neither haproxy nor dnsmasq nor
// wireguard-tools. That message is the first thing an operator reads, and
// step 1 ("start service") could only fail.
//
// These run against whatever this machine has installed, so they assert on the
// SHAPE of the report rather than a fixed package list — the content is
// covered by internal/autoheal's tests with a stubbed PATH.

func TestReportDependenciesNamesWhatIsMissingAndWhyItMatters(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{DNSMasqEnabled: true, HAProxyEnabled: true}
	missing := reportDependencies(&buf, cfg)
	out := buf.String()

	if out == "" {
		t.Fatal("the dependency report printed nothing at all")
	}
	if len(missing) == 0 {
		if !strings.Contains(out, "all present") {
			t.Errorf("a complete box must say so plainly; got %q", out)
		}
		return
	}

	if !strings.Contains(out, "MISSING") {
		t.Errorf("a box with missing dependencies must say MISSING; got %q", out)
	}
	for _, d := range missing {
		if !strings.Contains(out, d.Package) {
			t.Errorf("the report does not name package %q:\n%s", d.Package, out)
		}
		if !strings.Contains(out, d.Purpose) {
			t.Errorf("the report does not say what %q is for:\n%s", d.Package, out)
		}
	}
}

// One remedy, named once, and it must be the explicit verb rather than a bare
// apt-get that bypasses hz's package allow-list.
func TestDependencyFixHintPointsAtTheExplicitVerb(t *testing.T) {
	if !strings.Contains(dependencyFixHint, "install-deps") {
		t.Fatalf("the fix hint must name the install-deps verb; got %q", dependencyFixHint)
	}
	if strings.Contains(dependencyFixHint, "apt install ") || strings.Contains(dependencyFixHint, "apt-get install") {
		t.Fatalf("the fix hint must not tell operators to apt-get around the allow-list; got %q", dependencyFixHint)
	}
}

// The CLI must expose an explicit, scriptable way to install dependencies. A
// UI button is not one: you cannot open the admin UI of a gateway whose
// dependencies are missing, and a button is not scriptable.
func TestInstallDepsIsAVerbAndInstallCarriesWithDeps(t *testing.T) {
	var sawInstallDeps, sawInstall bool
	for _, c := range newRoot().Commands() {
		switch c.Name() {
		case "install-deps":
			sawInstallDeps = true
		case "install":
			sawInstall = true
			if c.Flags().Lookup("with-deps") == nil {
				t.Error("install has no --with-deps flag: provisioning cannot do it in one step")
			}
			if !strings.Contains(strings.ToLower(c.Short), "dependenc") {
				t.Errorf("install's help must say it reports dependencies; got %q", c.Short)
			}
		}
	}
	if !sawInstallDeps {
		t.Fatal("there is no install-deps verb: installing dependencies is not scriptable")
	}
	if !sawInstall {
		t.Fatal("the install verb disappeared")
	}
}

// --dry-run must report without installing. It is the persistent flag the CLI
// already has, so this is the report-only mode with no new vocabulary.
func TestInstallDepsDryRunInstallsNothing(t *testing.T) {
	// A non-existent config path makes loadConfig fail, which is the wrong
	// thing to assert on; use the auto-discovered one and only check that a
	// dry run never shells out to apt (it would fail as a non-root user in a
	// sandbox, and the test would not be silent about it).
	if err := runInstallDeps("", true); err != nil {
		t.Fatalf("a dry run must succeed and change nothing; got %v", err)
	}
}
