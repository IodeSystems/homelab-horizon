package acme

import (
	"fmt"
	"os"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/namedotcom"
	"github.com/go-acme/lego/v4/providers/dns/route53"
)

// LoggingProvider wraps a DNS provider to add logging
type LoggingProvider struct {
	provider challenge.Provider
	logFn    func(string)
}

func (p *LoggingProvider) Present(domain, token, keyAuth string) error {
	// Extract the challenge record name from the domain
	fqdn := fmt.Sprintf("_acme-challenge.%s", domain)
	p.logFn(fmt.Sprintf("  Creating DNS TXT record: %s", fqdn))

	start := time.Now()
	err := p.provider.Present(domain, token, keyAuth)
	duration := time.Since(start).Round(time.Millisecond)

	if err != nil {
		p.logFn(fmt.Sprintf("  ✗ Failed to create DNS record (%v): %v", duration, err))
		return err
	}

	p.logFn(fmt.Sprintf("  ✓ DNS record created (%v)", duration))
	p.logFn("  Waiting for DNS propagation (this may take 30-120 seconds)...")
	return nil
}

func (p *LoggingProvider) CleanUp(domain, token, keyAuth string) error {
	fqdn := fmt.Sprintf("_acme-challenge.%s", domain)
	p.logFn(fmt.Sprintf("  Cleaning up DNS TXT record: %s", fqdn))

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
	if t, ok := p.provider.(interface{ Timeout() (time.Duration, time.Duration) }); ok {
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

// DNSProviderType identifies the DNS provider for ACME challenges
type DNSProviderType string

const (
	DNSProviderRoute53    DNSProviderType = "route53"
	DNSProviderNamecom    DNSProviderType = "namecom"
	DNSProviderCloudflare DNSProviderType = "cloudflare"
)

// DNSProviderConfig holds provider-specific credentials for ACME challenges
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

// createRoute53Provider creates a Lego Route53 provider
func createRoute53Provider(cfg *DNSProviderConfig) (challenge.Provider, error) {
	// Set environment variables for lego's route53 provider
	// The provider reads these during initialization
	if cfg.AWSAccessKeyID != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", cfg.AWSAccessKeyID)
	}
	if cfg.AWSSecretAccessKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", cfg.AWSSecretAccessKey)
	}
	if cfg.AWSRegion != "" {
		os.Setenv("AWS_REGION", cfg.AWSRegion)
	}
	if cfg.AWSHostedZoneID != "" {
		os.Setenv("AWS_HOSTED_ZONE_ID", cfg.AWSHostedZoneID)
	}
	if cfg.AWSProfile != "" {
		os.Setenv("AWS_PROFILE", cfg.AWSProfile)
	}

	provider, err := route53.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to create route53 provider: %w", err)
	}

	return provider, nil
}

// createNamecomProvider creates a Lego Name.com provider
func createNamecomProvider(cfg *DNSProviderConfig) (challenge.Provider, error) {
	// Set environment variables for lego's namedotcom provider
	if cfg.NamecomUsername != "" {
		os.Setenv("NAMECOM_USERNAME", cfg.NamecomUsername)
	}
	if cfg.NamecomAPIToken != "" {
		os.Setenv("NAMECOM_API_TOKEN", cfg.NamecomAPIToken)
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
		os.Setenv("CF_DNS_API_TOKEN", cfg.CloudflareAPIToken)
	}
	if cfg.CloudflareZoneID != "" {
		os.Setenv("CF_ZONE_API_TOKEN", cfg.CloudflareAPIToken) // Same token for zone API
	}

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare provider: %w", err)
	}

	return provider, nil
}

// ProviderName returns the name of the provider for a given config
func ProviderName(cfg *DNSProviderConfig) string {
	if cfg == nil {
		return "unknown"
	}
	return string(cfg.Type)
}
