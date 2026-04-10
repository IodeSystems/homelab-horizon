package letsencrypt

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homelab-horizon/internal/acme"
)

// DNSProviderType identifies the DNS provider for ACME challenges
type DNSProviderType string

const (
	DNSProviderRoute53    DNSProviderType = "route53"
	DNSProviderNamecom    DNSProviderType = "namecom"
	DNSProviderCloudflare DNSProviderType = "cloudflare"
)

// DNSProviderConfig holds provider-specific credentials (copied from config to avoid import cycle)
type DNSProviderConfig struct {
	Type DNSProviderType

	// Route53
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegion          string
	AWSHostedZoneID    string
	AWSProfile         string

	// Name.com
	NamecomUsername string
	NamecomAPIToken string

	// Cloudflare
	CloudflareAPIToken string
	CloudflareZoneID   string
}

// DomainConfig holds configuration for a single domain (or multiple SANs)
type DomainConfig struct {
	Domain      string   // Primary domain (e.g., "*.example.com")
	ExtraSANs   []string // Additional SANs (e.g., "*.vpn.example.com", "vpn.example.com")
	Email       string
	DNSProvider *DNSProviderConfig // DNS provider configuration
}

// Config holds Let's Encrypt configuration
type Config struct {
	Domains        []DomainConfig
	CertDir        string // where certs are stored
	HAProxyCertDir string // directory for combined haproxy certs
	Staging        bool   // use Let's Encrypt staging environment
}

// DomainStatus represents the current state of SSL certificate for a domain
type DomainStatus struct {
	Domain           string
	Email            string
	ProviderType     string // DNS provider type
	AWSProfile       string // AWS profile used for DNS challenge
	CertExists       bool
	CertPath         string
	KeyPath          string
	HAProxyCertPath  string
	HAProxyCertReady bool
	ExpiryInfo       string
}

// Status represents the overall SSL status
type Status struct {
	LegoAvailable bool // Always true (compiled in)
	Domains       []DomainStatus
}

// Manager handles Let's Encrypt operations
type Manager struct {
	config Config
	acme   *acme.Client
}

// New creates a new Let's Encrypt manager
func New(cfg Config) *Manager {
	if cfg.CertDir == "" {
		cfg.CertDir = "/etc/letsencrypt"
	}
	if cfg.HAProxyCertDir == "" {
		cfg.HAProxyCertDir = "/etc/haproxy/certs"
	}

	// Create ACME client with account storage in cert dir
	accountDir := filepath.Join(cfg.CertDir, "accounts")
	acmeClient := acme.NewClient(accountDir, cfg.Staging)

	return &Manager{
		config: cfg,
		acme:   acmeClient,
	}
}

// GetStatus returns the current SSL status
func (m *Manager) GetStatus() Status {
	status := Status{
		LegoAvailable: true, // Lego is compiled in
	}

	// Check status of each domain
	for _, d := range m.config.Domains {
		ds := m.GetDomainStatus(d)
		status.Domains = append(status.Domains, ds)
	}

	return status
}

// GetDomainStatus returns the status of a specific domain
func (m *Manager) GetDomainStatus(d DomainConfig) DomainStatus {
	ds := DomainStatus{
		Domain: d.Domain,
		Email:  d.Email,
	}

	// Get provider info from config
	if d.DNSProvider != nil {
		ds.ProviderType = string(d.DNSProvider.Type)
		ds.AWSProfile = d.DNSProvider.AWSProfile
	}

	// Get the domain without wildcard for cert path
	domain := strings.TrimPrefix(d.Domain, "*.")

	// Check if cert exists
	ds.CertPath = filepath.Join(m.config.CertDir, "live", domain, "fullchain.pem")
	ds.KeyPath = filepath.Join(m.config.CertDir, "live", domain, "privkey.pem")

	if _, err := os.Stat(ds.CertPath); err == nil {
		if _, err := os.Stat(ds.KeyPath); err == nil {
			ds.CertExists = true
		}
	}

	// Check HAProxy combined cert
	ds.HAProxyCertPath = filepath.Join(m.config.HAProxyCertDir, domain+".pem")
	if _, err := os.Stat(ds.HAProxyCertPath); err == nil {
		ds.HAProxyCertReady = true
	}

	// Get expiry info
	if ds.CertExists {
		cmd := exec.Command("openssl", "x509", "-enddate", "-noout", "-in", ds.CertPath)
		if out, err := cmd.Output(); err == nil {
			ds.ExpiryInfo = strings.TrimSpace(strings.TrimPrefix(string(out), "notAfter="))
		}
	}

	return ds
}

// LogFunc is a function that receives log lines
type LogFunc func(line string)

// RequestCertForDomain requests a certificate for a specific domain
func (m *Manager) RequestCertForDomain(d DomainConfig) error {
	return m.RequestCertForDomainWithLog(d, nil)
}

// RequestCertForDomainWithLog requests a certificate and streams output to a log function
func (m *Manager) RequestCertForDomainWithLog(d DomainConfig, logFn LogFunc) error {
	if d.Domain == "" {
		return fmt.Errorf("no domain specified")
	}
	if d.Email == "" {
		return fmt.Errorf("no email specified")
	}

	// Get DNS provider config
	if d.DNSProvider == nil {
		return fmt.Errorf("no DNS provider configured for domain %s", d.Domain)
	}
	providerCfg := d.DNSProvider

	// Convert to acme.DNSProviderConfig
	acmeProviderCfg := &acme.DNSProviderConfig{
		Type:               acme.DNSProviderType(providerCfg.Type),
		AWSAccessKeyID:     providerCfg.AWSAccessKeyID,
		AWSSecretAccessKey: providerCfg.AWSSecretAccessKey,
		AWSRegion:          providerCfg.AWSRegion,
		AWSHostedZoneID:    providerCfg.AWSHostedZoneID,
		AWSProfile:         providerCfg.AWSProfile,
		NamecomUsername:    providerCfg.NamecomUsername,
		NamecomAPIToken:    providerCfg.NamecomAPIToken,
		CloudflareAPIToken: providerCfg.CloudflareAPIToken,
		CloudflareZoneID:   providerCfg.CloudflareZoneID,
	}

	// Build domain list - primary domain plus any extra SANs
	// Note: ExtraSANs should include the base domain if needed (no auto-add)
	domains := []string{d.Domain}
	domains = append(domains, d.ExtraSANs...)

	// Request certificate using ACME client
	certs, err := m.acme.ObtainCertificate(d.Email, domains, acmeProviderCfg, logFn)
	if err != nil {
		return fmt.Errorf("failed to obtain certificate: %w", err)
	}

	// Save certificates in certbot-compatible layout
	baseDomain := strings.TrimPrefix(d.Domain, "*.")
	certDir := filepath.Join(m.config.CertDir, "live", baseDomain)

	if err := os.MkdirAll(certDir, 0700); err != nil {
		return fmt.Errorf("creating cert directory: %w", err)
	}

	// Save fullchain.pem (certificate)
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), certs.Certificate, 0600); err != nil {
		return fmt.Errorf("writing certificate: %w", err)
	}

	// Save privkey.pem (private key)
	if err := os.WriteFile(filepath.Join(certDir, "privkey.pem"), certs.PrivateKey, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	// Save issuer cert if available
	if certs.IssuerCertificate != nil {
		if err := os.WriteFile(filepath.Join(certDir, "chain.pem"), certs.IssuerCertificate, 0600); err != nil {
			return fmt.Errorf("writing issuer certificate: %w", err)
		}
	}

	if logFn != nil {
		logFn("Packaging certificate for HAProxy...")
	}

	// Package cert for HAProxy
	return m.PackageForHAProxyDomain(d.Domain)
}

// NeedsRenewal reports whether a domain's certificate expires within the given
// number of days. Returns true when the cert doesn't exist (needs initial
// issuance) or when the NotAfter timestamp is within the window.
func (m *Manager) NeedsRenewal(d DomainConfig, withinDays int) bool {
	baseDomain := strings.TrimPrefix(d.Domain, "*.")
	certPath := filepath.Join(m.config.CertDir, "live", baseDomain, "fullchain.pem")

	data, err := os.ReadFile(certPath)
	if err != nil {
		return true // cert doesn't exist
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return true
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}

	return time.Until(cert.NotAfter) < time.Duration(withinDays)*24*time.Hour
}

// PackageForHAProxyDomain combines cert and key into a single PEM for HAProxy
func (m *Manager) PackageForHAProxyDomain(domain string) error {
	baseDomain := strings.TrimPrefix(domain, "*.")
	certPath := filepath.Join(m.config.CertDir, "live", baseDomain, "fullchain.pem")
	keyPath := filepath.Join(m.config.CertDir, "live", baseDomain, "privkey.pem")

	// Read cert and key
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("reading cert: %w", err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading key: %w", err)
	}

	// Ensure HAProxy cert dir exists
	if err := os.MkdirAll(m.config.HAProxyCertDir, 0700); err != nil {
		return fmt.Errorf("creating haproxy cert dir: %w", err)
	}

	// Write combined PEM (cert + key)
	combined := append(cert, key...)
	outPath := filepath.Join(m.config.HAProxyCertDir, baseDomain+".pem")
	if err := os.WriteFile(outPath, combined, 0600); err != nil {
		return fmt.Errorf("writing combined cert: %w", err)
	}

	return nil
}

// PackageAllForHAProxy packages all configured domain certs for HAProxy
func (m *Manager) PackageAllForHAProxy() error {
	for _, d := range m.config.Domains {
		if err := m.PackageForHAProxyDomain(d.Domain); err != nil {
			// Continue with other domains even if one fails
			fmt.Printf("Warning: failed to package cert for %s: %v\n", d.Domain, err)
		}
	}
	return nil
}

// CertInfo holds detailed certificate information
type CertInfo struct {
	Domain    string   `json:"domain"`
	SANs      []string `json:"sans"` // Subject Alternative Names
	Issuer    string   `json:"issuer"`
	NotBefore string   `json:"not_before"`
	NotAfter  string   `json:"not_after"`
	Serial    string   `json:"serial"`
}

// GetCertInfo returns detailed information about a certificate
func GetCertInfo(certPath string) (*CertInfo, error) {
	info := &CertInfo{}

	// Get subject (CN)
	cmd := exec.Command("openssl", "x509", "-in", certPath, "-noout", "-subject")
	if out, err := cmd.Output(); err == nil {
		subject := strings.TrimSpace(string(out))
		// Extract CN from "subject=CN = *.example.com" or "subject= /CN=*.example.com"
		if idx := strings.Index(subject, "CN"); idx != -1 {
			cn := subject[idx+2:]
			cn = strings.TrimPrefix(cn, " = ")
			cn = strings.TrimPrefix(cn, "=")
			cn = strings.TrimSpace(cn)
			if commaIdx := strings.Index(cn, ","); commaIdx != -1 {
				cn = cn[:commaIdx]
			}
			info.Domain = cn
		}
	}

	// Get SANs
	cmd = exec.Command("openssl", "x509", "-in", certPath, "-noout", "-ext", "subjectAltName")
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "DNS:") || strings.Contains(line, "DNS:") {
				// Parse "DNS:*.example.com, DNS:example.com" format
				parts := strings.Split(line, ",")
				for _, part := range parts {
					part = strings.TrimSpace(part)
					if strings.HasPrefix(part, "DNS:") {
						san := strings.TrimPrefix(part, "DNS:")
						info.SANs = append(info.SANs, san)
					}
				}
			}
		}
	}

	// Get issuer
	cmd = exec.Command("openssl", "x509", "-in", certPath, "-noout", "-issuer")
	if out, err := cmd.Output(); err == nil {
		issuer := strings.TrimSpace(string(out))
		issuer = strings.TrimPrefix(issuer, "issuer=")
		issuer = strings.TrimSpace(issuer)
		info.Issuer = issuer
	}

	// Get validity dates
	cmd = exec.Command("openssl", "x509", "-in", certPath, "-noout", "-dates")
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "notBefore=") {
				info.NotBefore = strings.TrimPrefix(line, "notBefore=")
			} else if strings.HasPrefix(line, "notAfter=") {
				info.NotAfter = strings.TrimPrefix(line, "notAfter=")
			}
		}
	}

	// Get serial
	cmd = exec.Command("openssl", "x509", "-in", certPath, "-noout", "-serial")
	if out, err := cmd.Output(); err == nil {
		serial := strings.TrimSpace(string(out))
		serial = strings.TrimPrefix(serial, "serial=")
		info.Serial = serial
	}

	return info, nil
}

// GetCertInfoForDomain returns cert info for a domain managed by this manager
func (m *Manager) GetCertInfoForDomain(domain string) (*CertInfo, error) {
	baseDomain := strings.TrimPrefix(domain, "*.")
	certPath := filepath.Join(m.config.CertDir, "live", baseDomain, "fullchain.pem")

	if _, err := os.Stat(certPath); err != nil {
		return nil, fmt.Errorf("certificate not found: %s", certPath)
	}

	return GetCertInfo(certPath)
}

// CheckCertSANs checks if an existing certificate has all expected SANs
// Returns (hasCert, missingSANs, error)
func (m *Manager) CheckCertSANs(d DomainConfig) (bool, []string, error) {
	info, err := m.GetCertInfoForDomain(d.Domain)
	if err != nil {
		return false, nil, nil // Cert doesn't exist
	}

	// Build expected SANs set
	expected := make(map[string]bool)

	// Primary wildcard and its base
	expected[d.Domain] = true
	if strings.HasPrefix(d.Domain, "*.") {
		expected[strings.TrimPrefix(d.Domain, "*.")] = true
	}

	// Extra SANs (sub-zone wildcards only, not their bases)
	for _, san := range d.ExtraSANs {
		expected[san] = true
	}

	// Build actual SANs set
	actual := make(map[string]bool)
	for _, san := range info.SANs {
		actual[san] = true
	}

	// Find missing SANs
	var missing []string
	for san := range expected {
		if !actual[san] {
			missing = append(missing, san)
		}
	}

	return true, missing, nil
}

// GetAWSProfiles returns a list of available AWS profiles
func GetAWSProfiles() []string {
	var profiles []string

	// Check both credentials and config files
	homeDir, _ := os.UserHomeDir()
	credPaths := []string{
		filepath.Join(homeDir, ".aws", "credentials"),
		filepath.Join(homeDir, ".aws", "config"),
		"/root/.aws/credentials",
		"/root/.aws/config",
	}

	seen := make(map[string]bool)
	for _, path := range credPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				profile := strings.TrimPrefix(strings.TrimSuffix(line, "]"), "[")
				// Config file uses "profile name" format
				profile = strings.TrimPrefix(profile, "profile ")
				if profile != "" && !seen[profile] {
					profiles = append(profiles, profile)
					seen[profile] = true
				}
			}
		}
	}

	// Always include "default" as an option
	if !seen["default"] {
		profiles = append([]string{"default"}, profiles...)
	}

	return profiles
}
