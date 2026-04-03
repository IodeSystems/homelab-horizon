package server

import (
	"net/http"
)

func (s *Server) handleHZClientScript(w http.ResponseWriter, r *http.Request) {
	// Auth handled by backupAuthMiddleware (Bearer token or session cookie)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=hz-client")
	w.Write([]byte(hzClientScriptContent))
}

func (s *Server) syncHAProxyBackends() {
	// Derive HAProxy backends from services
	s.haproxy.SetBackends(s.config.DeriveHAProxyBackends())
}
