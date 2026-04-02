package monitor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/config"
)

// CheckStatus represents the current status of a service check
type CheckStatus struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	Status    string    `json:"status"` // "ok", "failed", "pending", "disabled"
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error,omitempty"`
	Interval  int       `json:"interval"`
	Enabled   bool      `json:"enabled"`
	AutoGen   bool      `json:"auto_gen"` // True if auto-generated from HAProxy service
}

// CheckResult records a single check execution for history tracking
type CheckResult struct {
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`          // "ok", "failed"
	Latency   int64     `json:"latency"`         // milliseconds
	Error     string    `json:"error,omitempty"`
}

const maxHistoryPerCheck = 100

// Monitor manages service health checks and notifications
type Monitor struct {
	mu       sync.RWMutex
	config   *config.Config
	statuses map[string]*CheckStatus   // keyed by check name
	history  map[string][]CheckResult   // keyed by check name, ring buffer of last 100 results
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
		if svc.Proxy == nil || svc.Proxy.Backend == "" {
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

		// Create auto-generated check
		checkType := "ping"
		target := svc.Proxy.Backend

		// If service has a health check path, use HTTP check
		if svc.Proxy.HealthCheck != nil && svc.Proxy.HealthCheck.Path != "" {
			checkType = "http"
			// Build HTTP URL from backend
			target = "http://" + svc.Proxy.Backend + svc.Proxy.HealthCheck.Path
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

	return checks
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
		status := "pending"
		if !check.Enabled {
			status = "disabled"
		}
		m.statuses[check.Name] = &CheckStatus{
			Name:     check.Name,
			Type:     check.Type,
			Target:   check.Target,
			Status:   status,
			Interval: interval,
			Enabled:  check.Enabled,
			AutoGen:  strings.HasPrefix(check.Name, "svc:"),
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
		status.Status = "disabled"
	} else if status.Status == "disabled" {
		status.Status = "pending"
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
	default:
		err = fmt.Errorf("unknown check type: %s", check.Type)
	}

	latency := time.Since(start).Milliseconds()

	if err != nil {
		newStatus = "failed"
	} else {
		newStatus = "ok"
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
			Status:   "pending",
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

	// Send notification if transitioning to failed
	if newStatus == "failed" && previousStatus != "failed" {
		m.sendNotification(check, err)
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
			conn.Close()
			return nil
		}
	}

	// All ports failed, try ICMP-style check via dialer
	conn, err := net.DialTimeout("ip4:icmp", target, 5*time.Second)
	if err != nil {
		return fmt.Errorf("host unreachable: %s", target)
	}
	conn.Close()
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
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}

	return nil
}

// sendNotification sends an ntfy notification for a failed check
func (m *Monitor) sendNotification(check config.ServiceCheck, checkErr error) {
	if m.config.NtfyURL == "" {
		return
	}

	title := fmt.Sprintf("🔴 %s is DOWN", check.Name)
	message := fmt.Sprintf("Health check failed for %s (%s)\n\nTarget: %s\nError: %s",
		check.Name, check.Type, check.Target, checkErr.Error())

	body := bytes.NewBufferString(message)
	req, err := http.NewRequest("POST", m.config.NtfyURL, body)
	if err != nil {
		return
	}

	req.Header.Set("Title", title)
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "warning,skull")

	resp, err := m.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
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
