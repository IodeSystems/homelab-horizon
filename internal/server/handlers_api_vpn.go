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

	clientIP, err := s.wg.GetNextIP(s.cfg().VPNRange)
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

	s.cfg().SetPeerProfile(name, profile)
	s.syncWGPeersToConfig()
	config.Save(s.configPath, s.cfg())

	s.wg.Reload()
	s.rebuildWGForwardChain()

	clientConfig := s.generateClientConfig(privKey, strings.TrimSuffix(clientIP, "/32"), profile)

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
		for i, adminName := range s.cfg().VPNAdmins {
			if adminName == peer.Name {
				s.cfg().VPNAdmins[i] = name
				break
			}
		}
		s.cfg().RenamePeerProfile(peer.Name, name)
	}

	// Update profile
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = config.ProfileLanAccess
	}
	s.cfg().SetPeerProfile(name, profile)
	s.syncWGPeersToConfig()
	config.Save(s.configPath, s.cfg())

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
		s.cfg().DeletePeerProfile(peer.Name)
	}
	s.syncWGPeersToConfig()
	config.Save(s.configPath, s.cfg())

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
	for _, adminName := range s.cfg().VPNAdmins {
		if adminName == clientName {
			isCurrentlyAdmin = true
			break
		}
	}

	if isCurrentlyAdmin {
		newAdmins := make([]string, 0, len(s.cfg().VPNAdmins)-1)
		for _, adminName := range s.cfg().VPNAdmins {
			if adminName != clientName {
				newAdmins = append(newAdmins, adminName)
			}
		}
		s.cfg().VPNAdmins = newAdmins
	} else {
		s.cfg().VPNAdmins = append(s.cfg().VPNAdmins, clientName)
	}

	config.Save(s.configPath, s.cfg())

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

	s.cfg().SetPeerProfile(name, profile)
	config.Save(s.configPath, s.cfg())
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "profile": profile})
}

// generateClientConfig produces a WG client config, using multi-site mode
// when fleet peers have VPNRange configured (site-to-site topology).
func (s *Server) generateClientConfig(clientPrivKey, clientIP, profile string) string {
	cfg := s.cfg()

	// Check if any fleet peer has VPNRange — if so, multi-site mode.
	var sites []wireguard.SitePeer
	for _, p := range cfg.Peers {
		if p.VPNRange != "" && p.ServerPublicKey != "" && p.ServerEndpoint != "" {
			sites = append(sites, wireguard.SitePeer{
				PublicKey:  p.ServerPublicKey,
				Endpoint:   p.ServerEndpoint,
				AllowedIPs: p.VPNRange,
			})
		}
	}

	if len(sites) > 0 {
		// Multi-site: add local site as a peer too.
		sites = append([]wireguard.SitePeer{{
			PublicKey:  cfg.ServerPublicKey,
			Endpoint:   cfg.ServerEndpoint,
			AllowedIPs: cfg.VPNRange,
		}}, sites...)
		return wireguard.GenerateMultiSiteClientConfig(clientPrivKey, clientIP, cfg.DNS, sites)
	}

	// Single-site: use the original generator with profile-based AllowedIPs.
	return wireguard.GenerateClientConfig(
		clientPrivKey, clientIP,
		cfg.ServerPublicKey, cfg.ServerEndpoint,
		cfg.DNS, cfg.GetAllowedIPsForProfile(profile),
	)
}

// rebuildWGForwardChain rebuilds the iptables WG-FORWARD chain based on current peers and profiles.
func (s *Server) rebuildWGForwardChain() {
	peers := s.wg.GetPeers()
	lanCIDR := config.GetLocalNetworkCIDR(config.DetectDefaultInterface())
	profiles := s.cfg().VPNProfiles
	if err := wireguard.RebuildForwardChain(peers, profiles, s.cfg().VPNRange, lanCIDR); err != nil {
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
		url := strings.TrimSuffix(s.cfg().KioskURL, "/") + "/invite/" + token
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

	url := strings.TrimSuffix(s.cfg().KioskURL, "/") + "/invite/" + token

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

	profile := s.cfg().GetPeerProfile(peer.Name)
	if profile == "" {
		profile = "lan-access"
	}

	clientConfig := s.generateClientConfig("<YOUR_PRIVATE_KEY>", primaryIP, profile)

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

	s.syncWGPeersToConfig()
	config.Save(s.configPath, s.cfg())
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

	profile := s.cfg().GetPeerProfile(peer.Name)
	if profile == "" {
		profile = "lan-access"
	}

	clientConfig := s.generateClientConfig(privKey, primaryIP, profile)

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

	shareURL := strings.TrimSuffix(s.cfg().KioskURL, "/") + "/share/" + token

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
		url := strings.TrimSuffix(s.cfg().KioskURL, "/") + "/share/" + share.Token
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
