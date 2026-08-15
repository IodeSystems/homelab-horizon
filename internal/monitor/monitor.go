package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Check states.
//
// StatusWarning is the third state: the check succeeded, and something about
// the answer says it will stop succeeding. A certificate six days from expiry
// serves perfectly today, so calling it "failed" is a lie the operator learns
// to ignore — and calling it "ok" is the silence this state exists to break.
const (
	StatusOK       = "ok"
	StatusWarning  = "warning"
	StatusFailed   = "failed"
	StatusPending  = "pending"
	StatusDisabled = "disabled"
)

// WarningError marks a check result as degraded rather than broken. A check
// returns it in place of a plain error; everything else about the check's
// contract is unchanged.
type WarningError struct{ Msg string }

func (w *WarningError) Error() string { return w.Msg }

// Warnf builds a WarningError.
func Warnf(format string, args ...any) error {
	return &WarningError{Msg: fmt.Sprintf(format, args...)}
}

// isWarning reports whether an error is a warning rather than a failure.
func isWarning(err error) bool {
	var w *WarningError
	return errors.As(err, &w)
}

// CheckStatus represents the current status of a service check
type CheckStatus struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	Status    string    `json:"status"` // "ok", "warning", "failed", "pending", "disabled"
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error,omitempty"`
	Interval  int       `json:"interval"`
	Enabled   bool      `json:"enabled"`
	AutoGen   bool      `json:"auto_gen"` // True if auto-generated from HAProxy service
}

// CheckResult records a single check execution for history tracking
type CheckResult struct {
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`  // "ok", "warning", "failed"
	Latency   int64     `json:"latency"` // milliseconds
	Error     string    `json:"error,omitempty"`
}

const maxHistoryPerCheck = 100

// defaultCertWarningDays is how far ahead of expiry a certificate starts
// warning. Seven days covers a weekend plus slack: Let's Encrypt renews at 30
// days left, so anything still unrenewed inside a week means renewal is broken
// rather than pending, and there is still time to fix it by hand.
const defaultCertWarningDays = 7

// certWarningWindow is the configured lead time, or the default.
func (m *Monitor) certWarningWindow() time.Duration {
	days := defaultCertWarningDays
	if m.config != nil && m.config.CertWarningDays > 0 {
		days = m.config.CertWarningDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Monitor manages service health checks and notifications
type Monitor struct {
	mu       sync.RWMutex
	config   *config.Config
	statuses map[string]*CheckStatus  // keyed by check name
	history  map[string][]CheckResult // keyed by check name, ring buffer of last 100 results
	ctx      context.Context
	cancel   context.CancelFunc
	client   *http.Client
}

// New creates a new Monitor
func New(cfg *config.Config) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Monitor{
		config:   cfg,
		statuses: make(map[string]*CheckStatus),
		history:  make(map[string][]CheckResult),
		ctx:      ctx,
		cancel:   cancel,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// getAllChecks returns all checks: manual ServiceChecks + auto-generated from HAProxy services
func (m *Monitor) getAllChecks() []config.ServiceCheck {
	checks := make([]config.ServiceCheck, 0, len(m.config.ServiceChecks))

	// Add manual checks
	checks = append(checks, m.config.ServiceChecks...)

	// Auto-generate checks from HAProxy-proxied services
	for _, svc := range m.config.Services {
		if svc.Proxy == nil {
			continue
		}

		// Check if this service already has a manual check
		hasManualCheck := false
		for _, c := range m.config.ServiceChecks {
			if c.Name == svc.Name || c.Name == "svc:"+svc.Name {
				hasManualCheck = true
				break
			}
		}
		if hasManualCheck {
			continue
		}

		// Resolve what to probe:
		//   - proxied service: its backend host:port (http if a health path is set).
		//   - static service: hz's internal file server, which confirms the
		//     unprivileged child process is alive. Ping only — it host-routes,
		//     so a plain HTTP check without the right Host header would 404.
		//   - self service: hz itself; pointless to self-monitor, so skip.
		checkType := "ping"
		var target string
		switch {
		case svc.Proxy.StaticRoot != "":
			target = m.config.StaticServeAddr()
		case svc.Proxy.Backend != "":
			target = svc.Proxy.Backend
			if svc.Proxy.HealthCheck != nil && svc.Proxy.HealthCheck.Path != "" {
				checkType = "http"
				target = "http://" + svc.Proxy.Backend + svc.Proxy.HealthCheck.Path
			}
		default:
			continue
		}

		// Check if this auto-generated check was disabled
		checkName := "svc:" + svc.Name
		enabled := true
		for _, disabled := range m.config.DisabledAutoChecks {
			if disabled == checkName {
				enabled = false
				break
			}
		}

		checks = append(checks, config.ServiceCheck{
			Name:     checkName,
			Type:     checkType,
			Target:   target,
			Interval: 300,
			Enabled:  enabled,
		})
	}

	checks = append(checks, m.tlsChecks()...)

	return checks
}

// tlsChecks generates one TLS check per served domain.
//
// The backend checks above prove the application is alive; none of them touch
// the TLS edge in front of it, which is why a domain could read green while
// HAProxy served a certificate that did not carry its name. This closes that
// gap by asking the only question that settles it: complete a handshake for
// this hostname and look at what comes back.
//
// Hourly, not every five minutes. The two things it detects — a certificate
// that stops covering a name, and one running out of time — move on the scale
// of days, and the check costs a full handshake per domain.
func (m *Monitor) tlsChecks() []config.ServiceCheck {
	if !m.config.SSLEnabled {
		return nil
	}

	seen := make(map[string]bool)
	var out []config.ServiceCheck

	for _, svc := range m.config.Services {
		if svc.Proxy == nil {
			continue
		}
		for _, domain := range svc.Domains {
			domain = strings.ToLower(strings.TrimSpace(domain))
			// A wildcard is not a hostname a client can connect to; the
			// concrete names it covers are checked on their own.
			if domain == "" || seen[domain] || strings.HasPrefix(domain, "*.") {
				continue
			}
			seen[domain] = true

			name := "tls:" + domain
			enabled := true
			for _, disabled := range m.config.DisabledAutoChecks {
				if disabled == name {
					enabled = false
					break
				}
			}

			out = append(out, config.ServiceCheck{
				Name:     name,
				Type:     "tls",
				Target:   domain,
				Interval: 3600,
				Enabled:  enabled,
			})
		}
	}

	return out
}

// Start begins running health checks in the background
func (m *Monitor) Start() {
	allChecks := m.getAllChecks()

	// Initialize statuses for all checks
	m.mu.Lock()
	for _, check := range allChecks {
		interval := check.Interval
		if interval <= 0 {
			interval = 300
		}
		status := StatusPending
		if !check.Enabled {
			status = StatusDisabled
		}
		m.statuses[check.Name] = &CheckStatus{
			Name:     check.Name,
			Type:     check.Type,
			Target:   check.Target,
			Status:   status,
			Interval: interval,
			Enabled:  check.Enabled,
			AutoGen:  isAutoGen(check.Name),
		}
	}
	m.mu.Unlock()

	// Start a goroutine for each enabled check
	for _, check := range allChecks {
		if check.Enabled {
			go m.runCheck(check)
		}
	}
}

// isAutoGen reports whether a check was generated rather than declared. Both
// prefixes are reserved: a manual check may not claim one, or it would collide
// with the generated check it shadows.
func isAutoGen(name string) bool {
	return strings.HasPrefix(name, "svc:") || strings.HasPrefix(name, "tls:")
}

// Stop halts all health checks
func (m *Monitor) Stop() {
	m.cancel()
}

// GetStatuses returns all current check statuses, ordered:
// manual checks first (in config order), then auto-generated (alphabetical).
func (m *Monitor) GetStatuses() []CheckStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allChecks := m.getAllChecks()
	result := make([]CheckStatus, 0, len(allChecks))

	for _, check := range allChecks {
		if s, ok := m.statuses[check.Name]; ok {
			result = append(result, *s)
		}
	}

	return result
}

// GetStatus returns the status for a specific check
func (m *Monitor) GetStatus(name string) *CheckStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if s, ok := m.statuses[name]; ok {
		copy := *s
		return &copy
	}
	return nil
}

// GetHistory returns the check history for a specific check
func (m *Monitor) GetHistory(name string) []CheckResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	h := m.history[name]
	if h == nil {
		return []CheckResult{}
	}
	out := make([]CheckResult, len(h))
	copy(out, h)
	return out
}

// GetAllHistory returns the check history for all checks
func (m *Monitor) GetAllHistory() map[string][]CheckResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string][]CheckResult, len(m.history))
	for k, v := range m.history {
		c := make([]CheckResult, len(v))
		copy(c, v)
		out[k] = c
	}
	return out
}

// Reload updates the monitor with new configuration
func (m *Monitor) Reload(cfg *config.Config) {
	m.Stop()
	m.config = cfg
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.mu.Lock()
	m.statuses = make(map[string]*CheckStatus)
	m.history = make(map[string][]CheckResult)
	m.mu.Unlock()
	m.Start()
}

// SetCheckEnabled enables or disables a check
func (m *Monitor) SetCheckEnabled(name string, enabled bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	status, ok := m.statuses[name]
	if !ok {
		return false
	}

	status.Enabled = enabled
	if !enabled {
		status.Status = StatusDisabled
	} else if status.Status == StatusDisabled {
		status.Status = StatusPending
	}

	return true
}

// UpdateConfig updates the config pointer (for saving enabled state)
func (m *Monitor) UpdateConfig() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Update enabled state in config for non-auto-generated checks
	for i := range m.config.ServiceChecks {
		if status, ok := m.statuses[m.config.ServiceChecks[i].Name]; ok {
			m.config.ServiceChecks[i].Enabled = status.Enabled
		}
	}

	// Update disabled auto-generated checks list
	var disabledAutoChecks []string
	for name, status := range m.statuses {
		if status.AutoGen && !status.Enabled {
			disabledAutoChecks = append(disabledAutoChecks, name)
		}
	}
	m.config.DisabledAutoChecks = disabledAutoChecks
}

// runCheck runs a single check on its interval
func (m *Monitor) runCheck(check config.ServiceCheck) {
	interval := check.Interval
	if interval <= 0 {
		interval = 300
	}

	// Wait before first check to let services finish starting
	select {
	case <-time.After(10 * time.Second):
	case <-m.ctx.Done():
		return
	}
	m.executeCheck(check)

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.executeCheck(check)
		}
	}
}

// executeCheck performs a single health check
func (m *Monitor) executeCheck(check config.ServiceCheck) {
	var err error
	var newStatus string

	start := time.Now()

	switch strings.ToLower(check.Type) {
	case "ping":
		err = m.doPing(check.Target)
	case "http":
		err = m.doHTTP(check.Target)
	case "tls":
		err = m.doTLS(check.Target)
	default:
		err = fmt.Errorf("unknown check type: %s", check.Type)
	}

	latency := time.Since(start).Milliseconds()

	switch {
	case err == nil:
		newStatus = StatusOK
	case isWarning(err):
		newStatus = StatusWarning
	default:
		newStatus = StatusFailed
	}

	// Build history result
	result := CheckResult{
		Timestamp: time.Now(),
		Status:    newStatus,
		Latency:   latency,
	}
	if err != nil {
		result.Error = err.Error()
	}

	// Update status and check for state change
	m.mu.Lock()
	status := m.statuses[check.Name]
	if status == nil {
		interval := check.Interval
		if interval <= 0 {
			interval = 300
		}
		status = &CheckStatus{
			Name:     check.Name,
			Type:     check.Type,
			Target:   check.Target,
			Status:   StatusPending,
			Interval: interval,
		}
		m.statuses[check.Name] = status
	}

	previousStatus := status.Status
	status.Status = newStatus
	status.LastCheck = time.Now()
	if err != nil {
		status.LastError = err.Error()
	} else {
		status.LastError = ""
	}

	// Append to history ring buffer
	h := m.history[check.Name]
	h = append(h, result)
	if len(h) > maxHistoryPerCheck {
		h = h[len(h)-maxHistoryPerCheck:]
	}
	m.history[check.Name] = h

	m.mu.Unlock()

	// Notify on entering a bad state, once per transition.
	//
	// Warnings page too: an expiring certificate that only warns silently is
	// indistinguishable from one nobody is watching, and the whole point of the
	// state is to arrive before the outage. They go out at lower priority so
	// they read as "this needs scheduling", not "this is on fire".
	if newStatus != previousStatus && (newStatus == StatusFailed || newStatus == StatusWarning) {
		m.sendNotification(check, err, newStatus)
	}
}

// doPing performs an ICMP-like ping check (TCP connect fallback)
func (m *Monitor) doPing(target string) error {
	// Use TCP connect to port 80 as a ping substitute (ICMP requires root)
	// Try common ports
	ports := []string{"80", "443", "22"}

	for _, port := range ports {
		addr := target
		if !strings.Contains(target, ":") {
			addr = net.JoinHostPort(target, port)
		}

		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
	}

	// All ports failed, try ICMP-style check via dialer
	conn, err := net.DialTimeout("ip4:icmp", target, 5*time.Second)
	if err != nil {
		return fmt.Errorf("host unreachable: %s", target)
	}
	_ = conn.Close()
	return nil
}

// doHTTP performs an HTTP GET check
func (m *Monitor) doHTTP(target string) error {
	req, err := http.NewRequestWithContext(m.ctx, "GET", target, nil)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}

	return nil
}

// doTLS completes a real TLS handshake and reports on the certificate the
// server actually hands a client.
//
// This is the check nothing else performs. Everywhere else — the Domains page,
// `domain list`, the SSL coverage map — reads what the config says should be
// covered, so a domain reads green while HAProxy serves a default certificate
// that does not carry its name. Only a handshake against the public hostname
// can tell the difference, because hostname verification is exactly the step
// those surfaces skip.
//
// Failure vs warning splits on whether it works today: a name the certificate
// does not cover is broken for every verifying client right now, while one
// expiring on Friday is fine until Friday.
func (m *Monitor) doTLS(target string) error {
	host, port := tlsHostPort(target)
	if host == "" {
		return fmt.Errorf("invalid TLS target: %s", target)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	// ServerName drives SNI and hostname verification both: the gateway picks
	// its certificate from it, and the client then holds the server to it.
	// It stays the public hostname even when the dial is redirected below.
	conn, err := tls.DialWithDialer(dialer, "tcp", m.dialAddrFor(host, port), &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("TLS handshake failed for %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()

	chain := conn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return fmt.Errorf("no certificate presented by %s", host)
	}
	leaf := chain[0]

	left := time.Until(leaf.NotAfter)
	switch {
	case left <= 0:
		return fmt.Errorf("certificate for %s expired %s", host, leaf.NotAfter.Format(time.DateOnly))
	case left <= m.certWarningWindow():
		return Warnf("certificate for %s expires in %d day(s), on %s",
			host, int(left.Hours()/24), leaf.NotAfter.Format(time.DateOnly))
	}
	return nil
}

// dialAddrFor decides where to actually connect for a TLS check.
//
// Normally that is the hostname itself. But hz sits behind its own public IP,
// and a domain it serves resolves to exactly that address — so a check running
// on the gateway leaves the box, arrives back at its own WAN interface, and
// needs the router to hairpin it. Plenty of routers don't, and the check then
// fails with a timeout that says nothing about TLS. Detecting our own public
// IP and dialling loopback instead keeps the handshake pointed at the same
// HAProxy while removing the round trip that has nothing to do with what is
// being tested. SNI is unchanged, so HAProxy still picks the certificate by
// name and verification is still against the public hostname.
func (m *Monitor) dialAddrFor(host, port string) string {
	direct := net.JoinHostPort(host, port)
	if m.config == nil || m.config.PublicIP == "" {
		return direct
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return direct
	}
	for _, ip := range ips {
		if ip.String() == m.config.PublicIP {
			return net.JoinHostPort("127.0.0.1", port)
		}
	}
	return direct
}

// tlsHostPort splits a check target into host and port, accepting a bare
// hostname, host:port, or an https:// URL.
func tlsHostPort(target string) (host, port string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil || u.Hostname() == "" {
			return "", ""
		}
		port = u.Port()
		if port == "" {
			port = "443"
		}
		return u.Hostname(), port
	}
	if h, p, err := net.SplitHostPort(target); err == nil {
		return h, p
	}
	return target, "443"
}

// sendNotification sends an ntfy notification for a check entering a bad state
func (m *Monitor) sendNotification(check config.ServiceCheck, checkErr error, status string) {
	if m.config.NtfyURL == "" {
		return
	}

	title := fmt.Sprintf("🔴 %s is DOWN", check.Name)
	message := fmt.Sprintf("Health check failed for %s (%s)\n\nTarget: %s\nError: %s",
		check.Name, check.Type, check.Target, checkErr.Error())
	priority, tags := "high", "warning,skull"

	if status == StatusWarning {
		title = fmt.Sprintf("🟡 %s needs attention", check.Name)
		message = fmt.Sprintf("Health check degraded for %s (%s)\n\nTarget: %s\nWarning: %s",
			check.Name, check.Type, check.Target, checkErr.Error())
		priority, tags = "default", "warning"
	}

	body := bytes.NewBufferString(message)
	req, err := http.NewRequest("POST", m.config.NtfyURL, body)
	if err != nil {
		return
	}

	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)

	resp, err := m.client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// RunCheck manually runs a check and returns the result.
// Searches both manual and auto-generated checks.
func (m *Monitor) RunCheck(name string) *CheckStatus {
	m.mu.RLock()
	allChecks := m.getAllChecks()
	m.mu.RUnlock()

	var check *config.ServiceCheck
	for i := range allChecks {
		if allChecks[i].Name == name {
			check = &allChecks[i]
			break
		}
	}

	if check == nil {
		return nil
	}

	m.executeCheck(*check)
	return m.GetStatus(name)
}
