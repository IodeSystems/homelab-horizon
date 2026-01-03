package server

import (
	"fmt"
	"net/http"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/letsencrypt"
)

// Zone handlers

func (s *Server) handleAddZone(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	zoneID := strings.TrimSpace(r.FormValue("zone_id"))
	sslEmail := strings.TrimSpace(r.FormValue("ssl_email"))

	// DNS Provider configuration
	providerType := config.DNSProviderType(strings.TrimSpace(r.FormValue("dns_provider_type")))
	if providerType == "" {
		providerType = config.DNSProviderRoute53 // Default to Route53
	}

	// Build DNS provider config based on type
	var dnsProvider *config.DNSProviderConfig
	switch providerType {
	case config.DNSProviderRoute53:
		awsProfile := strings.TrimSpace(r.FormValue("aws_profile"))
		awsAccessKeyID := strings.TrimSpace(r.FormValue("aws_access_key_id"))
		awsSecretAccessKey := strings.TrimSpace(r.FormValue("aws_secret_access_key"))
		awsRegion := strings.TrimSpace(r.FormValue("aws_region"))

		dnsProvider = &config.DNSProviderConfig{
			Type:               config.DNSProviderRoute53,
			AWSProfile:         awsProfile,
			AWSAccessKeyID:     awsAccessKeyID,
			AWSSecretAccessKey: awsSecretAccessKey,
			AWSRegion:          awsRegion,
			AWSHostedZoneID:    zoneID,
		}
	case config.DNSProviderNamecom:
		namecomUsername := strings.TrimSpace(r.FormValue("namecom_username"))
		namecomAPIToken := strings.TrimSpace(r.FormValue("namecom_api_token"))

		dnsProvider = &config.DNSProviderConfig{
			Type:            config.DNSProviderNamecom,
			NamecomUsername: namecomUsername,
			NamecomAPIToken: namecomAPIToken,
		}
	default:
		http.Redirect(w, r, "/admin?err=Unknown+DNS+provider+type:+"+string(providerType), http.StatusSeeOther)
		return
	}

	if name == "" {
		http.Redirect(w, r, "/admin?err=Zone+name+required", http.StatusSeeOther)
		return
	}

	// Validate provider config
	if dnsProvider != nil {
		if err := dnsProvider.Validate(); err != nil {
			http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
			return
		}
	}

	zone := config.Zone{
		Name:        name,
		ZoneID:      zoneID,
		DNSProvider: dnsProvider,
		SubZones:    []string{}, // No default - only request explicitly configured domains
	}

	// Enable SSL if email provided
	if sslEmail != "" {
		zone.SSL = &config.ZoneSSL{
			Enabled: true,
			Email:   sslEmail,
		}
	}

	if err := s.config.AddZone(zone); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncLetsEncrypt()
	http.Redirect(w, r, "/admin?msg=Zone+added", http.StatusSeeOther)
}

func (s *Server) handleDeleteZone(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := r.FormValue("name")
	if !s.config.RemoveZone(name) {
		http.Redirect(w, r, "/admin?err=Zone+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncServices()
	http.Redirect(w, r, "/admin?msg=Zone+removed", http.StatusSeeOther)
}

func (s *Server) handleEditZone(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	originalName := strings.TrimSpace(r.FormValue("original_name"))
	sslEmail := strings.TrimSpace(r.FormValue("ssl_email"))
	subZonesStr := strings.TrimSpace(r.FormValue("sub_zones"))

	if originalName == "" {
		http.Redirect(w, r, "/admin?err=Zone+name+required", http.StatusSeeOther)
		return
	}

	// Parse sub-zones from comma-separated string
	// Empty string "" is valid and means root domain
	var subZones []string
	if subZonesStr != "" {
		for _, sz := range strings.Split(subZonesStr, ",") {
			sz = strings.TrimSpace(strings.ToLower(sz))
			// Keep empty strings (they mean root domain)
			subZones = append(subZones, sz)
		}
	}

	// Find and update the zone
	var found bool
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == originalName {
			// Update SSL settings
			if sslEmail != "" {
				if s.config.Zones[i].SSL == nil {
					s.config.Zones[i].SSL = &config.ZoneSSL{}
				}
				s.config.Zones[i].SSL.Enabled = true
				s.config.Zones[i].SSL.Email = sslEmail
			} else {
				s.config.Zones[i].SSL = nil
			}

			// Update sub-zones
			s.config.Zones[i].SubZones = subZones

			found = true
			break
		}
	}

	if !found {
		http.Redirect(w, r, "/admin?err=Zone+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin?msg=Zone+updated", http.StatusSeeOther)
}

func (s *Server) handleAddSubZone(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	zoneName := r.FormValue("zone")
	subZone := strings.ToLower(strings.TrimSpace(r.FormValue("subzone")))

	if zoneName == "" || subZone == "" {
		http.Redirect(w, r, "/admin?err=Zone+and+sub-zone+required", http.StatusSeeOther)
		return
	}

	// Find the zone and add the sub-zone
	var found bool
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == zoneName {
			// Check if sub-zone already exists
			for _, existing := range s.config.Zones[i].SubZones {
				if existing == subZone {
					http.Redirect(w, r, "/admin?err=Sub-zone+already+exists", http.StatusSeeOther)
					return
				}
			}
			s.config.Zones[i].SubZones = append(s.config.Zones[i].SubZones, subZone)
			found = true
			break
		}
	}

	if !found {
		http.Redirect(w, r, "/admin?err=Zone+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin?msg=Sub-zone+added.+Run+Sync+to+update+SSL+certificate.", http.StatusSeeOther)
}

func (s *Server) syncLetsEncrypt() {
	// Derive SSL domains from zones
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.config.DeriveSSLDomains(),
		CertDir:        s.config.SSLCertDir,
		HAProxyCertDir: s.config.SSLHAProxyCertDir,
	})
}

// handleApplyZoneTemplate applies a VPN services template to a zone
func (s *Server) handleApplyZoneTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	zoneName := strings.TrimSpace(r.FormValue("zone"))
	if zoneName == "" {
		http.Redirect(w, r, "/admin?err=Zone+name+required", http.StatusSeeOther)
		return
	}

	// Verify zone exists
	zone := s.config.GetZone(zoneName)
	if zone == nil {
		http.Redirect(w, r, "/admin?err=Zone+not+found", http.StatusSeeOther)
		return
	}

	// Get IPs for the template
	natIP := s.config.LocalInterface
	publicIP := s.config.PublicIP
	wgGatewayIP := s.config.GetWGGatewayIP()

	// Create template services
	services := []config.Service{
		{
			Name:   "vpn",
			Domain: "vpn." + zoneName,
			InternalDNS: &config.InternalDNS{
				IP: natIP,
			},
			ExternalDNS: &config.ExternalDNS{
				IP:  publicIP,
				TTL: 300,
			},
		},
		{
			Name:   "vpn-kiosk",
			Domain: "kiosk.vpn." + zoneName,
			InternalDNS: &config.InternalDNS{
				IP: natIP,
			},
			ExternalDNS: &config.ExternalDNS{
				IP:  publicIP,
				TTL: 300,
			},
			Proxy: &config.ProxyConfig{
				Backend: "localhost:8080",
				HealthCheck: &config.HealthCheck{
					Path: "/health",
				},
			},
		},
		{
			Name:   "vpn-admin",
			Domain: "admin.vpn." + zoneName,
			InternalDNS: &config.InternalDNS{
				IP: wgGatewayIP,
			},
			Proxy: &config.ProxyConfig{
				Backend: "localhost:8080",
				HealthCheck: &config.HealthCheck{
					Path: "/health",
				},
			},
		},
	}

	// Add services (skip duplicates)
	var added int
	for _, svc := range services {
		if err := s.config.AddService(svc); err == nil {
			added++
		}
	}

	// Add *.vpn subzone for SSL if not already present
	hasVPNSubZone := false
	for _, sz := range zone.SubZones {
		if sz == "*.vpn" {
			hasVPNSubZone = true
			break
		}
	}
	if !hasVPNSubZone {
		zone.SubZones = append(zone.SubZones, "*.vpn")
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncServices()
	http.Redirect(w, r, fmt.Sprintf("/admin?msg=Template+applied:+%d+services+created", added), http.StatusSeeOther)
}
