package haproxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Backend represents a HAProxy backend service
type Backend struct {
	Name          string   `json:"name"`
	DomainMatch   string   `json:"domain_match,omitempty"`  // Deprecated: use DomainMatches
	DomainMatches []string `json:"domain_matches,omitempty"` // e.g., [".example.com", "app.other.com"]
	Server        string   `json:"server"`                  // e.g., "192.168.1.10:8080"
	HTTPCheck    bool   `json:"http_check"`
	CheckPath    string `json:"check_path"`    // e.g., "/health"
	InternalOnly bool   `json:"internal_only"` // Restrict to local network access only

	// Blue-green deploy fields (when Deploy is true, CurrentServer/NextServer are used instead of Server)
	Deploy        bool   `json:"deploy,omitempty"`
	CurrentServer string `json:"current_server,omitempty"`  // host:port for active slot
	NextServer    string `json:"next_server,omitempty"`     // host:port for inactive slot
	DeployBalance string `json:"deploy_balance,omitempty"`  // "first" or "roundrobin" (default "first")
}

// GetDomainMatches returns all domain matches, falling back to DomainMatch for backwards compat
func (b *Backend) GetDomainMatches() []string {
	if len(b.DomainMatches) > 0 {
		return b.DomainMatches
	}
	if b.DomainMatch != "" {
		return []string{b.DomainMatch}
	}
	return nil
}

// BackendStatus contains runtime status of a backend
type BackendStatus struct {
	Backend
	Healthy      bool
	LastCheck    time.Time
	Error        string
	CurrentState string // "up", "down", "drain", "maint", "unknown" - for deploy backends
	NextState    string // "up", "down", "drain", "maint", "unknown" - for deploy backends
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

// GetBackendStatuses checks health of all backends using HAProxy's own health check data.
// Uses the HAProxy admin socket "show stat" to get real status — no redundant external checks.
func (h *HAProxy) GetBackendStatuses() []BackendStatus {
	// Query HAProxy for all stats
	haStats := h.getHAProxyStats()

	var statuses []BackendStatus
	for _, b := range h.backends {
		bs := BackendStatus{
			Backend:   b,
			LastCheck: time.Now(),
		}

		backendName := sanitizeName(b.Name) + "_backend"

		if b.Deploy {
			// Deploy backends: get per-server state from HAProxy
			currentInfo := haStats[backendName+"/current"]
			nextInfo := haStats[backendName+"/next"]

			bs.CurrentState = currentInfo.state
			bs.NextState = nextInfo.state
			if currentInfo.checkDesc != "" {
				bs.Error = currentInfo.checkDesc
			}
			bs.Healthy = currentInfo.state == "up" || nextInfo.state == "up"
		} else {
			// Single backend: get server state from HAProxy
			srvName := sanitizeName(b.Name)
			info := haStats[backendName+"/"+srvName]
			bs.CurrentState = info.state
			bs.Healthy = info.state == "up"
			if !bs.Healthy && info.checkDesc != "" {
				bs.Error = info.checkDesc
			}
		}

		statuses = append(statuses, bs)
	}

	return statuses
}

type haStatInfo struct {
	state     string // "up", "down", "no check"
	checkDesc string // e.g., "Layer7 check passed", "Connection refused"
}

// getHAProxyStats queries "show stat" from the HAProxy socket and returns
// a map keyed by "backend_name/server_name" with status info.
func (h *HAProxy) getHAProxyStats() map[string]haStatInfo {
	result := make(map[string]haStatInfo)

	conn, err := net.DialTimeout("unix", h.statsSocket, 2*time.Second)
	if err != nil {
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("show stat\n")); err != nil {
		return result
	}

	scanner := bufio.NewScanner(conn)
	// Read header
	if !scanner.Scan() {
		return result
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 38 {
			continue
		}

		pxName := fields[0]  // backend name
		svName := fields[1]  // server name or FRONTEND/BACKEND
		status := fields[17] // UP, DOWN, MAINT, DRAIN, no check, etc.
		lastChk := ""
		if len(fields) > 76 {
			lastChk = fields[76] // last_chk description
		}

		// Skip FRONTEND and BACKEND aggregate rows — we want individual servers
		if svName == "FRONTEND" || svName == "BACKEND" {
			continue
		}

		key := pxName + "/" + svName
		state := "unknown"
		switch {
		case status == "UP":
			state = "up"
		case status == "DOWN":
			state = "down"
		case status == "MAINT":
			state = "maint"
		case strings.Contains(status, "DRAIN"):
			state = "drain"
		case status == "no check":
			state = "no check"
		}

		result[key] = haStatInfo{state: state, checkDesc: lastChk}
	}

	return result
}

func (h *HAProxy) httpCheck(server, path string) (bool, string) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s%s", server, path)
	resp, err := client.Get(url)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, ""
	}

	return false, fmt.Sprintf("HTTP %d %s", resp.StatusCode, resp.Status)
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

# Cache configuration (RAM-based)
cache mycache
    total-max-size 256
    max-object-size 64

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

	// Check if SSL is enabled and collect domains with certs
	sslEnabled := false
	var sslDomains []string
	if ssl != nil && ssl.Enabled && ssl.CertDir != "" {
		entries, err := os.ReadDir(ssl.CertDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".pem") {
					sslEnabled = true
					// Extract domain name from filename (e.g., "example.com.pem" -> "example.com")
					domain := strings.TrimSuffix(e.Name(), ".pem")
					// Convert to wildcard pattern for matching (e.g., "example.com" -> "*.example.com")
					sslDomains = append(sslDomains, "*."+domain)
				}
			}
		}
	}

	// Check if any backends are internal-only
	hasInternalOnly := false
	for _, b := range h.backends {
		if b.InternalOnly {
			hasInternalOnly = true
			break
		}
	}

	// HTTP frontend
	if sslEnabled {
		// Redirect HTTP to HTTPS only for domains with SSL certificates
		sb.WriteString(fmt.Sprintf(`frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    acl is_router_check path /router-check
    acl has_horizon_header hdr(X-Homelab-Horizon-Check) -m found
    http-request return status 200 content-type "text/plain" string "OK" if is_router_check has_horizon_header
`, httpPort))

		// Add local_access ACL if any backend is internal-only
		if hasInternalOnly {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add ACLs for domains with SSL certificates
		if len(sslDomains) > 0 {
			sb.WriteString("    # Domains with SSL certificates\n")
			for i, domain := range sslDomains {
				aclPattern := domainToACLPattern(domain)
				sb.WriteString(fmt.Sprintf("    acl ssl_domain_%d hdr_end(host) -i %s\n", i, aclPattern))
			}
			// Redirect to HTTPS for each SSL domain
			sb.WriteString("    # Only redirect to HTTPS for domains with SSL certificates\n")
			for i := range sslDomains {
				sb.WriteString(fmt.Sprintf("    redirect scheme https code 301 if ssl_domain_%d !is_router_check\n", i))
			}
		}

		// Add backend ACLs and routing to HTTP frontend (for non-SSL domains)
		sb.WriteString("    # Backend routing (for domains without SSL)\n")
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			sb.WriteString(fmt.Sprintf("    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " ")))
		}
		sb.WriteString("\n")
		// Deny external access to internal-only backends
		for _, b := range h.backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				sb.WriteString(fmt.Sprintf("    http-request deny deny_status 403 if host_%s !local_access\n", aclName))
			}
		}
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			sb.WriteString(fmt.Sprintf("    use_backend %s_backend if host_%s\n", aclName, aclName))
		}
		sb.WriteString("\n")

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
    # Caching - strip cache headers and use HAProxy cache
    http-request del-header Cache-Control
    http-request del-header Pragma
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpsPort, certDir))

		// Add local_access ACL if any backend is internal-only
		if hasInternalOnly {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing to HTTPS frontend
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			sb.WriteString(fmt.Sprintf("    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " ")))
		}
		sb.WriteString("\n")
		// Deny external access to internal-only backends
		for _, b := range h.backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				sb.WriteString(fmt.Sprintf("    http-request deny deny_status 403 if host_%s !local_access\n", aclName))
			}
		}
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			sb.WriteString(fmt.Sprintf("    use_backend %s_backend if host_%s\n", aclName, aclName))
		}
		sb.WriteString("\n")
	} else {
		// HTTP only - no SSL
		sb.WriteString(fmt.Sprintf(`frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Caching - strip cache headers and use HAProxy cache
    http-request del-header Cache-Control
    http-request del-header Pragma
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpPort))

		// Add local_access ACL if any backend is internal-only
		if hasInternalOnly {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			sb.WriteString(fmt.Sprintf("    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " ")))
		}
		sb.WriteString("\n")
		// Deny external access to internal-only backends
		for _, b := range h.backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				sb.WriteString(fmt.Sprintf("    http-request deny deny_status 403 if host_%s !local_access\n", aclName))
			}
		}
		for _, b := range h.backends {
			aclName := sanitizeName(b.Name)
			sb.WriteString(fmt.Sprintf("    use_backend %s_backend if host_%s\n", aclName, aclName))
		}
		sb.WriteString("\n")
	}

	// Backend definitions
	for _, b := range h.backends {
		aclName := sanitizeName(b.Name)
		sb.WriteString(fmt.Sprintf("backend %s_backend\n", aclName))
		sb.WriteString("    mode http\n")

		if b.Deploy {
			balance := b.DeployBalance
			if balance == "" {
				balance = "first"
			}
			sb.WriteString(fmt.Sprintf("    balance %s\n", balance))
			checkPath := b.CheckPath
			if checkPath == "" {
				checkPath = "/"
			}
			sb.WriteString(fmt.Sprintf("    option httpchk GET %s\n", checkPath))
			sb.WriteString("    http-check expect status 200\n")
			sb.WriteString(fmt.Sprintf("    server next %s check inter 3s fall 2 rise 2\n", b.NextServer))
			sb.WriteString(fmt.Sprintf("    server current %s check inter 3s fall 2 rise 2\n", b.CurrentServer))
		} else {
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
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// SanitizeName converts a service name to a safe HAProxy identifier
func SanitizeName(name string) string {
	return sanitizeName(name)
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

// SetServerState sends a state change command to the HAProxy admin socket.
// backend is the backend name (e.g., "myservice_backend"), server is "current" or "next",
// state is "ready", "drain", or "maint".
func (h *HAProxy) SetServerState(backend, server, state string) error {
	conn, err := net.DialTimeout("unix", h.statsSocket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connecting to haproxy socket: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cmd := fmt.Sprintf("set server %s/%s state %s\n", backend, server, state)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("writing to haproxy socket: %w", err)
	}

	// Read response
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	resp := strings.TrimSpace(string(buf[:n]))
	if resp != "" {
		return fmt.Errorf("haproxy: %s", resp)
	}
	return nil
}

// GetServerState queries the HAProxy admin socket for server states in a backend.
// Returns a map of server name -> state (e.g., "ready", "drain", "maint").
func (h *HAProxy) GetServerState(backend string) (map[string]string, error) {
	conn, err := net.DialTimeout("unix", h.statsSocket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to haproxy socket: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cmd := fmt.Sprintf("show servers state %s\n", backend)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, fmt.Errorf("writing to haproxy socket: %w", err)
	}

	states := make(map[string]string)
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// Format: be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state ...
		// srv_op_state: 0=stopped, 2=running
		// srv_admin_state bitmask: 0=ready, bit0=FMAINT, bit5=FDRAIN
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		srvName := fields[3]
		opState := fields[5]
		adminState := fields[6]

		switch {
		case adminState != "0" && adminState != "6": // has MAINT bit
			states[srvName] = "maint"
		case adminState == "6" || strings.Contains(adminState, "drain"):
			states[srvName] = "drain"
		case opState == "2":
			states[srvName] = "up"
		case opState == "0":
			states[srvName] = "down"
		default:
			states[srvName] = "unknown"
		}
	}
	return states, nil
}

// GetStatsSocket returns the stats socket path
func (h *HAProxy) GetStatsSocket() string {
	return h.statsSocket
}

// Available checks if haproxy is installed
func Available() bool {
	_, err := exec.LookPath("haproxy")
	return err == nil
}
