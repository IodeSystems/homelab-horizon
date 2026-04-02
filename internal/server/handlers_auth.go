package server

import (
	"net/http"
	"strings"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{"Error": ""}
	s.templates["login"].Execute(w, data)
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	token := strings.TrimSpace(r.FormValue("token"))

	if token == s.adminToken {
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    s.signCookie("admin"),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	if s.isValidInvite(token) {
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{"Error": "Invalid token"}
	s.templates["login"].Execute(w, data)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
