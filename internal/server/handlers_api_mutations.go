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
		extDNS := &config.ExternalDNS{TTL: ttl}
		if len(req.ExternalDNS.IPs) > 0 {
			extDNS.IPs = req.ExternalDNS.IPs
		} else if req.ExternalDNS.IP != "" {
			extDNS.IPs = []string{req.ExternalDNS.IP}
		}
		svc.ExternalDNS = extDNS
	}
	if req.Proxy != nil && req.Proxy.Backend != "" {
		svc.Proxy = &config.ProxyConfig{
			Backend:      req.Proxy.Backend,
			InternalOnly: req.Proxy.InternalOnly,
		}
		if req.Proxy.HealthCheck != nil && req.Proxy.HealthCheck.Path != "" {
			svc.Proxy.HealthCheck = &config.HealthCheck{Path: req.Proxy.HealthCheck.Path}
		}
		if req.Proxy.Deploy != nil && req.Proxy.Deploy.NextBackend != "" {
			svc.Proxy.Deploy = &config.DeployConfig{
				NextBackend: req.Proxy.Deploy.NextBackend,
				Token:       config.GenerateDeployToken(),
				ActiveSlot:  "a",
				Balance:     req.Proxy.Deploy.Balance,
			}
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

	var addErr error
	if err := s.updateConfig(func(cfg *config.Config) {
		addErr = cfg.AddService(svc)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if addErr != nil {
		writeJSONError(w, http.StatusBadRequest, addErr.Error())
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
	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Services {
			if cfg.Services[i].Name != req.OriginalName {
				continue
			}
			cfg.Services[i].Name = req.Name
			cfg.Services[i].Domains = domains

			// Internal DNS
			if req.InternalDNS != nil && req.InternalDNS.IP != "" {
				cfg.Services[i].InternalDNS = &config.InternalDNS{IP: req.InternalDNS.IP}
			} else {
				cfg.Services[i].InternalDNS = nil
			}

			// External DNS
			if req.ExternalDNS != nil {
				ttl := req.ExternalDNS.TTL
				if ttl <= 0 {
					ttl = 300
				}
				extDNS := &config.ExternalDNS{TTL: ttl}
				if len(req.ExternalDNS.IPs) > 0 {
					extDNS.IPs = req.ExternalDNS.IPs
				} else if req.ExternalDNS.IP != "" {
					extDNS.IPs = []string{req.ExternalDNS.IP}
				}
				cfg.Services[i].ExternalDNS = extDNS
			} else {
				cfg.Services[i].ExternalDNS = nil
			}

			// Proxy
			if req.Proxy != nil && req.Proxy.Backend != "" {
				// Preserve existing deploy config for token/activeSlot
				var existingDeploy *config.DeployConfig
				if cfg.Services[i].Proxy != nil {
					existingDeploy = cfg.Services[i].Proxy.Deploy
				}
				cfg.Services[i].Proxy = &config.ProxyConfig{
					Backend:      req.Proxy.Backend,
					InternalOnly: req.Proxy.InternalOnly,
				}
				if req.Proxy.HealthCheck != nil && req.Proxy.HealthCheck.Path != "" {
					cfg.Services[i].Proxy.HealthCheck = &config.HealthCheck{Path: req.Proxy.HealthCheck.Path}
				}
				// Handle deploy config from request
				if req.Proxy.Deploy != nil && req.Proxy.Deploy.NextBackend != "" {
					if existingDeploy != nil {
						existingDeploy.NextBackend = req.Proxy.Deploy.NextBackend
						existingDeploy.Balance = req.Proxy.Deploy.Balance
						cfg.Services[i].Proxy.Deploy = existingDeploy
					} else {
						cfg.Services[i].Proxy.Deploy = &config.DeployConfig{
							NextBackend: req.Proxy.Deploy.NextBackend,
							Token:       config.GenerateDeployToken(),
							ActiveSlot:  "a",
							Balance:     req.Proxy.Deploy.Balance,
						}
					}
				} else if existingDeploy != nil && (req.Proxy.Deploy == nil || req.Proxy.Deploy.NextBackend == "") {
					cfg.Services[i].Proxy.Deploy = nil
				}
			} else {
				cfg.Services[i].Proxy = nil
			}

			found = true
			return
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !found {
		writeJSONError(w, http.StatusNotFound, "Service not found")
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

	var removed bool
	if err := s.updateConfig(func(cfg *config.Config) {
		removed = cfg.RemoveService(req.Name)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !removed {
		writeJSONError(w, http.StatusNotFound, "Service not found")
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
	for i := range s.cfg().Services {
		for _, d := range s.cfg().Services[i].Domains {
			if d == req.Domain {
				svc = &s.cfg().Services[i]
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

	zone := s.cfg().GetZoneForDomain(req.Domain)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "No zone found for domain")
		return
	}

	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		writeJSONError(w, http.StatusBadRequest, "No DNS provider configured for zone")
		return
	}

	publicIPs := s.cfg().GetPublicIPsForService(svc)
	if len(publicIPs) == 0 {
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

	var records []dns.Record
	for _, ip := range publicIPs {
		records = append(records, dns.Record{
			Name:  req.Domain,
			Type:  "A",
			Value: ip,
			TTL:   ttl,
		})
	}

	changed, err := provider.SyncRecordSet(zone.ZoneID, records)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Sync failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DNSSyncResponse{OK: true, Changed: changed})
}

// POST /api/v1/dns/sync-all
//
// Per-instance op (registered via handlePeerInstance, exempt from
// nonPrimaryGuardMiddleware) — see plan/plan.md "External DNS". In a
// multi-peer fleet each peer must be able to publish A records without
// bouncing through the config primary. Two cases:
//
//  1. Service has explicit ExternalDNS.IPs (the round-robin HA case from
//     ce8a872): every peer publishes the same record set, last write wins,
//     converges trivially.
//  2. Service falls back to the peer's own PublicIP: each peer would
//     publish its own IP. This is only correct for single-peer deployments;
//     in a fleet, services that need HA must use case 1.
//
// Either way, the rule from the principles section ("each peer manages only
// its own external resources") is preserved — there is no cross-peer write
// path here, and a non-primary calling sync-all is doing the right thing.
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

	for _, svc := range s.cfg().Services {
		if svc.ExternalDNS == nil {
			continue
		}

		publicIPs := s.cfg().GetPublicIPsForService(&svc)
		if len(publicIPs) == 0 {
			failed++
			continue
		}

		ttl := 300
		if svc.ExternalDNS.TTL > 0 {
			ttl = svc.ExternalDNS.TTL
		}

		for _, domain := range svc.Domains {
			zone := s.cfg().GetZoneForDomain(domain)
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

			var records []dns.Record
			for _, ip := range publicIPs {
				records = append(records, dns.Record{
					Name:  domain,
					Type:  "A",
					Value: ip,
					TTL:   ttl,
				})
			}

			changed, err := provider.SyncRecordSet(zone.ZoneID, records)
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

	// Validate before mutating
	cfg := s.cfg()
	var zoneFound bool
	for i := range cfg.Zones {
		if cfg.Zones[i].Name == zoneName {
			zoneFound = true
			for _, existing := range cfg.Zones[i].SubZones {
				if existing == subZone {
					writeJSONError(w, http.StatusConflict, "Sub-zone already exists")
					return
				}
			}
			break
		}
	}
	if !zoneFound {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name == zoneName {
				cfg.Zones[i].SubZones = append(cfg.Zones[i].SubZones, subZone)
				return
			}
		}
	}); err != nil {
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

	zone := s.cfg().GetZone(req.Zone)
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

	zone := s.cfg().GetZoneForDomain(req.Domain)
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
	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name == zone.Name {
				cfg.Zones[i].SubZones = append(cfg.Zones[i].SubZones, subZone)
				return
			}
		}
	}); err != nil {
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

	zone := s.cfg().GetZoneForDomain(req.Domain)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "No zone found for domain")
		return
	}

	// Find which SubZone produces this domain
	// A SubZone expands to: "" → zone.Name, "*" → *.zone.Name, "foo" → foo.zone.Name
	subZone := ""
	subZoneIdx := -1
	for i := range s.cfg().Zones {
		if s.cfg().Zones[i].Name != zone.Name {
			continue
		}
		for j, sz := range s.cfg().Zones[i].SubZones {
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
		for _, svc := range s.cfg().Services {
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
	for _, svc := range s.cfg().Services {
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
	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name == zone.Name {
				cfg.Zones[i].SubZones = append(
					cfg.Zones[i].SubZones[:subZoneIdx],
					cfg.Zones[i].SubZones[subZoneIdx+1:]...,
				)
				return
			}
		}
	}); err != nil {
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
