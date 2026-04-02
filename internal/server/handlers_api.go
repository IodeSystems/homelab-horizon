package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"homelab-horizon/internal/apitypes"
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DashboardResponse{
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

	sorted := make([]apitypes.ServiceResp, 0, len(s.config.Services))
	for _, svc := range s.config.Services {
		sr := apitypes.ServiceResp{
			Name:    svc.Name,
			Domains: svc.Domains,
		}
		if svc.InternalDNS != nil {
			sr.InternalDNS = &apitypes.InternalDNSResp{IP: svc.InternalDNS.IP}
		}
		if svc.ExternalDNS != nil {
			sr.ExternalDNS = &apitypes.ExternalDNSResp{
				IP:  s.config.GetPublicIPForService(&svc),
				TTL: svc.ExternalDNS.TTL,
			}
		}
		if svc.Proxy != nil && svc.Proxy.Backend != "" {
			pr := &apitypes.ProxyResp{
				Backend:      svc.Proxy.Backend,
				InternalOnly: svc.Proxy.InternalOnly,
			}
			if svc.Proxy.HealthCheck != nil {
				pr.HealthCheck = &apitypes.HealthCheckResp{Path: svc.Proxy.HealthCheck.Path}
			}
			if svc.Proxy.Deploy != nil {
				pr.Deploy = &apitypes.DeployResp{
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

	// Gather domains from services
	domainMap := make(map[string]*apitypes.DomainResp)

	for _, svc := range s.config.Services {
		for _, domain := range svc.Domains {
			dr := &apitypes.DomainResp{
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
			dr.CanSyncDNS = dr.HasExternalDNS
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
				domainMap[domain] = &apitypes.DomainResp{Domain: domain}
			}
		}
	}

	// Populate zone info
	for _, dr := range domainMap {
		zone := s.config.GetZoneForDomain(dr.Domain)
		if zone != nil {
			dr.HasZone = true
			dr.ZoneName = zone.Name
			dr.ZoneHasSSL = zone.SSL != nil && zone.SSL.Enabled
		}
	}

	// Check SSL coverage
	for _, zone := range s.config.Zones {
		if zone.SSL == nil || !zone.SSL.Enabled {
			continue
		}
		wildcardDomain := "*." + zone.Name
		cc := struct {
			exists bool
			expiry string
		}{}
		info, err := s.letsencrypt.GetCertInfoForDomain(wildcardDomain)
		if err == nil && info != nil {
			cc.exists = true
			cc.expiry = info.NotAfter
		}

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

	// Compute action flags and SSL gaps
	var sslGaps []apitypes.SSLGapResp
	for _, dr := range domainMap {
		if dr.HasZone && !dr.HasSSLCoverage {
			subZone, display, reason := neededSubZoneForDomain(dr.Domain, dr.ZoneName)
			dr.CanEnableHTTPS = true
			dr.NeededSubZone = subZone
			dr.NeededSubZoneDisplay = display
			if dr.HasService {
				sslGaps = append(sslGaps, apitypes.SSLGapResp{
					Domain: dr.Domain, ZoneName: dr.ZoneName,
					SubZone: subZone, Display: display, Reason: reason,
				})
			}
		}
		if dr.HasSSLCoverage && !dr.CertExists {
			dr.CanRequestCert = true
		}
	}

	// Build zone SSL statuses
	var zoneStatuses []apitypes.ZoneSSLResp
	for _, zone := range s.config.Zones {
		zs := apitypes.ZoneSSLResp{
			ZoneName:          zone.Name,
			SSLEnabled:        zone.SSL != nil && zone.SSL.Enabled,
			ConfiguredDomains: []string{},
			ActualSANs:        []string{},
			MissingSANs:       []string{},
			ExtraSANs:         []string{},
		}
		for _, sub := range zone.SubZones {
			if sub == "" {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, zone.Name)
			} else if sub == "*" {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, "*."+zone.Name)
			} else {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, sub+"."+zone.Name)
			}
		}
		if zs.SSLEnabled {
			info, err := s.letsencrypt.GetCertInfoForDomain("*." + zone.Name)
			if err == nil && info != nil {
				zs.CertExists = true
				zs.CertExpiry = info.NotAfter
				zs.CertIssuer = info.Issuer
				zs.ActualSANs = info.SANs
				if zs.ActualSANs == nil {
					zs.ActualSANs = []string{}
				}
				configSet := make(map[string]bool)
				for _, d := range zs.ConfiguredDomains {
					configSet[d] = true
				}
				actualSet := make(map[string]bool)
				for _, s := range zs.ActualSANs {
					actualSet[s] = true
				}
				for _, d := range zs.ConfiguredDomains {
					if !actualSet[d] {
						zs.MissingSANs = append(zs.MissingSANs, d)
					}
				}
				for _, s := range zs.ActualSANs {
					if !configSet[s] {
						zs.ExtraSANs = append(zs.ExtraSANs, s)
					}
				}
			}
		}
		zoneStatuses = append(zoneStatuses, zs)
	}

	// Flatten, sort, count
	domains := make([]apitypes.DomainResp, 0, len(domainMap))
	for _, dr := range domainMap {
		domains = append(domains, *dr)
	}
	sort.Slice(domains, func(i, j int) bool {
		return domains[i].Domain < domains[j].Domain
	})
	if sslGaps == nil {
		sslGaps = []apitypes.SSLGapResp{}
	}

	var intDNS, extDNS, https, proxy int
	for _, d := range domains {
		if d.HasInternalDNS {
			intDNS++
		}
		if d.HasExternalDNS {
			extDNS++
		}
		if d.CertExists {
			https++
		}
		if d.HasProxy {
			proxy++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DomainsResponse{
		Domains:         domains,
		TotalCount:      len(domains),
		IntDNSCount:     intDNS,
		ExtDNSCount:     extDNS,
		HTTPSCount:      https,
		ProxyCount:      proxy,
		SSLGaps:         sslGaps,
		ZoneSSLStatuses: zoneStatuses,
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

	peers := make([]apitypes.PeerResp, 0, len(configPeers))
	for _, p := range configPeers {
		pr := apitypes.PeerResp{
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

	zones := make([]apitypes.ZoneResp, 0, len(s.config.Zones))
	for _, z := range s.config.Zones {
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(zones)
}
