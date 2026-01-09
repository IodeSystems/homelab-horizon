package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"

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

	// Create DNS challenge provider
	dnsProvider, err := CreateChallengeProvider(providerCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS provider: %w", err)
	}

	logFn(fmt.Sprintf("Using DNS provider: %s", ProviderName(providerCfg)))

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

	// Set DNS provider
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

	// Request certificate
	request := certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	}

	certificates, err := client.Certificate.Obtain(request)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	logFn("Certificate obtained successfully!")

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
