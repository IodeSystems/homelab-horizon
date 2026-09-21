package autoheal

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

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
	// on the KnownPackages allowlist, so the install endpoint can fetch it on
	// request without widening that allowlist to arbitrary packages.
	{"Prometheus node exporter", "prometheus-node-exporter", "prometheus-node-exporter",
		"host metrics for the scrape config hz publishes; optional",
		func(c *config.Config) bool { return c.NodeExporterEnabled }},
}

// KnownPackages returns the set of apt package names autoheal knows how to
// install. The system/install/package API endpoint uses this as a whitelist
// so an admin can't coerce horizon into running `apt-get install anything`.
func KnownPackages() []string {
	pkgs := make([]string, 0, len(dependencies))
	for _, d := range dependencies {
		pkgs = append(pkgs, d.pkg)
	}
	return pkgs
}

// lookPath is exec.LookPath, replaceable in tests. Observation has to be
// testable separately from installation: the whole point of Missing is that
// something other than Run — the startup report — can ask what is absent
// without any chance of it installing something as a side effect.
var lookPath = exec.LookPath

// Missing reports the dependencies this config requires whose binary is not on
// PATH. It is pure observation: it installs nothing, changes nothing, and does
// not need root.
//
// Run() is gated behind the `auto_heal` config flag, which is off by default,
// so on an ordinary box nothing ever asks this question at boot. That is how a
// gateway came up with haproxy, dnsmasq, wireguard-tools and qrencode all
// absent and said nothing about it. Startup calls Missing and reports the
// answer whether or not auto-heal is allowed to act on it.
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
// Every package goes through KnownPackages, the same allowlist the HTTP
// install endpoint uses, so this path cannot install anything hz does not
// already name.
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
	return aptInstall(pkgs)
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

// InstallPackage runs `apt-get update` + `apt-get install -y -qq <pkg>` through
// systemd-run so it escapes horizon's own ProtectSystem=strict sandbox at
// runtime. Validates pkg against KnownPackages before executing. Returns the
// combined stdout/stderr for logging even on success.
//
// NOTE: Unlike Run() — which is the startup bootstrap path and assumes it has
// unsandboxed access — this is the runtime HTTP path. systemd-run is required;
// on hosts without systemd (e.g. Docker) this will error, and the caller
// should surface that cleanly rather than retry.
func InstallPackage(pkg string) (string, error) {
	allowed := false
	for _, p := range KnownPackages() {
		if p == pkg {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("package %q not in allow-list", pkg)
	}

	// apt-get update in its own one-shot so a slow mirror doesn't block the
	// install step if apt metadata is already fresh enough.
	upd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"apt-get", "update", "-qq")
	updOut, updErr := upd.CombinedOutput()

	ins := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"apt-get", "install", "-y", "-qq", pkg)
	insOut, insErr := ins.CombinedOutput()

	// Packages may install systemd units; reload so systemctl sees them.
	_ = exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"systemctl", "daemon-reload").Run()

	combined := strings.TrimSpace(string(updOut)) + "\n" + strings.TrimSpace(string(insOut))
	if insErr != nil {
		if updErr != nil {
			return combined, fmt.Errorf("apt-get update + install failed: %w", insErr)
		}
		return combined, fmt.Errorf("apt-get install failed: %w", insErr)
	}
	return combined, nil
}

var requiredDirs = []struct {
	path string
	mode os.FileMode
}{
	{"/etc/wireguard", 0700},
	{"/etc/dnsmasq.d", 0755},
	{"/etc/haproxy", 0755},
	{"/etc/haproxy/certs", 0755},
	{"/etc/haproxy/errors", 0755},
	{"/etc/homelab-horizon", 0755},
}

// Run detects and installs missing dependencies, creates required directories,
// and configures the system for homelab-horizon.
func Run(cfg *config.Config) error {
	// Detect missing packages. Same observation the startup report uses, so
	// the two can never disagree about what is absent.
	var missing []string
	for _, dep := range Missing(cfg) {
		slog.Warn("dependency missing", "name", dep.Name, "package", dep.Package)
		missing = append(missing, dep.Package)
	}

	// Install missing packages
	if len(missing) > 0 {
		slog.Info("installing packages", "packages", strings.Join(missing, ", "))
		if err := aptInstall(missing); err != nil {
			return err
		}
		slog.Info("packages installed")
	}

	// Create required directories
	for _, dir := range requiredDirs {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("creating %s: %w", dir.path, err)
		}
	}

	// Enable IP forwarding
	if err := enableIPForwarding(); err != nil {
		slog.Warn("could not enable IP forwarding", "err", err)
	}

	// Stop system-provided dnsmasq if it was just installed — HZ manages its own
	if cfg.DNSMasqEnabled {
		stopSystemDnsmasq()
	}

	return nil
}

func enableIPForwarding() error {
	current, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(current)) == "1" {
		return nil
	}
	slog.Info("enabling IP forwarding")
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

func stopSystemDnsmasq() {
	// Best-effort: stop and disable the system dnsmasq service so it doesn't
	// conflict with the one HZ manages. Errors are expected in Docker (no systemd).
	cmd := exec.Command("systemctl", "disable", "--now", "dnsmasq")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
