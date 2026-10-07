package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

func (s *Server) handleHZClientScript(w http.ResponseWriter, r *http.Request) {
	// Auth handled by backupAuthMiddleware (Bearer token or session cookie)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=hz-client")
	_, _ = w.Write([]byte(hzClientScriptContent))
}

// haproxySideFiles is every file a rendered haproxy.cfg may name that hz
// writes: the MFA jail list and the per-service maintenance pages. WriteConfig
// asks for it at write time and writes these BEFORE the config, on every path
// that renders HAProxy (full sync, start-sync, service edits, the deploy swap,
// the MFA and rate-limit paths), because HAProxy refuses a config whose
// referenced file is missing.
//
// The jail list is listed whenever there is a path for it, MFA on or off: the
// list must exist and be current, never stale (syncMFAJailACL's rule). An
// empty jail renders a header-only file, never an absent one.
func haproxySideFiles(cfg *config.Config) []haproxy.SideFile {
	var out []haproxy.SideFile
	if p := cfg.MFAJailACLPath(); p != "" {
		out = append(out, haproxy.SideFile{Path: p, Contents: haproxy.RenderJailACL(cfg.JailedPeerIPs())})
	}
	errorsDir := cfg.HAProxyErrorsDir()
	for _, page := range cfg.MaintenancePages() {
		out = append(out, haproxy.SideFile{Path: filepath.Join(errorsDir, page.Name), Contents: []byte(page.Contents)})
	}
	return out
}

func (s *Server) syncHAProxyBackends() {
	// Write per-service 503 maintenance pages (no-op if none configured)
	if err := s.cfg().WriteMaintenancePageFiles(); err != nil {
		slog.Warn("WriteMaintenancePageFiles", "err", err)
	}
	// Refresh the host->root map for static-folder services so the internal
	// file server reflects the current config alongside the HAProxy backends
	// that route to it.
	if s.static != nil {
		s.static.Rebuild(s.cfg())
	}
	// Derive HAProxy backends from services
	backends := s.cfg().DeriveHAProxyBackends()
	s.haproxy.SetBackends(backends)
	s.haproxy.SetMFAJail(mfaJailFor(s.cfg(), backends))
	s.haproxy.SetRateLimit(rateLimitFor(s.cfg()))
	s.haproxy.SetMetricsPort(s.cfg().HAProxyMetricsPort)
	s.haproxy.SetTLSMinVersion(s.cfg().TLSMinVersion())
}

// kioskUIURL is hz's UI on the kiosk name (scheme://<kiosk host>/app/), or ""
// with no usable kiosk_url. The MFA portal links it once a peer is through.
func kioskUIURL(cfg *config.Config) string {
	raw := strings.TrimSpace(cfg.KioskURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/app/"
}

// haproxySideFilesNow is the provider New hands HAProxy.SetSideFiles: the side
// files of the LIVE config at the moment of the write.
func (s *Server) haproxySideFilesNow() []haproxy.SideFile {
	return haproxySideFiles(s.cfg())
}
