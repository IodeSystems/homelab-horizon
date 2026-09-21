package haproxy

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
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
//   - READ the certificate store, to learn which hosts HTTPS covers
//   - run `haproxy -c` to validate, and `systemctl reload|restart|start haproxy`
//   - drive the admin socket to change a server's state
//
// Nothing here decides what the config should say — that is render.go, which is
// pure. When this half moves into hz-agent (plan/architecture.md, phase 4, item
// 10), this list is what moves.
//
// The cert-store READ is on that list on purpose. It is not a write and it does
// not reload anything, but it is the one thing config generation needed root
// (or at least the certs group) for, and item 12 step 3 takes it away from hz
// web. It sits behind CertStore so the caller that loses the privilege can
// supply the facts instead of the file.

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
	// Through GenerateConfig rather than RenderConfig directly: that accessor
	// is what the preview and hz-agent's desired state are built from, and a
	// second render call here is a second answer waiting to diverge from it.
	config := h.GenerateConfig(httpPort, httpsPort, ssl)

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
		_ = os.WriteFile(dir+"/"+Error503Path, []byte(RenderError503()), 0644)
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

// ScanCertDir is the default CertStore: it reads every .pem file in certDir and
// reports each one's DNS SANs.
//
// This is the reading half of what used to be certRedirectPatterns. It returns
// facts, not patterns — turning SANs into HAProxy host matches is
// TLSAssetsFor's job, and keeping that on the pure side is what lets the
// HTTP→HTTPS redirect rules be rendered and diffed with no certificates on
// disk and no privilege to read them.
//
// A directory that cannot be read returns nothing, which the renderer reads as
// "no HTTPS frontend" — the same answer the old code gave. An entry whose leaf
// certificate cannot be parsed is still returned, with no DNSNames, so the
// filename fallback still applies.
//
// NOTE what this deliberately does NOT return: no key bytes, no certificate
// bytes, no chain. A combined PEM in this directory holds the private key
// beside the leaf, and the only thing lifted out of it is the list of
// hostnames — which is public, and is already written into haproxy.cfg.
func ScanCertDir(certDir string) []Cert {
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return nil
	}
	var certs []Cert
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		certs = append(certs, Cert{
			File:     e.Name(),
			DNSNames: certDNSNames(filepath.Join(certDir, e.Name())),
		})
	}
	return certs
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
