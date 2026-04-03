package server

import (
	"context"
	"net"
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
