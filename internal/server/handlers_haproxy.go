package server

import (
	"net/http"
	"strconv"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
)

// HAProxy handlers

func (s *Server) handleHAProxyStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	hapStatus := s.haproxy.GetStatus()
	sslStatus := s.letsencrypt.GetStatus()
	awsProfiles := letsencrypt.GetAWSProfiles()

	// Only check backend health if requested
	var backendStatuses []haproxy.BackendStatus
	checkHealth := r.URL.Query().Get("check") == "1"
	if checkHealth {
		backendStatuses = s.haproxy.GetBackendStatuses()
	}

	// Generate config preview
	var sslConfig *haproxy.SSLConfig
	if s.config.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.config.SSLHAProxyCertDir,
		}
	}
	configPreview := s.haproxy.GenerateConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, sslConfig)

	data := map[string]interface{}{
		"Config":          s.config,
		"HAProxyStatus":   hapStatus,
		"Backends":        s.haproxy.GetBackends(),
		"BackendStatuses": backendStatuses,
		"CheckHealth":     checkHealth,
		"HAProxyAvail":    haproxy.Available(),
		"SSLStatus":       sslStatus,
		"AWSProfiles":     awsProfiles,
		"ConfigPreview":   configPreview,
		"Message":         r.URL.Query().Get("msg"),
		"Error":           r.URL.Query().Get("err"),
		"CSRFToken":       s.getCSRFToken(r),
	}
	s.templates["haproxy"].Execute(w, data)
}

// handleHAProxyAddBackend is deprecated - use handleAddService instead
// This handler redirects to service management
func (s *Server) handleHAProxyAddBackend(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin?msg=Use+the+Add+Service+form+to+manage+HAProxy+backends", http.StatusSeeOther)
}

// handleHAProxyDeleteBackend is deprecated - use handleDeleteService instead
func (s *Server) handleHAProxyDeleteBackend(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin?msg=Use+the+Services+list+to+manage+HAProxy+backends", http.StatusSeeOther)
}

func (s *Server) handleHAProxyWriteConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	// Build SSL config from letsencrypt settings
	var sslConfig *haproxy.SSLConfig
	if s.config.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.config.SSLHAProxyCertDir,
		}
	}

	if err := s.haproxy.WriteConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, sslConfig); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/haproxy?msg=HAProxy+config+written", http.StatusSeeOther)
}

func (s *Server) handleHAProxyReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.haproxy.Reload(); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/haproxy?msg=HAProxy+reloaded", http.StatusSeeOther)
}

func (s *Server) handleHAProxyStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.haproxy.Start(); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/haproxy?msg=HAProxy+started", http.StatusSeeOther)
}

func (s *Server) handleHAProxySaveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	s.config.HAProxyEnabled = r.FormValue("haproxy_enabled") == "on"
	s.config.HAProxyConfigPath = r.FormValue("haproxy_config_path")

	httpPort, _ := strconv.Atoi(r.FormValue("haproxy_http_port"))
	httpsPort, _ := strconv.Atoi(r.FormValue("haproxy_https_port"))
	if httpPort > 0 {
		s.config.HAProxyHTTPPort = httpPort
	}
	if httpsPort > 0 {
		s.config.HAProxyHTTPSPort = httpsPort
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	// Reinitialize haproxy with new config path
	s.haproxy = haproxy.New(s.config.HAProxyConfigPath, "/run/haproxy/admin.sock")
	s.syncHAProxyBackends()

	http.Redirect(w, r, "/admin/haproxy?msg=Settings+saved", http.StatusSeeOther)
}

func (s *Server) syncHAProxyBackends() {
	// Derive HAProxy backends from services
	s.haproxy.SetBackends(s.config.DeriveHAProxyBackends())
}
