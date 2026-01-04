package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// DNSProviderType identifies the DNS provider for ACME challenges and DNS management
type DNSProviderType string

const (
	DNSProviderRoute53 DNSProviderType = "route53"
	DNSProviderNamecom DNSProviderType = "namecom"
)

// DNSProviderConfig holds provider-specific credentials for DNS operations
type DNSProviderConfig struct {
	Type DNSProviderType `json:"type"`

	// Route53 credentials
	AWSAccessKeyID     string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AWSRegion          string `json:"aws_region,omitempty"`
	AWSHostedZoneID    string `json:"aws_hosted_zone_id,omitempty"`
	AWSProfile         string `json:"aws_profile,omitempty"`

	// Name.com credentials
	NamecomUsername string `json:"namecom_username,omitempty"`
	NamecomAPIToken string `json:"namecom_api_token,omitempty"`
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
	default:
		return fmt.Errorf("unknown dns provider type: %s", d.Type)
	}
	return nil
}

// SearchPaths defines where to look for config files, in order of preference
var SearchPaths = []string{
	"./config.json",
	"./homelab-horizon.json",
	"/etc/homelab-horizon/config.json",
	"/etc/homelab-horizon.json",
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
}

// Zone represents a DNS zone with shared configuration for DNS provider and SSL
type Zone struct {
	Name        string             `json:"name"`                   // e.g., "example.com"
	ZoneID      string             `json:"zone_id"`                // Provider-specific zone ID
	DNSProvider *DNSProviderConfig `json:"dns_provider,omitempty"` // DNS provider configuration
	SSL         *ZoneSSL           `json:"ssl,omitempty"`
	SubZones    []string           `json:"sub_zones,omitempty"` // Sub-domains needing wildcard certs (e.g., "vpn" for *.vpn.example.com)
}

// GetDNSProvider returns the DNS provider config
func (z *Zone) GetDNSProvider() *DNSProviderConfig {
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
	Domain      string       `json:"domain"`                 // FQDN, e.g., "grafana.example.com"
	InternalDNS *InternalDNS `json:"internal_dns,omitempty"` // dnsmasq config for VPN clients
	ExternalDNS *ExternalDNS `json:"external_dns,omitempty"` // Route53 config for public access
	Proxy       *ProxyConfig `json:"proxy,omitempty"`        // HAProxy reverse proxy config
}

// InternalDNS configures how VPN clients (via dnsmasq) resolve this domain
type InternalDNS struct {
	IP string `json:"ip"` // IP that dnsmasq returns for this domain
}

// ExternalDNS configures how public internet clients (via Route53) resolve this domain
type ExternalDNS struct {
	IP  string `json:"ip,omitempty"`  // IP for Route53 ("" = auto-detect public IP)
	TTL int    `json:"ttl,omitempty"` // Route53 TTL (default 300)
}

// ProxyConfig configures HAProxy reverse proxying for this service
type ProxyConfig struct {
	Backend     string       `json:"backend"`                // host:port for HAProxy to forward to
	HealthCheck *HealthCheck `json:"health_check,omitempty"` // Optional health check
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

func Default() *Config {
	return &Config{
		// Core server settings
		ListenAddr: ":8080",
		KioskURL:   "https://kiosk.vpn.example.com",

		// WireGuard VPN configuration
		WGInterface:    "wg0",
		WGConfigPath:   "/etc/wireguard/wg0.conf",
		InvitesFile:    "invites.txt",
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

// Load reads config from path, overlaying on defaults
// Supports JSONC format (JSON with // comments)
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // just use defaults
		}
		return nil, err
	}

	// Strip JSONC comments before parsing
	data = stripJSONCComments(data)

	// Overlay file values on defaults
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	return cfg, nil
}

// LoadAuto finds and loads config from standard paths
func LoadAuto() (*Config, string, error) {
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

// DetectLocalInterface attempts to detect the local interface IP
// Tries eth0 first, then falls back to deriving from VPN range
func (c *Config) DetectLocalInterface() string {
	// Try eth0 first
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

	// Try to detect local network from eth0
	if localCIDR := GetLocalNetworkCIDR("eth0"); localCIDR != "" {
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
