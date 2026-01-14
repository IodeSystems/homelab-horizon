package config

import (
	"net"
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

// GetPublicIPForService returns the appropriate public IP for a service
// Returns service's ExternalDNS.IP if set, otherwise falls back to global PublicIP
func (c *Config) GetPublicIPForService(svc *Service) string {
	if svc.ExternalDNS == nil {
		return ""
	}
	if svc.ExternalDNS.IP != "" {
		return svc.ExternalDNS.IP
	}
	return c.PublicIP
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
		mappings[svc.Domain] = ip
	}
	return mappings
}

// DeriveHAProxyBackends generates HAProxy backends from services with Proxy config
func (c *Config) DeriveHAProxyBackends() []haproxy.Backend {
	var backends []haproxy.Backend
	for _, svc := range c.Services {
		if svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}

		b := haproxy.Backend{
			Name:        svc.Name,
			DomainMatch: svc.Domain,
			Server:      svc.Proxy.Backend,
		}
		if svc.Proxy.HealthCheck != nil && svc.Proxy.HealthCheck.Path != "" {
			b.HTTPCheck = true
			b.CheckPath = svc.Proxy.HealthCheck.Path
		}
		backends = append(backends, b)
	}
	return backends
}

// DeriveRoute53Records generates Route53 A records from services with ExternalDNS config
func (c *Config) DeriveRoute53Records() []route53.Record {
	var records []route53.Record
	for _, svc := range c.Services {
		if svc.ExternalDNS == nil {
			continue
		}

		zone := c.GetZoneForDomain(svc.Domain)
		if zone == nil {
			continue // No zone configured for this domain
		}

		publicIP := c.GetPublicIPForService(&svc)
		if publicIP == "" {
			continue // No public IP available
		}

		ttl := svc.ExternalDNS.TTL
		if ttl <= 0 {
			ttl = 300 // Default TTL
		}

		awsProfile := ""
		if provider := zone.GetDNSProvider(); provider != nil {
			awsProfile = provider.AWSProfile
		}
		records = append(records, route53.Record{
			Name:       svc.Domain,
			Type:       "A",
			Value:      publicIP,
			TTL:        ttl,
			ZoneID:     zone.ZoneID,
			ZoneName:   zone.Name,
			AWSProfile: awsProfile,
		})
	}
	return records
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
				// Empty string means root domain
				allDomains = append(allDomains, zone.Name)
			} else if subZone == "*" {
				// Asterisk alone means root wildcard
				allDomains = append(allDomains, "*."+zone.Name)
			} else {
				allDomains = append(allDomains, subZone+"."+zone.Name)
			}
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

// GetServicesForZone returns all services belonging to a zone
func (c *Config) GetServicesForZone(zone *Zone) []Service {
	var services []Service
	for _, svc := range c.Services {
		if svc.Domain == zone.Name || strings.HasSuffix(svc.Domain, "."+zone.Name) {
			services = append(services, svc)
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
	if svc.Domain == "" {
		return &ValidationError{Field: "domain", Message: "domain is required"}
	}

	// Validate wildcard domain format if present
	if strings.HasPrefix(svc.Domain, "*") {
		if !strings.HasPrefix(svc.Domain, "*.") {
			return &ValidationError{Field: "domain", Message: "wildcard must be in format *.subdomain.example.com"}
		}
		// Ensure there's a valid domain after the wildcard
		baseDomain := strings.TrimPrefix(svc.Domain, "*.")
		if baseDomain == "" || !strings.Contains(baseDomain, ".") {
			return &ValidationError{Field: "domain", Message: "wildcard must have a valid domain (e.g., *.api.example.com)"}
		}
	}

	// Check zone exists for domain
	zone := c.GetZoneForDomain(svc.Domain)
	if zone == nil {
		return &ValidationError{Field: "domain", Message: "no zone configured for this domain"}
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
		if existing.Domain == svc.Domain {
			return &ValidationError{Field: "domain", Message: "service with this domain already exists"}
		}
		if existing.Name == svc.Name {
			return &ValidationError{Field: "name", Message: "service with this name already exists"}
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
			// Remove all services in this zone
			var remaining []Service
			for _, svc := range c.Services {
				if svc.Domain != zone.Name && !strings.HasSuffix(svc.Domain, "."+zone.Name) {
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
