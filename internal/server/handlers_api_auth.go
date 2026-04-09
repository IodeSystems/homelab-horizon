package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"homelab-horizon/internal/apitypes"
)

func (s *Server) handleAPIAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	primaryID := ""
	if p := s.cfg().PrimaryPeer(); p != nil {
		primaryID = p.ID
	}

	if s.isAdmin(r) {
		method := "cookie"
		if s.isVPNAdmin(r) {
			method = "vpn"
		}
		json.NewEncoder(w).Encode(apitypes.AuthStatusResponse{
			Authenticated: true,
			Method:        method,
			PeerID:        s.cfg().PeerID,
			ConfigPrimary: s.cfg().ConfigPrimary,
			PrimaryID:     primaryID,
		})
		return
	}

	json.NewEncoder(w).Encode(apitypes.AuthStatusResponse{
		Authenticated: false,
		PeerID:        s.cfg().PeerID,
		ConfigPrimary: s.cfg().ConfigPrimary,
		PrimaryID:     primaryID,
	})
}

func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	var body apitypes.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	token := strings.TrimSpace(body.Token)

	if token == s.adminToken {
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    s.signCookie("admin"),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		return
	}

	if s.isValidInvite(token) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apitypes.LoginResponse{
			OK:       true,
			Invite:   true,
			Redirect: "/invite/" + token,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": "Invalid token"})
}

func (s *Server) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}
