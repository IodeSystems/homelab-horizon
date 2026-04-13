package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
	"homelab-horizon/internal/route53"
)

// GetZoneForDomain finds the zone that a domain belongs to
// Returns nil if no matching zone is found
// Supports wildcard domains (e.g., "*.api.example.com" matches zone "example.com")
func (c *Config) GetZoneForDomain(domain string) *Zone {
	// Strip wildcard prefix for zone lookup
	checkDomain := strings.TrimPrefix(domain, "*.")

	for i := range c.Zones {
		zoneName := c.Zones[i].Name
		// Domain must be a subdomain of the zone or equal to it
		if checkDomain == zoneName || strings.HasSuffix(checkDomain, "."+zoneName) {
			return &c.Zones[i]
		}
	}
	return nil
}

// GetPublicIPForService returns the first public IP for a service (for display/backward compat).
func (c *Config) GetPublicIPForService(svc *Service) string {
	ips := c.GetPublicIPsForService(svc)
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// GetPublicIPsForService returns all public IPs for a service.
// Returns service's ExternalDNS.IPs if set, otherwise falls back to global PublicIP.
func (c *Config) GetPublicIPsForService(svc *Service) []string {
	if svc.ExternalDNS == nil {
		return nil
	}
	if ips := svc.ExternalDNS.GetIPs(); len(ips) > 0 {
		return ips
	}
	if c.PublicIP != "" {
		return []string{c.PublicIP}
	}
	return nil
}

// DeriveDNSMappings generates dnsmasq address mappings from services
// Maps domain -> internal IP from InternalDNS config
// Replaces localhost/127.0.0.1 with LocalInterface IP since dnsmasq requires an IP address
func (c *Config) DeriveDNSMappings() map[string]string {
	mappings := make(map[string]string)
	for _, svc := range c.Services {
		if svc.InternalDNS == nil || svc.InternalDNS.IP == "" {
			continue
		}
		ip := svc.InternalDNS.IP
		// Replace localhost with LocalInterface IP - dnsmasq requires an IP address
		if ip == "localhost" || ip == "127.0.0.1" {
			if c.LocalInterface != "" {
				ip = c.LocalInterface
			}
		}
		for _, domain := range svc.Domains {
			mappings[domain] = ip
		}
	}
	return mappings
}

// haproxyErrorsDir returns the directory where per-service HAProxy error files are written.
func (c *Config) haproxyErrorsDir() string {
	if c.HAProxyConfigPath != "" {
		return filepath.Join(filepath.Dir(c.HAProxyConfigPath), "errors")
	}
	return "/etc/haproxy/errors"
}

// DeriveHAProxyBackends generates HAProxy backends from services with Proxy config
func (c *Config) DeriveHAProxyBackends() []haproxy.Backend {
	var backends []haproxy.Backend
	for _, svc := range c.Services {
		if svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}

		b := haproxy.Backend{
			Name:          svc.Name,
			DomainMatches: svc.Domains,
			Server:        svc.Proxy.Backend,
			InternalOnly:  svc.Proxy.InternalOnly,
		}
		if svc.Proxy.HealthCheck != nil && svc.Proxy.HealthCheck.Path != "" {
			b.HTTPCheck = true
			b.CheckPath = svc.Proxy.HealthCheck.Path
		}

		// Blue-green deploy: override server with current/next slots
		if svc.Proxy.Deploy != nil {
			b.Deploy = true
			b.HTTPCheck = true
			b.DeployBalance = svc.Proxy.Deploy.Balance
			b.CurrentServer = svc.Proxy.Deploy.CurrentServer(svc.Proxy.Backend)
			b.NextServer = svc.Proxy.Deploy.InactiveServer(svc.Proxy.Backend)
		}

		// Custom 503 maintenance page
		if svc.Proxy.MaintenancePage != "" {
			b.ErrorFile503 = filepath.Join(c.haproxyErrorsDir(), haproxy.SanitizeName(svc.Name)+"_503.http")
		}

		backends = append(backends, b)
	}
	return backends
}

// WriteMaintenancePageFiles writes per-service 503.http error files for any service
// with MaintenancePage set, and removes stale files for services where it was cleared.
func (c *Config) WriteMaintenancePageFiles() error {
	errorsDir := c.haproxyErrorsDir()
	if err := os.MkdirAll(errorsDir, 0755); err != nil {
		return err
	}

	// Collect active filenames and write them
	active := make(map[string]bool)
	for _, svc := range c.Services {
		if svc.Proxy == nil || svc.Proxy.MaintenancePage == "" {
			continue
		}
		filename := haproxy.SanitizeName(svc.Name) + "_503.http"
		active[filename] = true
		path := filepath.Join(errorsDir, filename)
		content := "HTTP/1.0 503 Service Unavailable\r\nCache-Control: no-cache\r\nConnection: close\r\nContent-Type: text/html\r\n\r\n" + svc.Proxy.MaintenancePage
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
	}

	// Remove stale files (previously written, now cleared)
	entries, err := os.ReadDir(errorsDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_503.http") && !active[e.Name()] {
			os.Remove(filepath.Join(errorsDir, e.Name()))
		}
	}
	return nil
}

// DeriveRoute53Records generates Route53 A records from services with ExternalDNS config.
// Services with multiple IPs produce multiple records per domain (round-robin DNS).
func (c *Config) DeriveRoute53Records() []route53.Record {
	var records []route53.Record
	for _, svc := range c.Services {
		if svc.ExternalDNS == nil {
			continue
		}

		publicIPs := c.GetPublicIPsForService(&svc)
		if len(publicIPs) == 0 {
			continue // No public IP available
		}

		ttl := svc.ExternalDNS.TTL
		if ttl <= 0 {
			ttl = 300 // Default TTL
		}

		for _, domain := range svc.Domains {
			zone := c.GetZoneForDomain(domain)
			if zone == nil {
				continue // No zone configured for this domain
			}

			awsProfile := ""
			if provider := zone.GetDNSProvider(); provider != nil {
				awsProfile = provider.AWSProfile
			}
			for _, ip := range publicIPs {
				records = append(records, route53.Record{
					Name:       domain,
					Type:       "A",
					Value:      ip,
					TTL:        ttl,
					ZoneID:     zone.ZoneID,
					ZoneName:   zone.Name,
					AWSProfile: awsProfile,
				})
			}
		}
	}
	return records
}

// filterRedundantDomains removes non-wildcard domains that are already covered
// by a wildcard in the same list. A wildcard *.X covers any single-level
// subdomain of X, so "dev.example.com" is redundant if "*.example.com" is
// present. Let's Encrypt rejects such requests as malformed.
func filterRedundantDomains(domains []string) []string {
	// Collect all wildcard suffixes: "*.example.com" → ".example.com"
	wildcardSuffixes := make(map[string]bool)
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") {
			wildcardSuffixes[d[1:]] = true // ".example.com"
		}
	}
	if len(wildcardSuffixes) == 0 {
		return domains
	}

	var filtered []string
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") {
			// Keep all wildcards
			filtered = append(filtered, d)
			continue
		}
		// Check if this domain is a single-level match for any wildcard
		dotIdx := strings.Index(d, ".")
		if dotIdx > 0 {
			suffix := d[dotIdx:] // ".example.com"
			if wildcardSuffixes[suffix] {
				// Redundant — skip it
				continue
			}
		}
		filtered = append(filtered, d)
	}
	return filtered
}

// DeriveSSLDomains generates SSL domains from zones with SSL enabled
// Only requests certificates for domains explicitly specified in SubZones.
// SubZones specify which domains to include:
//   - "" (empty string) = root domain (e.g., "example.com")
//   - "*" = root wildcard (e.g., "*.example.com")
//   - "api" = subdomain (e.g., "api.example.com")
//   - "*.vpn" = wildcard subdomain (e.g., "*.vpn.example.com")
//
// The first SubZone becomes the primary domain, the rest are extra SANs.
// If SubZones is empty, no certificate is requested for that zone.
func (c *Config) DeriveSSLDomains() []letsencrypt.DomainConfig {
	var domains []letsencrypt.DomainConfig
	for i := range c.Zones {
		zone := &c.Zones[i]
		if zone.SSL == nil || !zone.SSL.Enabled {
			continue
		}

		// Skip if no SubZones specified - only request what's explicitly configured
		if len(zone.SubZones) == 0 {
			continue
		}

		// Build domain list from sub-zones
		// Empty string "" means root domain, "*" means root wildcard, otherwise append to zone name
		var allDomains []string
		for _, subZone := range zone.SubZones {
			if subZone == "" {
				allDomains = append(allDomains, zone.Name)
			} else if subZone == "*" {
				allDomains = append(allDomains, "*."+zone.Name)
			} else {
				allDomains = append(allDomains, subZone+"."+zone.Name)
			}
		}

		// Filter out non-wildcard domains that are redundant with a wildcard
		// in the same request. E.g., if *.iodesystems.com is present, remove
		// dev.iodesystems.com — Let's Encrypt rejects this as malformed.
		allDomains = filterRedundantDomains(allDomains)

		if len(allDomains) == 0 {
			continue
		}

		// First domain is primary, rest are extra SANs
		primaryDomain := allDomains[0]
		var extraSANs []string
		if len(allDomains) > 1 {
			extraSANs = allDomains[1:]
		}

		// Build DNS provider config for letsencrypt
		var dnsProvider *letsencrypt.DNSProviderConfig
		if providerCfg := zone.GetDNSProvider(); providerCfg != nil {
			// Use provider's AWSHostedZoneID if set, otherwise fall back to zone's ZoneID
			awsHostedZoneID := providerCfg.AWSHostedZoneID
			if awsHostedZoneID == "" && providerCfg.Type == "route53" {
				awsHostedZoneID = zone.ZoneID
			}
			// Same for Cloudflare
			cloudflareZoneID := providerCfg.CloudflareZoneID
			if cloudflareZoneID == "" && providerCfg.Type == "cloudflare" {
				cloudflareZoneID = zone.ZoneID
			}

			dnsProvider = &letsencrypt.DNSProviderConfig{
				Type:               letsencrypt.DNSProviderType(providerCfg.Type),
				AWSAccessKeyID:     providerCfg.AWSAccessKeyID,
				AWSSecretAccessKey: providerCfg.AWSSecretAccessKey,
				AWSRegion:          providerCfg.AWSRegion,
				AWSHostedZoneID:    awsHostedZoneID,
				AWSProfile:         providerCfg.AWSProfile,
				NamecomUsername:    providerCfg.NamecomUsername,
				NamecomAPIToken:    providerCfg.NamecomAPIToken,
				CloudflareAPIToken: providerCfg.CloudflareAPIToken,
				CloudflareZoneID:   cloudflareZoneID,
			}
		}

		domains = append(domains, letsencrypt.DomainConfig{
			Domain:      primaryDomain,
			ExtraSANs:   extraSANs,
			Email:       zone.SSL.Email,
			DNSProvider: dnsProvider,
		})
	}
	return domains
}

// HostPortEntry represents a single port reservation on a host
type HostPortEntry struct {
	Port    string `json:"port"`
	Proto   string `json:"proto"` // "tcp" or "udp"
	Service string `json:"service"`
	Domain  string `json:"domain,omitempty"`
}

// HostPortMap represents all port reservations grouped by host IP
type HostPortMap struct {
	Hosts map[string][]HostPortEntry `json:"hosts"`
}

// DeriveHostPortMap builds a map of host -> reserved ports from the full config.
// Includes service proxy backends, HAProxy listen ports, WireGuard, dnsmasq, and the admin server.
func (c *Config) DeriveHostPortMap() HostPortMap {
	m := make(map[string][]HostPortEntry)
	gateway := c.GetWGGatewayIP()
	if gateway == "" {
		gateway = "server"
	}

	// Service proxy backends
	for _, svc := range c.Services {
		if svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}
		host, port, err := net.SplitHostPort(svc.Proxy.Backend)
		if err != nil {
			continue
		}
		// Normalize localhost to gateway
		if host == "localhost" || host == "127.0.0.1" {
			if c.LocalInterface != "" {
				host = c.LocalInterface
			} else {
				host = gateway
			}
		}
		m[host] = append(m[host], HostPortEntry{
			Port:    port,
			Proto:   "tcp",
			Service: svc.Name,
			Domain:  svc.PrimaryDomain(),
		})
	}

	// HAProxy listen ports (on gateway)
	if c.HAProxyEnabled {
		httpPort := c.HAProxyHTTPPort
		if httpPort == 0 {
			httpPort = 80
		}
		httpsPort := c.HAProxyHTTPSPort
		if httpsPort == 0 {
			httpsPort = 443
		}
		m[gateway] = append(m[gateway], HostPortEntry{
			Port:    fmt.Sprintf("%d", httpPort),
			Proto:   "tcp",
			Service: "haproxy",
		})
		m[gateway] = append(m[gateway], HostPortEntry{
			Port:    fmt.Sprintf("%d", httpsPort),
			Proto:   "tcp",
			Service: "haproxy-tls",
		})
	}

	// WireGuard port (from ServerEndpoint)
	if c.ServerEndpoint != "" {
		if _, port, err := net.SplitHostPort(c.ServerEndpoint); err == nil {
			m[gateway] = append(m[gateway], HostPortEntry{
				Port:    port,
				Proto:   "udp",
				Service: "wireguard",
			})
		}
	}

	// dnsmasq
	if c.DNSMasqEnabled {
		m[gateway] = append(m[gateway], HostPortEntry{
			Port:    "53",
			Proto:   "udp",
			Service: "dnsmasq",
		})
	}

	// Admin server
	if c.ListenAddr != "" {
		if _, port, err := net.SplitHostPort(c.ListenAddr); err == nil {
			m[gateway] = append(m[gateway], HostPortEntry{
				Port:    port,
				Proto:   "tcp",
				Service: "homelab-horizon",
			})
		}
	}

	return HostPortMap{Hosts: m}
}

// GetServicesForZone returns all services belonging to a zone
func (c *Config) GetServicesForZone(zone *Zone) []Service {
	var services []Service
	for _, svc := range c.Services {
		for _, domain := range svc.Domains {
			if domain == zone.Name || strings.HasSuffix(domain, "."+zone.Name) {
				services = append(services, svc)
				break
			}
		}
	}
	return services
}

// GetExternalServices returns all services with ExternalDNS configured
func (c *Config) GetExternalServices() []Service {
	var services []Service
	for _, svc := range c.Services {
		if svc.ExternalDNS != nil {
			services = append(services, svc)
		}
	}
	return services
}

// GetInternalOnlyServices returns all services without ExternalDNS
func (c *Config) GetInternalOnlyServices() []Service {
	var services []Service
	for _, svc := range c.Services {
		if svc.ExternalDNS == nil {
			services = append(services, svc)
		}
	}
	return services
}

// GetProxiedServices returns all services with Proxy configured
func (c *Config) GetProxiedServices() []Service {
	var services []Service
	for _, svc := range c.Services {
		if svc.Proxy != nil {
			services = append(services, svc)
		}
	}
	return services
}

// ValidateService validates a service configuration
func (c *Config) ValidateService(svc *Service) error {
	if svc.Name == "" {
		return &ValidationError{Field: "name", Message: "service name is required"}
	}
	if len(svc.Domains) == 0 {
		return &ValidationError{Field: "domain", Message: "at least one domain is required"}
	}

	for _, domain := range svc.Domains {
		if domain == "" {
			return &ValidationError{Field: "domain", Message: "domain must not be empty"}
		}

		// Validate wildcard domain format if present
		if strings.HasPrefix(domain, "*") {
			if !strings.HasPrefix(domain, "*.") {
				return &ValidationError{Field: "domain", Message: "wildcard must be in format *.subdomain.example.com"}
			}
			// Ensure there's a valid domain after the wildcard
			baseDomain := strings.TrimPrefix(domain, "*.")
			if baseDomain == "" || !strings.Contains(baseDomain, ".") {
				return &ValidationError{Field: "domain", Message: "wildcard must have a valid domain (e.g., *.api.example.com)"}
			}
		}

		// Check zone exists for domain
		zone := c.GetZoneForDomain(domain)
		if zone == nil {
			return &ValidationError{Field: "domain", Message: fmt.Sprintf("no zone configured for domain %q", domain)}
		}
	}

	// Validate InternalDNS if present
	if svc.InternalDNS != nil && svc.InternalDNS.IP != "" {
		ip := svc.InternalDNS.IP
		// Allow localhost or valid IP
		if ip != "localhost" && ip != "127.0.0.1" {
			if net.ParseIP(ip) == nil {
				return &ValidationError{Field: "internal_dns.ip", Message: "invalid IP address"}
			}
		}
	}

	// Validate Proxy backend address format if present
	if svc.Proxy != nil && svc.Proxy.Backend != "" {
		_, _, err := net.SplitHostPort(svc.Proxy.Backend)
		if err != nil {
			return &ValidationError{Field: "proxy.backend", Message: "invalid address format (expected host:port)"}
		}
	}

	return nil
}

// ValidateZone validates a zone configuration
func (c *Config) ValidateZone(zone *Zone) error {
	if zone.Name == "" {
		return &ValidationError{Field: "name", Message: "zone name is required"}
	}
	if zone.ZoneID == "" {
		return &ValidationError{Field: "zone_id", Message: "Route53 zone ID is required"}
	}
	if zone.SSL != nil && zone.SSL.Enabled && zone.SSL.Email == "" {
		return &ValidationError{Field: "ssl.email", Message: "email is required for SSL"}
	}
	return nil
}

// ValidationError represents a validation error for a specific field
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

// extractIP extracts the IP address from an address string (removes port if present)
func extractIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// No port, return as-is if it's a valid IP
		if ip := net.ParseIP(address); ip != nil {
			return address
		}
		return address // Return anyway, might be a hostname
	}
	return host
}

// AddService adds a service to the configuration
// Returns an error if validation fails or service already exists
func (c *Config) AddService(svc Service) error {
	// Check for duplicate
	for _, existing := range c.Services {
		if existing.Name == svc.Name {
			return &ValidationError{Field: "name", Message: "service with this name already exists"}
		}
		for _, newDomain := range svc.Domains {
			for _, existingDomain := range existing.Domains {
				if newDomain == existingDomain {
					return &ValidationError{Field: "domain", Message: fmt.Sprintf("domain %q already used by service %q", newDomain, existing.Name)}
				}
			}
		}
	}

	if err := c.ValidateService(&svc); err != nil {
		return err
	}

	c.Services = append(c.Services, svc)
	return nil
}

// RemoveService removes a service by name
func (c *Config) RemoveService(name string) bool {
	for i, svc := range c.Services {
		if svc.Name == name {
			c.Services = append(c.Services[:i], c.Services[i+1:]...)
			return true
		}
	}
	return false
}

// AddZone adds a zone to the configuration
// Returns an error if validation fails or zone already exists
func (c *Config) AddZone(zone Zone) error {
	// Check for duplicate
	for _, existing := range c.Zones {
		if existing.Name == zone.Name {
			return &ValidationError{Field: "name", Message: "zone already exists"}
		}
	}

	if err := c.ValidateZone(&zone); err != nil {
		return err
	}

	c.Zones = append(c.Zones, zone)
	return nil
}

// RemoveZone removes a zone by name
// Also removes all services belonging to that zone
func (c *Config) RemoveZone(name string) bool {
	for i, zone := range c.Zones {
		if zone.Name == name {
			// Remove all services that have any domain in this zone
			var remaining []Service
			for _, svc := range c.Services {
				inZone := false
				for _, domain := range svc.Domains {
					if domain == zone.Name || strings.HasSuffix(domain, "."+zone.Name) {
						inZone = true
						break
					}
				}
				if !inZone {
					remaining = append(remaining, svc)
				}
			}
			c.Services = remaining

			// Remove the zone
			c.Zones = append(c.Zones[:i], c.Zones[i+1:]...)
			return true
		}
	}
	return false
}

// GetZone returns a zone by name
// Also handles wildcard domain format (e.g., "*.example.com" -> "example.com")
func (c *Config) GetZone(name string) *Zone {
	// Strip wildcard prefix if present
	zoneName := strings.TrimPrefix(name, "*.")

	for i := range c.Zones {
		if c.Zones[i].Name == zoneName {
			return &c.Zones[i]
		}
	}
	return nil
}

// GetService returns a service by name
func (c *Config) GetService(name string) *Service {
	for i := range c.Services {
		if c.Services[i].Name == name {
			return &c.Services[i]
		}
	}
	return nil
}
