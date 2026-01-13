package dns

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

// LibdnsProvider is the interface that libdns providers must implement
type LibdnsProvider interface {
	libdns.RecordGetter
	libdns.RecordSetter
	libdns.RecordDeleter
}

// LibdnsZoneLister is an optional interface for providers that can list zones
type LibdnsZoneLister interface {
	libdns.ZoneLister
}

// LibdnsAdapter wraps any libdns-compatible provider to implement our Provider interface
type LibdnsAdapter struct {
	name        string
	zone        string // FQDN with trailing dot (e.g., "example.com.")
	provider    LibdnsProvider
	rawProvider any // Keep reference to original provider for interface checks
}

// NewLibdnsAdapter creates a new adapter wrapping a libdns provider
// zone can be empty for discovery-only operations (ListZones)
func NewLibdnsAdapter(name, zone string, provider LibdnsProvider) *LibdnsAdapter {
	// Ensure zone has trailing dot (FQDN format) if provided
	if zone != "" && !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	return &LibdnsAdapter{
		name:        name,
		zone:        zone,
		provider:    provider,
		rawProvider: provider,
	}
}

// Name returns the provider identifier
func (p *LibdnsAdapter) Name() string {
	return p.name
}

func (p *LibdnsAdapter) log(action string) {
	fmt.Printf("[%s] %s\n", p.name, action)
}

// Route53ZoneLister is an interface for providers that can list zones with IDs (Route53-specific)
type Route53ZoneLister interface {
	ListZonesWithIDs(ctx context.Context) ([]Route53ZoneWithID, error)
}

// ListZones returns available zones from the provider
func (p *LibdnsAdapter) ListZones() ([]Zone, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check if provider implements Route53-specific zone lister with IDs
	if r53Lister, ok := p.rawProvider.(Route53ZoneLister); ok {
		p.log("Listing zones (with hosted zone IDs)...")
		r53Zones, err := r53Lister.ListZonesWithIDs(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list zones: %w", err)
		}

		var zones []Zone
		for _, z := range r53Zones {
			zones = append(zones, Zone{
				ID:         z.ID,
				Name:       strings.TrimSuffix(z.Name, "."),
				DNSManaged: true,
			})
		}
		return zones, nil
	}

	// Check if provider implements standard ZoneLister for discovery
	if lister, ok := p.rawProvider.(LibdnsZoneLister); ok {
		p.log("Listing zones...")
		libZones, err := lister.ListZones(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list zones: %w", err)
		}

		var zones []Zone
		for _, z := range libZones {
			zones = append(zones, Zone{
				ID:         z.Name,
				Name:       strings.TrimSuffix(z.Name, "."),
				DNSManaged: true,
			})
		}
		return zones, nil
	}

	// Fall back to returning the configured zone if no ZoneLister
	if p.zone == "" {
		return nil, fmt.Errorf("provider does not support zone listing and no zone configured")
	}
	return []Zone{{
		ID:         p.zone,
		Name:       strings.TrimSuffix(p.zone, "."),
		DNSManaged: true,
	}}, nil
}

// GetRecord retrieves a DNS record
func (p *LibdnsAdapter) GetRecord(zoneID, name, recordType string) (*Record, error) {
	p.log(fmt.Sprintf("Getting %s (%s)...", name, recordType))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	records, err := p.provider.GetRecords(ctx, p.zone)
	if err != nil {
		return nil, fmt.Errorf("failed to get records: %w", err)
	}

	// Convert to relative name for comparison
	relName := p.toRelativeName(name)

	for _, r := range records {
		rr := r.RR()
		if rr.Name == relName && rr.Type == recordType {
			return &Record{
				Name:   name,
				Type:   rr.Type,
				Value:  rr.Data,
				TTL:    int(rr.TTL.Seconds()),
				ZoneID: zoneID,
			}, nil
		}
	}

	return nil, nil // Not found
}

// CreateRecord creates a new DNS record
func (p *LibdnsAdapter) CreateRecord(zoneID string, record Record) error {
	p.log(fmt.Sprintf("Creating %s -> %s...", record.Name, record.Value))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	libRecord, err := p.toLibdnsRecord(record)
	if err != nil {
		p.log(fmt.Sprintf("Create %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to convert record: %w", err)
	}

	_, err = p.provider.SetRecords(ctx, p.zone, []libdns.Record{libRecord})
	if err != nil {
		p.log(fmt.Sprintf("Create %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to create record: %w", err)
	}

	p.log(fmt.Sprintf("Create %s SUCCESS", record.Name))
	return nil
}

// UpdateRecord updates an existing DNS record (or creates if not exists)
func (p *LibdnsAdapter) UpdateRecord(zoneID string, record Record) error {
	p.log(fmt.Sprintf("Updating %s -> %s...", record.Name, record.Value))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	libRecord, err := p.toLibdnsRecord(record)
	if err != nil {
		p.log(fmt.Sprintf("Update %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to convert record: %w", err)
	}

	// SetRecords handles both create and update
	_, err = p.provider.SetRecords(ctx, p.zone, []libdns.Record{libRecord})
	if err != nil {
		p.log(fmt.Sprintf("Update %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to update record: %w", err)
	}

	p.log(fmt.Sprintf("Update %s SUCCESS", record.Name))
	return nil
}

// DeleteRecord deletes a DNS record
func (p *LibdnsAdapter) DeleteRecord(zoneID, name, recordType string) error {
	p.log(fmt.Sprintf("Deleting %s (%s)...", name, recordType))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relName := p.toRelativeName(name)

	// Use RR with empty Data to match any value for this name/type
	_, err := p.provider.DeleteRecords(ctx, p.zone, []libdns.Record{
		libdns.RR{
			Name: relName,
			Type: recordType,
		},
	})
	if err != nil {
		p.log(fmt.Sprintf("Delete %s FAILED: %v", name, err))
		return fmt.Errorf("failed to delete record: %w", err)
	}

	p.log(fmt.Sprintf("Delete %s SUCCESS", name))
	return nil
}

// SyncRecord creates or updates a record, returns true if changed
func (p *LibdnsAdapter) SyncRecord(zoneID string, record Record) (changed bool, err error) {
	// Check current value
	currentRecord, err := p.GetRecord(zoneID, record.Name, record.Type)
	if err != nil {
		p.log(fmt.Sprintf("GetRecord error for %s: %v, will try to create", record.Name, err))
		return true, p.CreateRecord(zoneID, record)
	}

	if currentRecord == nil {
		// Record doesn't exist, create it
		return true, p.CreateRecord(zoneID, record)
	}

	if currentRecord.Value == record.Value {
		p.log(fmt.Sprintf("%s already set to %s", record.Name, record.Value))
		return false, nil
	}

	p.log(fmt.Sprintf("%s value mismatch: current=%q new=%q", record.Name, currentRecord.Value, record.Value))
	return true, p.UpdateRecord(zoneID, record)
}

// toRelativeName converts a FQDN or partial name to a relative name for libdns
func (p *LibdnsAdapter) toRelativeName(name string) string {
	// Remove trailing dot if present
	name = strings.TrimSuffix(name, ".")

	// If it's a full domain name, make it relative to the zone
	zoneName := strings.TrimSuffix(p.zone, ".")
	if strings.HasSuffix(name, "."+zoneName) {
		name = strings.TrimSuffix(name, "."+zoneName)
	} else if name == zoneName {
		name = "@"
	}

	return name
}

// toLibdnsRecord converts our Record to a libdns Record
func (p *LibdnsAdapter) toLibdnsRecord(record Record) (libdns.Record, error) {
	ttl := time.Duration(record.TTL) * time.Second
	if ttl == 0 {
		ttl = 300 * time.Second
	}

	relName := p.toRelativeName(record.Name)

	switch record.Type {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(record.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid IP address %q: %w", record.Value, err)
		}
		return libdns.Address{
			Name: relName,
			TTL:  ttl,
			IP:   ip,
		}, nil

	case "CNAME":
		target := record.Value
		// Ensure CNAME target is FQDN with trailing dot
		if !strings.HasSuffix(target, ".") {
			target += "."
		}
		return libdns.CNAME{
			Name:   relName,
			TTL:    ttl,
			Target: target,
		}, nil

	case "TXT":
		return libdns.TXT{
			Name: relName,
			TTL:  ttl,
			Text: record.Value,
		}, nil

	default:
		// Use generic RR for other record types
		return libdns.RR{
			Name: relName,
			TTL:  ttl,
			Type: record.Type,
			Data: record.Value,
		}, nil
	}
}
