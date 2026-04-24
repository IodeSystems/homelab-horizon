package autoheal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"homelab-horizon/internal/config"
)

type dependency struct {
	name    string
	binary  string
	pkg     string
	require func(*config.Config) bool
}

var dependencies = []dependency{
	{"iproute2", "ip", "iproute2", func(*config.Config) bool { return true }},
	{"WireGuard tools", "wg", "wireguard-tools", func(*config.Config) bool { return true }},
	{"iptables", "iptables", "iptables", func(*config.Config) bool { return true }},
	{"qrencode", "qrencode", "qrencode", func(*config.Config) bool { return true }},
	{"dnsmasq", "dnsmasq", "dnsmasq", func(c *config.Config) bool { return c.DNSMasqEnabled }},
	{"HAProxy", "haproxy", "haproxy", func(c *config.Config) bool { return c.HAProxyEnabled }},
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
	// Detect missing packages
	var missing []string
	for _, dep := range dependencies {
		if !dep.require(cfg) {
			fmt.Printf("  [skip] %s (not enabled)\n", dep.name)
			continue
		}
		if _, err := exec.LookPath(dep.binary); err != nil {
			fmt.Printf("  [missing] %s (%s)\n", dep.name, dep.pkg)
			missing = append(missing, dep.pkg)
		} else {
			fmt.Printf("  [ok] %s\n", dep.name)
		}
	}

	// Install missing packages
	if len(missing) > 0 {
		fmt.Printf("Installing: %s\n", strings.Join(missing, ", "))

		// Set DEBIAN_FRONTEND to avoid interactive prompts
		env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

		update := exec.Command("apt-get", "update", "-qq")
		update.Env = env
		update.Stdout = os.Stdout
		update.Stderr = os.Stderr
		if err := update.Run(); err != nil {
			return fmt.Errorf("apt-get update failed: %w", err)
		}

		args := append([]string{"install", "-y", "-qq"}, missing...)
		install := exec.Command("apt-get", args...)
		install.Env = env
		install.Stdout = os.Stdout
		install.Stderr = os.Stderr
		if err := install.Run(); err != nil {
			return fmt.Errorf("apt-get install failed: %w", err)
		}

		fmt.Println("Packages installed successfully")
	}

	// Create required directories
	for _, dir := range requiredDirs {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("creating %s: %w", dir.path, err)
		}
	}

	// Enable IP forwarding
	if err := enableIPForwarding(); err != nil {
		fmt.Printf("Warning: could not enable IP forwarding: %v\n", err)
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
	fmt.Println("Enabling IP forwarding")
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

func stopSystemDnsmasq() {
	// Best-effort: stop and disable the system dnsmasq service so it doesn't
	// conflict with the one HZ manages. Errors are expected in Docker (no systemd).
	cmd := exec.Command("systemctl", "disable", "--now", "dnsmasq")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
