package server

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DomainAnalysis holds the unified status for a single domain
type DomainAnalysis struct {
	Domain   string
	DomainID string // dots replaced with dashes for HTML IDs

	// Zone
	ZoneName   string
	ZoneHasSSL bool
	HasZone    bool

	// Service
	ServiceName string
	HasService  bool

	// Internal DNS
	HasInternalDNS bool
	InternalIP     string

	// External DNS
	HasExternalDNS bool
	ExternalIP     string // configured target IP

	// DNS Resolution (actual lookups)
	DnsmasqResolvedIP string // resolved via dnsmasq on WG interface
	RemoteResolvedIP  string // resolved via 1.1.1.1
	DnsmasqDNSMatch   bool   // dnsmasq result matches configured internal IP
	RemoteDNSMatch    bool   // remote matches configured external IP

	// Proxy
	HasProxy       bool
	ProxyBackend   string
	InternalOnly   bool
	HasHealthCheck bool
	HealthPath     string

	// SSL
	HasSSLCoverage bool   // domain is covered by a zone wildcard/SAN pattern
	CertExists     bool   // actual cert file found on disk
	CertExpiry     string // expiry date
	CertDomain     string // the wildcard domain that covers this

	// Actionable flags
	CanEnableInternalDNS bool
	CanEnableExternalDNS bool
	CanEnableHTTPS       bool
	NeededSubZone        string // the SubZone value needed to cover this domain
	NeededSubZoneDisplay string // human-readable form (e.g., "*.vpn.example.com")
	CanRequestCert       bool
	CanSyncDNS           bool
}

// SSLGap describes a wildcard coverage gap for display
type SSLGap struct {
	Domain   string // the uncovered domain
	ZoneName string
	SubZone  string // the SubZone value to add
	Display  string // e.g., "*.vpn.example.com"
	Reason   string // explanation of why it's not covered
}

// ZoneSSLStatus shows the configured vs actual SSL state for a zone
type ZoneSSLStatus struct {
	ZoneName          string
	SSLEnabled        bool
	ConfiguredDomains []string // SubZones expanded to FQDNs (what user asked for)
	ActualSANs        []string // SANs from the cert on disk (what's actually there)
	CertExists        bool
	CertExpiry        string
	CertIssuer        string
	MissingSANs       []string // configured but not on cert
	ExtraSANs         []string // on cert but not configured
}

func (s *Server) handleDomainAnalysis(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Collect all domains from services
	domainMap := make(map[string]*DomainAnalysis)

	for _, svc := range s.config.Services {
		for _, domain := range svc.Domains {
			da := &DomainAnalysis{
				Domain:      domain,
				DomainID:    strings.ReplaceAll(strings.ReplaceAll(domain, ".", "-"), "*", "wc"),
				ServiceName: svc.Name,
				HasService:  true,
			}

			// Internal DNS
			if svc.InternalDNS != nil && svc.InternalDNS.IP != "" {
				da.HasInternalDNS = true
				da.InternalIP = svc.InternalDNS.IP
			}

			// External DNS
			if svc.ExternalDNS != nil {
				da.HasExternalDNS = true
				da.ExternalIP = s.config.GetPublicIPForService(&svc)
			}

			// Proxy
			if svc.Proxy != nil && svc.Proxy.Backend != "" {
				da.HasProxy = true
				da.ProxyBackend = svc.Proxy.Backend
				da.InternalOnly = svc.Proxy.InternalOnly
				if svc.Proxy.HealthCheck != nil {
					da.HasHealthCheck = true
					da.HealthPath = svc.Proxy.HealthCheck.Path
				}
			}

			// Action flags
			da.CanEnableInternalDNS = da.HasService && !da.HasInternalDNS
			da.CanEnableExternalDNS = da.HasService && !da.HasExternalDNS
			da.CanSyncDNS = da.HasExternalDNS

			domainMap[domain] = da
		}
	}

	// Also add zone-derived domains that aren't already in the map
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
				domainMap[domain] = &DomainAnalysis{
					Domain:   domain,
					DomainID: strings.ReplaceAll(strings.ReplaceAll(domain, ".", "-"), "*", "wc"),
				}
			}
		}
	}

	// Populate zone info for all domains
	for _, da := range domainMap {
		zone := s.config.GetZoneForDomain(da.Domain)
		if zone != nil {
			da.HasZone = true
			da.ZoneName = zone.Name
			da.ZoneHasSSL = zone.SSL != nil && zone.SSL.Enabled
		}
	}

	// Check SSL coverage: expand zone SubZones into patterns and match
	// Also cache cert info per zone to avoid repeated filesystem lookups
	type certCache struct {
		exists bool
		expiry string
	}
	certInfoCache := make(map[string]*certCache)

	for _, zone := range s.config.Zones {
		if zone.SSL == nil || !zone.SSL.Enabled {
			continue
		}

		// Check if cert exists for this zone (wildcard cert)
		wildcardDomain := "*." + zone.Name
		cc := &certCache{}
		info, err := s.letsencrypt.GetCertInfoForDomain(wildcardDomain)
		if err == nil && info != nil {
			cc.exists = true
			cc.expiry = info.NotAfter
		}
		certInfoCache[zone.Name] = cc

		// Build coverage patterns from SubZones
		for _, sub := range zone.SubZones {
			var pattern string
			if sub == "" {
				pattern = zone.Name
			} else if sub == "*" {
				pattern = "*." + zone.Name
			} else {
				pattern = sub + "." + zone.Name
			}

			// Match pattern against all domains
			for _, da := range domainMap {
				if domainMatchesPattern(da.Domain, pattern) {
					da.HasSSLCoverage = true
					da.CertDomain = wildcardDomain
					if cc.exists {
						da.CertExists = true
						da.CertExpiry = cc.expiry
					}
				}
			}
		}
	}

	// Build zone SSL status overview
	var zoneSSLStatuses []ZoneSSLStatus
	for _, zone := range s.config.Zones {
		zs := ZoneSSLStatus{
			ZoneName:   zone.Name,
			SSLEnabled: zone.SSL != nil && zone.SSL.Enabled,
		}

		// Expand SubZones to FQDNs
		for _, sub := range zone.SubZones {
			if sub == "" {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, zone.Name)
			} else if sub == "*" {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, "*."+zone.Name)
			} else {
				zs.ConfiguredDomains = append(zs.ConfiguredDomains, sub+"."+zone.Name)
			}
		}

		// Get actual cert info
		if zs.SSLEnabled {
			wildcardDomain := "*." + zone.Name
			info, err := s.letsencrypt.GetCertInfoForDomain(wildcardDomain)
			if err == nil && info != nil {
				zs.CertExists = true
				zs.CertExpiry = info.NotAfter
				zs.CertIssuer = info.Issuer
				zs.ActualSANs = info.SANs

				// Compare configured vs actual
				configuredSet := make(map[string]bool)
				for _, d := range zs.ConfiguredDomains {
					configuredSet[d] = true
				}
				actualSet := make(map[string]bool)
				for _, s := range info.SANs {
					actualSet[s] = true
				}
				for _, d := range zs.ConfiguredDomains {
					if !actualSet[d] {
						zs.MissingSANs = append(zs.MissingSANs, d)
					}
				}
				for _, s := range info.SANs {
					if !configuredSet[s] {
						zs.ExtraSANs = append(zs.ExtraSANs, s)
					}
				}
			}
		}

		zoneSSLStatuses = append(zoneSSLStatuses, zs)
	}

	// Set HTTPS action flags and compute needed SubZones
	var sslGaps []SSLGap
	for _, da := range domainMap {
		if da.HasZone && !da.HasSSLCoverage {
			da.CanEnableHTTPS = true
			da.NeededSubZone = da.Domain
			da.NeededSubZoneDisplay = da.Domain
			if da.HasService {
				sslGaps = append(sslGaps, SSLGap{
					Domain:   da.Domain,
					ZoneName: da.ZoneName,
					SubZone:  da.Domain,
					Display:  da.Domain,
					Reason:   "No SSL certificate coverage for this domain",
				})
			}
		}
		if da.HasSSLCoverage && !da.CertExists {
			da.CanRequestCert = true
		}
	}

	// Resolve DNS for all non-wildcard service domains in parallel
	// Query dnsmasq directly (on WG gateway) and 1.1.1.1 for remote
	dnsmasqAddr := s.config.GetWGGatewayIP() + ":53"
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, da := range domainMap {
		if !da.HasService || strings.HasPrefix(da.Domain, "*.") {
			continue
		}
		da := da // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			dnsmasqResult, remoteResult := resolveDomain(da.Domain, dnsmasqAddr)
			mu.Lock()
			da.DnsmasqResolvedIP = dnsmasqResult
			da.RemoteResolvedIP = remoteResult
			da.DnsmasqDNSMatch = dnsmasqResult != "" && dnsmasqResult == da.InternalIP
			da.RemoteDNSMatch = remoteResult != "" && remoteResult == da.ExternalIP
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Sort domains alphabetically
	var domains []DomainAnalysis
	for _, da := range domainMap {
		domains = append(domains, *da)
	}
	sort.Slice(domains, func(i, j int) bool {
		return domains[i].Domain < domains[j].Domain
	})

	// Compute summary counts
	var countIntDNS, countExtDNS, countHTTPS, countProxy int
	for _, da := range domains {
		if da.HasInternalDNS {
			countIntDNS++
		}
		if da.HasExternalDNS {
			countExtDNS++
		}
		if da.CertExists {
			countHTTPS++
		}
		if da.HasProxy {
			countProxy++
		}
	}

	// Sort SSL gaps by domain for consistent display
	sort.Slice(sslGaps, func(i, j int) bool {
		return sslGaps[i].Domain < sslGaps[j].Domain
	})

	data := map[string]interface{}{
		"Domains":     domains,
		"TotalCount":  len(domains),
		"IntDNSCount": countIntDNS,
		"ExtDNSCount": countExtDNS,
		"HTTPSCount":  countHTTPS,
		"ProxyCount":  countProxy,
		"SSLGaps":          sslGaps,
		"ZoneSSLStatuses": zoneSSLStatuses,
		"Config":      s.config,
		"CSRFToken":   s.getCSRFToken(r),
		"Message":     r.URL.Query().Get("msg"),
		"Error":       r.URL.Query().Get("err"),
	}
	s.templates["domains"].Execute(w, data)
}

// neededSubZoneForDomain computes the SubZone value needed to cover a domain
// with an SSL certificate, plus a human-readable display and explanation.
//
// Wildcard certs only cover one level: *.example.com covers app.example.com
// but NOT app.vpn.example.com. For multi-level subdomains, you need additional
// wildcard SubZones at each level.
//
// Examples for zone "example.com":
//
//	"app.example.com"         -> SubZone "*"      -> *.example.com
//	"vpn.example.com"         -> SubZone "*"      -> *.example.com
//	"app.vpn.example.com"     -> SubZone "*.vpn"  -> *.vpn.example.com
//	"deep.app.vpn.example.com"-> SubZone "*.app.vpn" -> *.app.vpn.example.com
//	"example.com"             -> SubZone ""        -> example.com (root)
func neededSubZoneForDomain(domain, zoneName string) (subZone, display, reason string) {
	// Strip wildcard prefix from domain for analysis
	checkDomain := strings.TrimPrefix(domain, "*.")

	if checkDomain == zoneName {
		// Root domain needs explicit root SubZone
		return "", zoneName, "Root domain requires an explicit SubZone entry"
	}

	// Strip zone suffix to get the subdomain part
	// e.g., "app.vpn.example.com" with zone "example.com" -> "app.vpn"
	suffix := "." + zoneName
	if !strings.HasSuffix(checkDomain, suffix) {
		return "*", "*." + zoneName, "Domain doesn't match zone"
	}
	subPart := checkDomain[:len(checkDomain)-len(suffix)]

	// Count levels in the subdomain
	parts := strings.Split(subPart, ".")
	if len(parts) == 1 {
		// Single level: app.example.com -> needs *.example.com
		return "*", "*." + zoneName, "Single-level subdomain needs a root wildcard (*.)"
	}

	// Multi-level: app.vpn.example.com -> needs *.vpn.example.com
	// The wildcard replaces the leftmost label
	wildcardBase := strings.Join(parts[1:], ".")
	subZone = "*." + wildcardBase
	display = subZone + "." + zoneName
	reason = "Wildcard DNS only covers one level. *." + zoneName +
		" covers " + parts[0] + "." + zoneName +
		" but NOT " + domain +
		". You need " + display + " for this domain."
	return subZone, display, reason
}

// resolveDomain performs DNS lookups against dnsmasq (via dnsmasqAddr) and remote (1.1.1.1).
// Returns the first A record IP from each, or "" on failure. Uses short timeouts to avoid blocking.
func resolveDomain(domain, dnsmasqAddr string) (dnsmasqIP, remoteIP string) {
	var wg sync.WaitGroup
	wg.Add(2)

	resolveVia := func(server string) string {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 2 * time.Second}
				return d.DialContext(ctx, "udp", server)
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		addrs, err := resolver.LookupHost(ctx, domain)
		if err == nil && len(addrs) > 0 {
			return addrs[0]
		}
		return ""
	}

	go func() {
		defer wg.Done()
		dnsmasqIP = resolveVia(dnsmasqAddr)
	}()

	go func() {
		defer wg.Done()
		remoteIP = resolveVia("1.1.1.1:53")
	}()

	wg.Wait()
	return
}

// domainMatchesPattern checks if a domain matches a wildcard or exact pattern.
// Examples:
//
//	"*.example.com" matches "app.example.com" but not "sub.app.example.com"
//	"example.com" matches "example.com" only
//	"*.vpn.example.com" matches "app.vpn.example.com"
func domainMatchesPattern(domain, pattern string) bool {
	if domain == pattern {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		// Domain must end with suffix and not have additional dots before it
		if strings.HasSuffix(domain, suffix) {
			prefix := domain[:len(domain)-len(suffix)]
			// prefix should not contain dots (single level wildcard)
			return !strings.Contains(prefix, ".")
		}
	}
	return false
}
