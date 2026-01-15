package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// User implements registration.User for Lego
type User struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration,omitempty"`
	key          crypto.PrivateKey
}

func (u *User) GetEmail() string                        { return u.Email }
func (u *User) GetRegistration() *registration.Resource { return u.Registration }
func (u *User) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Client wraps the Lego ACME client
type Client struct {
	accountDir string
	staging    bool
}

// NewClient creates a new ACME client
func NewClient(accountDir string, staging bool) *Client {
	return &Client{
		accountDir: accountDir,
		staging:    staging,
	}
}

// ObtainCertificate requests a certificate for the given domains
func (c *Client) ObtainCertificate(email string, domains []string, providerCfg *DNSProviderConfig, logFn func(string)) (*certificate.Resource, error) {
	if logFn == nil {
		logFn = func(s string) {}
	}

	logFn(fmt.Sprintf("Using DNS provider: %s", ProviderName(providerCfg)))

	// Log provider config details (without secrets)
	if providerCfg != nil {
		switch providerCfg.Type {
		case DNSProviderRoute53:
			if providerCfg.AWSProfile != "" {
				logFn(fmt.Sprintf("  AWS Profile: %s", providerCfg.AWSProfile))
			}
			if providerCfg.AWSHostedZoneID != "" {
				logFn(fmt.Sprintf("  AWS Hosted Zone ID: %s", providerCfg.AWSHostedZoneID))
				// Verify the zone exists and get its name
				zoneName, err := verifyRoute53Zone(providerCfg.AWSHostedZoneID, providerCfg.AWSProfile)
				if err != nil {
					logFn(fmt.Sprintf("  ⚠ Zone verification failed: %v", err))
				} else {
					logFn(fmt.Sprintf("  Zone name: %s", zoneName))
				}
			} else {
				logFn("  ⚠ No AWS Hosted Zone ID configured - Lego will try to auto-detect")
			}
			if providerCfg.AWSRegion != "" {
				logFn(fmt.Sprintf("  AWS Region: %s", providerCfg.AWSRegion))
			}
		case DNSProviderCloudflare:
			if providerCfg.CloudflareZoneID != "" {
				logFn(fmt.Sprintf("  Cloudflare Zone ID: %s", providerCfg.CloudflareZoneID))
			}
		}
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

	// Set DNS provider with custom pre-check using recursive DNS
	// Lego's authoritative NS lookup is broken (returns TLD servers like gtld-servers.net)
	// We use our own propagation check with Google/Cloudflare DNS instead
	logFn("Using custom propagation check with recursive DNS (8.8.8.8, 1.1.1.1)")
	err = client.Challenge.SetDNS01Provider(dnsProvider,
		dns01.WrapPreCheck(func(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
			// Use our own DNS check with recursive resolvers
			return checkDNSPropagation(fqdn, value, logFn)
		}),
	)
	if err != nil {
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
		errStr := err.Error()
		if strings.Contains(errStr, "NXDOMAIN") {
			logFn("  ✗ DNS record not found - check that the zone ID is correct")
		} else if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "Timeout") {
			logFn("  ✗ DNS propagation timeout - the TXT record may not have propagated in time")
		} else if strings.Contains(errStr, "unauthorized") {
			logFn("  ✗ Authorization failed - Let's Encrypt could not verify domain ownership")
		} else if strings.Contains(errStr, "rateLimited") {
			logFn("  ✗ Rate limited - too many certificate requests, try again later")
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
	// Normalize zone ID - remove /hostedzone/ prefix if present
	zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")

	args := []string{
		"route53", "get-hosted-zone",
		"--id", zoneID,
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

	zoneName := strings.TrimSpace(string(output))
	zoneName = strings.TrimSuffix(zoneName, ".") // Remove trailing dot
	return zoneName, nil
}

// checkDNSPropagation checks if a TXT record has propagated using recursive DNS servers
func checkDNSPropagation(fqdn, expectedValue string, logFn func(string)) (bool, error) {
	// Remove trailing dot if present
	fqdn = strings.TrimSuffix(fqdn, ".")

	resolvers := []string{"8.8.8.8:53", "1.1.1.1:53", "208.67.222.222:53"}
	timeout := 5 * time.Minute
	interval := 10 * time.Second

	deadline := time.Now().Add(timeout)
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		allFound := true

		for _, resolver := range resolvers {
			found, err := checkTXTRecord(fqdn, expectedValue, resolver)
			if err != nil {
				if attempt == 1 || attempt%6 == 0 { // Log every minute
					logFn(fmt.Sprintf("  Checking %s via %s: %v", fqdn, resolver, err))
				}
				allFound = false
				break
			}
			if !found {
				if attempt == 1 || attempt%6 == 0 {
					logFn(fmt.Sprintf("  Checking %s via %s: not found yet", fqdn, resolver))
				}
				allFound = false
				break
			}
		}

		if allFound {
			logFn(fmt.Sprintf("  ✓ TXT record verified on all resolvers after %d attempts", attempt))
			return true, nil
		}

		time.Sleep(interval)
	}

	return false, fmt.Errorf("timeout waiting for DNS propagation after %v", timeout)
}

// checkTXTRecord checks if a TXT record exists with the expected value
func checkTXTRecord(fqdn, expectedValue, resolver string) (bool, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, "udp", resolver)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	records, err := r.LookupTXT(ctx, fqdn)
	if err != nil {
		// NXDOMAIN or other DNS error - record doesn't exist yet
		return false, nil
	}

	for _, record := range records {
		if record == expectedValue {
			return true, nil
		}
	}

	return false, nil
}
