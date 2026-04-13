package server

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
)

// unixOrZero returns t.Unix() unless t is the zero value, in which case it
// returns 0 so the JSON omitempty drops the field on the wire.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

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
	for _, svc := range s.cfg().Services {
		domainCount += len(svc.Domains)
	}

	peerCount := len(s.wg.GetPeers()) - 1
	if peerCount < 0 {
		peerCount = 0
	}

	haStatus := s.haproxy.GetStatus()

	// Compute checks overview
	statuses := s.monitor.GetStatuses()
	var checksHealthy, checksFailed int
	for _, cs := range statuses {
		switch cs.Status {
		case "ok":
			checksHealthy++
		case "failed":
			checksFailed++
		}
	}

	resp := apitypes.DashboardResponse{
		ServiceCount:   len(s.cfg().Services),
		DomainCount:    domainCount,
		ZoneCount:      len(s.cfg().Zones),
		PeerCount:      peerCount,
		HAProxyRunning: haStatus.Running,
		SSLEnabled:     s.cfg().SSLEnabled,
		Version:        s.version,
		ChecksTotal:    len(statuses),
		ChecksHealthy:  checksHealthy,
		ChecksFailed:   checksFailed,
	}

	// Surface peer-sync state on non-primary instances so the dashboard can
	// show "last successful pull". Primary instances and standalone setups
	// have nothing to report. See plan/plan.md Phase 1 hardening item 5.
	if cfg := s.cfg(); cfg.PeerID != "" && !cfg.ConfigPrimary {
		if primary := cfg.PrimaryPeer(); primary != nil {
			snap := s.peerSyncSnapshot()
			resp.PeerSync = &apitypes.PeerSyncStatus{
				PrimaryID:     primary.ID,
				PullCount:     snap.PullCount,
				LastPullAt:    unixOrZero(snap.LastPullAt),
				LastSuccessAt: unixOrZero(snap.LastSuccessAt),
				LastApplyAt:   unixOrZero(snap.LastApplyAt),
				LastError:     snap.LastError,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAPIServices(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	dnsmasqAddr := s.cfg().GetWGGatewayIP() + ":53"

	// Build service list
	sorted := make([]apitypes.ServiceResp, 0, len(s.cfg().Services))
	for _, svc := range s.cfg().Services {
		sr := apitypes.ServiceResp{
			Name:    svc.Name,
			Domains: svc.Domains,
		}
		if svc.InternalDNS != nil {
			sr.InternalDNS = &apitypes.InternalDNSResp{IP: svc.InternalDNS.IP}
		}
		if svc.ExternalDNS != nil {
			ips := s.cfg().GetPublicIPsForService(&svc)
			sr.ExternalDNS = &apitypes.ExternalDNSResp{
				IP:  s.cfg().GetPublicIPForService(&svc),
				IPs: ips,
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
			if svc.Proxy.MaintenancePage != "" {
				sum := md5.Sum([]byte(svc.Proxy.MaintenancePage))
				pr.MaintenancePageMD5 = fmt.Sprintf("%x", sum)
			}
			sr.Proxy = pr
		}
		sorted = append(sorted, sr)
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	// Check live status in parallel for each service
	var wg sync.WaitGroup
	backendStatuses := s.haproxy.GetBackendStatuses()
	type backendInfo struct {
		healthy   bool
		err       string
		state     string
		nextState string
	}
	backendMap := make(map[string]backendInfo)
	for _, bs := range backendStatuses {
		backendMap[bs.Name] = backendInfo{
			healthy:   bs.Healthy,
			err:       bs.Error,
			state:     bs.CurrentState,
			nextState: bs.NextState,
		}
	}

	for i := range sorted {
		sr := &sorted[i]
		primaryDomain := ""
		if len(sr.Domains) > 0 {
			primaryDomain = sr.Domains[0]
		}
		if primaryDomain == "" || strings.HasPrefix(primaryDomain, "*.") {
			continue
		}

		wg.Add(1)
		go func(sr *apitypes.ServiceResp, domain string) {
			defer wg.Done()
			dnsmasqIP, remoteIP := resolveDomain(domain, dnsmasqAddr)
			sr.Status.InternalDNSUp = dnsmasqIP != ""
			sr.Status.InternalDNSResolved = dnsmasqIP
			sr.Status.ExternalDNSUp = remoteIP != ""
			sr.Status.ExternalDNSResolved = remoteIP
		}(sr, primaryDomain)

		// Check HAProxy backend health
		if sr.Proxy != nil {
			if bi, ok := backendMap[sr.Name]; ok {
				sr.Status.ProxyUp = bi.healthy
				sr.Status.ProxyError = bi.err
				sr.Status.ProxyState = bi.state
				sr.Status.ProxyNextState = bi.nextState
			}
		}
	}
	wg.Wait()

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

	for _, svc := range s.cfg().Services {
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
				dr.ExternalIP = s.cfg().GetPublicIPForService(&svc)
				dr.ExternalIPs = s.cfg().GetPublicIPsForService(&svc)
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
	for _, zone := range s.cfg().Zones {
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
		zone := s.cfg().GetZoneForDomain(dr.Domain)
		if zone != nil {
			dr.HasZone = true
			dr.ZoneName = zone.Name
			dr.ZoneHasSSL = zone.SSL != nil && zone.SSL.Enabled
		}
	}

	// Check SSL coverage
	// Build a map of zone → cert info using DeriveSSLDomains (which knows the actual primary domain)
	type certInfo struct {
		exists bool
		expiry string
	}
	zoneCerts := make(map[string]*certInfo)
	for _, dc := range s.cfg().DeriveSSLDomains() {
		ci := &certInfo{}
		info, err := s.letsencrypt.GetCertInfoForDomain(dc.Domain)
		if err == nil && info != nil {
			ci.exists = true
			ci.expiry = info.NotAfter
		}
		// Map zone name to cert info (strip wildcard prefix to get zone)
		zoneName := strings.TrimPrefix(dc.Domain, "*.")
		// Find the actual zone this belongs to
		if zone := s.cfg().GetZoneForDomain(zoneName); zone != nil {
			zoneCerts[zone.Name] = ci
		}
	}

	for _, zone := range s.cfg().Zones {
		if zone.SSL == nil || !zone.SSL.Enabled {
			continue
		}
		cc := zoneCerts[zone.Name]
		if cc == nil {
			cc = &certInfo{}
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
					dr.CertDomain = "*." + zone.Name
					if cc.exists {
						dr.CertExists = true
						dr.CertExpiry = cc.expiry
					}
				}
			}
		}
	}

	// Resolve DNS for service domains in parallel
	dnsmasqAddr := s.cfg().GetWGGatewayIP() + ":53"
	var wgDomains sync.WaitGroup
	for _, dr := range domainMap {
		if !dr.HasService || strings.HasPrefix(dr.Domain, "*.") {
			continue
		}
		dr := dr
		wgDomains.Add(1)
		go func() {
			defer wgDomains.Done()
			dnsmasqIP, remoteIP := resolveDomain(dr.Domain, dnsmasqAddr)
			dr.DnsmasqResolvedIP = dnsmasqIP
			dr.RemoteResolvedIP = remoteIP
			dr.DnsmasqDNSMatch = dnsmasqIP != "" && dnsmasqIP == dr.InternalIP
			dr.RemoteDNSMatch = remoteIP != "" && remoteIP == dr.ExternalIP
		}()
	}
	wgDomains.Wait()

	// Compute action flags and SSL gaps
	var sslGaps []apitypes.SSLGapResp
	for _, dr := range domainMap {
		if dr.HasZone && !dr.HasSSLCoverage {
			dr.CanEnableHTTPS = true
			dr.NeededSubZone = dr.Domain
			dr.NeededSubZoneDisplay = dr.Domain
			if dr.HasService {
				sslGaps = append(sslGaps, apitypes.SSLGapResp{
					Domain: dr.Domain, ZoneName: dr.ZoneName,
					SubZone: dr.Domain, Display: dr.Domain,
					Reason: "No SSL certificate coverage for this domain",
				})
			}
		}
		if dr.HasSSLCoverage && !dr.CertExists {
			dr.CanRequestCert = true
		}
	}

	// Compute wildcard relationships: CoveredBy, IsRedundant, AbsorbedDomains
	// For each zone, find wildcard SubZones and mark which domains they absorb
	for _, zone := range s.cfg().Zones {
		// Collect wildcard patterns on this zone
		var wildcardPatterns []string
		for _, sub := range zone.SubZones {
			if strings.HasPrefix(sub, "*") {
				if sub == "*" {
					wildcardPatterns = append(wildcardPatterns, "*."+zone.Name)
				} else {
					wildcardPatterns = append(wildcardPatterns, sub+"."+zone.Name)
				}
			}
		}

		for _, wp := range wildcardPatterns {
			wpDomain := domainMap[wp]
			if wpDomain == nil {
				continue
			}

			// Find all domains this wildcard absorbs
			var absorbed []apitypes.AbsorbedDomain
			for _, dr := range domainMap {
				if dr.Domain == wp || dr.ZoneName != zone.Name {
					continue
				}
				if domainMatchesPattern(dr.Domain, wp) {
					dr.CoveredBy = wp
					absorbed = append(absorbed, apitypes.AbsorbedDomain{
						Domain:  dr.Domain,
						Service: dr.ServiceName,
					})
				}
			}
			if len(absorbed) > 0 {
				sort.Slice(absorbed, func(i, j int) bool { return absorbed[i].Domain < absorbed[j].Domain })
				wpDomain.AbsorbedDomains = absorbed
			}

			// Mark non-wildcard SubZones redundant if covered by this wildcard
			for _, sub := range zone.SubZones {
				if strings.HasPrefix(sub, "*") || sub == "" {
					continue
				}
				expanded := sub + "." + zone.Name
				if domainMatchesPattern(expanded, wp) {
					if dr, ok := domainMap[expanded]; ok {
						dr.IsRedundant = true
					}
				}
			}
		}
	}

	// Build zone SSL statuses
	var zoneStatuses []apitypes.ZoneSSLResp
	for _, zone := range s.cfg().Zones {
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
			// Find the cert using the actual primary domain from DeriveSSLDomains
			var certLookupDomain string
			for _, dc := range s.cfg().DeriveSSLDomains() {
				dcZone := strings.TrimPrefix(dc.Domain, "*.")
				if z := s.cfg().GetZoneForDomain(dcZone); z != nil && z.Name == zone.Name {
					certLookupDomain = dc.Domain
					break
				}
			}
			if certLookupDomain == "" {
				certLookupDomain = "*." + zone.Name // fallback
			}
			info, err := s.letsencrypt.GetCertInfoForDomain(certLookupDomain)
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
			Profile:    s.cfg().GetPeerProfile(p.Name),
		}
		if status, ok := ifaceStatus.Peers[p.PublicKey]; ok {
			pr.Endpoint = status.Endpoint
			pr.LatestHandshake = status.LatestHandshake
			pr.TransferRx = status.TransferRx
			pr.TransferTx = status.TransferTx
			pr.Online = status.LatestHandshake != ""
		}
		for _, adminName := range s.cfg().VPNAdmins {
			if p.Name == adminName {
				pr.IsAdmin = true
				break
			}
		}
		// MFA status
		if s.cfg().VPNMFAEnabled {
			if s.cfg().VPNMFASecrets != nil {
				_, pr.MFAEnrolled = s.cfg().VPNMFASecrets[p.Name]
			}
			if s.cfg().VPNMFASessions != nil {
				if expiry, ok := s.cfg().VPNMFASessions[p.Name]; ok {
					pr.MFASessionActive = expiry == 0 || expiry > time.Now().Unix()
					if expiry != 0 {
						pr.MFASessionExpiry = time.Unix(expiry, 0).Format(time.RFC3339)
					}
				}
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(zones)
}

// handleAPIServiceIntegration returns the service token and integration instructions.
// GET /api/v1/services/integration?name=serviceName
func (s *Server) handleAPIServiceIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name parameter required")
		return
	}

	var svc *config.Service
	for i := range s.cfg().Services {
		if s.cfg().Services[i].Name == name {
			svc = &s.cfg().Services[i]
			break
		}
	}
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, "service not found")
		return
	}

	// Ensure token exists
	if svc.Token == "" {
		s.updateConfig(func(cfg *config.Config) {
			for i := range cfg.Services {
				if cfg.Services[i].Name == name && cfg.Services[i].Token == "" {
					cfg.Services[i].EnsureToken()
					break
				}
			}
		})
		// Re-lookup svc from the new config
		for i := range s.cfg().Services {
			if s.cfg().Services[i].Name == name {
				svc = &s.cfg().Services[i]
				break
			}
		}
	}

	// Build base URL from request
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	hasDeploy := svc.Proxy != nil && svc.Proxy.Deploy != nil

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.ServiceIntegration{
		Name:      svc.Name,
		Token:     svc.Token,
		BaseURL:   baseURL,
		HasDeploy: hasDeploy,
	})
}
