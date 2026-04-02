package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/qr"
	"homelab-horizon/internal/wireguard"
)

func (s *Server) handleAPIAddPeer(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name     string `json:"name"`
		ExtraIPs string `json:"extraIPs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}
	extraIPs := strings.TrimSpace(req.ExtraIPs)

	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	clientIP, err := s.wg.GetNextIP(s.config.VPNRange)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	allowedIPs := clientIP
	if extraIPs != "" {
		allowedIPs = clientIP + ", " + extraIPs
	}

	if err := s.wg.AddPeer(name, pubKey, allowedIPs); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.wg.Reload()

	clientConfig := wireguard.GenerateClientConfig(
		privKey,
		strings.TrimSuffix(clientIP, "/32"),
		s.config.ServerPublicKey,
		s.config.ServerEndpoint,
		s.config.DNS,
		s.config.GetAllowedIPs(),
	)

	qrCode := qr.GenerateSVG(clientConfig, 256)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":     true,
		"config": clientConfig,
		"qrCode": qrCode,
	})
}

func (s *Server) handleAPIEditPeer(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		PublicKey string `json:"publicKey"`
		Name     string `json:"name"`
		ExtraIPs string `json:"extraIPs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	pubkey := strings.TrimSpace(req.PublicKey)
	name := strings.TrimSpace(req.Name)
	if pubkey == "" {
		writeJSONError(w, http.StatusBadRequest, "Public key required")
		return
	}
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	peer := s.wg.GetPeerByPublicKey(pubkey)
	if peer == nil {
		writeJSONError(w, http.StatusNotFound, "Peer not found")
		return
	}

	// Extract primary IP (first /32) from current AllowedIPs
	currentIPs := strings.Split(peer.AllowedIPs, ",")
	var primaryIP string
	for _, ip := range currentIPs {
		ip = strings.TrimSpace(ip)
		if strings.HasSuffix(ip, "/32") {
			primaryIP = ip
			break
		}
	}
	if primaryIP == "" {
		primaryIP = strings.TrimSpace(currentIPs[0])
	}

	extraIPs := strings.TrimSpace(req.ExtraIPs)
	allowedIPs := primaryIP
	if extraIPs != "" {
		allowedIPs = primaryIP + ", " + extraIPs
	}

	if err := s.wg.UpdatePeer(pubkey, name, allowedIPs); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Update VPNAdmins if name changed and old name was admin
	if peer.Name != name {
		for i, adminName := range s.config.VPNAdmins {
			if adminName == peer.Name {
				s.config.VPNAdmins[i] = name
				config.Save(s.configPath, s.config)
				break
			}
		}
	}

	s.wg.Reload()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func (s *Server) handleAPIDeletePeer(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if req.PublicKey == "" {
		writeJSONError(w, http.StatusBadRequest, "Public key required")
		return
	}

	if err := s.wg.RemovePeer(req.PublicKey); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.wg.Reload()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func (s *Server) handleAPIToggleAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	clientName := strings.TrimSpace(req.Name)
	if clientName == "" {
		writeJSONError(w, http.StatusBadRequest, "Client name required")
		return
	}

	isCurrentlyAdmin := false
	for _, adminName := range s.config.VPNAdmins {
		if adminName == clientName {
			isCurrentlyAdmin = true
			break
		}
	}

	if isCurrentlyAdmin {
		newAdmins := make([]string, 0, len(s.config.VPNAdmins)-1)
		for _, adminName := range s.config.VPNAdmins {
			if adminName != clientName {
				newAdmins = append(newAdmins, adminName)
			}
		}
		s.config.VPNAdmins = newAdmins
	} else {
		s.config.VPNAdmins = append(s.config.VPNAdmins, clientName)
	}

	config.Save(s.configPath, s.config)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"isAdmin": !isCurrentlyAdmin,
	})
}

func (s *Server) handleAPIReloadWG(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	if err := s.wg.Reload(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Reload failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func (s *Server) handleAPIListInvites(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	tokens := s.getInvites()

	type inviteResp struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}

	invites := make([]inviteResp, 0, len(tokens))
	for _, token := range tokens {
		url := strings.TrimSuffix(s.config.KioskURL, "/") + "/invite/" + token
		invites = append(invites, inviteResp{
			Token: token,
			URL:   url,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(invites)
}

func (s *Server) handleAPICreateInvite(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	token := generateToken(32)
	if err := s.addInvite(token); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	url := strings.TrimSuffix(s.config.KioskURL, "/") + "/invite/" + token

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    true,
		"token": token,
		"url":   url,
	})
}

func (s *Server) handleAPIDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if req.Token == "" {
		writeJSONError(w, http.StatusBadRequest, "Token required")
		return
	}

	if err := s.removeInvite(req.Token); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}
