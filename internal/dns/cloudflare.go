package dns

import (
	"fmt"

	"github.com/iodesystems/homelab-horizon/internal/config"

	"github.com/libdns/cloudflare"
)

// NewCloudflareProvider creates a new Cloudflare DNS provider using libdns
// ZoneName is optional for zone discovery (ListZones) but required for record operations
func NewCloudflareProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.CloudflareAPIToken == "" {
		return nil, fmt.Errorf("cloudflare requires cloudflare_api_token")
	}

	provider := &cloudflare.Provider{
		APIToken: cfg.CloudflareAPIToken,
	}

	// ZoneName is optional for discovery, required for record operations
	return NewLibdnsAdapter("cloudflare", cfg.ZoneName, provider), nil
}
