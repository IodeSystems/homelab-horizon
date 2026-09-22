package autoheal

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Dependency is one piece of on-host software hz needs, and the apt package
// that supplies it.
type Dependency struct {
	Name    string `json:"name"`
	Binary  string `json:"binary"`
	Package string `json:"package"`
	// Purpose says what stops working without it, in the words of someone
	// looking at the box rather than the code. "wireguard-tools MISSING" tells
	// an operator nothing they can act on; "the VPN cannot come up" does.
	Purpose string `json:"purpose"`
}

type dependency struct {
	name    string
	binary  string
	pkg     string
	purpose string
	require func(*config.Config) bool
}

var dependencies = []dependency{
	{"iproute2", "ip", "iproute2", "interface and route management; without it hz cannot configure any network state",
		func(*config.Config) bool { return true }},
	{"WireGuard tools", "wg", "wireguard-tools", "brings the VPN interface up; without it no peer can connect",
		func(*config.Config) bool { return true }},
	{"iptables", "iptables", "iptables", "the per-peer firewall, NAT and the MFA jail; without it VPN traffic is unfiltered or unrouted",
		func(*config.Config) bool { return true }},
	{"qrencode", "qrencode", "qrencode", "renders the QR code a phone scans to enrol as a VPN peer",
		func(*config.Config) bool { return true }},
	{"dnsmasq", "dnsmasq", "dnsmasq", "serves split-horizon DNS; without it internal names do not resolve",
		func(c *config.Config) bool { return c.DNSMasqEnabled }},
	{"HAProxy", "haproxy", "haproxy", "the reverse proxy and TLS edge; without it no published service is reachable",
		func(c *config.Config) bool { return c.HAProxyEnabled }},
	// Opt-in: hz does not need node-exporter to work, it just folds it into
	// the scrape config it serves when present. Listing it here also puts it
	// on the KnownPackages allowlist, so `install-deps` will fetch it on a box
	// whose config enables it, without widening that allowlist.
	{"Prometheus node exporter", "prometheus-node-exporter", "prometheus-node-exporter",
		"host metrics for the scrape config hz publishes; optional",
		func(c *config.Config) bool { return c.NodeExporterEnabled }},
}

// KnownPackages returns the set of apt package names autoheal knows how to
// install. InstallMissing checks every package against it, so no path in hz
// can run `apt-get install anything`. It used to guard an HTTP endpoint too;
// that endpoint is gone (privilege-classification.md §3.1 #8) and the
// allowlist is now the invariant for the CLI verb alone.
func KnownPackages() []string {
	pkgs := make([]string, 0, len(dependencies))
	for _, d := range dependencies {
		pkgs = append(pkgs, d.pkg)
	}
	return pkgs
}

// lookPath is exec.LookPath, replaceable in tests. Observation has to be
// testable separately from installation: the whole point of Missing is that
// the startup report can ask what is absent without any chance of it
// installing something as a side effect.
var lookPath = exec.LookPath

// Missing reports the dependencies this config requires whose binary is not on
// PATH. It is pure observation: it installs nothing, changes nothing, and does
// not need root.
//
// It is what startup says out loud on every boot. The predecessor of that
// report was `autoheal.Run`, gated behind an `auto_heal` config key that
// defaulted off and that almost nothing set — which is how a gateway came up
// with haproxy, dnsmasq, wireguard-tools and qrencode all absent and said
// nothing about it. The gate is gone; the report is unconditional.
func Missing(cfg *config.Config) []Dependency {
	var missing []Dependency
	for _, dep := range dependencies {
		if !dep.require(cfg) {
			continue
		}
		if _, err := lookPath(dep.binary); err != nil {
			missing = append(missing, Dependency{
				Name: dep.name, Binary: dep.binary, Package: dep.pkg, Purpose: dep.purpose,
			})
		}
	}
	return missing
}

// InstallMissing installs the packages Missing reports, deliberately and by
// name. It is the explicit entry point — `homelab-horizon install-deps`, and
// `install --with-deps` — that a provisioning script can call; nothing invokes
// it on its own.
//
// hz NEVER installs packages implicitly at boot. A surprise `apt-get install`
// on a live gateway is its own hazard: it can pull a dependency that restarts
// a daemon carrying production traffic, at a moment nobody chose. So startup
// reports what is absent and stops there, and this is what a human or a script
// runs when they have decided to change the box.
//
// Every package goes through KnownPackages, so this path cannot install
// anything hz does not already name. It is now the only apt path in the tree:
// the HTTP install endpoint that shared the allowlist is gone.
func InstallMissing(cfg *config.Config) error {
	missing := Missing(cfg)
	if len(missing) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	for _, p := range KnownPackages() {
		allowed[p] = true
	}
	pkgs := make([]string, 0, len(missing))
	for _, d := range missing {
		if !allowed[d.Package] {
			// Unreachable while Missing derives from the same table, and
			// checked anyway: the allowlist is the invariant, not the table
			// two functions happen to share today.
			return fmt.Errorf("package %q not in allow-list", d.Package)
		}
		pkgs = append(pkgs, d.Package)
	}
	if err := aptInstall(pkgs); err != nil {
		return err
	}

	// The one side effect that belongs here and nowhere else.
	//
	// Installing the dnsmasq package starts and enables the distro's own
	// dnsmasq, which then holds :53 against the one hz manages. This is the
	// single moment that can be true — nothing else in hz installs a package —
	// so the disable lives beside the install rather than at boot, where it
	// used to sit inside autoheal.Run re-running on every start of a box that
	// never had the problem. It only fires when dnsmasq was in THIS install.
	for _, p := range pkgs {
		if p == "dnsmasq" {
			stopSystemDNSMasq()
			break
		}
	}
	return nil
}

// stopSystemDNSMasq disables the distro's dnsmasq unit so it does not compete
// with the one hz manages. Best-effort: on a host with no systemd (Docker) the
// command simply fails, and a box where the package shipped no unit has
// nothing to disable.
func stopSystemDNSMasq() {
	slog.Info("disabling the distro dnsmasq unit; hz manages its own")
	cmd := exec.Command("systemctl", "disable", "--now", "dnsmasq")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// aptInstall runs apt-get update + install for an already-validated package
// list. apt is verbose tooling output (diagnostics) — keep it off stdout.
func aptInstall(pkgs []string) error {
	env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

	update := exec.Command("apt-get", "update", "-qq")
	update.Env = env
	update.Stdout = os.Stderr
	update.Stderr = os.Stderr
	if err := update.Run(); err != nil {
		return fmt.Errorf("apt-get update failed: %w", err)
	}

	args := append([]string{"install", "-y", "-qq"}, pkgs...)
	install := exec.Command("apt-get", args...)
	install.Env = env
	install.Stdout = os.Stderr
	install.Stderr = os.Stderr
	if err := install.Run(); err != nil {
		return fmt.Errorf("apt-get install failed: %w", err)
	}
	return nil
}
