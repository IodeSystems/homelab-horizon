package dns

import (
	"fmt"

	"github.com/iodesystems/homelab-horizon/internal/config"

	"github.com/libdns/digitalocean"
	"github.com/libdns/duckdns"
	"github.com/libdns/gandi"
	"github.com/libdns/googleclouddns"
	"github.com/libdns/hetzner"
)

// NewDigitalOceanProvider creates a new DigitalOcean DNS provider using libdns
func NewDigitalOceanProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("digitalocean requires api_token")
	}

	provider := &digitalocean.Provider{
		APIToken: cfg.APIToken,
	}

	return NewLibdnsAdapter("digitalocean", cfg.ZoneName, provider), nil
}

// NewHetznerProvider creates a new Hetzner DNS provider using libdns
func NewHetznerProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("hetzner requires api_token")
	}

	provider := hetzner.New(cfg.APIToken)

	return NewLibdnsAdapter("hetzner", cfg.ZoneName, provider), nil
}

// NewGandiProvider creates a new Gandi DNS provider using libdns
func NewGandiProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("gandi requires api_token (bearer token)")
	}

	provider := &gandi.Provider{
		BearerToken: cfg.APIToken,
	}

	return NewLibdnsAdapter("gandi", cfg.ZoneName, provider), nil
}

// NewGoogleCloudProvider creates a new Google Cloud DNS provider using libdns
func NewGoogleCloudProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.GCPProject == "" {
		return nil, fmt.Errorf("googlecloud requires gcp_project")
	}

	provider := &googleclouddns.Provider{
		Project:            cfg.GCPProject,
		ServiceAccountJSON: cfg.GCPServiceAccountJSON,
	}

	return NewLibdnsAdapter("googlecloud", cfg.ZoneName, provider), nil
}

// NewDuckDNSProvider creates a new DuckDNS provider using libdns
// Note: DuckDNS only supports A/AAAA/TXT records for *.duckdns.org subdomains
func NewDuckDNSProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("duckdns requires api_token")
	}

	provider := &duckdns.Provider{
		APIToken: cfg.APIToken,
	}

	return NewLibdnsAdapter("duckdns", cfg.ZoneName, provider), nil
}
