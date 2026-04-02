package server

import (
	"encoding/json"
	"net/http"
	"sort"
)

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handleAPIDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	domainCount := 0
	for _, svc := range s.config.Services {
		domainCount += len(svc.Domains)
	}

	peerCount := len(s.wg.GetPeers()) - 1
	if peerCount < 0 {
		peerCount = 0
	}

	haStatus := s.haproxy.GetStatus()

	type dashboardResponse struct {
		ServiceCount   int    `json:"serviceCount"`
		DomainCount    int    `json:"domainCount"`
		ZoneCount      int    `json:"zoneCount"`
		PeerCount      int    `json:"peerCount"`
		HAProxyRunning bool   `json:"haproxyRunning"`
		SSLEnabled     bool   `json:"sslEnabled"`
		Version        string `json:"version"`
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dashboardResponse{
		ServiceCount:   len(s.config.Services),
		DomainCount:    domainCount,
		ZoneCount:      len(s.config.Zones),
		PeerCount:      peerCount,
		HAProxyRunning: haStatus.Running,
		SSLEnabled:     s.config.SSLEnabled,
		Version:        s.version,
	})
}

func (s *Server) handleAPIServices(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	type healthCheckResp struct {
		Path string `json:"path"`
	}

	type deployResp struct {
		NextBackend string `json:"nextBackend"`
		ActiveSlot  string `json:"activeSlot"`
		Balance     string `json:"balance"`
	}

	type proxyResp struct {
		Backend      string           `json:"backend"`
		HealthCheck  *healthCheckResp `json:"healthCheck,omitempty"`
		InternalOnly bool             `json:"internalOnly"`
		Deploy       *deployResp      `json:"deploy,omitempty"`
	}

	type internalDNSResp struct {
		IP string `json:"ip"`
	}

	type externalDNSResp struct {
		IP  string `json:"ip"`
		TTL int    `json:"ttl"`
	}

	type serviceResp struct {
		Name        string           `json:"name"`
		Domains     []string         `json:"domains"`
		InternalDNS *internalDNSResp `json:"internalDNS,omitempty"`
		ExternalDNS *externalDNSResp `json:"externalDNS,omitempty"`
		Proxy       *proxyResp       `json:"proxy,omitempty"`
	}

	sorted := make([]serviceResp, 0, len(s.config.Services))
	for _, svc := range s.config.Services {
		sr := serviceResp{
			Name:    svc.Name,
			Domains: svc.Domains,
		}
		if svc.InternalDNS != nil {
			sr.InternalDNS = &internalDNSResp{IP: svc.InternalDNS.IP}
		}
		if svc.ExternalDNS != nil {
			sr.ExternalDNS = &externalDNSResp{
				IP:  s.config.GetPublicIPForService(&svc),
				TTL: svc.ExternalDNS.TTL,
			}
		}
		if svc.Proxy != nil && svc.Proxy.Backend != "" {
			pr := &proxyResp{
				Backend:      svc.Proxy.Backend,
				InternalOnly: svc.Proxy.InternalOnly,
			}
			if svc.Proxy.HealthCheck != nil {
				pr.HealthCheck = &healthCheckResp{Path: svc.Proxy.HealthCheck.Path}
			}
			if svc.Proxy.Deploy != nil {
				pr.Deploy = &deployResp{
					NextBackend: svc.Proxy.Deploy.NextBackend,
					ActiveSlot:  svc.Proxy.Deploy.ActiveSlot,
					Balance:     svc.Proxy.Deploy.Balance,
				}
			}
			sr.Proxy = pr
		}
		sorted = append(sorted, sr)
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sorted)
}

func (s *Server) handleAPIDomains(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	type domainResp struct {
		Domain         string `json:"domain"`
		ZoneName       string `json:"zoneName,omitempty"`
		HasZone        bool   `json:"hasZone"`
		ServiceName    string `json:"serviceName,omitempty"`
		HasService     bool   `json:"hasService"`
		HasInternalDNS bool   `json:"hasInternalDNS"`
		InternalIP     string `json:"internalIP,omitempty"`
		HasExternalDNS bool   `json:"hasExternalDNS"`
		ExternalIP     string `json:"externalIP,omitempty"`
		HasProxy       bool   `json:"hasProxy"`
		ProxyBackend   string `json:"proxyBackend,omitempty"`
		InternalOnly   bool   `json:"internalOnly"`
		HasHealthCheck bool   `json:"hasHealthCheck"`
		HealthPath     string `json:"healthPath,omitempty"`
		HasSSLCoverage bool   `json:"hasSSLCoverage"`
		CertExists     bool   `json:"certExists"`
		CertExpiry     string `json:"certExpiry,omitempty"`
		CertDomain     string `json:"certDomain,omitempty"`
	}

	// Gather domains from services
	domainMap := make(map[string]*domainResp)

	for _, svc := range s.config.Services {
		for _, domain := range svc.Domains {
			dr := &domainResp{
				Domain:      domain,
				ServiceName: svc.Name,
				HasService:  true,
			}
			if svc.InternalDNS != nil && svc.InternalDNS.IP != "" {
				dr.HasInternalDNS = true
				dr.InternalIP = svc.InternalDNS.IP
			}
			if svc.ExternalDNS != nil {
				dr.HasExternalDNS = true
				dr.ExternalIP = s.config.GetPublicIPForService(&svc)
			}
			if svc.Proxy != nil && svc.Proxy.Backend != "" {
				dr.HasProxy = true
				dr.ProxyBackend = svc.Proxy.Backend
				dr.InternalOnly = svc.Proxy.InternalOnly
				if svc.Proxy.HealthCheck != nil {
					dr.HasHealthCheck = true
					dr.HealthPath = svc.Proxy.HealthCheck.Path
				}
			}
			domainMap[domain] = dr
		}
	}

	// Add zone-derived domains not already present
	for _, zone := range s.config.Zones {
		for _, sub := range zone.SubZones {
			var domain string
			if sub == "" {
				domain = zone.Name
			} else if sub == "*" {
				domain = "*." + zone.Name
			} else {
				domain = sub + "." + zone.Name
			}
			if _, exists := domainMap[domain]; !exists {
				domainMap[domain] = &domainResp{Domain: domain}
			}
		}
	}

	// Populate zone info
	for _, dr := range domainMap {
		zone := s.config.GetZoneForDomain(dr.Domain)
		if zone != nil {
			dr.HasZone = true
			dr.ZoneName = zone.Name
		}
	}

	// Check SSL coverage using zone SubZones patterns
	type certCache struct {
		exists bool
		expiry string
	}
	certInfoCache := make(map[string]*certCache)

	for _, zone := range s.config.Zones {
		if zone.SSL == nil || !zone.SSL.Enabled {
			continue
		}

		wildcardDomain := "*." + zone.Name
		cc := &certCache{}
		info, err := s.letsencrypt.GetCertInfoForDomain(wildcardDomain)
		if err == nil && info != nil {
			cc.exists = true
			cc.expiry = info.NotAfter
		}
		certInfoCache[zone.Name] = cc

		for _, sub := range zone.SubZones {
			var pattern string
			if sub == "" {
				pattern = zone.Name
			} else if sub == "*" {
				pattern = "*." + zone.Name
			} else {
				pattern = sub + "." + zone.Name
			}

			for _, dr := range domainMap {
				if domainMatchesPattern(dr.Domain, pattern) {
					dr.HasSSLCoverage = true
					dr.CertDomain = wildcardDomain
					if cc.exists {
						dr.CertExists = true
						dr.CertExpiry = cc.expiry
					}
				}
			}
		}
	}

	// Flatten and sort
	domains := make([]domainResp, 0, len(domainMap))
	for _, dr := range domainMap {
		domains = append(domains, *dr)
	}
	sort.Slice(domains, func(i, j int) bool {
		return domains[i].Domain < domains[j].Domain
	})

	// Summary counts
	sslCovered := 0
	withCert := 0
	withProxy := 0
	for _, d := range domains {
		if d.HasSSLCoverage {
			sslCovered++
		}
		if d.CertExists {
			withCert++
		}
		if d.HasProxy {
			withProxy++
		}
	}

	type domainsResponse struct {
		Domains        []domainResp `json:"domains"`
		TotalCount     int          `json:"totalCount"`
		SSLCoveredCount int         `json:"sslCoveredCount"`
		CertExistCount int          `json:"certExistCount"`
		ProxyCount     int          `json:"proxyCount"`
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(domainsResponse{
		Domains:         domains,
		TotalCount:      len(domains),
		SSLCoveredCount: sslCovered,
		CertExistCount:  withCert,
		ProxyCount:      withProxy,
	})
}

func (s *Server) handleAPIVPNPeers(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	s.wg.Load()
	ifaceStatus := s.wg.GetInterfaceStatus()
	configPeers := s.wg.GetPeers()

	type peerResp struct {
		Name            string `json:"name"`
		PublicKey       string `json:"publicKey"`
		AllowedIPs     string `json:"allowedIPs"`
		Endpoint       string `json:"endpoint,omitempty"`
		LatestHandshake string `json:"latestHandshake,omitempty"`
		TransferRx     string `json:"transferRx,omitempty"`
		TransferTx     string `json:"transferTx,omitempty"`
		Online         bool   `json:"online"`
		IsAdmin        bool   `json:"isAdmin"`
	}

	peers := make([]peerResp, 0, len(configPeers))
	for _, p := range configPeers {
		pr := peerResp{
			Name:       p.Name,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
		}
		if status, ok := ifaceStatus.Peers[p.PublicKey]; ok {
			pr.Endpoint = status.Endpoint
			pr.LatestHandshake = status.LatestHandshake
			pr.TransferRx = status.TransferRx
			pr.TransferTx = status.TransferTx
			pr.Online = status.LatestHandshake != ""
		}
		for _, adminName := range s.config.VPNAdmins {
			if p.Name == adminName {
				pr.IsAdmin = true
				break
			}
		}
		peers = append(peers, pr)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peers)
}

func (s *Server) handleAPIZones(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(zones)
}
