package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// DefaultPublicIPMaxAge is the staleness threshold used when PublicIPMaxAge
// is 0 (unset). Cached IPs older than this are not published.
const DefaultPublicIPMaxAge = 3600 // 1 hour

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

// EffectivePublicIP returns the IP currently representing this host:
// PublicIPOverride if set, otherwise the cached PublicIP (which may be stale —
// callers that publish to DNS should check IsPublicIPStale first).
func (c *Config) EffectivePublicIP() string {
	if c.PublicIPOverride != "" {
		return c.PublicIPOverride
	}
	return c.PublicIP
}

// EffectivePublicIPMaxAge returns the configured staleness threshold,
// substituting DefaultPublicIPMaxAge when unset.
func (c *Config) EffectivePublicIPMaxAge() int {
	if c.PublicIPMaxAge > 0 {
		return c.PublicIPMaxAge
	}
	return DefaultPublicIPMaxAge
}

// IsPublicIPStale reports whether the cached PublicIP is too old to trust.
// Always false when an override is set; always true when the cache is empty
// or has never been checked.
func (c *Config) IsPublicIPStale() bool {
	if c.PublicIPOverride != "" {
		return false
	}
	if c.PublicIP == "" || c.PublicIPLastChecked == 0 {
		return true
	}
	return time.Now().Unix()-c.PublicIPLastChecked > int64(c.EffectivePublicIPMaxAge())
}

// GetPublicIPForService returns the first public IP for a service (for display/backward compat).
// May return a stale value — use PublishablePublicIPs for the DNS publish path.
func (c *Config) GetPublicIPForService(svc *Service) string {
	ips := c.GetPublicIPsForService(svc)
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// GetPublicIPsForService returns all public IPs known for a service, for
// *display* purposes. Returns the service's explicit ExternalDNS.IPs if set,
// otherwise falls back to the host's effective public IP (which may be stale).
// For the DNS publish path use PublishablePublicIPs instead.
func (c *Config) GetPublicIPsForService(svc *Service) []string {
	if svc.ExternalDNS == nil {
		return nil
	}
	if ips := svc.ExternalDNS.GetIPs(); len(ips) > 0 {
		return ips
	}
	if ip := c.EffectivePublicIP(); ip != "" {
		return []string{ip}
	}
	return nil
}

// PublishablePublicIPs is the publish-path counterpart to
// GetPublicIPsForService. It returns IPs safe to write to DNS:
//   - Service-explicit ExternalDNS.IPs are returned verbatim.
//   - The host's public IP is returned only when it is *not* stale.
//
// Returning nil here means "don't publish" — callers must handle this rather
// than coerce to an empty record.
func (c *Config) PublishablePublicIPs(svc *Service) []string {
	if svc.ExternalDNS == nil {
		return nil
	}
	if ips := svc.ExternalDNS.GetIPs(); len(ips) > 0 {
		return ips
	}
	if c.IsPublicIPStale() {
		return nil
	}
	if ip := c.EffectivePublicIP(); ip != "" {
		return []string{ip}
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
		// Replace localhost with LocalInterface IP - dnsmasq requires an IP address.
		// Publishing a loopback address into dnsmasq would broadcast it LAN-wide,
		// making every client resolve the domain to its own 127.0.0.1. If we can't
		// resolve a real interface IP, skip the mapping rather than leak loopback.
		if ip == "localhost" || ip == "127.0.0.1" {
			if c.LocalInterface == "" {
				slog.Warn("skipping loopback DNS mapping: local_interface unset",
					"service", svc.Name, "domains", svc.Domains)
				continue
			}
			ip = c.LocalInterface
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
		if svc.Proxy == nil {
			continue
		}

		// A service routes either to an upstream host:port (Backend) or, for a
		// static-folder service, to hz's own loopback static listener. Validation
		// guarantees these are mutually exclusive.
		static := svc.Proxy.StaticRoot != ""
		self := svc.Proxy.Self
		server := svc.Proxy.Backend
		switch {
		case static:
			server = c.StaticServeAddr()
		case self:
			server = c.SelfBackendAddr()
		}
		if server == "" {
			continue
		}

		b := haproxy.Backend{
			Name:          svc.Name,
			DomainMatches: svc.Domains,
			Server:        server,
			InternalOnly:  svc.Proxy.InternalOnly,
		}
		if svc.Proxy.HealthCheck != nil && svc.Proxy.HealthCheck.Path != "" {
			b.HTTPCheck = true
			b.CheckPath = svc.Proxy.HealthCheck.Path
		}

		// Blue-green deploy: override server with current/next slots.
		// Not applicable to static or self services (validation rejects the combo).
		if !static && !self && svc.Proxy.Deploy != nil {
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

		// Per-backend timeout overrides
		if svc.Proxy.Timeouts != nil {
			b.TimeoutConnect = svc.Proxy.Timeouts.ConnectSeconds
			b.TimeoutServer = svc.Proxy.Timeouts.ServerSeconds
			b.TimeoutTunnel = svc.Proxy.Timeouts.TunnelSeconds
		}

		// Metrics endpoint: deny it from non-local sources at the edge (the
		// public domain path stays closed; Prometheus scrapes the backend
		// directly over the internal network). Default path /metrics.
		if svc.Integrations != nil && svc.Integrations.Metrics != nil && !svc.Integrations.Metrics.Disabled {
			b.MetricsPath = svc.Integrations.Metrics.MetricsPath()
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
			_ = os.Remove(filepath.Join(errorsDir, e.Name()))
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

		publicIPs := c.PublishablePublicIPs(&svc)
		if len(publicIPs) == 0 {
			continue // No publishable public IP (missing or stale)
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
			switch subZone {
			case "":
				allDomains = append(allDomains, zone.Name)
			case "*":
				allDomains = append(allDomains, "*."+zone.Name)
			default:
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

		// Blue-green deploy reserves a second "next" backend port for the
		// standby release — it's in use even when the service points at the
		// active one, so allocation must not hand it out.
		if svc.Proxy.Deploy != nil && svc.Proxy.Deploy.NextBackend != "" {
			if nh, np, err := net.SplitHostPort(svc.Proxy.Deploy.NextBackend); err == nil {
				if nh == "localhost" || nh == "127.0.0.1" {
					if c.LocalInterface != "" {
						nh = c.LocalInterface
					} else {
						nh = gateway
					}
				}
				m[nh] = append(m[nh], HostPortEntry{
					Port:    np,
					Proto:   "tcp",
					Service: svc.Name + " (deploy-next)",
					Domain:  svc.PrimaryDomain(),
				})
			}
		}
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

	// Declared hosts — ensure they appear in the map even if no service routes
	// to them, so the topology view (and exporter "*" expansion) sees them.
	for _, h := range c.Hosts {
		if h.IP == "" {
			continue
		}
		if _, ok := m[h.IP]; !ok {
			m[h.IP] = nil
		}
	}

	return HostPortMap{Hosts: m}
}

// DeriveKnownHostIPs returns the sorted, unique set of host IPs hz knows about —
// every key of the derived host/port map (service backends, gateway, declared
// hosts). This is the population an exporter's Hosts:["*"] expands over.
func (c *Config) DeriveKnownHostIPs() []string {
	m := c.DeriveHostPortMap()
	out := make([]string, 0, len(m.Hosts))
	for ip := range m.Hosts {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// ExporterTarget is one expanded, renderable exporter endpoint.
type ExporterTarget struct {
	Job     string
	Address string // host:port
	Path    string
	Bearer  string
	Labels  map[string]string // static + per-host declared labels (job/instance are set by Prometheus)
}

// metricsOptedIn reports whether a service already has per-service Prometheus
// metrics enabled, so a service-mode exporter rule can skip it (no duplicate job).
func (s *Service) metricsOptedIn() bool {
	return s.Integrations != nil && s.Integrations.Metrics != nil && !s.Integrations.Metrics.Disabled
}

// DeriveExporterTargets expands every configured Exporter into concrete targets
// per its mode: port (Port × Hosts, "*" = all known hosts), service (one target
// per non-opted-in service backend, blue-green per slot), or static (the Targets
// list). Per-host declared labels merge under the exporter's own Labels (exporter
// wins). Output is sorted (job, then address); duplicate addresses within a job
// collapse.
func (c *Config) DeriveExporterTargets() []ExporterTarget {
	// name/ip -> declared labels, and name -> ip resolution.
	labelsByIP := map[string]map[string]string{}
	ipByName := map[string]string{}
	for _, h := range c.Hosts {
		if h.IP == "" {
			continue
		}
		if len(h.Labels) > 0 {
			labelsByIP[h.IP] = h.Labels
		}
		if h.Name != "" {
			ipByName[h.Name] = h.IP
		}
	}

	var out []ExporterTarget
	for _, e := range c.Exporters {
		if e.Job == "" {
			continue
		}
		path := e.MetricsPath()

		// emit adds one target, merging declared host labels (by IP) under the
		// per-target labels already set (caller/exporter labels win).
		emit := func(addr string, labels map[string]string) {
			if addr == "" {
				return
			}
			if labels == nil {
				labels = map[string]string{}
			}
			for k, v := range e.Labels {
				if _, ok := labels[k]; !ok {
					labels[k] = v
				}
			}
			if host, _, err := net.SplitHostPort(addr); err == nil {
				for k, v := range labelsByIP[host] {
					if _, ok := labels[k]; !ok {
						labels[k] = v
					}
				}
			}
			out = append(out, ExporterTarget{Job: e.Job, Address: addr, Path: path, Bearer: e.Bearer, Labels: labels})
		}

		seen := map[string]bool{}
		emitOnce := func(addr string, labels map[string]string) {
			if addr == "" || seen[addr] {
				return
			}
			seen[addr] = true
			emit(addr, labels)
		}

		switch e.EffectiveMode() {
		case "static":
			for _, t := range e.Targets {
				emitOnce(strings.TrimSpace(t), nil)
			}

		case "service":
			for i := range c.Services {
				svc := &c.Services[i]
				if svc.Proxy == nil || svc.Proxy.Backend == "" || svc.metricsOptedIn() {
					continue
				}
				if svc.Proxy.Deploy != nil && svc.Proxy.Deploy.NextBackend != "" {
					emitOnce(svc.Proxy.Backend, map[string]string{"service": svc.Name, "slot": "current"})
					emitOnce(svc.Proxy.Deploy.NextBackend, map[string]string{"service": svc.Name, "slot": "next"})
					continue
				}
				emitOnce(svc.Proxy.Backend, map[string]string{"service": svc.Name})
			}

		default: // "port"
			hosts := e.Hosts
			if len(hosts) == 0 {
				hosts = []string{"*"}
			}
			for _, h := range hosts {
				if h == "*" {
					hosts = c.DeriveKnownHostIPs()
					break
				}
			}
			if e.Port > 0 {
				for _, h := range hosts {
					ip := strings.TrimSpace(h)
					if ip == "*" {
						continue
					}
					if resolved, ok := ipByName[ip]; ok {
						ip = resolved
					}
					emitOnce(net.JoinHostPort(ip, strconv.Itoa(e.Port)), nil)
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Job != out[j].Job {
			return out[i].Job < out[j].Job
		}
		return out[i].Address < out[j].Address
	})
	return out
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

		// Reject anything that isn't a plain hostname. Domains are written
		// verbatim into haproxy.cfg ACLs (hdr_end(host) -i <domain>); a stray
		// newline or metacharacter that still suffix-matched a zone would let a
		// crafted domain inject HAProxy directives.
		if !isValidDomainName(domain) {
			return &ValidationError{Field: "domain", Message: "domain contains invalid characters"}
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

	// Validate static-folder backend: mutually exclusive with proxying, absolute path.
	if svc.Proxy != nil && svc.Proxy.StaticRoot != "" {
		if svc.Proxy.Backend != "" {
			return &ValidationError{Field: "proxy.static_root", Message: "static_root and backend are mutually exclusive"}
		}
		if svc.Proxy.Deploy != nil {
			return &ValidationError{Field: "proxy.static_root", Message: "static_root cannot be combined with blue-green deploy"}
		}
		if !filepath.IsAbs(svc.Proxy.StaticRoot) {
			return &ValidationError{Field: "proxy.static_root", Message: "must be an absolute path"}
		}
		// Guardrail against the catastrophic copy-paste (serving / or /etc as
		// root). This is a footgun stop, not the isolation boundary — the
		// runtime os.Root confinement in the static server is.
		if isSensitiveRoot(svc.Proxy.StaticRoot) {
			return &ValidationError{Field: "proxy.static_root", Message: "refusing to serve a system directory"}
		}
	}

	// Self (proxy to this hz instance) is mutually exclusive with the other
	// backend sources and with blue-green deploy.
	if svc.Proxy != nil && svc.Proxy.Self {
		if svc.Proxy.Backend != "" || svc.Proxy.StaticRoot != "" {
			return &ValidationError{Field: "proxy.self", Message: "self is mutually exclusive with backend and static_root"}
		}
		if svc.Proxy.Deploy != nil {
			return &ValidationError{Field: "proxy.self", Message: "self cannot be combined with blue-green deploy"}
		}
	}

	// SPA fallback only applies to static-folder services.
	if svc.Proxy != nil && svc.Proxy.SPA && svc.Proxy.StaticRoot == "" {
		return &ValidationError{Field: "proxy.spa", Message: "spa requires static_root"}
	}

	return nil
}

// StaticWebDir is the base directory for hz-managed static-site roots. It sits
// under the systemd unit's state dir (/var/lib/homelab-horizon), namespaced
// beneath web/ so managed roots are grouped and kept separate from other state.
const StaticWebDir = "/var/lib/homelab-horizon/web"

// DeriveStaticRoot returns an hz-managed static-root path for a service, derived
// from its name (StaticWebDir/<slug>). The path is made unique against every
// other service's static_root by appending -2, -3, ... so two services never
// share a managed directory.
func (c *Config) DeriveStaticRoot(name string) string {
	slug := staticRootSlug(name)
	if slug == "" {
		slug = "site"
	}

	taken := make(map[string]bool)
	for i := range c.Services {
		if p := c.Services[i].Proxy; p != nil && p.StaticRoot != "" {
			taken[p.StaticRoot] = true
		}
	}

	base := filepath.Join(StaticWebDir, slug)
	candidate := base
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	return candidate
}

// staticRootSlug turns a service name into a safe single path segment:
// lowercased, with any run of characters outside [a-z0-9._-] collapsed to a
// single dash, and leading/trailing dashes/dots trimmed.
func staticRootSlug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}

// isValidDomainName reports whether d is a plain DNS hostname (optionally a
// leading "*." wildcard) made only of letters, digits, dots and hyphens. It
// deliberately rejects control characters, spaces, and HAProxy metacharacters
// so a domain cannot inject directives into the generated haproxy.cfg.
func isValidDomainName(d string) bool {
	d = strings.TrimPrefix(d, "*.")
	if d == "" || len(d) > 253 {
		return false
	}
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
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

// isSensitiveRoot reports whether p is, or resolves to, the filesystem root or
// a system directory that should never be exposed as a static site. The
// literal path and its symlink-resolved target are both checked, so a
// static_root that is a symlink to /etc can't slip past the guard.
func isSensitiveRoot(p string) bool {
	if isSensitivePath(filepath.Clean(p)) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return isSensitivePath(resolved)
	}
	return false
}

func isSensitivePath(clean string) bool {
	if clean == "/" {
		return true
	}
	for _, s := range []string{"/etc", "/root", "/boot", "/proc", "/sys", "/dev", "/run"} {
		if clean == s || strings.HasPrefix(clean, s+"/") {
			return true
		}
	}
	return false
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
