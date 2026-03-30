package server

import "net/http"

func (s *Server) handleNetworkMap(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"HostPortMap": s.config.DeriveHostPortMap(),
		"CSRFToken":   s.getCSRFToken(r),
	}
	s.templates["network"].Execute(w, data)
}
