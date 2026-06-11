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
	// Derive HAProxy backends from services
	s.haproxy.SetBackends(s.cfg().DeriveHAProxyBackends())
}
