package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/config"
)

const namecomAPIBase = "https://api.name.com/v4"
const namecomTimeout = 30 * time.Second

// Rate limiting constants for Name.com API
const (
	namecomMinRequestInterval = 100 * time.Millisecond // Max 10 requests/second
	namecomMaxRequestsPerHour = 3000
)

// Global rate limiter for Name.com (rate limits are per-account)
var namecomRateLimiter = &namecomLimiter{
	hourlyReset: time.Now(),
}

type namecomLimiter struct {
	mu          sync.Mutex
	lastRequest time.Time
	hourlyCount int
	hourlyReset time.Time
}

// wait blocks until it's safe to make another request
// Returns error if hourly limit would be exceeded
func (l *namecomLimiter) wait() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Reset hourly counter if an hour has passed
	if now.Sub(l.hourlyReset) >= time.Hour {
		l.hourlyCount = 0
		l.hourlyReset = now
	}

	// Check hourly limit
	if l.hourlyCount >= namecomMaxRequestsPerHour {
		remaining := time.Hour - now.Sub(l.hourlyReset)
		return fmt.Errorf("name.com hourly rate limit exceeded (3000/hour), resets in %v", remaining.Round(time.Minute))
	}

	// Ensure minimum interval between requests (10/sec limit)
	elapsed := now.Sub(l.lastRequest)
	if elapsed < namecomMinRequestInterval {
		sleepTime := namecomMinRequestInterval - elapsed
		l.mu.Unlock()
		time.Sleep(sleepTime)
		l.mu.Lock()
	}

	l.lastRequest = time.Now()
	l.hourlyCount++
	return nil
}

// NamecomProvider implements DNS operations using Name.com API
type NamecomProvider struct {
	username string
	apiToken string
	client   *http.Client
}

// NewNamecomProvider creates a new Name.com DNS provider
func NewNamecomProvider(cfg *config.DNSProviderConfig) (Provider, error) {
	if cfg.NamecomUsername == "" || cfg.NamecomAPIToken == "" {
		return nil, fmt.Errorf("namecom requires namecom_username and namecom_api_token")
	}
	return &NamecomProvider{
		username: cfg.NamecomUsername,
		apiToken: cfg.NamecomAPIToken,
		client:   &http.Client{Timeout: namecomTimeout},
	}, nil
}

// Name returns the provider identifier
func (p *NamecomProvider) Name() string {
	return "namecom"
}

func (p *NamecomProvider) log(action string) {
	fmt.Printf("[Name.com/%s] %s\n", p.username, action)
}

func (p *NamecomProvider) doRequest(method, path string, body interface{}) ([]byte, error) {
	// Apply rate limiting before making request
	if err := namecomRateLimiter.wait(); err != nil {
		return nil, err
	}

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, namecomAPIBase+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(p.username, p.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// ListZones returns available domains from Name.com
// Only returns domains where DNS is managed by Name.com nameservers
func (p *NamecomProvider) ListZones() ([]Zone, error) {
	p.log("Listing domains...")

	respBody, err := p.doRequest("GET", "/domains", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list domains: %w", err)
	}

	var result struct {
		Domains []struct {
			DomainName  string   `json:"domainName"`
			Nameservers []string `json:"nameservers"`
		} `json:"domains"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var zones []Zone
	for _, d := range result.Domains {
		// Check if DNS is managed by Name.com (nameservers contain name.com)
		managedByNamecom := p.isManagedByNamecom(d.Nameservers)

		zones = append(zones, Zone{
			ID:          d.DomainName, // Name.com uses domain name as ID
			Name:        d.DomainName,
			DNSManaged:  managedByNamecom,
			Nameservers: d.Nameservers,
		})
	}

	return zones, nil
}

// isManagedByNamecom checks if the nameservers indicate DNS is hosted at Name.com
func (p *NamecomProvider) isManagedByNamecom(nameservers []string) bool {
	for _, ns := range nameservers {
		ns = strings.ToLower(ns)
		if strings.Contains(ns, "name.com") || strings.Contains(ns, "name-services.com") {
			return true
		}
	}
	return false
}

// namecomRecord represents a Name.com DNS record
type namecomRecord struct {
	ID         int    `json:"id,omitempty"`
	DomainName string `json:"domainName,omitempty"`
	Host       string `json:"host"`
	Fqdn       string `json:"fqdn,omitempty"`
	Type       string `json:"type"`
	Answer     string `json:"answer"`
	TTL        int    `json:"ttl"`
}

// GetRecord retrieves a DNS record from Name.com
func (p *NamecomProvider) GetRecord(zoneID, name, recordType string) (*Record, error) {
	p.log(fmt.Sprintf("Getting %s (%s) for %s...", name, recordType, zoneID))

	// Name.com uses the domain name as the zone ID
	domainName := zoneID

	// Extract the host part (subdomain) from the full name
	host := extractHost(name, domainName)

	respBody, err := p.doRequest("GET", fmt.Sprintf("/domains/%s/records", domainName), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get records: %w", err)
	}

	var result struct {
		Records []namecomRecord `json:"records"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Find matching record
	for _, r := range result.Records {
		if r.Host == host && r.Type == recordType {
			return &Record{
				Name:   name,
				Type:   r.Type,
				Value:  r.Answer,
				TTL:    r.TTL,
				ZoneID: zoneID,
			}, nil
		}
	}

	return nil, nil // Record not found
}

// CreateRecord creates a new DNS record
func (p *NamecomProvider) CreateRecord(zoneID string, record Record) error {
	p.log(fmt.Sprintf("Creating %s -> %s...", record.Name, record.Value))

	domainName := zoneID
	host := extractHost(record.Name, domainName)

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	body := namecomRecord{
		Host:   host,
		Type:   record.Type,
		Answer: record.Value,
		TTL:    ttl,
	}

	_, err := p.doRequest("POST", fmt.Sprintf("/domains/%s/records", domainName), body)
	if err != nil {
		p.log(fmt.Sprintf("Create %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to create record: %w", err)
	}

	p.log(fmt.Sprintf("Create %s SUCCESS", record.Name))
	return nil
}

// UpdateRecord updates an existing DNS record
func (p *NamecomProvider) UpdateRecord(zoneID string, record Record) error {
	p.log(fmt.Sprintf("Updating %s -> %s...", record.Name, record.Value))

	domainName := zoneID
	host := extractHost(record.Name, domainName)

	// First, find the existing record ID
	recordID, err := p.findRecordID(domainName, host, record.Type)
	if err != nil {
		return err
	}

	ttl := record.TTL
	if ttl <= 0 {
		ttl = 300
	}

	if recordID == 0 {
		// Record doesn't exist, create it
		return p.CreateRecord(zoneID, record)
	}

	body := namecomRecord{
		Host:   host,
		Type:   record.Type,
		Answer: record.Value,
		TTL:    ttl,
	}

	_, err = p.doRequest("PUT", fmt.Sprintf("/domains/%s/records/%d", domainName, recordID), body)
	if err != nil {
		p.log(fmt.Sprintf("Update %s FAILED: %v", record.Name, err))
		return fmt.Errorf("failed to update record: %w", err)
	}

	p.log(fmt.Sprintf("Update %s SUCCESS", record.Name))
	return nil
}

// DeleteRecord deletes a DNS record
func (p *NamecomProvider) DeleteRecord(zoneID, name, recordType string) error {
	p.log(fmt.Sprintf("Deleting %s...", name))

	domainName := zoneID
	host := extractHost(name, domainName)

	recordID, err := p.findRecordID(domainName, host, recordType)
	if err != nil {
		return err
	}

	if recordID == 0 {
		return fmt.Errorf("record not found")
	}

	_, err = p.doRequest("DELETE", fmt.Sprintf("/domains/%s/records/%d", domainName, recordID), nil)
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}

	p.log(fmt.Sprintf("Delete %s SUCCESS", name))
	return nil
}

// SyncRecord updates a record if the value has changed
func (p *NamecomProvider) SyncRecord(zoneID string, record Record) (changed bool, err error) {
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

// SyncRecordSet syncs a set of records with the same name/type (round-robin DNS).
// Name.com doesn't natively support multi-value record sets the same way Route53 does,
// so we delete existing records and create new ones.
func (p *NamecomProvider) SyncRecordSet(zoneID string, records []Record) (changed bool, err error) {
	if len(records) <= 1 {
		if len(records) == 1 {
			return p.SyncRecord(zoneID, records[0])
		}
		return false, nil
	}

	name := records[0].Name
	recordType := records[0].Type
	p.log(fmt.Sprintf("SyncRecordSet %s (%s) with %d values...", name, recordType, len(records)))

	// Delete existing, then create all new records
	_ = p.DeleteRecord(zoneID, name, recordType)

	for _, rec := range records {
		if err := p.CreateRecord(zoneID, rec); err != nil {
			return false, fmt.Errorf("failed to create record %s -> %s: %w", rec.Name, rec.Value, err)
		}
	}

	return true, nil
}

// findRecordID finds the ID of a record by host and type
func (p *NamecomProvider) findRecordID(domainName, host, recordType string) (int, error) {
	respBody, err := p.doRequest("GET", fmt.Sprintf("/domains/%s/records", domainName), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get records: %w", err)
	}

	var result struct {
		Records []namecomRecord `json:"records"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	for _, r := range result.Records {
		if r.Host == host && r.Type == recordType {
			return r.ID, nil
		}
	}

	return 0, nil // Not found
}

// extractHost extracts the host/subdomain part from a full domain name
// e.g., "www.example.com" with domain "example.com" returns "www"
// e.g., "example.com" with domain "example.com" returns ""
func extractHost(fullName, domainName string) string {
	// Remove trailing dots
	fullName = strings.TrimSuffix(fullName, ".")
	domainName = strings.TrimSuffix(domainName, ".")

	if fullName == domainName {
		return ""
	}

	suffix := "." + domainName
	if strings.HasSuffix(fullName, suffix) {
		return strings.TrimSuffix(fullName, suffix)
	}

	return fullName
}
