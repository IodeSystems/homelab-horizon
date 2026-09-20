// Package haproxy computes and applies the gateway's HAProxy configuration.
//
// The package is split along one seam, and the split is load-bearing for the
// hz-agent work (plan/architecture.md, "hz-agent de-roots the hz web surface"):
//
//	render.go   pure     desired state in, bytes out. No files, no commands,
//	                     no clock, no environment. Runs anywhere, as anyone.
//	apply.go    root     the side effects: write files, reload the service.
//	haproxy.go  manager  holds the desired state, reads the machine for the
//	                     inputs render needs, and joins the two halves.
//
// Anything that computes what the config *should* be belongs in render.go, so
// that half can later run in an unprivileged hz web process while apply.go's
// short list of side effects moves to the agent.
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

		backendName := BackendName(b.Name)

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
	return RenderConfig(h.configInput(httpPort, httpsPort, ssl))
}

// configInput gathers the manager's desired state into the pure renderer's
// input. It is the only place config generation reads the machine, and it
// reads exactly one thing: the certificate store, to learn which hosts the
// HTTP→HTTPS redirect covers.
func (h *HAProxy) configInput(httpPort, httpsPort int, ssl *SSLConfig) ConfigInput {
	return ConfigInput{
		HTTPPort:      httpPort,
		HTTPSPort:     httpsPort,
		Backends:      h.backends,
		TLS:           loadTLSAssets(ssl),
		TLSMinVersion: h.tlsMinVersion,
		MetricsPort:   h.metricsPort,
		MFAJail:       h.mfaJail,
		RateLimit:     h.rateLimit,
	}
}

// loadTLSAssets reads the certificate store and returns what the renderer
// needs from it, or nil when there is nothing to serve HTTPS with: SSL off, no
// directory configured, or no cert file in the directory.
func loadTLSAssets(ssl *SSLConfig) *TLSAssets {
	if ssl == nil || !ssl.Enabled || ssl.CertDir == "" {
		return nil
	}
	exact, suffix, found := certRedirectPatterns(ssl.CertDir)
	if !found {
		return nil
	}
	return &TLSAssets{CertDir: ssl.CertDir, Exact: exact, Suffix: suffix}
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
