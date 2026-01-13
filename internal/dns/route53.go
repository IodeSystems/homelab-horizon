package dns

import (
	"context"
	"fmt"
	"strings"

	"homelab-horizon/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/libdns/libdns"
	libdnsroute53 "github.com/libdns/route53"
)

// Route53Wrapper wraps the libdns route53 provider and adds zone listing support
type Route53Wrapper struct {
	*libdnsroute53.Provider
	cfg *config.DNSProviderConfig
}

// Route53ZoneWithID represents a Route53 zone with its hosted zone ID
type Route53ZoneWithID struct {
	ID   string // AWS hosted zone ID (e.g., "Z1234567890ABC")
	Name string // Zone name (e.g., "example.com.")
}

// ListZonesWithIDs lists Route53 zones with their hosted zone IDs
func (w *Route53Wrapper) ListZonesWithIDs(ctx context.Context) ([]Route53ZoneWithID, error) {
	// Build AWS config
	opts := []func(*awsconfig.LoadOptions) error{}

	if w.cfg.AWSRegion != "" {
		opts = append(opts, awsconfig.WithRegion(w.cfg.AWSRegion))
	}

	if w.cfg.AWSProfile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(w.cfg.AWSProfile))
	}

	if w.cfg.AWSAccessKeyID != "" && w.cfg.AWSSecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				w.cfg.AWSAccessKeyID,
				w.cfg.AWSSecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := route53.NewFromConfig(awsCfg)

	var zones []Route53ZoneWithID
	var marker *string

	for {
		input := &route53.ListHostedZonesInput{
			Marker: marker,
		}

		output, err := client.ListHostedZones(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to list hosted zones: %w", err)
		}

		for _, hz := range output.HostedZones {
			// Extract the hosted zone ID (strip /hostedzone/ prefix if present)
			zoneID := aws.ToString(hz.Id)
			zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")

			zones = append(zones, Route53ZoneWithID{
				ID:   zoneID,
				Name: aws.ToString(hz.Name),
			})
		}

		if !output.IsTruncated {
			break
		}
		marker = output.NextMarker
	}

	return zones, nil
}

// ListZones implements ZoneLister for Route53 using the AWS SDK
func (w *Route53Wrapper) ListZones(ctx context.Context) ([]libdns.Zone, error) {
	zonesWithIDs, err := w.ListZonesWithIDs(ctx)
	if err != nil {
		return nil, err
	}

	var zones []libdns.Zone
	for _, z := range zonesWithIDs {
		zones = append(zones, libdns.Zone{
			Name: z.Name,
		})
	}
	return zones, nil
}

// NewRoute53Provider creates a new Route53 DNS provider using libdns
// ZoneName is optional for zone discovery (ListZones) but required for record operations
func NewRoute53Provider(cfg *config.DNSProviderConfig) (Provider, error) {
	// Either AWS profile or explicit credentials are required
	if cfg.AWSProfile == "" && (cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "") {
		return nil, fmt.Errorf("route53 requires aws_profile or aws_access_key_id + aws_secret_access_key")
	}

	libdnsProvider := &libdnsroute53.Provider{
		Profile:         cfg.AWSProfile,
		AccessKeyId:     cfg.AWSAccessKeyID,
		SecretAccessKey: cfg.AWSSecretAccessKey,
		Region:          cfg.AWSRegion,
		HostedZoneID:    cfg.AWSHostedZoneID,
	}

	// Wrap to add ListZones support
	wrapper := &Route53Wrapper{
		Provider: libdnsProvider,
		cfg:      cfg,
	}

	// Create adapter with the wrapper (which implements both Provider and ZoneLister interfaces)
	zone := cfg.ZoneName
	if zone != "" && !strings.HasSuffix(zone, ".") {
		zone += "."
	}

	return &LibdnsAdapter{
		name:        "route53",
		zone:        zone,
		provider:    wrapper,
		rawProvider: wrapper, // wrapper implements ZoneLister
	}, nil
}
