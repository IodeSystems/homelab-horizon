package haproxy

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backend represents a HAProxy backend service
type Backend struct {
	Name          string   `json:"name"`
	DomainMatch   string   `json:"domain_match,omitempty"`   // Deprecated: use DomainMatches
	DomainMatches []string `json:"domain_matches,omitempty"` // e.g., [".example.com", "app.other.com"]
	Server        string   `json:"server"`                   // e.g., "192.168.1.10:8080"
	HTTPCheck     bool     `json:"http_check"`
	CheckPath     string   `json:"check_path"`             // e.g., "/health"
	InternalOnly  bool     `json:"internal_only"`          // Restrict to local network access only
	MetricsPath   string   `json:"metrics_path,omitempty"` // if set, deny this path from non-local sources (Prometheus scrapes the backend directly)
	MFAPortal     bool     `json:"mfa_portal,omitempty"`   // this backend is the MFA portal — the one thing an MFA-jailed VPN peer may reach

	// RateLimitRequests is the per-source threshold for this backend within
	// the gateway's rate window. Zero means use the global default; negative
	// means never limit this one.
	RateLimitRequests int `json:"rate_limit_requests,omitempty"`

	// Blue-green deploy fields (when Deploy is true, CurrentServer/NextServer are used instead of Server)
	Deploy        bool   `json:"deploy,omitempty"`
	CurrentServer string `json:"current_server,omitempty"` // host:port for active slot
	NextServer    string `json:"next_server,omitempty"`    // host:port for inactive slot
	DeployBalance string `json:"deploy_balance,omitempty"` // "first" or "roundrobin" (default "first")

	// Custom error pages
	ErrorFile503 string `json:"error_file_503,omitempty"` // path to custom 503.http file

	// Per-backend timeout overrides in seconds. Zero = inherit the defaults
	// section. Emitted as `timeout <name> <n>s` inside the backend block.
	TimeoutConnect int `json:"timeout_connect,omitempty"`
	TimeoutServer  int `json:"timeout_server,omitempty"`
	TimeoutTunnel  int `json:"timeout_tunnel,omitempty"`
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

// MFAJail is the L7 half of the VPN MFA jail. The L3 half (internal/iptables
// WG-INPUT) confines a jailed peer to the gateway's HAProxy ports; this decides
// what HAProxy will then do for it.
//
// Both halves are needed. iptables can't tell "the portal" from "every other
// service" when they share a listener, and HAProxy can't stop a peer from
// talking straight to sshd. Each covers the other's blind spot.
type MFAJail struct {
	Enabled bool // emit the jail rules at all

	// ACLPath is the file HAProxy reads jailed source IPs from, one per line.
	// Referenced as `src -f`, so it must exist whenever the rules are emitted —
	// HAProxy refuses to start on a missing ACL file. WriteJailACL owns it.
	ACLPath string

	// PortalURL is where a jailed peer is redirected when it asks for anything
	// else. Empty falls back to a bare 403: correct, but it looks like a broken
	// service rather than a login prompt.
	PortalURL string
}

// HAProxy manages HAProxy configuration
type HAProxy struct {
	configPath    string
	statsSocket   string
	backends      []Backend
	mfaJail       MFAJail
	metricsPort   int
	tlsMinVersion string
	rateLimit     *RateLimit
}

// SetRateLimit configures the edge volume tier. Nil disables it.
func (h *HAProxy) SetRateLimit(rl *RateLimit) {
	h.rateLimit = rl
}

// New creates a new HAProxy manager
func New(configPath, statsSocket string) *HAProxy {
	return &HAProxy{
		configPath:  configPath,
		statsSocket: statsSocket,
	}
}

// SetBackends sets the backends list, ordered by routing specificity so all
// consumers (config generation, status, API) see the same order HAProxy
// evaluates `use_backend` in.
func (h *HAProxy) SetBackends(backends []Backend) {
	h.backends = sortBackendsBySpecificity(backends)
}

// GetBackends returns the backends list
func (h *HAProxy) GetBackends() []Backend {
	return h.backends
}

// SetMetricsPort sets the port HAProxy's built-in Prometheus exporter listens
// on. 0 omits the listener entirely.
func (h *HAProxy) SetMetricsPort(port int) {
	h.metricsPort = port
}

// SetTLSMinVersion sets the ssl-min-ver floor applied to every bind. Empty
// falls back to TLSv1.2 rather than emitting nothing, so a caller that forgets
// still gets a floor.
func (h *HAProxy) SetTLSMinVersion(v string) {
	h.tlsMinVersion = v
}

// SetMFAJail sets the L7 jail parameters used by the next config generation.
// Changing it requires WriteConfig + Reload.
//
// Jailed *membership* lives in the ACL file instead, but note that a file-backed
// ACL is loaded into memory at startup — HAProxy does not re-read it per
// request. Membership changes therefore also need a reload (see WriteJailACL),
// or an `add acl`/`del acl` runtime-API update.
func (h *HAProxy) SetMFAJail(j MFAJail) {
	h.mfaJail = j
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
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
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

// SSLConfig holds SSL configuration for HAProxy
type SSLConfig struct {
	Enabled bool
	CertDir string // directory containing combined PEM files
}

// GenerateConfig returns the HAProxy configuration as a string (for preview)
func (h *HAProxy) GenerateConfig(httpPort, httpsPort int, ssl *SSLConfig) string {
	if h.tlsMinVersion == "" {
		h.tlsMinVersion = "TLSv1.2"
	}
	return h.generateConfig(httpPort, httpsPort, ssl)
}

// default503Page is the HAProxy-format error file written to errors/503.http at each reload.
// Per-service maintenance pages override this at the backend level.
const default503Page = "HTTP/1.0 503 Service Unavailable\r\n" +
	"Cache-Control: no-cache\r\n" +
	"Connection: close\r\n" +
	"Content-Type: text/html\r\n" +
	"\r\n" +
	`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Service Unavailable</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center}
.card{text-align:center;padding:2rem 3rem}
.code{font-size:5rem;font-weight:700;color:#f8fafc;line-height:1;letter-spacing:-2px}
.msg{margin-top:.75rem;font-size:1.1rem;color:#94a3b8}
.hint{margin-top:2rem;font-size:.8rem;color:#475569}
</style>
</head>
<body>
<div class="card">
  <div class="code">503</div>
  <div class="msg">Service temporarily unavailable</div>
  <div class="hint">We'll be back shortly.</div>
</div>
</body>
</html>`

// WriteConfig generates and writes the HAProxy configuration
func (h *HAProxy) WriteConfig(httpPort, httpsPort int, ssl *SSLConfig) error {
	config := h.generateConfig(httpPort, httpsPort, ssl)

	// Ensure directory exists
	dir := strings.TrimSuffix(h.configPath, "/haproxy.cfg")
	if dir != h.configPath {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	// Write default 503 error page (overrides vanilla HAProxy; per-service pages override this)
	errorsDir := dir + "/errors"
	if err := os.MkdirAll(errorsDir, 0755); err == nil {
		_ = os.WriteFile(errorsDir+"/503.http", []byte(default503Page), 0644)
	}

	return os.WriteFile(h.configPath, []byte(config), 0644)
}

func (h *HAProxy) generateConfig(httpPort, httpsPort int, ssl *SSLConfig) string {
	var sb strings.Builder

	// Sort backends so more-specific domains evaluate before less-specific ones.
	// hdr_end(host) is a greedy suffix match, so without this `iodesystems.com`
	// would swallow requests intended for `ha.iodesystems.com`.
	backends := sortBackendsBySpecificity(h.backends)

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
`)

	// TLS floor and cipher policy, applied to every bind.
	//
	// Previously absent, so the config inherited whatever the distro build
	// defaulted to — probably TLS 1.2+ on a modern Ubuntu, but inheritance is
	// not evidence, and PCI DSS 4.2.1 has prohibited TLS 1.0/1.1 since 2018.
	// The cipher lists are Mozilla's "intermediate" profile: forward secrecy
	// and AEAD only, no RSA key exchange, no CBC.
	fmt.Fprintf(&sb, `    ssl-default-bind-options ssl-min-ver %s no-tls-tickets
    ssl-default-bind-ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384
    ssl-default-bind-ciphersuites TLS_AES_128_GCM_SHA256:TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256
`, h.tlsMinVersion)

	// total-max-size is MEGABYTES, and this said 1024 — a one-gigabyte RAM
	// cache on a box whose whole job is proxying. HAProxy allocates it up
	// front as shared memory, and a reload runs two workers briefly, so the
	// real requirement was two gigabytes to serve a homelab. On the e2e VM
	// (2 GB) that meant the kernel OOM-killed HAProxy during reloads, which
	// surfaced as connections refused for a second or two at exactly the
	// moments hz rewrites the config — a jail lifting, a rate limit landing.
	//
	// 64 MB is enough to keep hot static assets in RAM, which is what the
	// cache was added for; anything larger is better served by the object
	// itself being cacheable downstream. max-object-size stays at 512 KB, so
	// the cache still holds a useful number of objects.
	sb.WriteString(`

# Cache configuration (RAM-based). total-max-size is in megabytes.
cache mycache
    total-max-size 64
    max-object-size 524288

defaults
    log     global
    mode    http
    option  httplog
    option  dontlognull
    timeout connect 5000
    timeout client  50000
    timeout server  50000
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

	// Check if SSL is enabled and collect the HTTP->HTTPS redirect host patterns.
	// Patterns are derived from each cert's SANs (not its filename): a single cert
	// covers many subzones, so matching only the filename would redirect just the
	// primary subzone to HTTPS and leave every other SAN on plain HTTP.
	sslEnabled := false
	var sslExact, sslSuffix []string
	if ssl != nil && ssl.Enabled && ssl.CertDir != "" {
		sslExact, sslSuffix, sslEnabled = certRedirectPatterns(ssl.CertDir)
	}

	// Whether to emit the local_access ACL: any internal-only backend (whole-
	// service restriction) or any metrics endpoint (path restriction) needs it.
	needLocalAccess := false
	for _, b := range backends {
		if b.InternalOnly || b.MetricsPath != "" {
			needLocalAccess = true
			break
		}
	}

	// Prometheus exporter frontend. HAProxy has carried this service since
	// 2.0, so exposing it costs a listener rather than another process
	// scraping the stats socket from outside.
	//
	// Restricted to RFC1918 sources on its own port: HAProxy's metrics name
	// every backend and their health, which is a map of the estate. The deny
	// comes first so a non-local request never reaches the service.
	if h.metricsPort > 0 {
		fmt.Fprintf(&sb, `frontend prometheus_metrics
    bind *:%d
    mode http
    no log
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
    http-request deny deny_status 403 unless local_access
    http-request use-service prometheus-exporter if { path /metrics }
    http-request return status 404

`, h.metricsPort)
	}

	// HTTP frontend
	if sslEnabled {
		// Redirect HTTP to HTTPS only for domains with SSL certificates
		fmt.Fprintf(&sb, `frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    acl is_router_check path /router-check
    acl has_horizon_header hdr(X-Homelab-Horizon-Check) -m found
    http-request return status 200 content-type "text/plain" string "OK" if is_router_check has_horizon_header
`, httpPort)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add ACLs for hosts covered by an SSL certificate, derived from cert SANs.
		// Non-wildcard SANs (example.com, dev.example.com) become exact host
		// matches; wildcard SANs (*.office.example.com) become suffix matches
		// (.office.example.com). All patterns share one ACL name so HAProxy ORs
		// them, and a single redirect covers every SSL-backed host.
		if len(sslExact) > 0 || len(sslSuffix) > 0 {
			sb.WriteString("    # Hosts covered by an SSL certificate (from cert SANs)\n")
			if len(sslExact) > 0 {
				fmt.Fprintf(&sb, "    acl ssl_host hdr(host) -i %s\n", strings.Join(sslExact, " "))
			}
			if len(sslSuffix) > 0 {
				fmt.Fprintf(&sb, "    acl ssl_host hdr_end(host) -i %s\n", strings.Join(sslSuffix, " "))
			}
			sb.WriteString("    # Only redirect to HTTPS for hosts with SSL certificates\n")
			sb.WriteString("    redirect scheme https code 301 if ssl_host !is_router_check\n")
		}

		// Add backend ACLs and routing to HTTP frontend (for non-SSL domains)
		sb.WriteString("    # Backend routing (for domains without SSL)\n")
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(h.rateLimit, backends))
		sb.WriteString(mfaJailRules(h.mfaJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access\n", aclName)
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")

		// HTTPS frontend - HAProxy loads all certs from directory
		certDir := ssl.CertDir
		if !strings.HasSuffix(certDir, "/") {
			certDir += "/"
		}
		fmt.Fprintf(&sb, `frontend https_front
    bind *:%d ssl crt %s
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    http-request set-header X-Forwarded-Proto https
    # Compression: gzip is a FALLBACK for backends that return raw responses. No 'offload' —
    # that strips Accept-Encoding before the backend, forcing it to send raw so HAProxy re-gzips
    # (and HAProxy only does gzip, never brotli). Without offload, Accept-Encoding reaches the
    # backend, so a backend serving PRECOMPRESSED assets (its response already carries a
    # Content-Encoding) passes straight through untouched — letting a brotli-precompressing
    # backend (e.g. redline's webui) deliver brotli to clients instead of a mediocre re-gzip.
    compression algo gzip
    compression type text/html text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml
    # HAProxy LRU cache
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpsPort, certDir)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing to HTTPS frontend
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(h.rateLimit, backends))
		sb.WriteString(mfaJailRules(h.mfaJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access\n", aclName)
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")
	} else {
		// HTTP only - no SSL
		fmt.Fprintf(&sb, `frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    # Compression: gzip is a FALLBACK for backends that return raw responses. No 'offload' —
    # that strips Accept-Encoding before the backend, forcing it to send raw so HAProxy re-gzips
    # (and HAProxy only does gzip, never brotli). Without offload, Accept-Encoding reaches the
    # backend, so a backend serving PRECOMPRESSED assets (its response already carries a
    # Content-Encoding) passes straight through untouched — letting a brotli-precompressing
    # backend (e.g. redline's webui) deliver brotli to clients instead of a mediocre re-gzip.
    compression algo gzip
    compression type text/html text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml
    # HAProxy LRU cache
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, httpPort)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(h.rateLimit, backends))
		sb.WriteString(mfaJailRules(h.mfaJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access\n", aclName)
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")
	}

	// The rate-limit table, before the backends that reference it. A backend
	// with no servers: HAProxy carries stick-tables this way and never routes
	// to it.
	sb.WriteString(rateLimitBackend(h.rateLimit))

	// Backend definitions
	for _, b := range backends {
		aclName := sanitizeName(b.Name)
		fmt.Fprintf(&sb, "backend %s_backend\n", aclName)
		sb.WriteString("    mode http\n")
		if b.ErrorFile503 != "" {
			fmt.Fprintf(&sb, "    errorfile 503 %s\n", b.ErrorFile503)
		}

		// Per-backend timeout overrides. Omitted timeouts inherit the defaults section.
		if b.TimeoutConnect > 0 {
			fmt.Fprintf(&sb, "    timeout connect %ds\n", b.TimeoutConnect)
		}
		if b.TimeoutServer > 0 {
			fmt.Fprintf(&sb, "    timeout server %ds\n", b.TimeoutServer)
		}
		if b.TimeoutTunnel > 0 {
			fmt.Fprintf(&sb, "    timeout tunnel %ds\n", b.TimeoutTunnel)
		}

		if b.Deploy {
			balance := b.DeployBalance
			if balance == "" {
				balance = "first"
			}
			fmt.Fprintf(&sb, "    balance %s\n", balance)
			checkPath := b.CheckPath
			if checkPath == "" {
				checkPath = "/"
			}
			fmt.Fprintf(&sb, "    option httpchk GET %s\n", checkPath)
			sb.WriteString("    http-check expect status 200\n")
			fmt.Fprintf(&sb, "    server next %s check inter 3s fall 2 rise 2\n", b.NextServer)
			fmt.Fprintf(&sb, "    server current %s check inter 3s fall 2 rise 2\n", b.CurrentServer)
		} else {
			sb.WriteString("    balance roundrobin\n")
			if b.HTTPCheck {
				checkPath := b.CheckPath
				if checkPath == "" {
					checkPath = "/"
				}
				fmt.Fprintf(&sb, "    option httpchk GET %s\n", checkPath)
				fmt.Fprintf(&sb, "    server %s %s check\n", aclName, b.Server)
			} else {
				fmt.Fprintf(&sb, "    server %s %s\n", aclName, b.Server)
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// SanitizeName converts a service name to a safe HAProxy identifier
// mfaJailRules renders the jail's frontend block: the source-list ACL plus the
// rule that bounces a jailed peer asking for anything but the portal. Returns
// "" when the jail is off or no backend is flagged as the portal — emitting the
// deny with no portal exception would lock every jailed peer out of the page
// that un-jails them.
//
// Must be emitted *after* the `acl host_<name>` declarations it references and
// *before* the `use_backend` lines, so it is generated alongside the other
// deny rules rather than with the ACL preamble.
//
// Interaction with the HTTP→HTTPS upgrade in the SSL http_front: HAProxy runs
// every `http-request` rule before any legacy `redirect` rule, whatever the
// textual order (it warns about this at parse time — the same warning the
// pre-existing internal-only/metrics denies already produce). That ordering is
// the one we want: a jailed peer asking for some other host is sent to the
// portal rather than first upgraded to HTTPS on a host it may not reach.
func mfaJailRules(j MFAJail, backends []Backend) string {
	if !j.Enabled || j.ACLPath == "" {
		return ""
	}
	var portals []string
	for _, b := range backends {
		if b.MFAPortal {
			portals = append(portals, "!host_"+sanitizeName(b.Name))
		}
	}
	if len(portals) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("    # VPN MFA jail: peers with no verified session may reach only the portal.\n")
	sb.WriteString("    # Source list is rewritten by horizon on every jail transition.\n")
	fmt.Fprintf(&sb, "    acl mfa_jailed src -f %s\n", j.ACLPath)
	cond := "mfa_jailed " + strings.Join(portals, " ")
	if j.PortalURL != "" {
		fmt.Fprintf(&sb, "    http-request redirect location %s code 302 if %s\n", j.PortalURL, cond)
	} else {
		fmt.Fprintf(&sb, "    http-request deny deny_status 403 if %s\n", cond)
	}
	return sb.String()
}

// RateLimit is the edge volume tier (EDGE-4), or nil when disabled.
type RateLimit struct {
	WindowSeconds int
	Requests      int // global default; per-backend overrides win
	ExemptLocal   bool
}

// rateLimitTable is the stick-table backend name. One table for the gateway:
// each distinct window would need its own, and the thresholds are per-service
// anyway.
const rateLimitTable = "hz_rate_limit"

// rateLimitBackend emits the stick-table that holds per-source request rates.
//
// A backend with no servers, which is how HAProxy carries a table that
// frontends reference — it is never routed to.
func rateLimitBackend(rl *RateLimit) string {
	if rl == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Edge rate limiting (EDGE-4): per-source request rates.\n")
	sb.WriteString("# A table, not a WAF — it catches volume, which is the tier that sat\n")
	sb.WriteString("# missing between \"no limit at all\" and an iptables ban.\n")
	fmt.Fprintf(&sb, "backend %s\n", rateLimitTable)
	// 1m entries is ~1MB and covers far more distinct sources than a homelab
	// edge will see; expire well past the window so a burst stays counted.
	fmt.Fprintf(&sb, "    stick-table type ip size 1m expire %ds store http_req_rate(%ds)\n\n",
		rl.WindowSeconds*6, rl.WindowSeconds)
	return sb.String()
}

// rateLimitRules emits the tracking and deny rules for one frontend.
//
// Tracking is unconditional so the table reflects real traffic even for exempt
// sources — an operator looking at the table wants to see what is arriving, not
// a filtered view. The exemption applies to the deny, which is where it matters.
func rateLimitRules(rl *RateLimit, backends []Backend) string {
	if rl == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("    # Rate limiting: track every source, deny the ones over threshold\n")
	fmt.Fprintf(&sb, "    http-request track-sc0 src table %s\n", rateLimitTable)

	exempt := ""
	if rl.ExemptLocal {
		// local_access is already defined in both frontends for internal-only
		// services; reusing it keeps one definition of "inside".
		exempt = " !local_access"
	}

	for _, b := range backends {
		// Never limit the MFA portal. These rules are evaluated before the
		// jail rules, so a jailed peer hammering the portal would be answered
		// 429 by the very endpoint that exists to un-jail them — locking them
		// out of the recovery path with no way back. The portal is already the
		// one host a jailed peer may reach; it is exempt here for the same
		// reason.
		if b.MFAPortal {
			continue
		}

		threshold := b.RateLimitRequests
		if threshold == 0 {
			threshold = rl.Requests
		}
		if threshold <= 0 {
			// Negative is an explicit opt-out, zero with no global default
			// means nothing to enforce.
			continue
		}
		fmt.Fprintf(&sb,
			"    http-request deny deny_status 429 if host_%s%s { sc_http_req_rate(0) gt %d }\n",
			sanitizeName(b.Name), exempt, threshold)
	}
	sb.WriteString("\n")
	return sb.String()
}

// WriteJailACL writes the jailed-source list HAProxy reads via `src -f`, and
// reports whether the contents changed. A changed list needs a reload to take
// effect — the file is read into memory at load time, not per request.
//
// Always writes the file, even when empty: the `acl ... src -f` line references
// it unconditionally and HAProxy refuses to start if it's missing.
func WriteJailACL(path string, ips []string) (changed bool, err error) {
	if path == "" {
		return false, nil
	}
	sorted := append([]string(nil), ips...)
	sort.Strings(sorted)

	var sb strings.Builder
	sb.WriteString("# Managed by homelab-horizon — VPN peers without a verified MFA session.\n")
	for _, ip := range sorted {
		sb.WriteString(ip)
		sb.WriteString("\n")
	}
	next := sb.String()

	if prev, readErr := os.ReadFile(path); readErr == nil && string(prev) == next {
		return false, nil
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(next), 0644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

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

// sortBackendsBySpecificity returns a copy of backends ordered so the most
// specific domain match evaluates first in HAProxy's first-match-wins
// `use_backend` chain. Specificity is ranked by the *least* specific domain in
// each backend (its greediest suffix), since that's the one that can swallow
// shorter hosts on other backends.
func sortBackendsBySpecificity(backends []Backend) []Backend {
	out := make([]Backend, len(backends))
	copy(out, backends)
	sort.SliceStable(out, func(i, j int) bool {
		iDots, iLen, iKey := backendMinSpecificity(out[i])
		jDots, jLen, jKey := backendMinSpecificity(out[j])
		if iDots != jDots {
			return iDots > jDots
		}
		if iLen != jLen {
			return iLen > jLen
		}
		return iKey < jKey
	})
	return out
}

// backendMinSpecificity returns the specificity of the backend's least-specific
// domain (most dots/longest pattern wins). Backends with no domains sort last.
func backendMinSpecificity(b Backend) (dots, length int, key string) {
	domains := b.GetDomainMatches()
	if len(domains) == 0 {
		return -1, -1, ""
	}
	dots, length = -1, -1
	for _, d := range domains {
		p := domainToACLPattern(d)
		dc := strings.Count(p, ".")
		ln := len(p)
		if dots == -1 || dc < dots || (dc == dots && ln < length) {
			dots, length, key = dc, ln, p
		}
	}
	return
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

// certRedirectPatterns reads every .pem cert in certDir and returns the HAProxy
// host-match patterns used to redirect HTTP->HTTPS, derived from each cert's SANs
// rather than its filename. Non-wildcard SANs become exact matches; wildcard SANs
// (*.x) become suffix matches (.x). found reports whether any cert file exists
// (i.e. whether SSL should be considered enabled). Results are sorted for
// deterministic config output.
func certRedirectPatterns(certDir string) (exact, suffix []string, found bool) {
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return nil, nil, false
	}
	exactSet := map[string]struct{}{}
	suffixSet := map[string]struct{}{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		found = true
		names := certDNSNames(filepath.Join(certDir, e.Name()))
		if len(names) == 0 {
			// Cert couldn't be parsed (or has no SANs): fall back to the filename
			// so redirect coverage isn't silently lost. The filename is the primary
			// domain's base, matched both exactly and as a suffix.
			base := strings.ToLower(strings.TrimSuffix(e.Name(), ".pem"))
			exactSet[base] = struct{}{}
			suffixSet["."+base] = struct{}{}
			continue
		}
		for _, n := range names {
			n = strings.ToLower(strings.TrimSuffix(n, "."))
			if n == "" {
				continue
			}
			if strings.HasPrefix(n, "*.") {
				suffixSet[n[1:]] = struct{}{} // "*.office.x" -> ".office.x"
			} else {
				exactSet[n] = struct{}{}
			}
		}
	}
	for k := range exactSet {
		exact = append(exact, k)
	}
	for k := range suffixSet {
		suffix = append(suffix, k)
	}
	sort.Strings(exact)
	sort.Strings(suffix)
	return exact, suffix, found
}

// certDNSNames parses the leaf certificate from a PEM bundle (fullchain+key) and
// returns its DNS SANs. Returns nil if the file can't be read or parsed.
func certDNSNames(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil
		}
		return cert.DNSNames
	}
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
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
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
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
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
