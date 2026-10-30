package letsencrypt

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/acme"
)

// This file is the PRIVILEGED half of the package: the whole list of things hz
// needs root on the gateway for, as far as certificates are concerned.
//
//   - talk to a certificate authority, and to the DNS provider that answers its
//     challenge (through internal/acme)
//   - write /etc/letsencrypt/live/<domain>/{fullchain,privkey,chain}.pem, 0600
//     in a 0700 directory — privkey.pem is the most sensitive file hz writes
//     anywhere that is not a WireGuard config
//   - read those files back
//   - write /etc/haproxy/certs/<domain>.pem, the combined bundle HAProxy loads,
//     which is the certificate AND the key in one file
//   - list and DELETE files in /etc/haproxy/certs
//   - run `openssl x509` to inspect a certificate
//   - read ~/.aws and /root/.aws to list profiles
//   - TestIssue: ask Let's Encrypt STAGING, under its own account in
//     <certdir>/accounts-staging, and keep nothing of the answer but a summary
//
// Nothing here decides anything — which paths, which SANs, which files are
// orphans, whether a certificate is stale: all of that is render.go, which is
// pure. When this half moves into hz-agent (plan/design/architecture.md, phase 4,
// items 10 and 12 step 3), this list is what moves.
//
// This is the surface that fails on a CLOCK rather than on a click. Everything
// else hz does wrong is noticed by somebody pressing a button; a renewal that
// silently stopped working is noticed when a certificate expires, weeks later.
// That is the reason this list is written out rather than left to be grepped.

// LogFunc is a function that receives log lines
type LogFunc func(line string)

// RequestCertForDomain requests a certificate for a specific domain
func (m *Manager) RequestCertForDomain(d DomainConfig) error {
	return m.RequestCertForDomainWithLog(d, nil)
}

// RequestCertForDomainWithLog requests a certificate and streams output to a log function
func (m *Manager) RequestCertForDomainWithLog(d DomainConfig, logFn LogFunc) error {
	if d.Domain == "" {
		return fmt.Errorf("no domain specified")
	}
	if d.Email == "" {
		return fmt.Errorf("no email specified")
	}

	// Get DNS provider config
	if d.DNSProvider == nil {
		return fmt.Errorf("no DNS provider configured for domain %s", d.Domain)
	}

	// Save certificates in certbot-compatible layout
	if err := m.obtainInto(d, acmeProvider(d.DNSProvider), CertPathsFor(m.config.CertDir, d.Domain), logFn); err != nil {
		return err
	}

	if logFn != nil {
		logFn("Packaging certificate for HAProxy...")
	}

	// Package cert for HAProxy
	return m.PackageForHAProxyDomain(d.Domain)
}

// RequestInstanceCert issues the certificate a nested hz instance pulls: the
// same CA conversation, account and DNS provider as this hz's own
// certificates, filed under InstanceCertPaths — and NOT packaged for this hz's
// HAProxy, which never serves an instance's names.
func (m *Manager) RequestInstanceCert(instance string, d DomainConfig, logFn LogFunc) error {
	if d.Domain == "" {
		return fmt.Errorf("no domain specified")
	}
	if d.Email == "" {
		return fmt.Errorf("no email specified")
	}
	if d.DNSProvider == nil {
		return fmt.Errorf("no DNS provider configured for domain %s", d.Domain)
	}
	return m.obtainInto(d, acmeProvider(d.DNSProvider), InstanceCertPaths(m.config.CertDir, instance), logFn)
}

// TestIssueResult is what a staging issuance proved: the CA issued these
// SANs. The certificate and its key are not kept.
type TestIssueResult struct {
	SANs     []string
	Issuer   string
	NotAfter time.Time
}

// TestIssue asks Let's Encrypt STAGING for exactly the certificate d would get
// (RequestedSANs, the same DNS provider) and writes none of it: not the live
// directory, not the HAProxy bundle. It proves the DNS-01 path — credentials,
// zone, propagation — without spending a production rate limit or putting an
// untrusted certificate where HAProxy would serve it, and without leaving a
// staging certificate the renewal sweep would read as valid.
//
// Staging has its own account (<certdir>/accounts-staging): an account
// registered with production does not exist at staging.
func (m *Manager) TestIssue(d DomainConfig, logFn LogFunc) (*TestIssueResult, error) {
	if d.Domain == "" {
		return nil, fmt.Errorf("no domain specified")
	}
	if d.Email == "" {
		return nil, fmt.Errorf("no email specified")
	}
	if d.DNSProvider == nil {
		return nil, fmt.Errorf("no DNS provider configured for domain %s", d.Domain)
	}
	staging := acme.NewClient(filepath.Join(m.config.CertDir, "accounts-staging"), true)
	certs, err := staging.ObtainCertificate(d.Email, RequestedSANs(d), acmeProvider(d.DNSProvider), logFn)
	if err != nil {
		return nil, fmt.Errorf("staging issuance failed: %w", err)
	}
	block, _ := pem.Decode(certs.Certificate)
	if block == nil {
		return nil, fmt.Errorf("staging answered without a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing the staging certificate: %w", err)
	}
	return &TestIssueResult{SANs: cert.DNSNames, Issuer: cert.Issuer.CommonName, NotAfter: cert.NotAfter}, nil
}

// acmeProvider is p in internal/acme's terms.
func acmeProvider(p *DNSProviderConfig) *acme.DNSProviderConfig {
	return &acme.DNSProviderConfig{
		Type:               acme.DNSProviderType(p.Type),
		AWSAccessKeyID:     p.AWSAccessKeyID,
		AWSSecretAccessKey: p.AWSSecretAccessKey,
		AWSRegion:          p.AWSRegion,
		AWSHostedZoneID:    p.AWSHostedZoneID,
		AWSProfile:         p.AWSProfile,
		NamecomUsername:    p.NamecomUsername,
		NamecomAPIToken:    p.NamecomAPIToken,
		CloudflareAPIToken: p.CloudflareAPIToken,
		CloudflareZoneID:   p.CloudflareZoneID,
	}
}

// ReadInstanceCert reads an instance certificate's fullchain and key, as
// filed by RequestInstanceCert. Missing wraps os.ErrNotExist.
func (m *Manager) ReadInstanceCert(instance string) (fullchain, key []byte, err error) {
	paths := InstanceCertPaths(m.config.CertDir, instance)
	if fullchain, err = os.ReadFile(paths.Fullchain); err != nil {
		return nil, nil, err
	}
	if key, err = os.ReadFile(paths.Privkey); err != nil {
		return nil, nil, err
	}
	return fullchain, key, nil
}

// obtainInto asks the CA for exactly RequestedSANs(d) and writes the result
// in the certbot layout at paths. The one obtain both callers share.
func (m *Manager) obtainInto(d DomainConfig, acmeProviderCfg *acme.DNSProviderConfig, paths CertPaths, logFn LogFunc) error {
	// Build the SAN list: exactly the configured domains — primary plus extra
	// SANs. See RequestedSANs for why the apex is not implied by a wildcard.
	domains := RequestedSANs(d)

	// Request certificate using ACME client
	certs, err := m.acme.ObtainCertificate(d.Email, domains, acmeProviderCfg, logFn)
	if err != nil {
		return fmt.Errorf("failed to obtain certificate: %w", err)
	}

	if err := os.MkdirAll(paths.LiveDir, 0700); err != nil {
		return fmt.Errorf("creating cert directory: %w", err)
	}

	// Save fullchain.pem (certificate)
	if err := os.WriteFile(paths.Fullchain, certs.Certificate, 0600); err != nil {
		return fmt.Errorf("writing certificate: %w", err)
	}

	// Save privkey.pem (private key)
	if err := os.WriteFile(paths.Privkey, certs.PrivateKey, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	// Save issuer cert if available
	if certs.IssuerCertificate != nil {
		if err := os.WriteFile(paths.Chain, certs.IssuerCertificate, 0600); err != nil {
			return fmt.Errorf("writing issuer certificate: %w", err)
		}
	}
	return nil
}

// PackageForHAProxyDomain combines cert and key into a single PEM for HAProxy
func (m *Manager) PackageForHAProxyDomain(domain string) error {
	paths := CertPathsFor(m.config.CertDir, domain)

	// Read cert and key
	cert, err := os.ReadFile(paths.Fullchain)
	if err != nil {
		return fmt.Errorf("reading cert: %w", err)
	}
	key, err := os.ReadFile(paths.Privkey)
	if err != nil {
		return fmt.Errorf("reading key: %w", err)
	}

	// Ensure HAProxy cert dir exists
	if err := os.MkdirAll(m.config.HAProxyCertDir, 0700); err != nil {
		return fmt.Errorf("creating haproxy cert dir: %w", err)
	}

	// Write combined PEM (cert + key)
	outPath := HAProxyCertPathFor(m.config.HAProxyCertDir, domain)
	if err := os.WriteFile(outPath, CombinePEM(cert, key), 0600); err != nil {
		return fmt.Errorf("writing combined cert: %w", err)
	}

	return nil
}

// PackageAllForHAProxy packages all configured domain certs for HAProxy.
// It does not prune orphans — callers pair this with PruneHAProxyCerts so they
// can act on the prune result (e.g. reload HAProxy only when the set changed).
func (m *Manager) PackageAllForHAProxy() error {
	for _, d := range m.config.Domains {
		if err := m.PackageForHAProxyDomain(d.Domain); err != nil {
			// Continue with other domains even if one fails
			slog.Warn("failed to package cert for haproxy", "domain", d.Domain, "err", err)
		}
	}
	return nil
}

// PruneHAProxyCerts deletes <base>.pem files in the HAProxy cert dir that don't
// correspond to any currently-configured domain, returning the number removed.
// Which files those are is OrphanedCertFiles' decision, and its doc comment
// says why a stale certificate is not merely untidy.
func (m *Manager) PruneHAProxyCerts() int {
	entries, err := os.ReadDir(m.config.HAProxyCertDir)
	if err != nil {
		return 0 // dir may not exist yet; nothing to prune
	}
	listing := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		listing = append(listing, DirEntry{Name: e.Name(), IsDir: e.IsDir()})
	}

	removed := 0
	for _, name := range OrphanedCertFiles(m.config.Domains, listing) {
		path := filepath.Join(m.config.HAProxyCertDir, name)
		if err := os.Remove(path); err != nil {
			slog.Warn("failed to remove orphaned haproxy cert", "file", path, "err", err)
			continue
		}
		removed++
		slog.Info("removed orphaned haproxy cert", "file", path)
	}
	return removed
}

// GetCertInfo returns detailed information about a certificate. It used to
// be five `openssl x509` reads of the file; it is one read and crypto/x509
// (CertInfoFromPEM). The error return stays nil, as it always was: an
// unreadable file is an empty CertInfo.
func GetCertInfo(certPath string) (*CertInfo, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return &CertInfo{}, nil
	}
	return CertInfoFromPEM(data), nil
}

// certEndDate returns the certificate's expiry line, or "" when the file
// could not be read.
func certEndDate(certPath string) string {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	return CertEndDate(data)
}

// fileExists reports whether a path can be stat'd. The certificate checks want
// "is it there", not "why not".
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetAWSProfiles returns a list of available AWS profiles
func GetAWSProfiles() []string {
	// Check both credentials and config files
	homeDir, _ := os.UserHomeDir()
	credPaths := []string{
		filepath.Join(homeDir, ".aws", "credentials"),
		filepath.Join(homeDir, ".aws", "config"),
		"/root/.aws/credentials",
		"/root/.aws/config",
	}

	var files [][]byte
	for _, path := range credPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		files = append(files, data)
	}

	return ParseAWSProfiles(files)
}
