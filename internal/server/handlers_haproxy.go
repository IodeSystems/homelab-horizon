package server

import (
	"log/slog"
	"net/http"
)

func (s *Server) handleHZClientScript(w http.ResponseWriter, r *http.Request) {
	// Auth handled by backupAuthMiddleware (Bearer token or session cookie)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=hz-client")
	_, _ = w.Write([]byte(hzClientScriptContent))
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
	s.haproxy.SetBackends(s.cfg().DeriveHAProxyBackends())
}
