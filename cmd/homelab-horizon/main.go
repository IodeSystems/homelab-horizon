package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/server"
	"homelab-horizon/internal/wireguard"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

const systemdServiceTemplate = `[Unit]
Description=Homelab Horizon - Split-Horizon DNS & VPN Management
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s%s
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
User=root
Group=root

[Install]
WantedBy=multi-user.target
`

const servicePath = "/etc/systemd/system/homelab-horizon.service"

func main() {
	configPath := flag.String("config", "", "Path to configuration file (optional)")
	install := flag.Bool("install", false, "Install systemd service")
	check := flag.Bool("check", false, "Check system configuration and offer to fix issues")
	configTemplate := flag.Bool("config-template", false, "Print a commented config template and exit")
	iamPolicy := flag.Bool("iam-policy", false, "Print IAM policy template for Route53 access")
	dryRun := flag.Bool("dry-run", false, "Dry run mode - show what would be done without making changes")
	showSystemdService := flag.Bool("show-systemd", false, "Show the systemd service file that would be generated")
	version := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	switch {
	case *version:
		fmt.Printf("homelab-horizon %s (built %s)\n", Version, BuildTime)
	case *configTemplate:
		fmt.Print(config.Template())
	case *iamPolicy:
		fmt.Print(config.IAMPolicyTemplate())
	case *showSystemdService:
		fmt.Println(generateSystemdService(*configPath))
	case *check:
		if err := runCheck(*dryRun); err != nil {
			log.Fatalf("Check failed: %v", err)
		}
	case *install:
		if err := installService(*dryRun); err != nil {
			log.Fatalf("Installation failed: %v", err)
		}
	default:
		runServer(*configPath, *dryRun)
	}
}

func runServer(configPath string, dryRun bool) {
	fmt.Printf("Homelab Horizon %s (built %s)\n", Version, BuildTime)

	if os.Geteuid() != 0 {
		log.Println("Warning: Not running as root. Some operations may fail.")
	}

	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("Config search paths: %s\n", strings.Join(config.SearchPaths, ", "))

	srv, err := server.NewWithConfig(cfg, cfgPath, dryRun)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	if err := srv.Run(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func loadConfig(configPath string) (*config.Config, string, error) {
	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, "", err
		}
		fmt.Printf("Loaded config from %s\n", configPath)
		return cfg, configPath, nil
	}
	return config.LoadAuto()
}

func generateSystemdService(configPath string) string {
	execPath, err := os.Executable()
	if err != nil {
		execPath = "/usr/local/bin/homelab-horizon"
	}
	execPath, _ = filepath.Abs(execPath)

	configFlag := ""
	workDir := "/etc/homelab-horizon"
	if configPath != "" {
		if abs, err := filepath.Abs(configPath); err == nil {
			configPath = abs
			workDir = filepath.Dir(abs)
		}
		configFlag = fmt.Sprintf(" -config %s", configPath)
	}

	return fmt.Sprintf(systemdServiceTemplate, execPath, configFlag, workDir)
}

type checker struct {
	isRoot  bool
	dryRun  bool
	allGood bool
	cfg     *config.Config
	cfgPath string
}

func runCheck(dryRun bool) error {
	fmt.Printf("Homelab Horizon %s System Check\n", Version)
	fmt.Println("================================")
	fmt.Println()

	c := &checker{
		isRoot:  os.Geteuid() == 0,
		dryRun:  dryRun,
		allGood: true,
	}

	if !c.isRoot {
		fmt.Println("Warning: Not running as root. Some checks may be incomplete.")
		fmt.Println()
	}

	c.cfg, c.cfgPath, _ = config.LoadAuto()
	fmt.Println()

	c.checkBinaries()
	c.checkServices()
	c.checkWireGuard()
	c.checkSystemdService()
	c.checkFileAccess()
	c.checkNetwork()
	c.printSummary()

	return nil
}

func (c *checker) checkBinaries() {
	c.checkBinary("WireGuard", "wg", "apt install wireguard-tools", true)
	c.checkBinary("dnsmasq", "dnsmasq", "apt install dnsmasq", c.cfg.DNSMasqEnabled)
	c.checkBinary("qrencode", "qrencode", "apt install qrencode", false)
	c.checkBinary("haproxy", "haproxy", "apt install haproxy", c.cfg.HAProxyEnabled)
}

func (c *checker) checkBinary(name, binary, installCmd string, required bool) {
	fmt.Printf("Checking %s... ", name)
	if _, err := exec.LookPath(binary); err != nil {
		fmt.Println("NOT FOUND")
		if required {
			fmt.Printf("  %s not installed.\n", name)
			fmt.Printf("  Install with: %s\n", installCmd)
			c.allGood = false
		} else {
			fmt.Printf("  Install with: %s\n", installCmd)
		}
	} else {
		fmt.Println("OK")
	}
}

func (c *checker) checkServices() {
	if !c.cfg.HAProxyEnabled {
		return
	}

	fmt.Print("Checking haproxy service... ")
	if err := exec.Command("systemctl", "is-active", "haproxy").Run(); err != nil {
		fmt.Println("NOT RUNNING")
		if c.canFix() && askYesNo("  Start haproxy service?") {
			if err := exec.Command("systemctl", "start", "haproxy").Run(); err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Println("  haproxy started.")
			}
		}
	} else {
		fmt.Println("RUNNING")
	}
}

func (c *checker) checkWireGuard() {
	fmt.Printf("Checking WireGuard config (%s)... ", c.cfg.WGConfigPath)
	if _, err := os.Stat(c.cfg.WGConfigPath); os.IsNotExist(err) {
		fmt.Println("NOT FOUND")
		c.allGood = false
		if c.canFix() && askYesNo("  Create WireGuard server config?") {
			if err := createWGConfig(c.cfg, c.cfgPath); err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Println("  Created WireGuard config.")
			}
		}
	} else if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		c.allGood = false
	} else {
		fmt.Println("OK")
	}

	fmt.Printf("Checking WireGuard interface (%s)... ", c.cfg.WGInterface)
	if err := exec.Command("wg", "show", c.cfg.WGInterface).Run(); err != nil {
		fmt.Println("DOWN")
		if c.canFix() && askYesNo("  Bring up WireGuard interface?") {
			if err := exec.Command("wg-quick", "up", c.cfg.WGInterface).Run(); err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Println("  Interface is now up.")
			}
		}
	} else {
		fmt.Println("UP")
	}
}

func (c *checker) checkSystemdService() {
	fmt.Print("Checking systemd service... ")
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		fmt.Println("NOT INSTALLED")
		fmt.Println()
		fmt.Println("  Generated service file preview:")
		fmt.Println("  --------------------------------")
		for _, line := range strings.Split(generateSystemdService(c.cfgPath), "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("  --------------------------------")
		if c.canFix() && askYesNo("  Install systemd service?") {
			if err := installService(c.dryRun); err != nil {
				fmt.Printf("  Error: %v\n", err)
			}
		}
		return
	}

	if err := exec.Command("systemctl", "is-enabled", "homelab-horizon").Run(); err != nil {
		fmt.Println("INSTALLED (not enabled)")
		if c.canFix() && askYesNo("  Enable service to start on boot?") {
			exec.Command("systemctl", "enable", "homelab-horizon").Run()
			fmt.Println("  Service enabled.")
		}
	} else {
		fmt.Println("OK (enabled)")
	}
}

func (c *checker) checkFileAccess() {
	fmt.Println()
	fmt.Println("Checking file access...")

	wgDir := filepath.Dir(c.cfg.WGConfigPath)
	fmt.Printf("  %s: ", wgDir)
	if err := checkWriteAccess(wgDir); err != nil {
		fmt.Printf("NO ACCESS (%v)\n", err)
		c.allGood = false
	} else {
		fmt.Println("OK")
	}

	if c.cfg.DNSMasqEnabled {
		dnsmasqDir := filepath.Dir(c.cfg.DNSMasqConfigPath)
		fmt.Printf("  %s: ", dnsmasqDir)
		if err := checkWriteAccess(dnsmasqDir); err != nil {
			fmt.Printf("NO ACCESS (%v)\n", err)
			if c.canFix() && askYesNo("  Create dnsmasq config directory?") {
				if err := os.MkdirAll(dnsmasqDir, 0755); err != nil {
					fmt.Printf("  Error: %v\n", err)
				} else {
					fmt.Println("  Created directory.")
				}
			}
		} else {
			fmt.Println("OK")
		}
	}
}

func (c *checker) checkNetwork() {
	fmt.Print("Checking IP forwarding... ")
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
	} else if strings.TrimSpace(string(data)) != "1" {
		fmt.Println("DISABLED")
		c.allGood = false
		if c.canFix() && askYesNo("  Enable IP forwarding?") {
			if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Println("  IP forwarding enabled.")
				fmt.Println("  To make permanent, add to /etc/sysctl.conf:")
				fmt.Println("    net.ipv4.ip_forward=1")
			}
		}
	} else {
		fmt.Println("OK")
	}

	fmt.Printf("Checking NAT masquerade (%s)... ", c.cfg.VPNRange)
	if err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", c.cfg.VPNRange, "-j", "MASQUERADE").Run(); err != nil {
		fmt.Println("NOT FOUND")
		c.allGood = false
		if c.canFix() && askYesNo("  Add masquerade rule?") {
			if err := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", c.cfg.VPNRange, "-j", "MASQUERADE").Run(); err != nil {
				fmt.Printf("  Error: %v\n", err)
			} else {
				fmt.Println("  Masquerade rule added.")
				fmt.Println("  To make permanent, save with: iptables-save > /etc/iptables/rules.v4")
			}
		}
	} else {
		fmt.Println("OK")
	}
}

func (c *checker) printSummary() {
	fmt.Println()
	fmt.Println("======================")
	if c.allGood {
		fmt.Println("All checks passed!")
	} else {
		fmt.Println("Some issues found. Review above and fix as needed.")
	}
	fmt.Println()
	fmt.Printf("Config file: %s\n", c.cfgPath)
	fmt.Printf("Web UI will listen on: %s\n", c.cfg.ListenAddr)
	fmt.Println()
}

func (c *checker) canFix() bool {
	return c.isRoot && !c.dryRun
}

func checkWriteAccess(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return checkWriteAccess(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}

	testFile := filepath.Join(path, ".homelab-horizon-test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("cannot write")
	}
	f.Close()
	os.Remove(testFile)
	return nil
}

func createWGConfig(cfg *config.Config, cfgPath string) error {
	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generating keys: %w", err)
	}

	serverIP := strings.Split(cfg.VPNRange, "/")[0]
	serverIP = strings.TrimSuffix(serverIP, ".0") + ".1"

	listenPort := askString("WireGuard listen port", "51820")

	endpoint := askString("Public endpoint (domain or IP)", cfg.ServerEndpoint)
	if !strings.Contains(endpoint, ":") {
		endpoint = endpoint + ":" + listenPort
	}

	wgDir := filepath.Dir(cfg.WGConfigPath)
	if err := os.MkdirAll(wgDir, 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	wgConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = %s
PostUp = iptables -A FORWARD -i %%i -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i %%i -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE
`, privKey, serverIP, listenPort)

	if err := os.WriteFile(cfg.WGConfigPath, []byte(wgConfig), 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	cfg.ServerPublicKey = pubKey
	cfg.ServerEndpoint = endpoint
	if err := config.Save(cfgPath, cfg); err != nil {
		fmt.Printf("Warning: could not save app config: %v\n", err)
	}

	fmt.Printf("  Server public key: %s\n", pubKey)
	return nil
}

func installService(dryRun bool) error {
	_, cfgPath, _ := config.LoadAuto()
	serviceContent := generateSystemdService(cfgPath)

	if dryRun {
		fmt.Println("DRY RUN: Would create systemd service file:")
		fmt.Printf("Path: %s\n", servicePath)
		fmt.Println("Content:")
		fmt.Println(serviceContent)
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to install systemd service")
	}

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("writing service file: %w", err)
	}
	fmt.Printf("Created %s\n", servicePath)

	fmt.Println("Validating service file...")
	if output, err := exec.Command("systemd-analyze", "verify", servicePath).CombinedOutput(); err != nil {
		fmt.Printf("Warning: systemd-analyze verify: %s\n", string(output))
	} else {
		fmt.Println("Service file validated OK")
	}

	fmt.Println("Reloading systemd...")
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}

	fmt.Println()
	fmt.Println("Installation complete!")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Start service: systemctl start homelab-horizon")
	fmt.Println("  2. Enable on boot: systemctl enable homelab-horizon")
	fmt.Println("  3. View logs: journalctl -u homelab-horizon -f")
	fmt.Println()

	return nil
}

func askYesNo(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, _ := reader.ReadString('\n')
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

func askString(prompt, defaultVal string) string {
	reader := bufio.NewReader(os.Stdin)
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response == "" {
		return defaultVal
	}
	return response
}
