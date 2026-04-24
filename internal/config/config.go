package config

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Routing profile constants for WireGuard peers
const (
	ProfileLanAccess  = "lan-access"
	ProfileFullTunnel = "full-tunnel"
	ProfileVPNOnly    = "vpn-only"
)

// DNSProviderType identifies the DNS provider for ACME challenges and DNS management
type DNSProviderType string

const (
	DNSProviderRoute53      DNSProviderType = "route53"
	DNSProviderNamecom      DNSProviderType = "namecom"
	DNSProviderCloudflare   DNSProviderType = "cloudflare"
	DNSProviderDigitalOcean DNSProviderType = "digitalocean"
	DNSProviderHetzner      DNSProviderType = "hetzner"
	DNSProviderGandi        DNSProviderType = "gandi"
	DNSProviderGoogleCloud  DNSProviderType = "googlecloud"
	DNSProviderDuckDNS      DNSProviderType = "duckdns"
)

// DNSProviderConfig holds provider-specific credentials for DNS operations
type DNSProviderConfig struct {
	Type DNSProviderType `json:"type"`

	// Zone name (domain) for libdns providers - e.g., "example.com"
	// This is automatically populated from the parent Zone.Name
	ZoneName string `json:"zone_name,omitempty"`

	// Generic API token (used by multiple providers)
	APIToken string `json:"api_token,omitempty"`

	// Route53 credentials
	AWSAccessKeyID     string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AWSRegion          string `json:"aws_region,omitempty"`
	AWSHostedZoneID    string `json:"aws_hosted_zone_id,omitempty"`
	AWSProfile         string `json:"aws_profile,omitempty"`

	// Name.com credentials
	NamecomUsername string `json:"namecom_username,omitempty"`
	NamecomAPIToken string `json:"namecom_api_token,omitempty"`

	// Cloudflare credentials
	CloudflareAPIToken string `json:"cloudflare_api_token,omitempty"`
	CloudflareZoneID   string `json:"cloudflare_zone_id,omitempty"` // Optional: Cloudflare zone ID (if not provided, looked up by zone name)

	// Google Cloud DNS credentials
	GCPProject            string `json:"gcp_project,omitempty"`
	GCPServiceAccountJSON string `json:"gcp_service_account_json,omitempty"` // JSON key file contents or path
}

// Validate checks if the provider config has required fields
func (d *DNSProviderConfig) Validate() error {
	switch d.Type {
	case DNSProviderRoute53:
		// Either AWS profile or explicit credentials required
		if d.AWSProfile == "" && (d.AWSAccessKeyID == "" || d.AWSSecretAccessKey == "") {
			return errors.New("route53 requires aws_profile or aws_access_key_id + aws_secret_access_key")
		}
	case DNSProviderNamecom:
		if d.NamecomUsername == "" || d.NamecomAPIToken == "" {
			return errors.New("namecom requires namecom_username and namecom_api_token")
		}
	case DNSProviderCloudflare:
		if d.CloudflareAPIToken == "" {
			return errors.New("cloudflare requires cloudflare_api_token")
		}
	case DNSProviderDigitalOcean:
		if d.APIToken == "" {
			return errors.New("digitalocean requires api_token")
		}
	case DNSProviderHetzner:
		if d.APIToken == "" {
			return errors.New("hetzner requires api_token")
		}
	case DNSProviderGandi:
		if d.APIToken == "" {
			return errors.New("gandi requires api_token (bearer token)")
		}
	case DNSProviderGoogleCloud:
		if d.GCPProject == "" {
			return errors.New("googlecloud requires gcp_project")
		}
	case DNSProviderDuckDNS:
		if d.APIToken == "" {
			return errors.New("duckdns requires api_token")
		}
	default:
		return fmt.Errorf("unknown dns provider type: %s", d.Type)
	}
	return nil
}

// SearchPaths defines where to look for config files, in order of preference
var SearchPaths = []string{
	"/etc/homelab-horizon/config.json",
	"/etc/homelab-horizon.json",
	"./config.json",
	"./homelab-horizon.json",
}

type Config struct {
	// Core server settings
	ListenAddr string `json:"listen_addr"`
	AdminToken string `json:"admin_token,omitempty"`
	KioskURL   string `json:"kiosk_url"`

	// WireGuard VPN configuration (Layer 1: VPN Clients)
	WGInterface     string `json:"wg_interface"`
	WGConfigPath    string `json:"wg_config_path"`
	InvitesFile     string `json:"invites_file"`
	ServerEndpoint  string `json:"server_endpoint"`
	ServerPublicKey string `json:"server_public_key"`
	VPNRange        string `json:"vpn_range"`
	DNS             string `json:"dns"`
	AllowedIPs      string `json:"allowed_ips"`

	// Services configuration (Layer 2: Services)
	Zones    []Zone    `json:"zones"`
	Services []Service `json:"services"`

	// Public IP for external access (auto-detected if empty)
	PublicIP         string `json:"public_ip,omitempty"`
	PublicIPInterval int    `json:"public_ip_interval"` // Sync interval in seconds (0 = disabled, default 300)

	// DNSMasq configuration
	DNSMasqEnabled    bool     `json:"dnsmasq_enabled"`
	DNSMasqConfigPath string   `json:"dnsmasq_config_path"`
	DNSMasqHostsPath  string   `json:"dnsmasq_hosts_path"`
	DNSMasqInterfaces []string `json:"dnsmasq_interfaces"` // Additional interfaces for dnsmasq (beyond WG interface)
	UpstreamDNS       []string `json:"upstream_dns"`
	LocalInterface    string   `json:"local_interface"` // Local interface IP for DNS resolution of localhost-bound services

	// LastLocalIface and LastLanCIDR persist what the interface sync last
	// reconciled against. On startup the watcher seeds from these (not from
	// live detection) so drift that occurred while horizon was down — e.g.
	// someone plugged in a new eth while the service was stopped — still
	// triggers reconfiguration. Empty = never reconciled; triggers a one-time
	// sync on first run. Also useful as a manual recovery lever: set to the
	// stale iface name to force cleanup of its MASQUERADE rule.
	LastLocalIface string `json:"last_local_iface,omitempty"`
	LastLanCIDR    string `json:"last_lan_cidr,omitempty"`

	// HAProxy configuration
	HAProxyEnabled    bool   `json:"haproxy_enabled"`
	HAProxyConfigPath string `json:"haproxy_config_path"`
	HAProxyHTTPPort   int    `json:"haproxy_http_port"`
	HAProxyHTTPSPort  int    `json:"haproxy_https_port"`

	// SSL/Let's Encrypt configuration
	SSLEnabled        bool   `json:"ssl_enabled"`
	SSLCertDir        string `json:"ssl_cert_dir"`
	SSLHAProxyCertDir string `json:"ssl_haproxy_cert_dir"`

	// Service monitoring with ntfy notifications
	NtfyURL            string         `json:"ntfy_url,omitempty"`             // e.g., "https://ntfy.sh/my-homelab-alerts"
	ServiceChecks      []ServiceCheck `json:"service_checks,omitempty"`       // Health checks for services
	DisabledAutoChecks []string       `json:"disabled_auto_checks,omitempty"` // Names of disabled auto-generated checks

	// VPN-based admin authentication
	VPNAdmins []string `json:"vpn_admins,omitempty"` // Client names with admin access via VPN IP

	// Per-peer routing profiles: "lan-access" (default), "full-tunnel", "vpn-only"
	VPNProfiles map[string]string `json:"vpn_profiles,omitempty"`

	// WireGuard MFA (TOTP per-connect)
	VPNMFAEnabled   bool              `json:"vpn_mfa_enabled,omitempty"`
	VPNMFADurations []string          `json:"vpn_mfa_durations,omitempty"` // e.g. ["2h","4h","8h","forever"]
	VPNMFASecrets   map[string]string `json:"vpn_mfa_secrets,omitempty"`   // peer name -> base32 TOTP secret
	VPNMFASessions  map[string]int64  `json:"vpn_mfa_sessions,omitempty"`  // peer name -> expiry unix timestamp (0 = forever)

	// WireGuard VPN client peers — shared state, replicated to non-primary
	// instances via the pull loop. Handlers update this after every WG
	// mutation; applyNewConfig applies it to the local WG config file.
	WGPeers []WGPeer `json:"wg_peers,omitempty"`

	// IP banning
	IPBans []IPBan `json:"ip_bans,omitempty"`

	// Auto-heal: automatically install and configure missing dependencies on startup
	AutoHeal bool `json:"auto_heal,omitempty"`

	// Multi-instance HA (fleet) — see plan/plan.md
	PeerID        string `json:"peer_id,omitempty"`        // local identity within the fleet
	ConfigPrimary bool   `json:"config_primary,omitempty"` // true if THIS instance is the config primary
	Peers         []Peer `json:"peers,omitempty"`          // every other instance in the fleet
}

// WGPeer represents a WireGuard VPN client peer. This is the shared-state
// representation stored in config.json and replicated across fleet members.
// The WG config file on disk is derived from this list.
type WGPeer struct {
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	AllowedIPs string `json:"allowed_ips"`
}

// Peer describes another homelab-horizon instance in the fleet.
// All inter-peer traffic flows over the WireGuard site-to-site tunnel,
// so WGAddr must be reachable on the WG interface.
type Peer struct {
	ID      string `json:"id"`
	WGAddr  string `json:"wg_addr"`           // host[:port] reachable over WG (port defaults to local listen port)
	Primary bool   `json:"primary,omitempty"` // marks the config primary on non-primary instances

	// Phase 3: needed for multi-site client config generation.
	// When populated, GenerateMultiSiteClientConfig emits one [Peer]
	// block per site. When empty, single-site mode is assumed.
	ServerEndpoint  string `json:"server_endpoint,omitempty"`  // e.g. "b.example.com:51820"
	ServerPublicKey string `json:"server_public_key,omitempty"`
	VPNRange        string `json:"vpn_range,omitempty"` // e.g. "10.0.2.0/24"
}

// PrimaryPeer returns the entry in Peers marked as primary, or nil if none.
func (c *Config) PrimaryPeer() *Peer {
	for i := range c.Peers {
		if c.Peers[i].Primary {
			return &c.Peers[i]
		}
	}
	return nil
}

// ValidateFleet checks the multi-instance fields are internally consistent.
// Returns nil if no fleet is configured (single-instance mode).
func (c *Config) ValidateFleet() error {
	if c.PeerID == "" && len(c.Peers) == 0 && !c.ConfigPrimary {
		return nil // single-instance mode
	}
	if c.PeerID == "" {
		return errors.New("peer_id required when peers[] or config_primary is set")
	}
	primaries := 0
	for _, p := range c.Peers {
		if p.ID == "" || p.WGAddr == "" {
			return fmt.Errorf("peer entry missing id or wg_addr: %+v", p)
		}
		if p.ID == c.PeerID {
			return fmt.Errorf("peer entry %q duplicates own peer_id", p.ID)
		}
		if p.Primary {
			primaries++
		}
	}
	if c.ConfigPrimary && primaries > 0 {
		return errors.New("config_primary is true on this instance but a peer is also marked primary")
	}
	if !c.ConfigPrimary && primaries > 1 {
		return errors.New("more than one peer marked primary")
	}

	// Validate VPN ranges don't overlap when configured (site-to-site).
	if c.VPNRange != "" {
		ranges := []struct {
			id    string
			cidr  string
		}{{c.PeerID, c.VPNRange}}
		for _, p := range c.Peers {
			if p.VPNRange != "" {
				ranges = append(ranges, struct {
					id   string
					cidr string
				}{p.ID, p.VPNRange})
			}
		}
		for i := 0; i < len(ranges); i++ {
			_, netI, errI := net.ParseCIDR(ranges[i].cidr)
			if errI != nil {
				continue
			}
			for j := i + 1; j < len(ranges); j++ {
				_, netJ, errJ := net.ParseCIDR(ranges[j].cidr)
				if errJ != nil {
					continue
				}
				if netI.Contains(netJ.IP) || netJ.Contains(netI.IP) {
					return fmt.Errorf("VPN ranges overlap: %s (%s) and %s (%s)",
						ranges[i].id, ranges[i].cidr, ranges[j].id, ranges[j].cidr)
				}
			}
		}
	}

	return nil
}

// IPBan represents a banned IP address
type IPBan struct {
	IP        string `json:"ip"`
	Timeout   int    `json:"timeout,omitempty"`    // seconds, 0 = permanent
	CreatedAt int64  `json:"created_at"`           // unix timestamp
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix timestamp, 0 = never
	Reason    string `json:"reason,omitempty"`
	Service   string `json:"service,omitempty"` // which service banned it
}

// Zone represents a DNS zone with shared configuration for DNS provider and SSL
type Zone struct {
	Name        string             `json:"name"`                   // e.g., "example.com"
	ZoneID      string             `json:"zone_id"`                // Provider-specific zone ID
	DNSProvider *DNSProviderConfig `json:"dns_provider,omitempty"` // DNS provider configuration
	SSL         *ZoneSSL           `json:"ssl,omitempty"`
	SubZones    []string           `json:"sub_zones,omitempty"` // Sub-domains needing wildcard certs (e.g., "vpn" for *.vpn.example.com)
}

// GetDNSProvider returns the DNS provider config with zone name populated
func (z *Zone) GetDNSProvider() *DNSProviderConfig {
	if z.DNSProvider == nil {
		return nil
	}
	// Ensure zone name is populated for libdns providers
	if z.DNSProvider.ZoneName == "" {
		z.DNSProvider.ZoneName = z.Name
	}
	return z.DNSProvider
}

// ZoneSSL configures wildcard SSL for a zone
type ZoneSSL struct {
	Enabled bool   `json:"enabled"`
	Email   string `json:"email"` // Let's Encrypt email
}

// Service represents a unified service configuration with clear separation of concerns
type Service struct {
	Name        string       `json:"name"`                   // Human-readable, e.g., "grafana"
	Token       string       `json:"token,omitempty"`        // API token for service integration (ban, status)
	Domains     []string     `json:"domains"`                // FQDNs, e.g., ["app.example.com", "book.example.com"]
	InternalDNS *InternalDNS `json:"internal_dns,omitempty"` // dnsmasq config for VPN clients
	ExternalDNS *ExternalDNS `json:"external_dns,omitempty"` // Route53 config for public access
	Proxy       *ProxyConfig `json:"proxy,omitempty"`        // HAProxy reverse proxy config
}

// EnsureToken generates a token for this service if one doesn't exist.
func (s *Service) EnsureToken() {
	if s.Token == "" {
		s.Token = GenerateDeployToken()
	}
}

// PrimaryDomain returns the first domain, or "" if none configured
func (s *Service) PrimaryDomain() string {
	if len(s.Domains) > 0 {
		return s.Domains[0]
	}
	return ""
}

// UnmarshalJSON handles backwards compatibility: accepts both "domain":"x" and "domains":["x","y"]
func (s *Service) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion
	type ServiceAlias Service
	var alias ServiceAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*s = Service(alias)

	// Check for legacy "domain" field
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if domainRaw, ok := raw["domain"]; ok && len(s.Domains) == 0 {
		var single string
		if err := json.Unmarshal(domainRaw, &single); err == nil && single != "" {
			s.Domains = []string{single}
		}
	}
	return nil
}

// InternalDNS configures how VPN clients (via dnsmasq) resolve this domain
type InternalDNS struct {
	IP string `json:"ip"` // IP that dnsmasq returns for this domain
}

// ExternalDNS configures how public internet clients (via Route53) resolve this domain
type ExternalDNS struct {
	IP  string   `json:"ip,omitempty"`  // Deprecated: use IPs. Single IP kept for backward compat.
	IPs []string `json:"ips,omitempty"` // Multiple IPs for round-robin DNS
	TTL int      `json:"ttl,omitempty"` // Route53 TTL (default 300)
}

// GetIPs returns all configured IPs, falling back to the legacy single IP field.
func (e *ExternalDNS) GetIPs() []string {
	if len(e.IPs) > 0 {
		return e.IPs
	}
	if e.IP != "" {
		return []string{e.IP}
	}
	return nil
}

// ProxyConfig configures HAProxy reverse proxying for this service
type ProxyConfig struct {
	Backend         string        `json:"backend"`                    // host:port for HAProxy to forward to
	HealthCheck     *HealthCheck  `json:"health_check,omitempty"`     // Optional health check
	InternalOnly    bool          `json:"internal_only,omitempty"`    // Restrict to local network access only
	Deploy          *DeployConfig `json:"deploy,omitempty"`           // Blue-green deploy with current/next slots
	MaintenancePage string        `json:"maintenance_page,omitempty"` // HTML body served as 503 during maintenance
}

// DeployConfig enables blue-green deployment with two server slots.
// Slot A uses the port from ProxyConfig.Backend. Slot B uses NextBackend.
// Health checks use ProxyConfig.HealthCheck.Path for both slots.
// The deploy API uses Token for auth and manages drain/down/up transitions.
type DeployConfig struct {
	NextBackend string `json:"next_backend"`          // Slot B address host:port (Slot A = ProxyConfig.Backend)
	Token       string `json:"token"`                 // Per-service deploy token
	ActiveSlot  string `json:"active_slot,omitempty"` // "a" or "b" — which slot is currently serving (default "a")
	Balance     string `json:"balance,omitempty"`     // "first" (active/standby) or "roundrobin" (even). Default "first".
}

// HealthCheck configures HAProxy health checks for a backend
type HealthCheck struct {
	Path string `json:"path"` // e.g., "/health", "/api/health"
}

// ServiceCheck configures a service health check with ntfy notifications
type ServiceCheck struct {
	Name     string `json:"name"`               // Human-readable name
	Type     string `json:"type"`               // "ping" or "http"
	Target   string `json:"target"`             // IP/hostname for ping, URL for http
	Interval int    `json:"interval,omitempty"` // Check interval in seconds (default 300)
	Enabled  bool   `json:"enabled"`            // Whether check is active (false = ignored)
}

// CurrentServer returns the host:port for the active slot.
// backend is ProxyConfig.Backend (slot A's address).
func (d *DeployConfig) CurrentServer(backend string) string {
	if d.ActiveSlot == "b" {
		return d.NextBackend
	}
	return backend
}

// InactiveServer returns the host:port for the inactive slot.
// backend is ProxyConfig.Backend (slot A's address).
func (d *DeployConfig) InactiveServer(backend string) string {
	if d.ActiveSlot == "b" {
		return backend
	}
	return d.NextBackend
}

// Swap flips the active slot
func (d *DeployConfig) Swap() {
	if d.ActiveSlot == "b" {
		d.ActiveSlot = "a"
	} else {
		d.ActiveSlot = "b"
	}
}

// GenerateDeployToken creates a random 32-char hex token
func GenerateDeployToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func Default() *Config {
	return &Config{
		// Core server settings
		ListenAddr: ":8080",
		KioskURL:   "https://kiosk.vpn.example.com",

		// WireGuard VPN configuration
		WGInterface:    "wg0",
		WGConfigPath:   "/etc/wireguard/wg0.conf",
		InvitesFile:    "/etc/homelab-horizon/invites.txt",
		ServerEndpoint: "vpn.example.com:51820",
		VPNRange:       "10.100.0.0/24",
		DNS:            "10.100.0.1",
		AllowedIPs:     "", // Empty = derive from VPN range + local network

		// Services configuration (Layer 2)
		Zones:            []Zone{},
		Services:         []Service{},
		PublicIP:         "",  // Auto-detected
		PublicIPInterval: 300, // 5 minutes

		// DNSMasq configuration
		DNSMasqEnabled:    true,
		DNSMasqConfigPath: "/etc/dnsmasq.d/wg-vpn.conf",
		DNSMasqHostsPath:  "/etc/dnsmasq.d/wg-hosts.conf",
		DNSMasqInterfaces: []string{},
		UpstreamDNS:       []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"},
		LocalInterface:    "", // Auto-detected from eth0 or falls back to VPN server IP

		// HAProxy configuration
		HAProxyEnabled:    false,
		HAProxyConfigPath: "/etc/haproxy/haproxy.cfg",
		HAProxyHTTPPort:   80,
		HAProxyHTTPSPort:  443,

		// SSL/Let's Encrypt configuration
		SSLEnabled:        false,
		SSLCertDir:        "/etc/letsencrypt",
		SSLHAProxyCertDir: "/etc/haproxy/certs",

		// Service monitoring
		NtfyURL:       "",
		ServiceChecks: []ServiceCheck{},
	}
}

// Find locates a config file from search paths, returns path and whether it exists
func Find() (string, bool) {
	for _, p := range SearchPaths {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return SearchPaths[0], false // default to first path for creation
}

// stripJSONCComments removes // comments from JSONC content
func stripJSONCComments(data []byte) []byte {
	var buf bytes.Buffer
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("//")) {
			continue
		}
		line = stripInlineComment(line)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// stripInlineComment removes trailing // comments, respecting quoted strings
func stripInlineComment(line []byte) []byte {
	inString := false
	escaped := false
	for i := 0; i < len(line); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch line[i] {
		case '\\':
			escaped = true
		case '"':
			inString = !inString
		case '/':
			if !inString && i+1 < len(line) && line[i+1] == '/' {
				return bytes.TrimRight(line[:i], " \t")
			}
		}
	}
	return line
}

// LoadFromJSON parses config JSON (with JSONC comment support), overlaying on defaults
func LoadFromJSON(data []byte) (*Config, error) {
	cfg := Default()
	data = stripJSONCComments(data)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// Load reads config from path, overlaying on defaults
// Supports JSONC format (JSON with // comments)
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil // just use defaults
		}
		return nil, err
	}
	return LoadFromJSON(data)
}

// LoadAuto finds and loads config from standard paths.
// If HZ_CONFIG env var is set, it is parsed as JSON config directly.
func LoadAuto() (*Config, string, error) {
	if envCfg := os.Getenv("HZ_CONFIG"); envCfg != "" {
		cfg, err := LoadFromJSON([]byte(envCfg))
		if err != nil {
			return nil, "", fmt.Errorf("parsing HZ_CONFIG: %w", err)
		}
		path := SearchPaths[0] // default save path
		fmt.Printf("Loaded config from HZ_CONFIG environment variable\n")
		return cfg, path, nil
	}

	path, found := Find()
	cfg, err := Load(path)
	if err != nil {
		return nil, "", err
	}
	if !found {
		fmt.Printf("No config file found, using defaults\n")
		fmt.Printf("Create %s to customize settings\n", path)
	} else {
		fmt.Printf("Loaded config from %s\n", path)
	}
	return cfg, path, nil
}

func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating config directory: %w", err)
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// GetInterfaceIP returns the first IPv4 address of the specified network interface
func GetInterfaceIP(ifaceName string) (string, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return "", err
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 address found on %s", ifaceName)
}

// DetectDefaultInterface returns the name of the network interface used for the default route.
func DetectDefaultInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// Destination 00000000 = default route
		if len(fields) >= 2 && fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// DetectLocalInterface attempts to detect the local interface IP
// Uses the default route interface, falls back to eth0, then VPN range
func (c *Config) DetectLocalInterface() string {
	// Try the default route interface first
	if iface := DetectDefaultInterface(); iface != "" {
		if ip, err := GetInterfaceIP(iface); err == nil {
			return ip
		}
	}

	// Try eth0 as fallback
	if ip, err := GetInterfaceIP("eth0"); err == nil {
		return ip
	}

	// Fall back to VPN server IP (first IP in VPN range, typically .1)
	if c.VPNRange != "" {
		parts := strings.Split(c.VPNRange, "/")
		if len(parts) > 0 {
			base := strings.TrimSuffix(parts[0], ".0")
			return base + ".1"
		}
	}

	return "10.100.0.1" // Last resort default
}

// EnsureLocalInterface sets LocalInterface if not already configured
func (c *Config) EnsureLocalInterface() {
	if c.LocalInterface == "" {
		c.LocalInterface = c.DetectLocalInterface()
	}
}

// GetLocalNetworkCIDR attempts to get the CIDR for the local network interface
func GetLocalNetworkCIDR(ifaceName string) string {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				// Return the network CIDR (e.g., 192.168.1.0/24)
				ones, _ := ipNet.Mask.Size()
				network := ip4.Mask(ipNet.Mask)
				return fmt.Sprintf("%s/%d", network.String(), ones)
			}
		}
	}
	return ""
}

// DeriveAllowedIPs returns AllowedIPs based on VPN range and local network
// This restricts VPN traffic to only internal networks, not all internet traffic
func (c *Config) DeriveAllowedIPs() string {
	var cidrs []string

	// Always include VPN range
	if c.VPNRange != "" {
		cidrs = append(cidrs, c.VPNRange)
	}

	// Try to detect local network from the default route interface, then fall back to eth0
	iface := DetectDefaultInterface()
	if iface == "" {
		iface = "eth0"
	}
	if localCIDR := GetLocalNetworkCIDR(iface); localCIDR != "" {
		// Don't duplicate if it's the same as VPN range
		if localCIDR != c.VPNRange {
			cidrs = append(cidrs, localCIDR)
		}
	}

	if len(cidrs) == 0 {
		return "10.100.0.0/24" // Fallback
	}

	return strings.Join(cidrs, ", ")
}

// GetAllowedIPs returns AllowedIPs, deriving it if not explicitly set
func (c *Config) GetAllowedIPs() string {
	if c.AllowedIPs != "" {
		return c.AllowedIPs
	}
	return c.DeriveAllowedIPs()
}

// GetPeerProfile returns the routing profile for a peer, defaulting to "lan-access"
func (c *Config) GetPeerProfile(name string) string {
	if c.VPNProfiles != nil {
		if p, ok := c.VPNProfiles[name]; ok && p != "" {
			return p
		}
	}
	return ProfileLanAccess
}

// SetPeerProfile sets the routing profile for a peer
func (c *Config) SetPeerProfile(name, profile string) {
	if c.VPNProfiles == nil {
		c.VPNProfiles = make(map[string]string)
	}
	if profile == "" || profile == ProfileLanAccess {
		delete(c.VPNProfiles, name)
	} else {
		c.VPNProfiles[name] = profile
	}
}

// RenamePeerProfile updates the profile map key when a peer is renamed
func (c *Config) RenamePeerProfile(oldName, newName string) {
	if c.VPNProfiles == nil {
		return
	}
	if p, ok := c.VPNProfiles[oldName]; ok {
		delete(c.VPNProfiles, oldName)
		c.VPNProfiles[newName] = p
	}
}

// DeletePeerProfile removes the profile entry for a peer
func (c *Config) DeletePeerProfile(name string) {
	if c.VPNProfiles != nil {
		delete(c.VPNProfiles, name)
	}
}

// IsPeerMFAJailed returns true if MFA is enabled, the peer has no active session,
// and the peer is not a VPN admin (admins bypass MFA).
func (c *Config) IsPeerMFAJailed(name string) bool {
	if !c.VPNMFAEnabled {
		return false
	}
	// VPN admins bypass MFA
	for _, admin := range c.VPNAdmins {
		if admin == name {
			return false
		}
	}
	if c.VPNMFASessions != nil {
		if expiry, ok := c.VPNMFASessions[name]; ok {
			if expiry == 0 { // forever
				return false
			}
			if expiry > time.Now().Unix() {
				return false
			}
		}
	}
	return true
}

// GetJailedPeers returns a set of peer names that are currently MFA-jailed.
func (c *Config) GetJailedPeers() map[string]bool {
	jailed := make(map[string]bool)
	if !c.VPNMFAEnabled {
		return jailed
	}
	for _, p := range c.WGPeers {
		if c.IsPeerMFAJailed(p.Name) {
			jailed[p.Name] = true
		}
	}
	return jailed
}

// SetMFASession sets an MFA session for a peer with the given expiry timestamp.
// Use expiry=0 for a permanent session.
func (c *Config) SetMFASession(name string, expiry int64) {
	if c.VPNMFASessions == nil {
		c.VPNMFASessions = make(map[string]int64)
	}
	c.VPNMFASessions[name] = expiry
}

// ClearMFASession removes a peer's MFA session.
func (c *Config) ClearMFASession(name string) {
	if c.VPNMFASessions != nil {
		delete(c.VPNMFASessions, name)
	}
}

// SetMFASecret sets a peer's TOTP secret.
func (c *Config) SetMFASecret(name, secret string) {
	if c.VPNMFASecrets == nil {
		c.VPNMFASecrets = make(map[string]string)
	}
	c.VPNMFASecrets[name] = secret
}

// ClearMFASecret removes a peer's TOTP secret (forces re-enrollment).
func (c *Config) ClearMFASecret(name string) {
	if c.VPNMFASecrets != nil {
		delete(c.VPNMFASecrets, name)
	}
	// Also clear any active session
	c.ClearMFASession(name)
}

// PruneExpiredMFASessions removes expired MFA sessions. Returns true if any were pruned.
func (c *Config) PruneExpiredMFASessions() bool {
	if c.VPNMFASessions == nil {
		return false
	}
	now := time.Now().Unix()
	pruned := false
	for name, expiry := range c.VPNMFASessions {
		if expiry != 0 && expiry <= now {
			delete(c.VPNMFASessions, name)
			pruned = true
		}
	}
	return pruned
}

// RenameMFAPeer updates MFA maps when a peer is renamed.
func (c *Config) RenameMFAPeer(oldName, newName string) {
	if c.VPNMFASecrets != nil {
		if s, ok := c.VPNMFASecrets[oldName]; ok {
			delete(c.VPNMFASecrets, oldName)
			c.VPNMFASecrets[newName] = s
		}
	}
	if c.VPNMFASessions != nil {
		if s, ok := c.VPNMFASessions[oldName]; ok {
			delete(c.VPNMFASessions, oldName)
			c.VPNMFASessions[newName] = s
		}
	}
}

// DeleteMFAPeer removes all MFA state for a peer.
func (c *Config) DeleteMFAPeer(name string) {
	c.ClearMFASecret(name)
}

// GetAllowedIPsForProfile returns client-side AllowedIPs for a routing profile
func (c *Config) GetAllowedIPsForProfile(profile string) string {
	switch profile {
	case ProfileFullTunnel:
		return "0.0.0.0/0, ::/0"
	case ProfileVPNOnly:
		if c.VPNRange != "" {
			return c.VPNRange
		}
		return "10.100.0.0/24"
	default:
		return c.GetAllowedIPs()
	}
}

// GetWGGatewayIP returns the WireGuard gateway IP (first IP in VPN range, typically .1)
func (c *Config) GetWGGatewayIP() string {
	if c.VPNRange != "" {
		parts := strings.Split(c.VPNRange, "/")
		if len(parts) > 0 {
			base := strings.TrimSuffix(parts[0], ".0")
			return base + ".1"
		}
	}
	return "10.100.0.1" // Fallback
}

// Template returns a commented config template for user reference
func Template() string {
	return strings.TrimSpace(`
{
  // HTTP server listen address
  "listen_addr": ":8080",

  // Public URL of this kiosk (for invite QR codes)
  "kiosk_url": "https://vpn.example.com",

  // === LAYER 1: VPN CLIENTS (WireGuard) ===

  // WireGuard interface name
  "wg_interface": "wg0",

  // Path to WireGuard configuration file
  "wg_config_path": "/etc/wireguard/wg0.conf",

  // File to store invite tokens (one per line)
  "invites_file": "invites.txt",

  // Public endpoint for clients to connect to (host:port)
  "server_endpoint": "vpn.example.com:51820",

  // Server's WireGuard public key (auto-detected if empty)
  "server_public_key": "",

  // VPN IP address range (CIDR notation)
  "vpn_range": "10.100.0.0/24",

  // DNS server for VPN clients
  "dns": "10.100.0.1",

  // Routes to send through VPN (empty = auto-detect VPN + local network)
  "allowed_ips": "",

  // === LAYER 2: SERVICES (Split-Horizon DNS) ===

  // DNS Zones - shared configuration for Route53 and SSL certificates
  "zones": [
    {
      "name": "example.com",
      "zone_id": "Z1234567890ABC",
      "aws_profile": "default",
      "ssl": {
        "enabled": true,
        "email": "admin@example.com"
      }
    }
  ],

  // Services - unified service definitions
  // Each service configures: internal DNS (dnsmasq), external DNS (Route53), proxy (HAProxy)
  "services": [
    {
      "name": "grafana",
      "domain": "grafana.example.com",
      "internal_dns": { "ip": "10.100.0.1" },
      "external_dns": { "ip": "", "ttl": 300 },
      "proxy": {
        "backend": "10.100.0.50:3000",
        "health_check": { "path": "/api/health" }
      }
    },
    {
      "name": "internal-db",
      "domain": "db.example.com",
      "internal_dns": { "ip": "10.100.0.100" }
    }
  ],

  // Public IP for HAProxy services (auto-detected if empty)
  "public_ip": "",
  "public_ip_interval": 300,

  // === SUBSYSTEM SETTINGS ===

  // DNSMasq (internal DNS for VPN clients)
  "dnsmasq_enabled": true,
  "dnsmasq_config_path": "/etc/dnsmasq.d/wg-vpn.conf",
  "dnsmasq_hosts_path": "/etc/dnsmasq.d/wg-hosts.conf",
  "upstream_dns": ["1.1.1.1", "8.8.8.8"],

  // Local interface IP for localhost-bound services (auto-detected from eth0 if empty)
  // When a service backend is "localhost:port", this IP is used in DNS mappings
  "local_interface": "",

  // HAProxy (reverse proxy for external access)
  "haproxy_enabled": true,
  "haproxy_config_path": "/etc/haproxy/haproxy.cfg",
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,

  // SSL/Let's Encrypt
  "ssl_enabled": true,
  "ssl_cert_dir": "/etc/letsencrypt",
  "ssl_haproxy_cert_dir": "/etc/haproxy/certs"
}
`) + "\n"
}

// IAMPolicyTemplate returns an IAM policy JSON that grants the minimum permissions
// needed for Route53 DNS management and Let's Encrypt DNS challenges
func IAMPolicyTemplate(zoneIDs ...string) string {
	if len(zoneIDs) == 0 {
		zoneIDs = []string{"YOUR_ZONE_ID_HERE"}
	}

	// Build the zone ARN list
	zoneARNs := ""
	for i, id := range zoneIDs {
		if i > 0 {
			zoneARNs += ",\n                "
		}
		zoneARNs += `"arn:aws:route53:::hostedzone/` + id + `"`
	}

	return `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "Route53ListZones",
            "Effect": "Allow",
            "Action": [
                "route53:ListHostedZones",
                "route53:ListHostedZonesByName"
            ],
            "Resource": "*"
        },
        {
            "Sid": "Route53ManageRecords",
            "Effect": "Allow",
            "Action": [
                "route53:GetHostedZone",
                "route53:ListResourceRecordSets",
                "route53:ChangeResourceRecordSets",
                "route53:GetChange"
            ],
            "Resource": [
                ` + zoneARNs + `
            ]
        },
        {
            "Sid": "Route53GetChanges",
            "Effect": "Allow",
            "Action": [
                "route53:GetChange"
            ],
            "Resource": "arn:aws:route53:::change/*"
        }
    ]
}

Instructions:
1. Go to AWS IAM Console: https://console.aws.amazon.com/iam/
2. Navigate to Users > [Your User] > Permissions > Add inline policy
3. Choose JSON tab and paste the policy above (without these instructions)
4. Replace YOUR_ZONE_ID_HERE with your actual Route53 hosted zone IDs
5. Name the policy "homelab-horizon-route53" and create it

Alternatively, you can use the AWS CLI:
  aws iam put-user-policy \
    --user-name YOUR_USER_NAME \
    --policy-name homelab-horizon-route53 \
    --policy-document file://policy.json
`
}

// systemdServiceTemplate is the single source of truth for the systemd service file.
// Args: workDir, execStart, workDir, workDir
const systemdServiceTemplate = `[Unit]
Description=Homelab Horizon - Split-Horizon DNS & VPN Management
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=+/bin/mkdir -p %s /etc/letsencrypt /etc/haproxy/certs
ExecStart=%s
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
User=root
Group=root

# File system isolation
ProtectSystem=strict
ReadWritePaths=-/etc/wireguard -/etc/dnsmasq.d -/etc/haproxy -/etc/letsencrypt -/etc/systemd/system -/proc/sys/net/ipv4 -/var/lib/haproxy -%s
ProtectHome=read-only
PrivateTmp=true
ProtectKernelTunables=true

# Network capabilities for WireGuard
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW

# Security restrictions
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
`

// GenerateServiceFile produces the systemd service file content.
// binaryPath is the absolute path to the homelab-horizon binary.
// configPath is the absolute path to the config file (may be empty).
func GenerateServiceFile(binaryPath, configPath string) string {
	execStart := binaryPath
	workDir := "/etc/homelab-horizon"

	if configPath != "" {
		execStart = fmt.Sprintf("%s -config %s", binaryPath, configPath)
		dir := filepath.Dir(configPath)
		if dir != "" && dir != "." {
			workDir = dir
		}
	}

	return fmt.Sprintf(systemdServiceTemplate, workDir, execStart, workDir, workDir)
}
