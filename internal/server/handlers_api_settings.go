package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
)

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Zones
	zones := make([]apitypes.ZoneResp, 0, len(s.cfg().Zones))
	for _, z := range s.cfg().Zones {
		zr := apitypes.ZoneResp{
			Name:     z.Name,
			ZoneID:   z.ZoneID,
			SubZones: z.SubZones,
		}
		if zr.SubZones == nil {
			zr.SubZones = []string{}
		}
		if z.SSL != nil {
			zr.SSLEnabled = z.SSL.Enabled
			zr.SSLEmail = z.SSL.Email
		}
		if z.DNSProvider != nil {
			zr.ProviderType = string(z.DNSProvider.Type)
		}
		zones = append(zones, zr)
	}

	// HAProxy
	haStatus := s.haproxy.GetStatus()
	ha := apitypes.HAProxyResp{
		Running:      haStatus.Running,
		ConfigExists: haStatus.ConfigExists,
		Version:      haStatus.Version,
		Enabled:      s.cfg().HAProxyEnabled,
		HTTPPort:     s.cfg().HAProxyHTTPPort,
		HTTPSPort:    s.cfg().HAProxyHTTPSPort,
	}

	// SSL
	ssl := apitypes.SSLResp{
		Enabled:        s.cfg().SSLEnabled,
		CertDir:        s.cfg().SSLCertDir,
		HAProxyCertDir: s.cfg().SSLHAProxyCertDir,
	}

	// Checks
	monitorChecks := s.monitor.GetStatuses()
	checks := make([]apitypes.CheckStatusResp, 0, len(monitorChecks))
	for _, c := range monitorChecks {
		checks = append(checks, apitypes.CheckStatusResp{
			Name:      c.Name,
			Type:      c.Type,
			Target:    c.Target,
			Status:    c.Status,
			LastCheck: c.LastCheck,
			LastError: c.LastError,
			Interval:  c.Interval,
			Enabled:   c.Enabled,
			AutoGen:   c.AutoGen,
		})
	}

	// Config
	vpnAdmins := s.cfg().VPNAdmins
	if vpnAdmins == nil {
		vpnAdmins = []string{}
	}
	cfg := apitypes.ConfigResp{
		PublicIP:       s.cfg().PublicIP,
		LocalInterface: s.cfg().LocalInterface,
		DnsmasqEnabled: s.cfg().DNSMasqEnabled,
		VPNAdmins:      vpnAdmins,
	}

	resp := apitypes.SettingsResponse{
		Zones:   zones,
		HAProxy: ha,
		SSL:     ssl,
		Checks:  checks,
		Config:  cfg,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAPIAddZone(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name               string `json:"name"`
		ZoneID             string `json:"zoneId"`
		ProviderType       string `json:"providerType"`
		SSLEmail           string `json:"sslEmail"`
		AWSProfile         string `json:"awsProfile"`
		AWSAccessKeyID     string `json:"awsAccessKeyId"`
		AWSSecretAccessKey string `json:"awsSecretAccessKey"`
		AWSRegion          string `json:"awsRegion"`
		NamecomUsername     string `json:"namecomUsername"`
		NamecomAPIToken     string `json:"namecomApiToken"`
		CloudflareAPIToken string `json:"cloudflareApiToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Zone name required")
		return
	}

	providerType := config.DNSProviderType(strings.TrimSpace(req.ProviderType))
	if providerType == "" {
		providerType = config.DNSProviderRoute53
	}

	var dnsProvider *config.DNSProviderConfig
	switch providerType {
	case config.DNSProviderRoute53:
		dnsProvider = &config.DNSProviderConfig{
			Type:               config.DNSProviderRoute53,
			AWSProfile:         strings.TrimSpace(req.AWSProfile),
			AWSAccessKeyID:     strings.TrimSpace(req.AWSAccessKeyID),
			AWSSecretAccessKey: strings.TrimSpace(req.AWSSecretAccessKey),
			AWSRegion:          strings.TrimSpace(req.AWSRegion),
			AWSHostedZoneID:    strings.TrimSpace(req.ZoneID),
		}
	case config.DNSProviderNamecom:
		dnsProvider = &config.DNSProviderConfig{
			Type:            config.DNSProviderNamecom,
			NamecomUsername: strings.TrimSpace(req.NamecomUsername),
			NamecomAPIToken: strings.TrimSpace(req.NamecomAPIToken),
		}
	case config.DNSProviderCloudflare:
		dnsProvider = &config.DNSProviderConfig{
			Type:               config.DNSProviderCloudflare,
			CloudflareAPIToken: strings.TrimSpace(req.CloudflareAPIToken),
			CloudflareZoneID:   strings.TrimSpace(req.ZoneID),
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "Unknown DNS provider type: "+string(providerType))
		return
	}

	if err := dnsProvider.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	zone := config.Zone{
		Name:        name,
		ZoneID:      strings.TrimSpace(req.ZoneID),
		DNSProvider: dnsProvider,
		SubZones:    []string{},
	}

	if sslEmail := strings.TrimSpace(req.SSLEmail); sslEmail != "" {
		zone.SSL = &config.ZoneSSL{
			Enabled: true,
			Email:   sslEmail,
		}
	}

	var addErr error
	if err := s.updateConfig(func(cfg *config.Config) {
		addErr = cfg.AddZone(zone)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if addErr != nil {
		writeJSONError(w, http.StatusBadRequest, addErr.Error())
		return
	}

	s.syncLetsEncrypt()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIEditZone(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		OriginalName string `json:"originalName"`
		SSLEmail     string `json:"sslEmail"`
		SubZones     string `json:"subZones"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	originalName := strings.TrimSpace(req.OriginalName)
	if originalName == "" {
		writeJSONError(w, http.StatusBadRequest, "Zone name required")
		return
	}

	var subZones []string
	subZonesStr := strings.TrimSpace(req.SubZones)
	if subZonesStr != "" {
		for _, sz := range strings.Split(subZonesStr, ",") {
			sz = strings.TrimSpace(strings.ToLower(sz))
			subZones = append(subZones, sz)
		}
	}

	sslEmail := strings.TrimSpace(req.SSLEmail)

	var found bool
	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name == originalName {
				if sslEmail != "" {
					if cfg.Zones[i].SSL == nil {
						cfg.Zones[i].SSL = &config.ZoneSSL{}
					}
					cfg.Zones[i].SSL.Enabled = true
					cfg.Zones[i].SSL.Email = sslEmail
				} else {
					cfg.Zones[i].SSL = nil
				}
				cfg.Zones[i].SubZones = subZones
				found = true
				break
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIDeleteZone(w http.ResponseWriter, r *http.Request) {
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

	removed := false
	if err := s.updateConfig(func(cfg *config.Config) {
		removed = cfg.RemoveZone(req.Name)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !removed {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	s.syncServices()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIHAProxyWriteConfig(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var sslConfig *haproxy.SSLConfig
	if s.cfg().SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.cfg().SSLHAProxyCertDir,
		}
	}

	if err := s.haproxy.WriteConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, sslConfig); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIHAProxyReload(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	if err := s.haproxy.Reload(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIHAProxyConfigPreview(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var sslConfig *haproxy.SSLConfig
	if s.cfg().SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.cfg().SSLHAProxyCertDir,
		}
	}

	preview := s.haproxy.GenerateConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, sslConfig)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.HAProxyConfigPreview{Config: preview})
}

func (s *Server) handleAPIChecks(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	monitorChecks := s.monitor.GetStatuses()
	checks := make([]apitypes.CheckStatusResp, 0, len(monitorChecks))
	for _, c := range monitorChecks {
		checks = append(checks, apitypes.CheckStatusResp{
			Name:      c.Name,
			Type:      c.Type,
			Target:    c.Target,
			Status:    c.Status,
			LastCheck: c.LastCheck,
			LastError: c.LastError,
			Interval:  c.Interval,
			Enabled:   c.Enabled,
			AutoGen:   c.AutoGen,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checks)
}

func (s *Server) handleAPICheckHistory(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name parameter required")
		return
	}

	history := s.monitor.GetHistory(name)
	results := make([]apitypes.CheckResult, 0, len(history))
	for _, h := range history {
		results = append(results, apitypes.CheckResult{
			Timestamp: h.Timestamp,
			Status:    h.Status,
			Latency:   h.Latency,
			Error:     h.Error,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.CheckHistoryResponse{
		Name:    name,
		Results: results,
	})
}

func (s *Server) handleAPIAddCheck(w http.ResponseWriter, r *http.Request) {
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
		Type     string `json:"type"`
		Target   string `json:"target"`
		Interval int    `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	checkType := strings.TrimSpace(req.Type)
	target := strings.TrimSpace(req.Target)

	if name == "" || checkType == "" || target == "" {
		writeJSONError(w, http.StatusBadRequest, "Name, type, and target required")
		return
	}

	for _, c := range s.cfg().ServiceChecks {
		if c.Name == name {
			writeJSONError(w, http.StatusBadRequest, "Check with this name already exists")
			return
		}
	}

	interval := req.Interval
	if interval <= 0 {
		interval = 300
	}

	check := config.ServiceCheck{
		Name:     name,
		Type:     checkType,
		Target:   target,
		Interval: interval,
		Enabled:  true,
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.ServiceChecks = append(cfg.ServiceChecks, check)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.monitor.Reload(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIDeleteCheck(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	found := false
	if err := s.updateConfig(func(cfg *config.Config) {
		for i, c := range cfg.ServiceChecks {
			if c.Name == name {
				cfg.ServiceChecks = append(cfg.ServiceChecks[:i], cfg.ServiceChecks[i+1:]...)
				found = true
				break
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Check not found")
		return
	}

	s.monitor.Reload(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIToggleCheck(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	status := s.monitor.GetStatus(name)
	if status == nil {
		writeJSONError(w, http.StatusNotFound, "Check not found")
		return
	}

	newEnabled := !status.Enabled
	s.monitor.SetCheckEnabled(name, newEnabled)

	if status.AutoGen {
		s.monitor.UpdateConfig()
	}
	if err := s.updateConfig(func(cfg *config.Config) {
		if !status.AutoGen {
			for i := range cfg.ServiceChecks {
				if cfg.ServiceChecks[i].Name == name {
					cfg.ServiceChecks[i].Enabled = newEnabled
					break
				}
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleAPIRunCheck(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	result := s.monitor.RunCheck(name)
	if result == nil {
		writeJSONError(w, http.StatusNotFound, "Check not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.RunCheckResponse{OK: true, Status: result.Status})
}

