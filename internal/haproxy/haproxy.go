// Package haproxy computes and applies the gateway's HAProxy configuration.
//
// The package is split along one seam, and the split is load-bearing for the
// hz-agent work (plan/architecture.md, "hz-agent de-roots the hz web surface"):
//
//	render.go   pure     desired state in, bytes out. No files, no commands,
//	                     no clock, no environment. Runs anywhere, as anyone.
//	apply.go    root     the side effects: write files, reload the service,
//	                     and the one READ — the certificate store.
//	haproxy.go  manager  holds the desired state, gathers the inputs render
//	                     needs, and joins the two halves.
//
// The cert store reaches render through CertStore, a replaceable function,
// rather than a read inside config generation. That is what lets an
// unprivileged hz web still render an HTTPS config once /etc/haproxy/certs is
// out of reach (plan/architecture.md, phase 4 item 12 step 3) — it supplies the
// facts instead of opening the directory.
//
// Anything that computes what the config *should* be belongs in render.go, so
// that half can later run in an unprivileged hz web process while apply.go's
// short list of side effects moves to the agent.
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

// BackendStatus contains runtime status of a backend
type BackendStatus struct {
	Backend
	Healthy      bool
	LastCheck    time.Time
	Error        string
	CurrentState string // "up", "down", "drain", "maint", "unknown" - for deploy backends
	NextState    string // "up", "down", "drain", "maint", "unknown" - for deploy backends
}

// CertStore reports the certificates HAProxy would load from a directory.
//
// It exists so the cert facts the renderer needs are an INPUT to the manager
// rather than a read performed inside it. The default is ScanCertDir, which
// opens the directory — that is the only reason config generation needs to be
// able to read /etc/haproxy/certs at all. hz web loses that ability in phase 4
// item 12 step 3, and this is the one function it replaces to keep rendering
// HTTPS configs afterwards; nothing else in the render path looks at a disk.
//
// A store that cannot read the directory returns nothing, which renders as
// "no HTTPS frontend" — the same answer the directory read gave on failure.
type CertStore func(certDir string) []Cert

// HAProxy manages HAProxy configuration
type HAProxy struct {
	configPath    string
	statsSocket   string
	backends      []Backend
	mfaJail       MFAJail
	metricsPort   int
	tlsMinVersion string
	rateLimit     *RateLimit

	// certs is where the cert facts come from. Never nil after New; see
	// SetCertStore.
	certs CertStore
}

// SetRateLimit configures the edge volume tier. Nil disables it.
func (h *HAProxy) SetRateLimit(rl *RateLimit) {
	h.rateLimit = rl
}

// SetCertStore replaces where the manager learns what is in the certificate
// store. Nil restores the default (ScanCertDir, which reads the directory).
//
// The replacement a de-rooted hz web will want is one that reports the certs
// hz itself asked Let's Encrypt for, rather than opening a directory it can no
// longer open.
func (h *HAProxy) SetCertStore(store CertStore) {
	if store == nil {
		store = ScanCertDir
	}
	h.certs = store
}

// New creates a new HAProxy manager
func New(configPath, statsSocket string) *HAProxy {
	return &HAProxy{
		configPath:  configPath,
		statsSocket: statsSocket,
		certs:       ScanCertDir,
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

// GenerateConfig returns the HAProxy configuration as a string (for preview)
func (h *HAProxy) GenerateConfig(httpPort, httpsPort int, ssl *SSLConfig) string {
	return RenderConfig(h.configInput(httpPort, httpsPort, ssl))
}

// configInput gathers the manager's desired state into the pure renderer's
// input. It is the only place config generation reads the machine, and it
// reads exactly one thing: the certificate store, to learn which hosts the
// HTTP→HTTPS redirect covers — and it reads that through h.certs, so the read
// can be replaced by a caller that already knows the answer.
func (h *HAProxy) configInput(httpPort, httpsPort int, ssl *SSLConfig) ConfigInput {
	return ConfigInput{
		HTTPPort:      httpPort,
		HTTPSPort:     httpsPort,
		Backends:      h.backends,
		TLS:           h.loadTLSAssets(ssl),
		TLSMinVersion: h.tlsMinVersion,
		MetricsPort:   h.metricsPort,
		MFAJail:       h.mfaJail,
		RateLimit:     h.rateLimit,
	}
}

// loadTLSAssets asks the cert store what HTTPS has to work with, and returns
// what the renderer needs from it — or nil when there is nothing to serve
// HTTPS with: SSL off, no directory configured, or no cert file found.
//
// The manager decides WHETHER to ask (TLSWanted) and the pure half decides what
// the answer means (TLSAssetsFor). Neither of those is a file read any more;
// the read is whatever h.certs is.
func (h *HAProxy) loadTLSAssets(ssl *SSLConfig) *TLSAssets {
	if !TLSWanted(ssl) {
		return nil
	}
	store := h.certs
	if store == nil {
		// A zero-value HAProxy still has to render the same config a
		// constructed one does; New sets this, so this is belt for a struct
		// literal somebody writes later.
		store = ScanCertDir
	}
	return TLSAssetsFor(ssl.CertDir, store(ssl.CertDir))
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
