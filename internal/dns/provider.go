package dns

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"homelab-horizon/internal/config"
)

// Zone represents a DNS zone
type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DNSManaged  bool     `json:"dns_managed,omitempty"` // Whether DNS is managed by this provider
	Nameservers []string `json:"nameservers,omitempty"` // Nameservers for the zone (if known)
}

// Record represents a DNS record
type Record struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // A, AAAA, CNAME, TXT
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
	ZoneID string `json:"zone_id"`
}

// Provider defines the interface for DNS operations
type Provider interface {
	// Name returns the provider identifier
	Name() string

	// ListZones returns available DNS zones
	ListZones() ([]Zone, error)

	// GetRecord retrieves a DNS record
	GetRecord(zoneID, name, recordType string) (*Record, error)

	// CreateRecord creates a new DNS record
	CreateRecord(zoneID string, record Record) error

	// UpdateRecord updates an existing DNS record (or creates if not exists)
	UpdateRecord(zoneID string, record Record) error

	// DeleteRecord deletes a DNS record
	DeleteRecord(zoneID, name, recordType string) error

	// SyncRecord creates or updates a record, returns true if changed
	SyncRecord(zoneID string, record Record) (changed bool, err error)

	// SyncRecordSet creates or updates a set of A records with the same name (round-robin DNS).
	// All values are set atomically so the provider treats them as a single record set.
	SyncRecordSet(zoneID string, records []Record) (changed bool, err error)
}

// NewProvider creates a DNS provider from configuration
func NewProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("dns provider config is nil")
	}

	switch cfg.Type {
	case config.DNSProviderRoute53:
		return NewRoute53Provider(cfg)
	case config.DNSProviderNamecom:
		return NewNamecomProvider(cfg)
	case config.DNSProviderCloudflare:
		return NewCloudflareProvider(cfg)
	case config.DNSProviderDigitalOcean:
		return NewDigitalOceanProvider(cfg)
	case config.DNSProviderHetzner:
		return NewHetznerProvider(cfg)
	case config.DNSProviderGandi:
		return NewGandiProvider(cfg)
	case config.DNSProviderGoogleCloud:
		return NewGoogleCloudProvider(cfg)
	case config.DNSProviderDuckDNS:
		return NewDuckDNSProvider(cfg)
	default:
		return nil, fmt.Errorf("unknown dns provider type: %s", cfg.Type)
	}
}

// GetPublicIP gets the current public IP address using external services
func GetPublicIP() (string, error) {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error

	for _, svc := range services {
		resp, err := client.Get(svc)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", svc, err)
			continue
		}

		if resp.StatusCode == 200 {
			buf := make([]byte, 64)
			n, _ := resp.Body.Read(buf)
			resp.Body.Close()
			ip := strings.TrimSpace(string(buf[:n]))
			if net.ParseIP(ip) != nil {
				return ip, nil
			}
			lastErr = fmt.Errorf("%s: invalid IP response: %q", svc, ip)
		} else {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: status %d", svc, resp.StatusCode)
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("could not determine public IP: %w", lastErr)
	}
	return "", fmt.Errorf("could not determine public IP: all services failed")
}
