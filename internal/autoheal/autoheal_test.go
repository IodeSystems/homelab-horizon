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

	for _, pkg := range []string{"wireguard-tools", "iptables", "dnsmasq", "haproxy"} {
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
	withPresent(t, "ip", "wg", "iptables")
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
	withPresent(t, "ip", "wg", "iptables", "dnsmasq", "haproxy")
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
	withPresent(t, "ip", "wg", "iptables")
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

// hz renders QR codes in process (internal/qr) since 2026-10-30; a box
// without qrencode is not missing anything.
func TestQrencodeIsNotADependency(t *testing.T) {
	withPresent(t, "ip", "wg", "iptables")
	for _, d := range Check(&config.Config{}) {
		if d.Binary == "qrencode" {
			t.Errorf("qrencode is still listed: %+v", d)
		}
	}
	if got := Missing(&config.Config{}); len(got) != 0 {
		t.Errorf("a box with ip, wg and iptables is complete for this config; got %+v", got)
	}
}

// Check lists what is required, present or not; Missing is its absent half.
func TestCheckListsEveryRequiredDependencyWithItsState(t *testing.T) {
	withPresent(t, "ip", "wg")
	got := map[string]bool{}
	for _, d := range Check(&config.Config{HAProxyEnabled: true}) {
		got[d.Binary] = d.Present
	}
	want := map[string]bool{"ip": true, "wg": true, "iptables": false, "haproxy": false}
	if len(got) != len(want) {
		t.Fatalf("Check = %v, want %v", got, want)
	}
	for b, p := range want {
		if present, ok := got[b]; !ok || present != p {
			t.Errorf("%s: present=%v listed=%v, want present=%v", b, present, ok, p)
		}
	}
}

// NewlyMissing is what an edit adds: a requirement this host cannot meet that
// the old config did not have. Already missing is not new, and an optional
// package (found by probing, never run by hz) is never a reason to refuse.
func TestNewlyMissingIsOnlyWhatTheEditAdds(t *testing.T) {
	withPresent(t, "ip", "wg", "iptables")
	off := &config.Config{}
	on := &config.Config{HAProxyEnabled: true}

	if got := NewlyMissing(off, on); len(got) != 1 || got[0].Binary != "haproxy" {
		t.Errorf("enabling HAProxy with no haproxy: got %+v, want haproxy", got)
	}
	if got := NewlyMissing(on, &config.Config{HAProxyEnabled: true, DNSMasqEnabled: false}); len(got) != 0 {
		t.Errorf("haproxy was already missing; an unrelated edit got %+v", got)
	}
	if got := NewlyMissing(off, &config.Config{NodeExporterEnabled: true}); len(got) != 0 {
		t.Errorf("the node exporter is optional; got %+v", got)
	}
	withPresent(t, "ip", "wg", "iptables", "haproxy")
	if got := NewlyMissing(off, on); len(got) != 0 {
		t.Errorf("haproxy is installed; got %+v", got)
	}
}
