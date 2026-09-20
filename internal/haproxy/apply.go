package haproxy

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// This file is the PRIVILEGED half of the package: the whole list of things hz
// needs root on the gateway for, as far as HAProxy is concerned.
//
//   - write haproxy.cfg and the errors/ directory beside it
//   - write the MFA jail's source-IP ACL file
//   - run `haproxy -c` to validate, and `systemctl reload|restart|start haproxy`
//   - drive the admin socket to change a server's state
//
// Nothing here decides what the config should say — that is render.go, which is
// pure. When this half moves into hz-agent (plan/architecture.md, phase 4, item
// 10), this list is what moves.

// writeFileIfChanged writes data to path only when the file's contents differ,
// and reports whether it wrote.
//
// This is the package's one statement of the reload discipline. Callers reload
// HAProxy when a write reports changed, so identical contents must not report
// changed — that reloads HAProxy for nothing. Stating it here rather than at
// each call site means a new writer gets the property by using this.
//
// A file that cannot be read — missing, or unreadable — counts as changed, so
// the first write always lands. The parent directory is created if needed.
func writeFileIfChanged(path string, data []byte, perm os.FileMode) (changed bool, err error) {
	if prev, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(prev, data) {
		return false, nil
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// WriteConfig generates and writes the HAProxy configuration
func (h *HAProxy) WriteConfig(httpPort, httpsPort int, ssl *SSLConfig) error {
	config := RenderConfig(h.configInput(httpPort, httpsPort, ssl))

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
	return writeFileIfChanged(path, RenderJailACL(ips), 0644)
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
