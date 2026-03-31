package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
	"homelab-horizon/internal/route53"
	"homelab-horizon/internal/wireguard"
)

// systemdServiceTemplate is the template for the systemd service file
// %s is the ExecStart command, %s is the working directory
const systemdServiceTemplate = `[Unit]
Description=Homelab Horizon - Split-Horizon DNS & VPN Management
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
User=root
Group=root

# File system isolation
ProtectSystem=strict
ReadWritePaths=-/etc/wireguard -/etc/dnsmasq.d -/etc/haproxy -/etc/systemd/system -/proc/sys/net/ipv4 -/var/lib/haproxy -%s
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true

# Network capabilities for WireGuard
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW

# Security restrictions
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
`

// SetupStatus represents the current setup state
type SetupStatus struct {
	IsFirstRun       bool // No WG config and no service - truly fresh install
	NeedsSetup       bool // Some critical items are missing
	WGConfigExists   bool
	ServiceInstalled bool
	RequirementsMet  bool // All required packages installed
	AllRequirements  []SystemRequirement
}

// CheckSetupStatus determines the current setup state
func CheckSetupStatus(wgConfigPath string) SetupStatus {
	status := SetupStatus{}

	// Check WireGuard config
	if _, err := os.Stat(wgConfigPath); err == nil {
		status.WGConfigExists = true
	}

	// Check systemd service
	if _, err := os.Stat("/etc/systemd/system/homelab-horizon.service"); err == nil {
		status.ServiceInstalled = true
	}

	// Check requirements
	status.AllRequirements = checkSystemRequirements()
	status.RequirementsMet = true
	for _, req := range status.AllRequirements {
		if !req.Installed {
			status.RequirementsMet = false
			break
		}
	}

	// Determine first run vs needs setup
	if !status.WGConfigExists && !status.ServiceInstalled {
		status.IsFirstRun = true
		status.NeedsSetup = true
	} else if !status.WGConfigExists || !status.ServiceInstalled || !status.RequirementsMet {
		status.NeedsSetup = true
	}

	return status
}

// generateServiceContent creates the expected systemd service file content
func generateServiceContent(configPath string) string {
	// Get the current executable path
	binaryPath := "/usr/local/bin/homelab-horizon" // fallback
	if execPath, err := os.Executable(); err == nil {
		if absPath, err := filepath.Abs(execPath); err == nil {
			binaryPath = absPath
		}
	}

	// Build ExecStart command with config argument if specified
	execStart := binaryPath
	if configPath != "" {
		absConfigPath, err := filepath.Abs(configPath)
		if err == nil {
			configPath = absConfigPath
		}
		execStart = fmt.Sprintf("%s --config %s", binaryPath, configPath)
	}

	// Determine working directory from config path (must be absolute for systemd)
	workDir := "/etc/homelab-horizon"
	if configPath != "" {
		absConfigPath, err := filepath.Abs(configPath)
		if err == nil {
			dir := filepath.Dir(absConfigPath)
			if dir != "" && dir != "." {
				workDir = dir
			}
		}
	}

	return fmt.Sprintf(systemdServiceTemplate, execStart, workDir, workDir)
}

// Setup and system handlers

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	serviceInstalled := false
	serviceEnabled := false
	serviceNeedsUpdate := false
	serviceVerifyOK := false
	serviceVerifyOutput := ""
	currentServiceContent := ""
	servicePath := "/etc/systemd/system/homelab-horizon.service"
	expectedServiceContent := generateServiceContent(s.configPath)

	if data, err := os.ReadFile(servicePath); err == nil {
		serviceInstalled = true
		currentServiceContent = string(data)
		if strings.TrimSpace(currentServiceContent) != strings.TrimSpace(expectedServiceContent) {
			serviceNeedsUpdate = true
		}
		if err := exec.Command("systemctl", "is-enabled", "homelab-horizon").Run(); err == nil {
			serviceEnabled = true
		}
		if output, err := exec.Command("systemd-analyze", "verify", servicePath).CombinedOutput(); err != nil {
			serviceVerifyOutput = strings.TrimSpace(string(output))
		} else {
			serviceVerifyOK = true
		}
	}

	wgConfigExists := false
	if _, err := os.Stat(s.config.WGConfigPath); err == nil {
		wgConfigExists = true
	}

	// Get system status (inline from handleTest)
	status := s.wg.CheckSystem(s.config.VPNRange)
	dnsStatus := s.dns.Status()
	requirements := checkSystemRequirements()
	ipv6Status := route53.CheckIPv6()

	setupStatus := CheckSetupStatus(s.config.WGConfigPath)

	data := map[string]interface{}{
		"Config":                 s.config,
		"ServiceInstalled":       serviceInstalled,
		"ServiceEnabled":         serviceEnabled,
		"ServiceNeedsUpdate":     serviceNeedsUpdate,
		"ServiceVerifyOK":        serviceVerifyOK,
		"ServiceVerifyOutput":    serviceVerifyOutput,
		"CurrentServiceContent":  currentServiceContent,
		"ExpectedServiceContent": expectedServiceContent,
		"WGConfigExists":         wgConfigExists,
		"Status":                 status,
		"DNSStatus":              dnsStatus,
		"Requirements":           requirements,
		"IPv6Status":             ipv6Status,
		"SetupComplete":          !setupStatus.NeedsSetup,
		"Message":                r.URL.Query().Get("msg"),
		"Error":                  r.URL.Query().Get("err"),
		"CSRFToken":              s.getCSRFToken(r),
		"Version":                s.version,
		"ConnectivityResults":    s.connectivityResults,
	}
	s.templates["setup"].Execute(w, data)
}

func (s *Server) handleInstallService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	serviceContent := generateServiceContent(s.configPath)
	servicePath := "/etc/systemd/system/homelab-horizon.service"

	// Use systemd-run to create a transient service that escapes ProtectSystem=strict
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("cat > %s", servicePath))
	cmd.Stdin = strings.NewReader(serviceContent)
	if out, err := cmd.CombinedOutput(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("Failed to write service file: "+err.Error()+" "+string(out)), http.StatusSeeOther)
		return
	}

	cmd = exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("Failed to reload systemd: "+err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/setup?msg=Service+installed+successfully", http.StatusSeeOther)
}

func (s *Server) handleEnableService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"systemctl", "enable", "homelab-horizon")
	if err := cmd.Run(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("Failed to enable service: "+err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/setup?msg=Service+enabled", http.StatusSeeOther)
}

func (s *Server) handleCreateWGConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("Failed to generate keys: "+err.Error()), http.StatusSeeOther)
		return
	}

	serverIP := strings.Split(s.config.VPNRange, "/")[0]
	serverIP = strings.TrimSuffix(serverIP, ".0") + ".1"

	wgConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = 51820
PostUp = iptables -A FORWARD -i %%i -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i %%i -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE
`, privKey, serverIP)

	// Use systemd-run to escape ProtectSystem=strict — /etc is read-only in the sandbox
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("mkdir -p /etc/wireguard && chmod 700 /etc/wireguard && cat > %s && chmod 600 %s",
			s.config.WGConfigPath, s.config.WGConfigPath))
	cmd.Stdin = strings.NewReader(wgConfig)
	if out, err := cmd.CombinedOutput(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("Failed to write WG config: "+err.Error()+" "+strings.TrimSpace(string(out))), http.StatusSeeOther)
		return
	}

	s.config.ServerPublicKey = pubKey
	_ = config.Save(s.configPath, s.config)
	s.wg.Load()

	http.Redirect(w, r, "/admin/setup?msg=WireGuard+config+created", http.StatusSeeOther)
}

// DNSMasq handlers

func (s *Server) handleDNSReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	// Write config and mappings before reloading to pick up any changes
	if err := s.dns.WriteConfig(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("dnsmasq write failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.dns.SetMappings(s.config.DeriveDNSMappings()); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("dnsmasq mappings failed: "+err.Error()), http.StatusSeeOther)
		return
	}

	if err := s.dns.Reload(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("dnsmasq reload failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?msg=dnsmasq+reloaded", http.StatusSeeOther)
}

func (s *Server) handleDNSMasqStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	// Ensure config exists before starting
	if err := s.dns.WriteConfig(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape("dnsmasq write config failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	if mappings := s.config.DeriveDNSMappings(); len(mappings) > 0 {
		s.dns.SetMappings(mappings)
	}

	if err := s.dns.Start(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/setup?msg=dnsmasq+started", http.StatusSeeOther)
}

func (s *Server) handleDNSMasqInit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.dns.WriteConfig(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	mappings := s.config.DeriveDNSMappings()
	if len(mappings) > 0 {
		s.dns.SetMappings(mappings)
	}

	http.Redirect(w, r, "/admin/setup?msg=dnsmasq+config+created", http.StatusSeeOther)
}

// Route53 Dynamic DNS handlers

func (s *Server) handleDNSStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	awsProfiles := letsencrypt.GetAWSProfiles()
	publicIP, _ := route53.GetPublicIP()

	// Derive Route53 records from services
	records := s.config.DeriveRoute53Records()

	data := map[string]interface{}{
		"Config":      s.config,
		"Records":     records,
		"Zones":       s.config.Zones,
		"Services":    s.config.GetExternalServices(),
		"AWSProfiles": awsProfiles,
		"PublicIP":    publicIP,
		"AWSAvail":    route53.Available(),
		"Message":     r.URL.Query().Get("msg"),
		"CSRFToken":   s.getCSRFToken(r),
		"Error":       r.URL.Query().Get("err"),
	}
	s.templates["dns"].Execute(w, data)
}

func (s *Server) handleRoute53AddRecord(w http.ResponseWriter, r *http.Request) {
	// Deprecated - Route53 records are now derived from services
	http.Redirect(w, r, "/admin?msg=Route53+records+are+now+auto-generated+from+services.+Add+a+service+with+external+access.", http.StatusSeeOther)
}

func (s *Server) handleRoute53DeleteRecord(w http.ResponseWriter, r *http.Request) {
	// Deprecated - Route53 records are now derived from services
	http.Redirect(w, r, "/admin?msg=Route53+records+are+now+auto-generated+from+services.+Remove+external+access+from+the+service.", http.StatusSeeOther)
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := map[string]interface{}{
		"Version": s.version,
	}
	s.templates["help"].Execute(w, data)
}

type ConnectivityCheck struct {
	Name    string
	Status  string // "ok", "failed", "pending"
	Message string
}

func (s *Server) handleTestConnectivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	results := []ConnectivityCheck{}

	// 0. Check if HAProxy config is up-to-date
	if s.config.HAProxyEnabled {
		var sslConfig *haproxy.SSLConfig
		if s.config.SSLEnabled {
			sslConfig = &haproxy.SSLConfig{
				Enabled: true,
				CertDir: s.config.SSLHAProxyCertDir,
			}
		}
		expectedConfig := s.haproxy.GenerateConfig(
			s.config.HAProxyHTTPPort,
			s.config.HAProxyHTTPSPort,
			sslConfig,
		)
		currentConfig, err := os.ReadFile(s.config.HAProxyConfigPath)
		if err != nil {
			results = append(results, ConnectivityCheck{
				Name:    "HAProxy Config",
				Status:  "failed",
				Message: fmt.Sprintf("Cannot read HAProxy config: %v", err),
			})
		} else if strings.TrimSpace(string(currentConfig)) != strings.TrimSpace(expectedConfig) {
			results = append(results, ConnectivityCheck{
				Name:    "HAProxy Config",
				Status:  "failed",
				Message: "HAProxy config is out of date. Go to HAProxy page and click 'Write Config' then 'Reload'.",
			})
		} else {
			results = append(results, ConnectivityCheck{
				Name:    "HAProxy Config",
				Status:  "ok",
				Message: "HAProxy config is up to date",
			})
		}
	}

	// 1. Check DNS Resolution
	// We check if a configured service domain resolves to its internal IP
	dnsStatus := "failed"
	dnsMsg := "No services configured for testing"
	if len(s.config.Services) > 0 {
		var testService *config.Service
		for i := range s.config.Services {
			if s.config.Services[i].InternalDNS != nil {
				testService = &s.config.Services[i]
				break
			}
		}

		if testService != nil {
			ips, err := net.LookupHost(testService.Domain)
			if err == nil {
				found := false
				for _, ip := range ips {
					if ip == testService.InternalDNS.IP {
						found = true
						break
					}
				}
				if found {
					dnsStatus = "ok"
					dnsMsg = fmt.Sprintf("Resolved %s to %s", testService.Domain, testService.InternalDNS.IP)
				} else {
					dnsMsg = fmt.Sprintf("Resolved %s to %v, expected %s", testService.Domain, ips, testService.InternalDNS.IP)
				}
			} else {
				dnsMsg = fmt.Sprintf("Failed to resolve %s: %v", testService.Domain, err)
			}
		} else {
			dnsMsg = "No services with internal DNS configured"
		}
	}
	results = append(results, ConnectivityCheck{
		Name:    "Internal DNS Resolution",
		Status:  dnsStatus,
		Message: dnsMsg,
	})

	// 2. Check Port Forwarding (Hairpin NAT)
	publicIP := s.config.PublicIP
	if publicIP == "" {
		// Try to get it now if not set
		publicIP, _ = route53.GetPublicIP()
	}

	if publicIP != "" {
		// HTTP client with short timeout, skip TLS verification, and don't follow redirects
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // Don't follow redirects
			},
		}

		// Always test HTTP
		httpURL := fmt.Sprintf("http://%s:%d/router-check", publicIP, s.config.HAProxyHTTPPort)
		req, _ := http.NewRequest("GET", httpURL, nil)
		req.Header.Set("X-Homelab-Horizon-Check", "1")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				results = append(results, ConnectivityCheck{
					Name:    "Port Forwarding (HTTP)",
					Status:  "ok",
					Message: fmt.Sprintf("Verified our server at %s:%d via /router-check", publicIP, s.config.HAProxyHTTPPort),
				})
			} else if resp.StatusCode == 301 || resp.StatusCode == 302 {
				// Redirect to HTTPS is expected when SSL is enabled
				results = append(results, ConnectivityCheck{
					Name:    "Port Forwarding (HTTP)",
					Status:  "ok",
					Message: fmt.Sprintf("Port %d reachable, redirects to HTTPS (expected)", s.config.HAProxyHTTPPort),
				})
			} else {
				results = append(results, ConnectivityCheck{
					Name:    "Port Forwarding (HTTP)",
					Status:  "failed",
					Message: fmt.Sprintf("Got status %d from %s (expected 200 or redirect)", resp.StatusCode, httpURL),
				})
			}
		} else {
			results = append(results, ConnectivityCheck{
				Name:    "Port Forwarding (HTTP)",
				Status:  "failed",
				Message: fmt.Sprintf("Failed to connect to %s: %v", httpURL, err),
			})
		}

		// Only test HTTPS if SSL is enabled
		if s.config.SSLEnabled {
			httpsURL := fmt.Sprintf("https://%s:%d/router-check", publicIP, s.config.HAProxyHTTPSPort)
			req, _ := http.NewRequest("GET", httpsURL, nil)
			req.Header.Set("X-Homelab-Horizon-Check", "1")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					results = append(results, ConnectivityCheck{
						Name:    "Port Forwarding (HTTPS)",
						Status:  "ok",
						Message: fmt.Sprintf("Verified our server at %s:%d via /router-check", publicIP, s.config.HAProxyHTTPSPort),
					})
				} else {
					results = append(results, ConnectivityCheck{
						Name:    "Port Forwarding (HTTPS)",
						Status:  "failed",
						Message: fmt.Sprintf("Got status %d from %s (expected 200)", resp.StatusCode, httpsURL),
					})
				}
			} else {
				results = append(results, ConnectivityCheck{
					Name:    "Port Forwarding (HTTPS)",
					Status:  "failed",
					Message: fmt.Sprintf("Failed to connect to %s: %v", httpsURL, err),
				})
			}
		}

		// UDP check for WireGuard (51820)
		wgPort := 51820
		addr := net.JoinHostPort(publicIP, fmt.Sprintf("%d", wgPort))
		conn, err := net.DialTimeout("udp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			results = append(results, ConnectivityCheck{
				Name:    "Port Forwarding (WireGuard)",
				Status:  "ok",
				Message: fmt.Sprintf("UDP Port %d is reachable at %s (Note: UDP check is limited)", wgPort, publicIP),
			})
		} else {
			results = append(results, ConnectivityCheck{
				Name:    "Port Forwarding (WireGuard)",
				Status:  "failed",
				Message: fmt.Sprintf("Failed to dial UDP %s: %v", addr, err),
			})
		}
	} else {
		results = append(results, ConnectivityCheck{
			Name:    "Port Forwarding",
			Status:  "failed",
			Message: "Public IP not detected",
		})
	}

	s.connectivityResults = results
	http.Redirect(w, r, "/admin/setup?msg=Connectivity+tests+completed", http.StatusSeeOther)
}

func (s *Server) handleInstallRequirement(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.FormValue("name")
	requirements := checkSystemRequirements()
	var req *SystemRequirement
	for i := range requirements {
		if requirements[i].Name == name {
			req = &requirements[i]
			break
		}
	}

	if req == nil {
		http.Error(w, "Requirement not found", http.StatusNotFound)
		return
	}

	if req.Installed {
		http.Redirect(w, r, "/admin/setup?msg=Requirement+already+installed", http.StatusSeeOther)
		return
	}

	// We use the command without "sudo" because the process should be running as root
	cmdStr := strings.TrimPrefix(req.Command, "sudo ")
	parts := strings.Fields(cmdStr)
	if len(parts) == 0 {
		http.Error(w, "Invalid install command", http.StatusInternalServerError)
		return
	}

	// Force non-interactive for apt
	if parts[0] == "apt" {
		parts = append([]string{"apt", "-y"}, parts[1:]...)
	}

	// Use systemd-run to create a transient service that escapes ProtectSystem=strict
	args := append([]string{"--pipe", "--wait", "--service-type=oneshot",
		"--setenv=DEBIAN_FRONTEND=noninteractive"}, parts...)
	cmd := exec.Command("systemd-run", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		errMsg := fmt.Sprintf("Failed to install %s: %v", name, err)
		if len(out) > 0 {
			errMsg += " — " + strings.TrimSpace(string(out))
		}
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(errMsg), http.StatusSeeOther)
		return
	}

	// Packages may install systemd units; reload so systemctl can find them.
	// Also re-run dpkg configure in case postinst scripts need to finish.
	exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"dpkg", "--configure", "-a").Run()
	exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"systemctl", "daemon-reload").Run()

	http.Redirect(w, r, fmt.Sprintf("/admin/setup?msg=%s+installed+successfully", name), http.StatusSeeOther)
}

// SystemRequirement represents a system requirement check
type SystemRequirement struct {
	Name        string
	Description string
	Installed   bool
	Command     string // Command to install
	Error       string // Error message if check failed
}

// checkSystemRequirements checks for required packages and system capabilities
func checkSystemRequirements() []SystemRequirement {
	requirements := []SystemRequirement{
		{
			Name:        "WireGuard Tools",
			Description: "Required for VPN management",
			Command:     "sudo apt install wireguard-tools",
		},
		{
			Name:        "HAProxy",
			Description: "Required for reverse proxy and SSL termination",
			Command:     "sudo apt install haproxy",
		},
		{
			Name:        "dnsmasq",
			Description: "Required for internal DNS resolution",
			Command:     "sudo apt install dnsmasq",
		},
	}

	// Check each requirement: binary must exist AND package must be installed
	type check struct {
		binary  string
		dpkgPkg string // verify this package is installed, not just the binary
	}
	checks := map[string]check{
		"WireGuard Tools": {binary: "wg", dpkgPkg: "wireguard-tools"},
		"HAProxy":         {binary: "haproxy", dpkgPkg: "haproxy"},
		"dnsmasq":         {binary: "dnsmasq", dpkgPkg: "dnsmasq"},
	}
	for i := range requirements {
		if c, ok := checks[requirements[i].Name]; ok {
			if _, err := exec.LookPath(c.binary); err != nil {
				requirements[i].Error = fmt.Sprintf("'%s' not found in PATH", c.binary)
			} else if out, err := exec.Command("dpkg", "-s", c.dpkgPkg).CombinedOutput(); err != nil {
				requirements[i].Error = fmt.Sprintf("binary found but package '%s' not fully installed (only dnsmasq-base?)", c.dpkgPkg)
			} else if !strings.Contains(string(out), "Status: install ok installed") {
				requirements[i].Error = fmt.Sprintf("package '%s' in broken state", c.dpkgPkg)
			} else {
				requirements[i].Installed = true
			}
		}
	}

	return requirements
}
