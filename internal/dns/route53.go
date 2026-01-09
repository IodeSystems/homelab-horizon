package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"homelab-horizon/internal/config"
)

const awsTimeout = 30 * time.Second

// Route53Provider implements DNS operations using AWS Route53
type Route53Provider struct {
	awsProfile      string
	awsAccessKeyID  string
	awsSecretKey    string
	awsRegion       string
	awsHostedZoneID string
}

// NewRoute53Provider creates a new Route53 DNS provider
func NewRoute53Provider(cfg *config.DNSProviderConfig) (*Route53Provider, error) {
	return &Route53Provider{
		awsProfile:      cfg.AWSProfile,
		awsAccessKeyID:  cfg.AWSAccessKeyID,
		awsSecretKey:    cfg.AWSSecretAccessKey,
		awsRegion:       cfg.AWSRegion,
		awsHostedZoneID: cfg.AWSHostedZoneID,
	}, nil
}

// Name returns the provider identifier
func (p *Route53Provider) Name() string {
	return "route53"
}

// buildEnv creates the environment with AWS credentials
func (p *Route53Provider) buildEnv() []string {
	env := os.Environ()
	if p.awsProfile != "" {
		env = append(env, "AWS_PROFILE="+p.awsProfile)
	}
	if p.awsAccessKeyID != "" {
		env = append(env, "AWS_ACCESS_KEY_ID="+p.awsAccessKeyID)
	}
	if p.awsSecretKey != "" {
		env = append(env, "AWS_SECRET_ACCESS_KEY="+p.awsSecretKey)
	}
	if p.awsRegion != "" {
		env = append(env, "AWS_REGION="+p.awsRegion)
	}
	return env
}

func (p *Route53Provider) log(action string) {
	profile := p.awsProfile
	if profile == "" {
		profile = "default"
	}
	fmt.Printf("[AWS/%s] %s\n", profile, action)
}

// ListZones returns available Route53 hosted zones
func (p *Route53Provider) ListZones() ([]Zone, error) {
	p.log("Listing hosted zones...")

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{"route53", "list-hosted-zones", "--output", "json"}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = p.buildEnv()

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		return nil, fmt.Errorf("failed to list hosted zones: %w", err)
	}

	var result struct {
		HostedZones []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"HostedZones"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var zones []Zone
	for _, z := range result.HostedZones {
		// Clean up the ID (remove /hostedzone/ prefix)
		id := strings.TrimPrefix(z.ID, "/hostedzone/")
		zones = append(zones, Zone{
			ID:         id,
			Name:       strings.TrimSuffix(z.Name, "."),
			DNSManaged: true, // Route53 zones are always DNS-managed
		})
	}

	return zones, nil
}

// GetRecord retrieves the current value of a DNS record
func (p *Route53Provider) GetRecord(zoneID, name, recordType string) (*Record, error) {
	p.log(fmt.Sprintf("Getting current value for %s (%s)...", name, recordType))

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{
		"route53", "list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--query", fmt.Sprintf("ResourceRecordSets[?Name=='%s.' && Type=='%s'].ResourceRecords[0].Value", name, recordType),
		"--output", "text",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = p.buildEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		errMsg := strings.TrimSpace(string(output))
		if errMsg != "" {
			return nil, fmt.Errorf("%s: %s", err, errMsg)
		}
		return nil, err
	}

	value := strings.TrimSpace(string(output))
	if value == "None" || value == "" {
		p.log(fmt.Sprintf("Current value for %s: (empty)", name))
		return nil, nil
	}
	p.log(fmt.Sprintf("Current value for %s: %q", name, value))
	return &Record{
		Name:   name,
		Type:   recordType,
		Value:  value,
		ZoneID: zoneID,
	}, nil
}

// CreateRecord creates a new DNS record
func (p *Route53Provider) CreateRecord(zoneID string, record Record) error {
	return p.upsertRecord(zoneID, record)
}

// UpdateRecord updates an existing DNS record (or creates if not exists)
func (p *Route53Provider) UpdateRecord(zoneID string, record Record) error {
	return p.upsertRecord(zoneID, record)
}

func (p *Route53Provider) upsertRecord(zoneID string, record Record) error {
	p.log(fmt.Sprintf("Updating %s -> %s...", record.Name, record.Value))

	if record.TTL <= 0 {
		record.TTL = 300
	}

	// Ensure name ends with dot
	name := record.Name
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	changeBatch := map[string]interface{}{
		"Changes": []map[string]interface{}{
			{
				"Action": "UPSERT",
				"ResourceRecordSet": map[string]interface{}{
					"Name": name,
					"Type": record.Type,
					"TTL":  record.TTL,
					"ResourceRecords": []map[string]string{
						{"Value": record.Value},
					},
				},
			},
		},
	}

	changeBatchJSON, err := json.Marshal(changeBatch)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{
		"route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", string(changeBatchJSON),
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = p.buildEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			p.log(fmt.Sprintf("Update %s TIMEOUT after %v", record.Name, awsTimeout))
			return fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		p.log(fmt.Sprintf("Update %s FAILED: %s", record.Name, string(output)))
		return fmt.Errorf("failed to update record: %s", string(output))
	}

	p.log(fmt.Sprintf("Update %s SUCCESS", record.Name))
	return nil
}

// DeleteRecord deletes a DNS record
func (p *Route53Provider) DeleteRecord(zoneID, name, recordType string) error {
	p.log(fmt.Sprintf("Deleting %s...", name))

	// First get the current value
	record, err := p.GetRecord(zoneID, name, recordType)
	if err != nil || record == nil {
		return fmt.Errorf("record not found")
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	// Ensure name ends with dot
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	changeBatch := map[string]interface{}{
		"Changes": []map[string]interface{}{
			{
				"Action": "DELETE",
				"ResourceRecordSet": map[string]interface{}{
					"Name": name,
					"Type": recordType,
					"TTL":  ttl,
					"ResourceRecords": []map[string]string{
						{"Value": record.Value},
					},
				},
			},
		},
	}

	changeBatchJSON, err := json.Marshal(changeBatch)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{
		"route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", string(changeBatchJSON),
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = p.buildEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		return fmt.Errorf("failed to delete record: %s", string(output))
	}

	return nil
}

// SyncRecord updates a record if the value has changed
func (p *Route53Provider) SyncRecord(zoneID string, record Record) (changed bool, err error) {
	currentRecord, err := p.GetRecord(zoneID, record.Name, record.Type)
	if err != nil {
		errStr := err.Error()
		// Check if this is a permission error
		if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") {
			p.log(fmt.Sprintf("Permission denied for %s: %v", record.Name, err))
			return false, err
		}
		p.log(fmt.Sprintf("GetRecord error for %s: %v, will try to create", record.Name, err))
		// Record might not exist, try to create it
		return true, p.UpdateRecord(zoneID, record)
	}

	if currentRecord == nil {
		// Record doesn't exist, create it
		return true, p.UpdateRecord(zoneID, record)
	}

	if currentRecord.Value == record.Value {
		p.log(fmt.Sprintf("%s already set to %s", record.Name, record.Value))
		return false, nil
	}

	p.log(fmt.Sprintf("%s value mismatch: current=%q new=%q", record.Name, currentRecord.Value, record.Value))
	return true, p.UpdateRecord(zoneID, record)
}
