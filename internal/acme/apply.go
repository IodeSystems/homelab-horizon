package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/namedotcom"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/registration"
)

// This file is the PRIVILEGED half of the package: the whole list of things
// that leave the process.
//
//   - talk to Let's Encrypt: register an account, request a certificate
//   - MINT an account key, and read and write it at <certDir>/accounts/
//     (account.key, 0600) — the only key material this package handles
//   - write DNS challenge records through a provider's API, using credentials
//     from the config, and delete them again
//   - set process-global AWS_*/NAMECOM_*/CF_* environment variables, because
//     that is the only interface lego's providers offer
//   - run `aws route53 get-hosted-zone` and `dig` against public DNS
//
// Nothing here decides anything an operator reads: which lines describe a
// provider, what a failure probably means, whether a zone looks delegated —
// all of that is render.go, which is pure. When this half moves into hz-agent
// (plan/architecture.md, phase 4, item 12 step 3), this list is what moves, and
// it is the list that decides whether it may: see plan/privilege-audit.md.

// ObtainCertificate requests a certificate for the given domains
func (c *Client) ObtainCertificate(email string, domains []string, providerCfg *DNSProviderConfig, logFn func(string)) (*certificate.Resource, error) {
	if logFn == nil {
		logFn = func(s string) {}
	}

	logFn(fmt.Sprintf("Using DNS provider: %s", ProviderName(providerCfg)))

	// Log provider config details (without secrets). The head/tail split is
	// where the hosted-zone lookup goes — see ProviderSummary.
	head, tail := ProviderSummary(providerCfg)
	for _, line := range head {
		logFn(line)
	}
	if ZoneVerifiable(providerCfg) {
		// Verify the zone exists and get its name
		zoneName, err := verifyRoute53Zone(providerCfg.AWSHostedZoneID, providerCfg.AWSProfile)
		if err != nil {
			logFn(fmt.Sprintf("  ⚠ Zone verification failed: %v", err))
		} else {
			logFn(fmt.Sprintf("  Zone name: %s", zoneName))
			// Check if domain has valid SOA record (is resolvable)
			if err := checkDomainSOA(zoneName, logFn); err != nil {
				return nil, err
			}
		}
	}
	for _, line := range tail {
		logFn(line)
	}

	// Create DNS challenge provider with logging
	dnsProvider, err := CreateChallengeProvider(providerCfg, logFn)
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS provider: %w", err)
	}

	// Load or create user
	user, err := c.loadOrCreateUser(email)
	if err != nil {
		return nil, fmt.Errorf("failed to load/create ACME user: %w", err)
	}

	// Configure lego client
	legoConfig := lego.NewConfig(user)
	legoConfig.Certificate.KeyType = certcrypto.RSA2048

	if c.staging {
		legoConfig.CADirURL = lego.LEDirectoryStaging
		logFn("Using Let's Encrypt STAGING environment")
	} else {
		logFn("Using Let's Encrypt PRODUCTION environment")
	}

	client, err := lego.NewClient(legoConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Set DNS provider (vanilla Lego)
	if err := client.Challenge.SetDNS01Provider(dnsProvider); err != nil {
		return nil, fmt.Errorf("failed to set DNS provider: %w", err)
	}

	// Register if needed
	if user.Registration == nil {
		logFn("Registering ACME account...")
		reg, err := client.Registration.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to register: %w", err)
		}
		user.Registration = reg
		if err := c.saveUser(user); err != nil {
			logFn(fmt.Sprintf("Warning: failed to save user registration: %v", err))
		}
	}

	logFn(fmt.Sprintf("Requesting certificate for: %v", domains))
	logFn("Starting ACME challenge process...")
	logFn(fmt.Sprintf("Staging all %d DNS challenge record(s), then checking propagation once (30-120s)...", len(domains)))

	// Request certificate
	request := certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	}

	start := time.Now()
	certificates, err := client.Certificate.Obtain(request)
	duration := time.Since(start).Round(time.Second)

	if err != nil {
		logFn(fmt.Sprintf("Certificate request failed after %v", duration))
		// Try to extract more useful error info
		for _, line := range ObtainFailureHints(err.Error()) {
			logFn(line)
		}
		return nil, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	logFn(fmt.Sprintf("✓ Certificate obtained successfully in %v", duration))

	return certificates, nil
}

func (c *Client) loadOrCreateUser(email string) (*User, error) {
	if err := os.MkdirAll(c.accountDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create account directory: %w", err)
	}

	accountFile := filepath.Join(c.accountDir, "account.json")
	keyFile := filepath.Join(c.accountDir, "account.key")

	user := &User{Email: email}

	// Try to load existing user
	if _, err := os.Stat(accountFile); err == nil {
		data, err := os.ReadFile(accountFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read account file: %w", err)
		}
		if err := json.Unmarshal(data, user); err != nil {
			return nil, fmt.Errorf("failed to parse account file: %w", err)
		}
	}

	// Try to load existing key
	if _, err := os.Stat(keyFile); err == nil {
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read key file: %w", err)
		}
		block, _ := pem.Decode(keyPEM)
		if block == nil {
			return nil, fmt.Errorf("failed to decode PEM block from key file")
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse EC private key: %w", err)
		}
		user.key = key
	} else {
		// Generate new key
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}
		user.key = key

		// Save the key
		keyBytes, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal key: %w", err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
		if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
			return nil, fmt.Errorf("failed to save key: %w", err)
		}
	}

	return user, nil
}

// saveUser saves the user registration to disk
func (c *Client) saveUser(user *User) error {
	accountFile := filepath.Join(c.accountDir, "account.json")
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}
	return os.WriteFile(accountFile, data, 0600)
}

// verifyRoute53Zone checks if a Route53 zone exists and returns its name
func verifyRoute53Zone(zoneID, awsProfile string) (string, error) {
	args := []string{
		"route53", "get-hosted-zone",
		"--id", NormalizeZoneID(zoneID),
		"--query", "HostedZone.Name",
		"--output", "text",
	}

	cmd := exec.Command("aws", args...)
	if awsProfile != "" {
		cmd.Env = append(os.Environ(), "AWS_PROFILE="+awsProfile)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("aws cli error: %s", strings.TrimSpace(string(output)))
	}

	return ParseZoneName(string(output)), nil
}

// checkDomainSOA checks if a domain has a valid SOA record (is properly configured in DNS)
// Returns an error if the domain is not properly delegated.
//
// The two `dig` calls are here; both verdicts are in render.go (SOAReport,
// NSReport), so what the operator is told about a broken delegation can be
// tested without one.
func checkDomainSOA(domain string, logFn func(string)) error {
	// Use dig to check SOA record
	cmd := exec.Command("dig", "+short", "SOA", domain, "@8.8.8.8")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logFn(fmt.Sprintf("  ⚠ Could not check SOA for %s: %v", domain, err))
		// Don't fail on dig errors, continue with cert request
		return nil
	}

	lines, needNS := SOAReport(string(output))
	for _, line := range lines {
		logFn(line)
	}
	if !needNS {
		return nil
	}

	// Also check NS records
	cmd = exec.Command("dig", "+short", "NS", domain, "@8.8.8.8")
	nsOutput, _ := cmd.CombinedOutput()
	lines, err = NSReport(domain, string(nsOutput))
	for _, line := range lines {
		logFn(line)
	}
	return err
}

// LoggingProvider wraps a DNS provider to add logging
type LoggingProvider struct {
	provider challenge.Provider
	logFn    func(string)
}

func (p *LoggingProvider) Present(domain, token, keyAuth string) error {
	// Extract the challenge record name from the domain
	p.logFn(fmt.Sprintf("  Creating DNS TXT record: %s", ChallengeRecordName(domain)))

	start := time.Now()
	err := p.provider.Present(domain, token, keyAuth)
	duration := time.Since(start).Round(time.Millisecond)

	if err != nil {
		p.logFn(fmt.Sprintf("  ✗ Failed to create DNS record (%v): %v", duration, err))
		return err
	}

	// Note: lego presents ALL challenge records first, then waits for
	// propagation once (parallelSolve). Do not log a per-record "waiting"
	// message here — the wait happens later, once, after every record below
	// is staged. Logging it per Present made a single batched wait look like
	// one serial wait per record.
	p.logFn(fmt.Sprintf("  ✓ DNS record staged (%v)", duration))
	return nil
}

func (p *LoggingProvider) CleanUp(domain, token, keyAuth string) error {
	p.logFn(fmt.Sprintf("  Cleaning up DNS TXT record: %s", ChallengeRecordName(domain)))

	start := time.Now()
	err := p.provider.CleanUp(domain, token, keyAuth)
	duration := time.Since(start).Round(time.Millisecond)

	if err != nil {
		p.logFn(fmt.Sprintf("  ⚠ Failed to clean up DNS record (%v): %v", duration, err))
		return err
	}

	p.logFn(fmt.Sprintf("  ✓ DNS record cleaned up (%v)", duration))
	return nil
}

// Timeout returns the timeout and interval for DNS propagation checks
func (p *LoggingProvider) Timeout() (timeout, interval time.Duration) {
	// Check if underlying provider has custom timeout
	if t, ok := p.provider.(interface {
		Timeout() (time.Duration, time.Duration)
	}); ok {
		return t.Timeout()
	}
	// Default timeout of 2 minutes with 5 second intervals
	return 2 * time.Minute, 5 * time.Second
}

// wrapWithLogging wraps a provider with logging
func wrapWithLogging(provider challenge.Provider, logFn func(string)) challenge.Provider {
	if logFn == nil {
		return provider
	}
	return &LoggingProvider{provider: provider, logFn: logFn}
}

// CreateChallengeProvider creates a Lego DNS challenge provider from configuration
func CreateChallengeProvider(cfg *DNSProviderConfig, logFn func(string)) (challenge.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("dns provider config is nil")
	}

	var provider challenge.Provider
	var err error

	switch cfg.Type {
	case DNSProviderRoute53:
		provider, err = createRoute53Provider(cfg)
	case DNSProviderNamecom:
		provider, err = createNamecomProvider(cfg)
	case DNSProviderCloudflare:
		provider, err = createCloudflareProvider(cfg)
	default:
		return nil, fmt.Errorf("unknown dns provider type for ACME: %s", cfg.Type)
	}

	if err != nil {
		return nil, err
	}

	// Wrap with logging if logFn provided
	return wrapWithLogging(provider, logFn), nil
}

// createRoute53Provider creates a Lego Route53 provider.
//
// Credentials, profile, and region flow through the AWS default credential chain
// via environment variables. These are process-global, but they are identical
// across every zone in a single sync, so setting them repeatedly is race-free
// even when several certificates are obtained concurrently (the same value is
// written every time).
//
// The per-zone HostedZoneID is the ONE value that differs between zones, so it is
// passed on the provider Config below rather than via a shared AWS_HOSTED_ZONE_ID
// env var. If it went through the environment, two concurrent obtains would clobber
// each other's zone and could write challenge records to the wrong hosted zone.
func createRoute53Provider(cfg *DNSProviderConfig) (challenge.Provider, error) {
	if cfg.AWSAccessKeyID != "" {
		if err := os.Setenv("AWS_ACCESS_KEY_ID", cfg.AWSAccessKeyID); err != nil {
			return nil, fmt.Errorf("setenv AWS_ACCESS_KEY_ID: %w", err)
		}
	}
	if cfg.AWSSecretAccessKey != "" {
		if err := os.Setenv("AWS_SECRET_ACCESS_KEY", cfg.AWSSecretAccessKey); err != nil {
			return nil, fmt.Errorf("setenv AWS_SECRET_ACCESS_KEY: %w", err)
		}
	}
	if cfg.AWSProfile != "" {
		if err := os.Setenv("AWS_PROFILE", cfg.AWSProfile); err != nil {
			return nil, fmt.Errorf("setenv AWS_PROFILE: %w", err)
		}
	}

	rcfg := route53.NewDefaultConfig()
	// Pin the hosted zone per provider (not via env) so parallel obtains don't race.
	// Setting it also skips lego's ListHostedZonesByName lookup at challenge time.
	if cfg.AWSHostedZoneID != "" {
		rcfg.HostedZoneID = cfg.AWSHostedZoneID
	}
	if cfg.AWSRegion != "" {
		rcfg.Region = cfg.AWSRegion
	}
	// Route53 changes can take time to propagate; allow up to 5 minutes.
	rcfg.PropagationTimeout = 5 * time.Minute
	rcfg.PollingInterval = 10 * time.Second

	provider, err := route53.NewDNSProviderConfig(rcfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create route53 provider: %w", err)
	}

	return provider, nil
}

// createNamecomProvider creates a Lego Name.com provider
func createNamecomProvider(cfg *DNSProviderConfig) (challenge.Provider, error) {
	// Set environment variables for lego's namedotcom provider
	if cfg.NamecomUsername != "" {
		if err := os.Setenv("NAMECOM_USERNAME", cfg.NamecomUsername); err != nil {
			return nil, fmt.Errorf("setenv NAMECOM_USERNAME: %w", err)
		}
	}
	if cfg.NamecomAPIToken != "" {
		if err := os.Setenv("NAMECOM_API_TOKEN", cfg.NamecomAPIToken); err != nil {
			return nil, fmt.Errorf("setenv NAMECOM_API_TOKEN: %w", err)
		}
	}

	provider, err := namedotcom.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to create namecom provider: %w", err)
	}

	return provider, nil
}

// createCloudflareProvider creates a Lego Cloudflare provider
func createCloudflareProvider(cfg *DNSProviderConfig) (challenge.Provider, error) {
	// Set environment variables for lego's cloudflare provider
	if cfg.CloudflareAPIToken != "" {
		if err := os.Setenv("CF_DNS_API_TOKEN", cfg.CloudflareAPIToken); err != nil {
			return nil, fmt.Errorf("setenv CF_DNS_API_TOKEN: %w", err)
		}
	}
	if cfg.CloudflareZoneID != "" {
		if err := os.Setenv("CF_ZONE_API_TOKEN", cfg.CloudflareAPIToken); err != nil { // Same token for zone API
			return nil, fmt.Errorf("setenv CF_ZONE_API_TOKEN: %w", err)
		}
	}

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare provider: %w", err)
	}

	return provider, nil
}
