package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"homelab-horizon/internal/apitypes"
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
		Profile  string `json:"profile"`
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
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = config.ProfileLanAccess
	}

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

	s.config.SetPeerProfile(name, profile)
	config.Save(s.configPath, s.config)

	s.wg.Reload()
	s.rebuildWGForwardChain()

	clientConfig := wireguard.GenerateClientConfig(
		privKey,
		strings.TrimSuffix(clientIP, "/32"),
		s.config.ServerPublicKey,
		s.config.ServerEndpoint,
		s.config.DNS,
		s.config.GetAllowedIPsForProfile(profile),
	)

	qrCode := qr.GenerateSVG(clientConfig, 256)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.AddPeerResponse{
		OK:     true,
		Config: clientConfig,
		QRCode: qrCode,
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
		Profile  string `json:"profile"`
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
				break
			}
		}
		s.config.RenamePeerProfile(peer.Name, name)
	}

	// Update profile
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = config.ProfileLanAccess
	}
	s.config.SetPeerProfile(name, profile)
	config.Save(s.configPath, s.config)

	s.wg.Reload()
	s.rebuildWGForwardChain()

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

	// Look up peer name before removing so we can clean up profile
	peer := s.wg.GetPeerByPublicKey(req.PublicKey)

	if err := s.wg.RemovePeer(req.PublicKey); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if peer != nil {
		s.config.DeletePeerProfile(peer.Name)
		config.Save(s.configPath, s.config)
	}

	s.wg.Reload()
	s.rebuildWGForwardChain()

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
	json.NewEncoder(w).Encode(apitypes.ToggleAdminResponse{
		OK:      true,
		IsAdmin: !isCurrentlyAdmin,
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

	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func (s *Server) handleAPISetPeerProfile(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name    string `json:"name"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	profile := strings.TrimSpace(req.Profile)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	switch profile {
	case config.ProfileLanAccess, config.ProfileFullTunnel, config.ProfileVPNOnly:
		// valid
	default:
		writeJSONError(w, http.StatusBadRequest, "Invalid profile: must be lan-access, full-tunnel, or vpn-only")
		return
	}

	s.config.SetPeerProfile(name, profile)
	config.Save(s.configPath, s.config)
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "profile": profile})
}

// rebuildWGForwardChain rebuilds the iptables WG-FORWARD chain based on current peers and profiles.
func (s *Server) rebuildWGForwardChain() {
	peers := s.wg.GetPeers()
	lanCIDR := config.GetLocalNetworkCIDR(config.DetectDefaultInterface())
	profiles := s.config.VPNProfiles
	if err := wireguard.RebuildForwardChain(peers, profiles, s.config.VPNRange, lanCIDR); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to rebuild WG-FORWARD chain: %v\n", err)
	}
}

func (s *Server) handleAPIListInvites(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	tokens := s.getInvites()

	invites := make([]apitypes.InviteResp, 0, len(tokens))
	for _, token := range tokens {
		url := strings.TrimSuffix(s.config.KioskURL, "/") + "/invite/" + token
		invites = append(invites, apitypes.InviteResp{
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
	json.NewEncoder(w).Encode(apitypes.CreateInviteResponse{
		OK:    true,
		Token: token,
		URL:   url,
	})
}

func (s *Server) handleAPIGetPeerConfig(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	publicKey := r.URL.Query().Get("publicKey")
	if publicKey == "" {
		writeJSONError(w, http.StatusBadRequest, "publicKey query param required")
		return
	}

	peer := s.wg.GetPeerByPublicKey(publicKey)
	if peer == nil {
		writeJSONError(w, http.StatusNotFound, "Peer not found")
		return
	}

	// Extract primary IP
	primaryIP := ""
	for _, part := range strings.Split(peer.AllowedIPs, ",") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, "/32") {
			primaryIP = strings.TrimSuffix(part, "/32")
			break
		}
	}
	if primaryIP == "" {
		primaryIP = strings.Split(strings.TrimSpace(peer.AllowedIPs), "/")[0]
	}

	profile := s.config.GetPeerProfile(peer.Name)
	if profile == "" {
		profile = "lan-access"
	}

	clientConfig := wireguard.GenerateClientConfig(
		"<YOUR_PRIVATE_KEY>",
		primaryIP,
		s.config.ServerPublicKey,
		s.config.ServerEndpoint,
		s.config.DNS,
		s.config.GetAllowedIPsForProfile(profile),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.PeerConfigResponse{
		OK:     true,
		Config: clientConfig,
	})
}

func (s *Server) handleAPIRekeyPeer(w http.ResponseWriter, r *http.Request) {
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

	peer := s.wg.GetPeerByPublicKey(req.PublicKey)
	if peer == nil {
		writeJSONError(w, http.StatusNotFound, "Peer not found")
		return
	}

	// Generate new keypair
	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Replace the public key in WG config
	if err := s.wg.ReplacePeerKey(req.PublicKey, pubKey); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.wg.Reload()

	// Extract primary IP
	primaryIP := ""
	for _, part := range strings.Split(peer.AllowedIPs, ",") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, "/32") {
			primaryIP = strings.TrimSuffix(part, "/32")
			break
		}
	}
	if primaryIP == "" {
		primaryIP = strings.Split(strings.TrimSpace(peer.AllowedIPs), "/")[0]
	}

	profile := s.config.GetPeerProfile(peer.Name)
	if profile == "" {
		profile = "lan-access"
	}

	clientConfig := wireguard.GenerateClientConfig(
		privKey,
		primaryIP,
		s.config.ServerPublicKey,
		s.config.ServerEndpoint,
		s.config.DNS,
		s.config.GetAllowedIPsForProfile(profile),
	)

	qrCode := qr.GenerateSVG(clientConfig, 256)

	// Create a config share link automatically
	token := generateToken(32)

	// Remove any existing share for this peer
	s.configSharesMu.Lock()
	for t, share := range s.configShares {
		if share.PeerName == peer.Name {
			delete(s.configShares, t)
		}
	}
	s.configShares[token] = &configShare{
		Token:    token,
		PeerName: peer.Name,
		Config:   clientConfig,
		QRCode:   qrCode,
	}
	s.configSharesMu.Unlock()

	shareURL := strings.TrimSuffix(s.config.KioskURL, "/") + "/share/" + token

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.RekeyPeerResponse{
		OK:         true,
		Config:     clientConfig,
		QRCode:     qrCode,
		ShareToken: token,
		ShareURL:   shareURL,
	})
}

func (s *Server) handleAPIListConfigShares(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	s.configSharesMu.Lock()
	shares := make([]apitypes.ConfigShareResp, 0, len(s.configShares))
	for _, share := range s.configShares {
		url := strings.TrimSuffix(s.config.KioskURL, "/") + "/share/" + share.Token
		shares = append(shares, apitypes.ConfigShareResp{
			Token:    share.Token,
			URL:      url,
			PeerName: share.PeerName,
		})
	}
	s.configSharesMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shares)
}

func (s *Server) handleAPIDeleteConfigShare(w http.ResponseWriter, r *http.Request) {
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

	s.configSharesMu.Lock()
	delete(s.configShares, req.Token)
	s.configSharesMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
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
