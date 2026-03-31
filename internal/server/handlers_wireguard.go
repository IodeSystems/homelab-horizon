package server

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/qr"
	"homelab-horizon/internal/wireguard"
)

func (s *Server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin?err=Name+required", http.StatusSeeOther)
		return
	}

	extraIPs := strings.TrimSpace(r.FormValue("extra_ips"))

	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	clientIP, err := s.wg.GetNextIP(s.config.VPNRange)
	if err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// Combine client IP with extra IPs if provided
	allowedIPs := clientIP
	if extraIPs != "" {
		allowedIPs = clientIP + ", " + extraIPs
	}

	if err := s.wg.AddPeer(name, pubKey, allowedIPs); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
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

	data := map[string]interface{}{
		"Name":      name,
		"Config":    clientConfig,
		"QRCode":    template.HTML(qrCode),
		"CSRFToken": s.getCSRFToken(r),
	}
	s.templates["clientConfig"].Execute(w, data)
}

func (s *Server) handleEditClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	pubkey := strings.TrimSpace(r.FormValue("pubkey"))
	name := strings.TrimSpace(r.FormValue("name"))
	extraIPs := strings.TrimSpace(r.FormValue("extra_ips"))

	if pubkey == "" {
		http.Redirect(w, r, "/admin?err=Public+key+required", http.StatusSeeOther)
		return
	}

	if name == "" {
		http.Redirect(w, r, "/admin?err=Name+required", http.StatusSeeOther)
		return
	}

	// Get the current peer to preserve the primary IP
	peer := s.wg.GetPeerByPublicKey(pubkey)
	if peer == nil {
		http.Redirect(w, r, "/admin?err=Peer+not+found", http.StatusSeeOther)
		return
	}

	// Extract the primary IP (first /32 IP) from current AllowedIPs
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
		// Fallback to using the first IP if no /32 found
		primaryIP = strings.TrimSpace(currentIPs[0])
	}

	// Build new AllowedIPs: primary IP + extra IPs
	allowedIPs := primaryIP
	if extraIPs != "" {
		allowedIPs = primaryIP + ", " + extraIPs
	}

	if err := s.wg.UpdatePeer(pubkey, name, allowedIPs); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// Update VPNAdmins if the name changed and the old name was an admin
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
	http.Redirect(w, r, "/admin?msg=Client+updated", http.StatusSeeOther)
}

func (s *Server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	pubkey := r.FormValue("pubkey")
	if err := s.wg.RemovePeer(pubkey); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	s.wg.Reload()
	http.Redirect(w, r, "/admin?msg=Client+removed", http.StatusSeeOther)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	cfg := r.FormValue("config")
	name := r.FormValue("name")
	if name == "" {
		name = "wireguard"
	}

	filename := strings.ReplaceAll(name, " ", "-") + ".conf"

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write([]byte(cfg))
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	token := generateToken(32)
	if err := s.addInvite(token); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin?msg=Invite+created:+"+token, http.StatusSeeOther)
}

func (s *Server) handleDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	token := r.FormValue("token")
	s.removeInvite(token)
	http.Redirect(w, r, "/admin?msg=Invite+revoked", http.StatusSeeOther)
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/invite/")
	token = strings.TrimSuffix(token, "/download")

	if !s.isValidInvite(token) {
		data := map[string]interface{}{
			"Error": "Invalid or expired invite token",
			"Token": token,
		}
		s.templates["invite"].Execute(w, data)
		return
	}

	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/download") {
		s.handleDownload(w, r)
		return
	}

	if r.Method == http.MethodPost {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			data := map[string]interface{}{
				"Error": "Device name is required",
				"Token": token,
			}
			s.templates["invite"].Execute(w, data)
			return
		}

		privKey, pubKey, err := wireguard.GenerateKeyPair()
		if err != nil {
			data := map[string]interface{}{
				"Error": "Failed to generate keys: " + err.Error(),
				"Token": token,
			}
			s.templates["invite"].Execute(w, data)
			return
		}

		clientIP, err := s.wg.GetNextIP(s.config.VPNRange)
		if err != nil {
			data := map[string]interface{}{
				"Error": "No available IPs: " + err.Error(),
				"Token": token,
			}
			s.templates["invite"].Execute(w, data)
			return
		}

		if err := s.wg.AddPeer(name, pubKey, clientIP); err != nil {
			data := map[string]interface{}{
				"Error": "Failed to add peer: " + err.Error(),
				"Token": token,
			}
			s.templates["invite"].Execute(w, data)
			return
		}

		s.wg.Reload()
		s.removeInvite(token)

		clientConfig := wireguard.GenerateClientConfig(
			privKey,
			strings.TrimSuffix(clientIP, "/32"),
			s.config.ServerPublicKey,
			s.config.ServerEndpoint,
			s.config.DNS,
			s.config.GetAllowedIPs(),
		)

		qrCode := qr.GenerateSVG(clientConfig, 256)

		data := map[string]interface{}{
			"Token":  token,
			"Name":   name,
			"Config": clientConfig,
			"QRCode": template.HTML(qrCode),
		}
		s.templates["invite"].Execute(w, data)
		return
	}

	data := map[string]interface{}{
		"Token": token,
	}
	s.templates["invite"].Execute(w, data)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.wg.Reload(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("Reload failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?msg=WireGuard+reloaded", http.StatusSeeOther)
}

func (s *Server) handleInterfaceUp(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.wg.InterfaceUp(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/setup?msg=Interface+started", http.StatusSeeOther)
}

func (s *Server) handleEnableForwarding(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := wireguard.EnableIPForwarding(); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/setup?msg=IP+forwarding+enabled", http.StatusSeeOther)
}

func (s *Server) handleAddMasquerade(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := wireguard.AddMasqueradeRule(s.config.VPNRange); err != nil {
		http.Redirect(w, r, "/admin/setup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/setup?msg=Masquerade+rule+added", http.StatusSeeOther)
}

func (s *Server) handleToggleClientAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	clientName := strings.TrimSpace(r.FormValue("name"))
	if clientName == "" {
		http.Redirect(w, r, "/admin?err=Client+name+required", http.StatusSeeOther)
		return
	}

	// Check if client is currently an admin
	isCurrentlyAdmin := false
	for _, adminName := range s.config.VPNAdmins {
		if adminName == clientName {
			isCurrentlyAdmin = true
			break
		}
	}

	if isCurrentlyAdmin {
		// Remove from admins
		newAdmins := make([]string, 0, len(s.config.VPNAdmins)-1)
		for _, adminName := range s.config.VPNAdmins {
			if adminName != clientName {
				newAdmins = append(newAdmins, adminName)
			}
		}
		s.config.VPNAdmins = newAdmins
		http.Redirect(w, r, "/admin?msg=Removed+admin+access+for+"+clientName, http.StatusSeeOther)
	} else {
		// Add to admins
		s.config.VPNAdmins = append(s.config.VPNAdmins, clientName)
		http.Redirect(w, r, "/admin?msg=Granted+admin+access+to+"+clientName, http.StatusSeeOther)
	}

	config.Save(s.configPath, s.config)
}
