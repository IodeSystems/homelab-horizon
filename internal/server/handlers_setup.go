package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homelab-horizon/internal/config"
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

	return fmt.Sprintf(systemdServiceTemplate, execStart, workDir)
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

	if err := os.WriteFile("/etc/systemd/system/homelab-horizon.service", []byte(serviceContent), 0644); err != nil {
		http.Redirect(w, r, "/admin/setup?err=Failed+to+write+service+file:+"+err.Error(), http.StatusSeeOther)
		return
	}

	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		http.Redirect(w, r, "/admin/setup?err=Failed+to+reload+systemd:+"+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/setup?msg=Service+installed+successfully", http.StatusSeeOther)
}

func (s *Server) handleEnableService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	cmd := exec.Command("systemctl", "enable", "homelab-horizon")
	if err := cmd.Run(); err != nil {
		http.Redirect(w, r, "/admin/setup?err=Failed+to+enable+service:+"+err.Error(), http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin/setup?err=Failed+to+generate+keys:+"+err.Error(), http.StatusSeeOther)
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

	if err := os.MkdirAll("/etc/wireguard", 0700); err != nil {
		http.Redirect(w, r, "/admin/setup?err=Failed+to+create+directory:+"+err.Error(), http.StatusSeeOther)
		return
	}

	if err := os.WriteFile(s.config.WGConfigPath, []byte(wgConfig), 0600); err != nil {
		http.Redirect(w, r, "/admin/setup?err=Failed+to+write+config:+"+err.Error(), http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin?err=dnsmasq+write+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}
	if err := s.dns.SetMappings(s.config.DeriveDNSMappings()); err != nil {
		http.Redirect(w, r, "/admin?err=dnsmasq+mappings+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}

	if err := s.dns.Reload(); err != nil {
		http.Redirect(w, r, "/admin?err=dnsmasq+reload+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?msg=dnsmasq+reloaded", http.StatusSeeOther)
}

func (s *Server) handleDNSMasqStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.dns.Start(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/setup?msg=dnsmasq+started", http.StatusSeeOther)
}

func (s *Server) handleDNSMasqInit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.dns.WriteConfig(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+err.Error(), http.StatusSeeOther)
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
		ports := []struct {
			port int
			name string
		}{
			{80, "HTTP"},
			{443, "HTTPS"},
		}

		for _, p := range ports {
			status := "failed"
			msg := ""
			addr := net.JoinHostPort(publicIP, fmt.Sprintf("%d", p.port))
			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err == nil {
				conn.Close()
				status = "ok"
				msg = fmt.Sprintf("Port %d is reachable at %s", p.port, publicIP)
			} else {
				msg = fmt.Sprintf("Failed to connect to %s: %v", addr, err)
			}
			results = append(results, ConnectivityCheck{
				Name:    fmt.Sprintf("Port Forwarding (%s)", p.name),
				Status:  status,
				Message: msg,
			})
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

// SystemRequirement represents a system requirement check
type SystemRequirement struct {
	Name        string
	Description string
	Installed   bool
	Command     string // Command to install
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

	// Check each requirement
	for i := range requirements {
		var cmd *exec.Cmd
		switch requirements[i].Name {
		case "WireGuard Tools":
			cmd = exec.Command("which", "wg")
		case "HAProxy":
			cmd = exec.Command("which", "haproxy")
		case "dnsmasq":
			cmd = exec.Command("which", "dnsmasq")
		}
		if cmd != nil {
			requirements[i].Installed = cmd.Run() == nil
		}
	}

	return requirements
}
