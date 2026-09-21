// Package letsencrypt obtains, stores and packages the gateway's TLS
// certificates.
//
// The package is split along one seam, and the split is load-bearing for the
// hz-agent work (plan/architecture.md, phase 4 items 10 and 12):
//
//	render.go      pure     facts in, decisions out. No files, no commands, no
//	                        clock, no CA. Runs anywhere, as anyone.
//	apply.go       root     the side effects: talk to the CA, write and delete
//	                        certificate files, run openssl.
//	letsencrypt.go manager  holds the configuration, reads the machine for the
//	                        facts the decisions need, and joins the two halves.
//
// Anything that decides what SHOULD be true belongs in render.go, so that half
// can run in an unprivileged hz web process while apply.go's short list of side
// effects moves to the agent.
//
// Why this package rather than another: every other privileged surface in hz
// fails on a click, where somebody is watching. This one fails on a clock.
// Renewal runs on a timer, so a break here is invisible until a certificate
// expires weeks later and the gateway stops serving HTTPS.
package letsencrypt

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/acme"
)

// Manager handles Let's Encrypt operations
type Manager struct {
	config Config
	acme   *acme.Client
}

// New creates a new Let's Encrypt manager
func New(cfg Config) *Manager {
	if cfg.CertDir == "" {
		cfg.CertDir = "/etc/letsencrypt"
	}
	if cfg.HAProxyCertDir == "" {
		cfg.HAProxyCertDir = "/etc/haproxy/certs"
	}

	// Create ACME client with account storage in cert dir
	accountDir := filepath.Join(cfg.CertDir, "accounts")
	acmeClient := acme.NewClient(accountDir, cfg.Staging)

	return &Manager{
		config: cfg,
		acme:   acmeClient,
	}
}

// GetStatus returns the current SSL status
func (m *Manager) GetStatus() Status {
	status := Status{
		LegoAvailable: true, // Lego is compiled in
	}

	// Check status of each domain
	for _, d := range m.config.Domains {
		ds := m.GetDomainStatus(d)
		status.Domains = append(status.Domains, ds)
	}

	return status
}

// GetDomainStatus returns the status of a specific domain.
//
// The paths come from the pure half; the three questions that need the machine
// — is the certificate there, is the key there, is the HAProxy bundle there —
// are stats, and the expiry is one openssl read.
func (m *Manager) GetDomainStatus(d DomainConfig) DomainStatus {
	ds := DomainStatus{
		Domain: d.Domain,
		Email:  d.Email,
	}

	// Get provider info from config
	if d.DNSProvider != nil {
		ds.ProviderType = string(d.DNSProvider.Type)
		ds.AWSProfile = d.DNSProvider.AWSProfile
	}

	paths := CertPathsFor(m.config.CertDir, d.Domain)
	ds.CertPath = paths.Fullchain
	ds.KeyPath = paths.Privkey

	// Check if cert exists
	if fileExists(ds.CertPath) && fileExists(ds.KeyPath) {
		ds.CertExists = true
	}

	// Check HAProxy combined cert
	ds.HAProxyCertPath = HAProxyCertPathFor(m.config.HAProxyCertDir, d.Domain)
	ds.HAProxyCertReady = fileExists(ds.HAProxyCertPath)

	// Get expiry info
	if ds.CertExists {
		ds.ExpiryInfo = certEndDate(ds.CertPath)
	}

	return ds
}

// NeedsRenewal reports whether a domain's certificate expires within the given
// number of days. Returns true when the cert doesn't exist (needs initial
// issuance), when it cannot be parsed, or when the NotAfter timestamp is within
// the window.
//
// The clock is read here and handed to NeedsRenewalAt, so the boundary itself
// is testable without waiting for it.
func (m *Manager) NeedsRenewal(d DomainConfig, withinDays int) bool {
	data, err := os.ReadFile(CertPathsFor(m.config.CertDir, d.Domain).Fullchain)
	if err != nil {
		return true // cert doesn't exist
	}
	notAfter, ok := CertNotAfter(data)
	if !ok {
		return true
	}
	return NeedsRenewalAt(notAfter, time.Now(), withinDays)
}

// GetCertInfoForDomain returns cert info for a domain managed by this manager
func (m *Manager) GetCertInfoForDomain(domain string) (*CertInfo, error) {
	certPath := CertPathsFor(m.config.CertDir, domain).Fullchain

	if !fileExists(certPath) {
		return nil, fmt.Errorf("certificate not found: %s", certPath)
	}

	return GetCertInfo(certPath)
}

// CheckCertSANs diffs an existing certificate's SANs against the configured
// set, both ways. See DiffSANs for why the check is symmetric.
// Returns (hasCert, missingSANs, extraSANs, error)
func (m *Manager) CheckCertSANs(d DomainConfig) (bool, []string, []string, error) {
	info, err := m.GetCertInfoForDomain(d.Domain)
	if err != nil {
		return false, nil, nil, nil // Cert doesn't exist
	}

	missing, extra := DiffSANs(d, info.SANs)
	return true, missing, extra, nil
}
