package server

import (
	"fmt"
	"net/http"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/dns"
)

// handleDNSDiscoverZones discovers available zones from a DNS provider
// Accepts provider type and credentials, returns available zones
func (s *Server) handleDNSDiscoverZones(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	providerType := config.DNSProviderType(r.URL.Query().Get("provider"))

	var cfg *config.DNSProviderConfig

	switch providerType {
	case config.DNSProviderRoute53:
		// Route53 uses AWS profile
		awsProfile := r.URL.Query().Get("aws_profile")
		awsAccessKeyID := r.URL.Query().Get("aws_access_key_id")
		awsSecretAccessKey := r.URL.Query().Get("aws_secret_access_key")
		awsRegion := r.URL.Query().Get("aws_region")
		if awsRegion == "" {
			awsRegion = "us-east-1"
		}

		cfg = &config.DNSProviderConfig{
			Type:               config.DNSProviderRoute53,
			AWSProfile:         awsProfile,
			AWSAccessKeyID:     awsAccessKeyID,
			AWSSecretAccessKey: awsSecretAccessKey,
			AWSRegion:          awsRegion,
		}

	case config.DNSProviderNamecom:
		username := r.URL.Query().Get("username")
		apiToken := r.URL.Query().Get("api_token")

		if username == "" || apiToken == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"username and api_token required"}`)
			return
		}

		cfg = &config.DNSProviderConfig{
			Type:            config.DNSProviderNamecom,
			NamecomUsername: username,
			NamecomAPIToken: apiToken,
		}

	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"unknown provider type: %s"}`, providerType)
		return
	}

	// Create provider and list zones
	provider, err := dns.NewProvider(cfg)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}

	zones, err := provider.ListZones()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"zones":[`)
	for i, z := range zones {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"ID":%q,"Name":%q,"DNSManaged":%t}`, z.ID, z.Name, z.DNSManaged)
	}
	fmt.Fprint(w, "]}")
}

// handleDNSSyncRecord syncs a single DNS record using the zone's configured provider
func (s *Server) handleDNSSyncRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := r.FormValue("name")

	// Find the service by domain name
	var svc *config.Service
	for i := range s.config.Services {
		if s.config.Services[i].Domain == name {
			svc = &s.config.Services[i]
			break
		}
	}

	if svc == nil || svc.ExternalDNS == nil {
		http.Redirect(w, r, "/admin/route53?err=Service+not+found+or+no+external+DNS", http.StatusSeeOther)
		return
	}

	// Get the zone for this domain
	zone := s.config.GetZoneForDomain(svc.Domain)
	if zone == nil {
		http.Redirect(w, r, "/admin/route53?err=No+zone+found+for+domain", http.StatusSeeOther)
		return
	}

	// Get DNS provider config
	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		http.Redirect(w, r, "/admin/route53?err=No+DNS+provider+configured+for+zone", http.StatusSeeOther)
		return
	}

	// Get public IP for this service
	publicIP := s.config.GetPublicIPForService(svc)
	if publicIP == "" {
		http.Redirect(w, r, "/admin/route53?err=No+public+IP+available", http.StatusSeeOther)
		return
	}

	// Create provider and sync record
	provider, err := dns.NewProvider(providerCfg)
	if err != nil {
		http.Redirect(w, r, "/admin/route53?err=Provider+error:+"+err.Error(), http.StatusSeeOther)
		return
	}

	ttl := 300
	if svc.ExternalDNS.TTL > 0 {
		ttl = svc.ExternalDNS.TTL
	}

	record := dns.Record{
		Name:  svc.Domain,
		Type:  "A",
		Value: publicIP,
		TTL:   ttl,
	}

	changed, err := provider.SyncRecord(zone.ZoneID, record)
	if err != nil {
		http.Redirect(w, r, "/admin/route53?err=Sync+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}

	if changed {
		http.Redirect(w, r, "/admin/route53?msg=Record+updated", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/admin/route53?msg=Already+up+to+date", http.StatusSeeOther)
	}
}

// handleDNSSyncAll syncs all external DNS records using each zone's configured provider
func (s *Server) handleDNSSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	var updated, failed int

	// Group services by zone
	for _, svc := range s.config.Services {
		if svc.ExternalDNS == nil {
			continue
		}

		zone := s.config.GetZoneForDomain(svc.Domain)
		if zone == nil {
			failed++
			continue
		}

		providerCfg := zone.GetDNSProvider()
		if providerCfg == nil {
			failed++
			continue
		}

		publicIP := s.config.GetPublicIPForService(&svc)
		if publicIP == "" {
			failed++
			continue
		}

		provider, err := dns.NewProvider(providerCfg)
		if err != nil {
			failed++
			continue
		}

		ttl := 300
		if svc.ExternalDNS.TTL > 0 {
			ttl = svc.ExternalDNS.TTL
		}

		record := dns.Record{
			Name:  svc.Domain,
			Type:  "A",
			Value: publicIP,
			TTL:   ttl,
		}

		changed, err := provider.SyncRecord(zone.ZoneID, record)
		if err != nil {
			failed++
		} else if changed {
			updated++
		}
	}

	msg := fmt.Sprintf("Synced:+%d+updated,+%d+failed", updated, failed)
	http.Redirect(w, r, "/admin/route53?msg="+msg, http.StatusSeeOther)
}
