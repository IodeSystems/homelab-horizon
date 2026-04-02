package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
	"homelab-horizon/internal/dns"
	"homelab-horizon/internal/letsencrypt"
)

func writeJSONOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func serviceRequestToService(req *apitypes.ServiceRequest) config.Service {
	svc := config.Service{
		Name:    req.Name,
		Domains: req.Domains,
	}
	if req.InternalDNS != nil && req.InternalDNS.IP != "" {
		svc.InternalDNS = &config.InternalDNS{IP: req.InternalDNS.IP}
	}
	if req.ExternalDNS != nil {
		ttl := req.ExternalDNS.TTL
		if ttl <= 0 {
			ttl = 300
		}
		svc.ExternalDNS = &config.ExternalDNS{IP: req.ExternalDNS.IP, TTL: ttl}
	}
	if req.Proxy != nil && req.Proxy.Backend != "" {
		svc.Proxy = &config.ProxyConfig{
			Backend:      req.Proxy.Backend,
			InternalOnly: req.Proxy.InternalOnly,
		}
		if req.Proxy.HealthCheck != nil && req.Proxy.HealthCheck.Path != "" {
			svc.Proxy.HealthCheck = &config.HealthCheck{Path: req.Proxy.HealthCheck.Path}
		}
	}
	return svc
}

// POST /api/v1/services/add
func (s *Server) handleAPIAddService(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.ServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.Name == "" || len(req.Domains) == 0 {
		writeJSONError(w, http.StatusBadRequest, "Name and domains required")
		return
	}

	// Clean domains
	var domains []string
	for _, d := range req.Domains {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, d)
		}
	}
	req.Domains = domains

	svc := serviceRequestToService(&req)

	if err := s.config.AddService(svc); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncServices()
	writeJSONOK(w)
}

// PUT /api/v1/services/edit
func (s *Server) handleAPIEditService(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}

	var req apitypes.ServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.OriginalName == "" || req.Name == "" || len(req.Domains) == 0 {
		writeJSONError(w, http.StatusBadRequest, "originalName, name, and domains required")
		return
	}

	// Clean domains
	var domains []string
	for _, d := range req.Domains {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, d)
		}
	}

	var found bool
	for i := range s.config.Services {
		if s.config.Services[i].Name == req.OriginalName {
			s.config.Services[i].Name = req.Name
			s.config.Services[i].Domains = domains

			// Internal DNS
			if req.InternalDNS != nil && req.InternalDNS.IP != "" {
				s.config.Services[i].InternalDNS = &config.InternalDNS{IP: req.InternalDNS.IP}
			} else {
				s.config.Services[i].InternalDNS = nil
			}

			// External DNS
			if req.ExternalDNS != nil {
				ttl := req.ExternalDNS.TTL
				if ttl <= 0 {
					ttl = 300
				}
				s.config.Services[i].ExternalDNS = &config.ExternalDNS{IP: req.ExternalDNS.IP, TTL: ttl}
			} else {
				s.config.Services[i].ExternalDNS = nil
			}

			// Proxy
			if req.Proxy != nil && req.Proxy.Backend != "" {
				// Preserve existing deploy config
				var existingDeploy *config.DeployConfig
				if s.config.Services[i].Proxy != nil {
					existingDeploy = s.config.Services[i].Proxy.Deploy
				}
				s.config.Services[i].Proxy = &config.ProxyConfig{
					Backend:      req.Proxy.Backend,
					InternalOnly: req.Proxy.InternalOnly,
				}
				if req.Proxy.HealthCheck != nil && req.Proxy.HealthCheck.Path != "" {
					s.config.Services[i].Proxy.HealthCheck = &config.HealthCheck{Path: req.Proxy.HealthCheck.Path}
				}
				// Preserve deploy config if it existed
				if existingDeploy != nil {
					s.config.Services[i].Proxy.Deploy = existingDeploy
				}
			} else {
				s.config.Services[i].Proxy = nil
			}

			found = true
			break
		}
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Service not found")
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncServices()
	writeJSONOK(w)
}

// DELETE /api/v1/services/delete
func (s *Server) handleAPIDeleteService(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or DELETE required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if !s.config.RemoveService(req.Name) {
		writeJSONError(w, http.StatusNotFound, "Service not found")
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncServices()
	writeJSONOK(w)
}

// POST /api/v1/dns/sync
func (s *Server) handleAPISyncDNS(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.Domain == "" {
		writeJSONError(w, http.StatusBadRequest, "domain required")
		return
	}

	// Find the service by domain name
	var svc *config.Service
	for i := range s.config.Services {
		for _, d := range s.config.Services[i].Domains {
			if d == req.Domain {
				svc = &s.config.Services[i]
				break
			}
		}
		if svc != nil {
			break
		}
	}

	if svc == nil || svc.ExternalDNS == nil {
		writeJSONError(w, http.StatusNotFound, "Service not found or no external DNS")
		return
	}

	zone := s.config.GetZoneForDomain(req.Domain)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "No zone found for domain")
		return
	}

	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		writeJSONError(w, http.StatusBadRequest, "No DNS provider configured for zone")
		return
	}

	publicIP := s.config.GetPublicIPForService(svc)
	if publicIP == "" {
		writeJSONError(w, http.StatusBadRequest, "No public IP available")
		return
	}

	provider, err := dns.NewProvider(providerCfg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Provider error: "+err.Error())
		return
	}

	ttl := 300
	if svc.ExternalDNS.TTL > 0 {
		ttl = svc.ExternalDNS.TTL
	}

	record := dns.Record{
		Name:  req.Domain,
		Type:  "A",
		Value: publicIP,
		TTL:   ttl,
	}

	changed, err := provider.SyncRecord(zone.ZoneID, record)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Sync failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DNSSyncResponse{OK: true, Changed: changed})
}

// POST /api/v1/dns/sync-all
func (s *Server) handleAPISyncAllDNS(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var updated, failed int

	for _, svc := range s.config.Services {
		if svc.ExternalDNS == nil {
			continue
		}

		publicIP := s.config.GetPublicIPForService(&svc)
		if publicIP == "" {
			failed++
			continue
		}

		ttl := 300
		if svc.ExternalDNS.TTL > 0 {
			ttl = svc.ExternalDNS.TTL
		}

		for _, domain := range svc.Domains {
			zone := s.config.GetZoneForDomain(domain)
			if zone == nil {
				failed++
				continue
			}

			providerCfg := zone.GetDNSProvider()
			if providerCfg == nil {
				failed++
				continue
			}

			provider, err := dns.NewProvider(providerCfg)
			if err != nil {
				failed++
				continue
			}

			record := dns.Record{
				Name:  domain,
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
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DNSSyncAllResponse{OK: true, Updated: updated, Failed: failed})
}

// POST /api/v1/zones/subzone
func (s *Server) handleAPIAddSubZone(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Zone    string `json:"zone"`
		SubZone string `json:"subzone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	zoneName := req.Zone
	subZone := strings.ToLower(strings.TrimSpace(req.SubZone))

	if zoneName == "" || subZone == "" {
		writeJSONError(w, http.StatusBadRequest, "zone and subzone required")
		return
	}

	var found bool
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == zoneName {
			for _, existing := range s.config.Zones[i].SubZones {
				if existing == subZone {
					writeJSONError(w, http.StatusConflict, "Sub-zone already exists")
					return
				}
			}
			s.config.Zones[i].SubZones = append(s.config.Zones[i].SubZones, subZone)
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

	writeJSONOK(w)
}

// POST /api/v1/ssl/request-cert
func (s *Server) handleAPIRequestCert(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Zone string `json:"zone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	zone := s.config.GetZone(req.Zone)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	if zone.SSL == nil || !zone.SSL.Enabled {
		writeJSONError(w, http.StatusBadRequest, "SSL not enabled for this zone")
		return
	}

	var dnsProvider *letsencrypt.DNSProviderConfig
	if providerCfg := zone.GetDNSProvider(); providerCfg != nil {
		dnsProvider = &letsencrypt.DNSProviderConfig{
			Type:               letsencrypt.DNSProviderType(providerCfg.Type),
			AWSAccessKeyID:     providerCfg.AWSAccessKeyID,
			AWSSecretAccessKey: providerCfg.AWSSecretAccessKey,
			AWSRegion:          providerCfg.AWSRegion,
			AWSHostedZoneID:    providerCfg.AWSHostedZoneID,
			AWSProfile:         providerCfg.AWSProfile,
			NamecomUsername:    providerCfg.NamecomUsername,
			NamecomAPIToken:    providerCfg.NamecomAPIToken,
		}
	}

	var extraSANs []string
	for _, subZone := range zone.SubZones {
		if subZone == "" {
			extraSANs = append(extraSANs, zone.Name)
		} else {
			extraSANs = append(extraSANs, subZone+"."+zone.Name)
		}
	}

	err := s.letsencrypt.RequestCertForDomain(letsencrypt.DomainConfig{
		Domain:      "*." + zone.Name,
		ExtraSANs:   extraSANs,
		Email:       zone.SSL.Email,
		DNSProvider: dnsProvider,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Cert request failed: %s", err.Error()))
		return
	}

	writeJSONOK(w)
}

// POST /api/v1/domains/ssl/add
func (s *Server) handleAPIDomainSSLAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.DomainSSLAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.Domain == "" {
		writeJSONError(w, http.StatusBadRequest, "domain required")
		return
	}

	zone := s.config.GetZoneForDomain(req.Domain)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "No zone found for domain")
		return
	}

	// Compute the exact SubZone for this domain (not a wildcard)
	// e.g., "alt-redline.iodesystems.com" → "alt-redline"
	// e.g., "iodesystems.com" → "" (root)
	// e.g., "*.vpn.iodesystems.com" → "*.vpn"
	var subZone string
	checkDomain := strings.TrimPrefix(req.Domain, "*.")
	if strings.HasPrefix(req.Domain, "*.") {
		// Preserve wildcard prefix for wildcard domains
		if checkDomain == zone.Name {
			subZone = "*"
		} else {
			subZone = "*." + strings.TrimSuffix(checkDomain, "."+zone.Name)
		}
	} else if checkDomain == zone.Name {
		subZone = ""
	} else {
		subZone = strings.TrimSuffix(checkDomain, "."+zone.Name)
	}

	// Check if SubZone already exists
	for _, existing := range zone.SubZones {
		if existing == subZone {
			writeJSONError(w, http.StatusConflict, "SSL coverage already exists")
			return
		}
	}

	// Add SubZone to the zone
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == zone.Name {
			s.config.Zones[i].SubZones = append(s.config.Zones[i].SubZones, subZone)
			break
		}
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncServices()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DomainSSLAddResponse{
		OK:      true,
		Zone:    zone.Name,
		SubZone: subZone,
	})
}

// POST /api/v1/domains/ssl/remove
func (s *Server) handleAPIDomainSSLRemove(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.DomainSSLRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if req.Domain == "" {
		writeJSONError(w, http.StatusBadRequest, "domain required")
		return
	}

	zone := s.config.GetZoneForDomain(req.Domain)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "No zone found for domain")
		return
	}

	// Find which SubZone produces this domain
	// A SubZone expands to: "" → zone.Name, "*" → *.zone.Name, "foo" → foo.zone.Name
	subZone := ""
	subZoneIdx := -1
	for i := range s.config.Zones {
		if s.config.Zones[i].Name != zone.Name {
			continue
		}
		for j, sz := range s.config.Zones[i].SubZones {
			var expanded string
			if sz == "" {
				expanded = zone.Name
			} else if sz == "*" {
				expanded = "*." + zone.Name
			} else {
				expanded = sz + "." + zone.Name
			}
			if expanded == req.Domain {
				subZone = sz
				subZoneIdx = j
				break
			}
		}
		break
	}

	if subZoneIdx == -1 {
		// Check if this is a service domain (not a SubZone-derived domain)
		for _, svc := range s.config.Services {
			for _, d := range svc.Domains {
				if d == req.Domain {
					writeJSONError(w, http.StatusBadRequest,
						fmt.Sprintf("Domain %s belongs to service %q — remove it from the service instead", req.Domain, svc.Name))
					return
				}
			}
		}
		writeJSONError(w, http.StatusNotFound, "No SubZone found for this domain on zone "+zone.Name)
		return
	}

	// Build the pattern this SubZone covers
	var pattern string
	if subZone == "" {
		pattern = zone.Name
	} else if subZone == "*" {
		pattern = "*." + zone.Name
	} else {
		pattern = subZone + "." + zone.Name
	}

	// Build remaining patterns (all SubZones except the one being removed)
	var remainingPatterns []string
	for _, sz := range zone.SubZones {
		if sz == subZone {
			continue
		}
		if sz == "" {
			remainingPatterns = append(remainingPatterns, zone.Name)
		} else if sz == "*" {
			remainingPatterns = append(remainingPatterns, "*."+zone.Name)
		} else {
			remainingPatterns = append(remainingPatterns, sz+"."+zone.Name)
		}
	}

	// Check if any service domains are covered ONLY by this SubZone pattern
	var dependentServices []string
	for _, svc := range s.config.Services {
		for _, domain := range svc.Domains {
			if !domainMatchesPattern(domain, pattern) {
				continue
			}
			// This service domain matches the pattern being removed.
			// Check if any remaining pattern also covers it.
			coveredByOther := false
			for _, rp := range remainingPatterns {
				if domainMatchesPattern(domain, rp) {
					coveredByOther = true
					break
				}
			}
			if !coveredByOther {
				dependentServices = append(dependentServices, svc.Name)
				break // one match per service is enough
			}
		}
	}

	if len(dependentServices) > 0 {
		writeJSONError(w, http.StatusConflict,
			fmt.Sprintf("Cannot remove: services [%s] depend on %s coverage",
				strings.Join(dependentServices, ", "), pattern))
		return
	}

	// Safe to remove: find the zone again and remove the SubZone
	for i := range s.config.Zones {
		if s.config.Zones[i].Name == zone.Name {
			s.config.Zones[i].SubZones = append(
				s.config.Zones[i].SubZones[:subZoneIdx],
				s.config.Zones[i].SubZones[subZoneIdx+1:]...,
			)
			break
		}
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncServices()
	writeJSONOK(w)
}

// POST /api/v1/services/sync
func (s *Server) handleAPITriggerSync(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	started := s.sync.Start()
	if started {
		go s.runSync()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.TriggerSyncResponse{OK: true, Started: started})
}
