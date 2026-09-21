package autoheal

import (
	"errors"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// withPresent replaces the PATH lookup with a fixed set of installed binaries
// for the duration of one test.
func withPresent(t *testing.T, present ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	prev := lookPath
	lookPath = func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("exec: \"" + name + "\": executable file not found in $PATH")
	}
	t.Cleanup(func() { lookPath = prev })
}

// The finding: after install + start as root, haproxy, dnsmasq,
// wireguard-tools and qrencode were all still missing and nothing said so.
// Missing is the observation that makes saying so possible, and it must work
// without root and without installing anything.
func TestMissingReportsEveryAbsentRequiredDependency(t *testing.T) {
	withPresent(t, "ip") // a base image: iproute2 only
	cfg := &config.Config{DNSMasqEnabled: true, HAProxyEnabled: true}

	got := map[string]Dependency{}
	for _, d := range Missing(cfg) {
		got[d.Package] = d
	}

	for _, pkg := range []string{"wireguard-tools", "iptables", "qrencode", "dnsmasq", "haproxy"} {
		d, ok := got[pkg]
		if !ok {
			t.Fatalf("%s is absent from PATH but Missing did not report it; reported %v", pkg, got)
		}
		if d.Purpose == "" {
			t.Errorf("%s: a missing dependency must say what stops working without it", pkg)
		}
	}
	if _, ok := got["iproute2"]; ok {
		t.Error("iproute2 is installed; Missing must not report it")
	}
}

func TestMissingRespectsWhichSubsystemsAreEnabled(t *testing.T) {
	withPresent(t, "ip", "wg", "iptables", "qrencode")
	// Both optional subsystems off: a box that does not run them is not
	// missing them, and reporting otherwise trains operators to ignore the
	// report.
	if got := Missing(&config.Config{}); len(got) != 0 {
		t.Fatalf("nothing required is absent; got %+v", got)
	}
	if got := Missing(&config.Config{DNSMasqEnabled: true}); len(got) != 1 || got[0].Package != "dnsmasq" {
		t.Fatalf("want only dnsmasq; got %+v", got)
	}
}

func TestMissingReportsNothingOnACompleteBox(t *testing.T) {
	withPresent(t, "ip", "wg", "iptables", "qrencode", "dnsmasq", "haproxy")
	if got := Missing(&config.Config{DNSMasqEnabled: true, HAProxyEnabled: true}); len(got) != 0 {
		t.Fatalf("every dependency is present; got %+v", got)
	}
}

// Everything Missing reports must be installable through the existing
// allow-list, or InstallMissing refuses its own input and the explicit install
// verb is dead on arrival.
func TestEveryDependencyIsOnTheKnownPackagesAllowList(t *testing.T) {
	allowed := map[string]bool{}
	for _, p := range KnownPackages() {
		allowed[p] = true
	}
	withPresent(t) // nothing installed at all
	cfg := &config.Config{DNSMasqEnabled: true, HAProxyEnabled: true, NodeExporterEnabled: true}
	for _, d := range Missing(cfg) {
		if !allowed[d.Package] {
			t.Errorf("%s is reported missing but is not on the KnownPackages allow-list, "+
				"so no install path may fetch it", d.Package)
		}
	}
}

func TestInstallMissingIsANoOpWhenNothingIsMissing(t *testing.T) {
	withPresent(t, "ip", "wg", "iptables", "qrencode")
	// No apt-get is reachable in the test environment, so this passing at all
	// proves it short-circuited rather than shelling out.
	if err := InstallMissing(&config.Config{}); err != nil {
		t.Fatalf("InstallMissing on a complete box must do nothing; got %v", err)
	}
}

func TestPurposeIsAnOperatorSentenceNotAPackageName(t *testing.T) {
	withPresent(t)
	for _, d := range Missing(&config.Config{DNSMasqEnabled: true, HAProxyEnabled: true}) {
		if len(strings.Fields(d.Purpose)) < 4 {
			t.Errorf("%s: purpose %q is not a sentence an operator can act on", d.Package, d.Purpose)
		}
	}
}
