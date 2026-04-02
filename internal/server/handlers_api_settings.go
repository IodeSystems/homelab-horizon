package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
)

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Zones
	type zoneResp struct {
		Name         string   `json:"name"`
		ZoneID       string   `json:"zoneId"`
		SSLEnabled   bool     `json:"sslEnabled"`
		SSLEmail     string   `json:"sslEmail,omitempty"`
		SubZones     []string `json:"subZones"`
		ProviderType string   `json:"providerType,omitempty"`
	}
	zones := make([]zoneResp, 0, len(s.config.Zones))
	for _, z := range s.config.Zones {
		zr := zoneResp{
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
	type haproxyResp struct {
		Running      bool   `json:"running"`
		ConfigExists bool   `json:"configExists"`
		Version      string `json:"version"`
		Enabled      bool   `json:"enabled"`
		HTTPPort     int    `json:"httpPort"`
		HTTPSPort    int    `json:"httpsPort"`
	}
	ha := haproxyResp{
		Running:      haStatus.Running,
		ConfigExists: haStatus.ConfigExists,
		Version:      haStatus.Version,
		Enabled:      s.config.HAProxyEnabled,
		HTTPPort:     s.config.HAProxyHTTPPort,
		HTTPSPort:    s.config.HAProxyHTTPSPort,
	}

	// SSL
	type sslResp struct {
		Enabled        bool   `json:"enabled"`
		CertDir        string `json:"certDir"`
		HAProxyCertDir string `json:"haproxyCertDir"`
	}
	ssl := sslResp{
		Enabled:        s.config.SSLEnabled,
		CertDir:        s.config.SSLCertDir,
		HAProxyCertDir: s.config.SSLHAProxyCertDir,
	}

	// Checks
	checks := s.monitor.GetStatuses()

	// Config
	vpnAdmins := s.config.VPNAdmins
	if vpnAdmins == nil {
		vpnAdmins = []string{}
	}
	type configResp struct {
		PublicIP       string   `json:"publicIP"`
		LocalInterface string   `json:"localInterface"`
		DnsmasqEnabled bool     `json:"dnsmasqEnabled"`
		VPNAdmins      []string `json:"vpnAdmins"`
	}
	cfg := configResp{
		PublicIP:       s.config.PublicIP,
		LocalInterface: s.config.LocalInterface,
		DnsmasqEnabled: s.config.DNSMasqEnabled,
		VPNAdmins:      vpnAdmins,
	}

	resp := struct {
		Zones   []zoneResp  `json:"zones"`
		HAProxy haproxyResp `json:"haproxy"`
		SSL     sslResp     `json:"ssl"`
		Checks  interface{} `json:"checks"`
		Config  configResp  `json:"config"`
	}{
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

	if err := s.config.AddZone(zone); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
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
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == originalName {
			if sslEmail != "" {
				if s.config.Zones[i].SSL == nil {
					s.config.Zones[i].SSL = &config.ZoneSSL{}
				}
				s.config.Zones[i].SSL.Enabled = true
				s.config.Zones[i].SSL.Email = sslEmail
			} else {
				s.config.Zones[i].SSL = nil
			}
			s.config.Zones[i].SubZones = subZones
			found = true
			break
		}
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
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

	if !s.config.RemoveZone(req.Name) {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
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
	if s.config.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.config.SSLHAProxyCertDir,
		}
	}

	if err := s.haproxy.WriteConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, sslConfig); err != nil {
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
	if s.config.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{
			Enabled: true,
			CertDir: s.config.SSLHAProxyCertDir,
		}
	}

	preview := s.haproxy.GenerateConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, sslConfig)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"config": preview})
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

	for _, c := range s.config.ServiceChecks {
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

	s.config.ServiceChecks = append(s.config.ServiceChecks, check)
	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.monitor.Reload(s.config)

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
	for i, c := range s.config.ServiceChecks {
		if c.Name == name {
			s.config.ServiceChecks = append(s.config.ServiceChecks[:i], s.config.ServiceChecks[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Check not found")
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.monitor.Reload(s.config)

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
	} else {
		for i := range s.config.ServiceChecks {
			if s.config.ServiceChecks[i].Name == name {
				s.config.ServiceChecks[i].Enabled = newEnabled
				break
			}
		}
	}
	config.Save(s.configPath, s.config)

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
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "status": result.Status})
}

