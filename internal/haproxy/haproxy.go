package haproxy

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Backend represents a HAProxy backend service
type Backend struct {
	Name        string `json:"name"`
	DomainMatch string `json:"domain_match"` // e.g., ".example.com" matches anything ending in .example.com
	Server      string `json:"server"`       // e.g., "192.168.1.10:8080"
	HTTPCheck   bool   `json:"http_check"`
	CheckPath   string `json:"check_path"` // e.g., "/health"
}

// BackendStatus contains runtime status of a backend
type BackendStatus struct {
	Backend
	Healthy   bool
	LastCheck time.Time
	Error     string
}

// HAProxy manages HAProxy configuration
type HAProxy struct {
	configPath  string
	statsSocket string
	backends    []Backend
}

// New creates a new HAProxy manager
func New(configPath, statsSocket string) *HAProxy {
	return &HAProxy{
		configPath:  configPath,
		statsSocket: statsSocket,
	}
}

// SetBackends sets the backends list
func (h *HAProxy) SetBackends(backends []Backend) {
	h.backends = backends
}

// GetBackends returns the backends list
func (h *HAProxy) GetBackends() []Backend {
	return h.backends
}

// Status returns HAProxy status
type Status struct {
	Running      bool
	ConfigExists bool
	Version      string
	Error        string
}

// GetStatus returns current HAProxy status
func (h *HAProxy) GetStatus() Status {
	status := Status{}

	// Check if config exists
	if _, err := os.Stat(h.configPath); err == nil {
		status.ConfigExists = true
	}

	// Check if haproxy is running
	cmd := exec.Command("systemctl", "is-active", "haproxy")
	if err := cmd.Run(); err == nil {
		status.Running = true
	}

	// Get version
	cmd = exec.Command("haproxy", "-v")
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) > 0 {
			status.Version = strings.TrimSpace(lines[0])
		}
	}

	return status
}

// GetBackendStatuses checks health of all backends
func (h *HAProxy) GetBackendStatuses() []BackendStatus {
	var statuses []BackendStatus

	for _, b := range h.backends {
		bs := BackendStatus{
			Backend:   b,
			LastCheck: time.Now(),
		}

		// Try to connect to the backend
		conn, err := net.DialTimeout("tcp", b.Server, 3*time.Second)
		if err != nil {
			bs.Error = err.Error()
			bs.Healthy = false
		} else {
			conn.Close()
			bs.Healthy = true

			// If HTTP check is enabled, do a basic check
			if b.HTTPCheck && b.CheckPath != "" {
				bs.Healthy, bs.Error = h.httpCheck(b.Server, b.CheckPath)
			}
		}

		statuses = append(statuses, bs)
	}

	return statuses
}

func (h *HAProxy) httpCheck(server, path string) (bool, string) {
	conn, err := net.DialTimeout("tcp", server, 3*time.Second)
	if err != nil {
		return false, err.Error()
	}
	defer conn.Close()

	// Send HTTP request
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n", path)
	_, err = conn.Write([]byte(request))
	if err != nil {
		return false, err.Error()
	}

	// Read response
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return false, err.Error()
	}

	// Check for 2xx status
	if strings.Contains(statusLine, " 2") {
		return true, ""
	}

	return false, "HTTP " + strings.TrimSpace(statusLine)
}

// SSLConfig holds SSL configuration for HAProxy
type SSLConfig struct {
	Enabled bool
	CertDir string // directory containing combined PEM files
}

// GenerateConfig returns the HAProxy configuration as a string (for preview)
func (h *HAProxy) GenerateConfig(httpPort, httpsPort int, ssl *SSLConfig) string {
	return h.generateConfig(httpPort, httpsPort, ssl)
}

// WriteConfig generates and writes the HAProxy configuration
func (h *HAProxy) WriteConfig(httpPort, httpsPort int, ssl *SSLConfig) error {
	config := h.generateConfig(httpPort, httpsPort, ssl)

	// Ensure directory exists
	dir := strings.TrimSuffix(h.configPath, "/haproxy.cfg")
	if dir != h.configPath {
		os.MkdirAll(dir, 0755)
	}

	return os.WriteFile(h.configPath, []byte(config), 0644)
}

func (h *HAProxy) generateConfig(httpPort, httpsPort int, ssl *SSLConfig) string {
	var sb strings.Builder

	// Global section
	sb.WriteString(`global
    log /dev/log local0
    log /dev/log local1 notice
    chroot /var/lib/haproxy
    stats socket /run/haproxy/admin.sock mode 660 level admin
    stats timeout 30s
    user haproxy
    group haproxy
    daemon

defaults
    log     global
    mode    http
    option  httplog
    option  dontlognull
    timeout connect 5000
    timeout client  50000
    timeout server  50000
    compression algo gzip
    compression type text/html text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml
    errorfile 400 /etc/haproxy/errors/400.http
    errorfile 403 /etc/haproxy/errors/403.http
    errorfile 408 /etc/haproxy/errors/408.http
    errorfile 500 /etc/haproxy/errors/500.http
    errorfile 502 /etc/haproxy/errors/502.http
    errorfile 503 /etc/haproxy/errors/503.http
    errorfile 504 /etc/haproxy/errors/504.http

`)

	// Stats frontend
	sb.WriteString(`# Stats page
listen stats
    bind *:8404
    stats enable
    stats uri /stats
    stats refresh 10s
    stats admin if LOCALHOST

`)

	// Check if SSL is enabled and cert directory has certs
	sslEnabled := false
	if ssl != nil && ssl.Enabled && ssl.CertDir != "" {
		entries, err := os.ReadDir(ssl.CertDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".pem") {
					sslEnabled = true
					break
				}
			}
		}
	}

	// HTTP frontend
	if sslEnabled {
		// Redirect HTTP to HTTPS (but allow /router-check on HTTP)
		sb.WriteString(fmt.Sprintf(`frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    acl is_router_check path /router-check
    acl has_horizon_header hdr(X-Homelab-Horizon-Check) -m found
    http-request return status 200 content-type "text/plain" string "OK" if is_router_check has_horizon_header
    redirect scheme https code 301 if !is_router_check

`, httpPort))

		// HTTPS frontend - HAProxy loads all certs from directory
		certDir := ssl.CertDir
		if !strings.HasSuffix(certDir, "/") {
			certDir += "/"
		}
		sb.WriteString(fmt.Sprintf(`frontend https_front
    bind *:%d ssl crt %s
    mode http
    option forwardfor
    http-request set-header X-Forwarded-Proto https
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpsPort, certDir))
	} else {
		// HTTP only
		sb.WriteString(fmt.Sprintf(`frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpPort))
	}

	// Add ACLs for each backend
	for _, b := range h.backends {
		aclName := sanitizeName(b.Name)
		aclPattern := domainToACLPattern(b.DomainMatch)
		sb.WriteString(fmt.Sprintf("    acl host_%s hdr_end(host) -i %s\n", aclName, aclPattern))
	}
	sb.WriteString("\n")

	// Add use_backend rules
	for _, b := range h.backends {
		aclName := sanitizeName(b.Name)
		sb.WriteString(fmt.Sprintf("    use_backend %s_backend if host_%s\n", aclName, aclName))
	}
	sb.WriteString("\n")

	// Backend definitions
	for _, b := range h.backends {
		aclName := sanitizeName(b.Name)
		sb.WriteString(fmt.Sprintf("backend %s_backend\n", aclName))
		sb.WriteString("    mode http\n")
		sb.WriteString("    balance roundrobin\n")

		if b.HTTPCheck {
			checkPath := b.CheckPath
			if checkPath == "" {
				checkPath = "/"
			}
			sb.WriteString(fmt.Sprintf("    option httpchk GET %s\n", checkPath))
			sb.WriteString(fmt.Sprintf("    server %s %s check\n", aclName, b.Server))
		} else {
			sb.WriteString(fmt.Sprintf("    server %s %s\n", aclName, b.Server))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func sanitizeName(name string) string {
	// Replace non-alphanumeric characters with underscores
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
	return strings.ToLower(result)
}

// domainToACLPattern converts a domain to an HAProxy ACL pattern
// For wildcard domains like "*.api.example.com", returns ".api.example.com" for suffix matching
// For exact domains like "grafana.example.com", returns the domain as-is
func domainToACLPattern(domain string) string {
	if strings.HasPrefix(domain, "*.") {
		// Convert *.api.example.com to .api.example.com for hdr_end suffix matching
		return domain[1:] // Remove the asterisk, keep the dot
	}
	return domain
}

// Reload reloads HAProxy configuration
func (h *HAProxy) Reload() error {
	// Validate config first
	cmd := exec.Command("haproxy", "-c", "-f", h.configPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("config validation failed: %s", string(out))
	}

	// Reload
	cmd = exec.Command("systemctl", "reload", "haproxy")
	if err := cmd.Run(); err != nil {
		// Try restart if reload fails
		cmd = exec.Command("systemctl", "restart", "haproxy")
		return cmd.Run()
	}
	return nil
}

// Start starts HAProxy
func (h *HAProxy) Start() error {
	cmd := exec.Command("systemctl", "start", "haproxy")
	return cmd.Run()
}

// Available checks if haproxy is installed
func Available() bool {
	_, err := exec.LookPath("haproxy")
	return err == nil
}
