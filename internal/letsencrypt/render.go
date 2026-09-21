package letsencrypt

// This file is the PURE half of the package: facts in, decisions out. It reads
// no files, runs no command, opens no socket, reads no clock and never speaks
// to a certificate authority — everything it needs arrives as an argument.
//
// That matters more here than in the other split packages. The rest of hz fails
// visibly: a click does nothing and somebody is watching. Certificate renewal
// fails on a CLOCK, weeks after the change that broke it, at which point the
// gateway simply stops serving HTTPS. So the decisions — which paths, which
// SANs to ask for, whether a certificate is stale, which files in the HAProxy
// cert directory are orphans — are computed here where they can be tested
// exhaustively with no CA, no network and no privilege, and the privileged half
// (apply.go) only carries them out.
//
// The seam is also the reason this half may be linked into an unprivileged hz
// web process after plan/architecture.md phase 4 item 12: nothing in this file
// can read a private key, because nothing in this file can read anything.

import (
	"crypto/x509"
	"encoding/pem"
	"path"
	"sort"
	"strings"
	"time"
)

// DNSProviderType identifies the DNS provider for ACME challenges
type DNSProviderType string

const (
	DNSProviderRoute53    DNSProviderType = "route53"
	DNSProviderNamecom    DNSProviderType = "namecom"
	DNSProviderCloudflare DNSProviderType = "cloudflare"
)

// DNSProviderConfig holds provider-specific credentials (copied from config to avoid import cycle)
type DNSProviderConfig struct {
	Type DNSProviderType

	// Route53
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegion          string
	AWSHostedZoneID    string
	AWSProfile         string

	// Name.com
	NamecomUsername string
	NamecomAPIToken string

	// Cloudflare
	CloudflareAPIToken string
	CloudflareZoneID   string
}

// DomainConfig holds configuration for a single domain (or multiple SANs)
type DomainConfig struct {
	Domain      string   // Primary domain (e.g., "*.example.com")
	ExtraSANs   []string // Additional SANs (e.g., "*.vpn.example.com", "vpn.example.com")
	Email       string
	DNSProvider *DNSProviderConfig // DNS provider configuration
}

// Config holds Let's Encrypt configuration
type Config struct {
	Domains        []DomainConfig
	CertDir        string // where certs are stored
	HAProxyCertDir string // directory for combined haproxy certs
	Staging        bool   // use Let's Encrypt staging environment
}

// DomainStatus represents the current state of SSL certificate for a domain
type DomainStatus struct {
	Domain           string
	Email            string
	ProviderType     string // DNS provider type
	AWSProfile       string // AWS profile used for DNS challenge
	CertExists       bool
	CertPath         string
	KeyPath          string
	HAProxyCertPath  string
	HAProxyCertReady bool
	ExpiryInfo       string
}

// Status represents the overall SSL status
type Status struct {
	LegoAvailable bool // Always true (compiled in)
	Domains       []DomainStatus
}

// CertInfo holds detailed certificate information
type CertInfo struct {
	Domain    string   `json:"domain"`
	SANs      []string `json:"sans"` // Subject Alternative Names
	Issuer    string   `json:"issuer"`
	NotBefore string   `json:"not_before"`
	NotAfter  string   `json:"not_after"`
	Serial    string   `json:"serial"`
}

// BaseDomain is the directory name a wildcard's certificate is filed under:
// "*.example.com" and "example.com" share one certificate and one directory.
//
// Every path in this package goes through it, so the wildcard stripping happens
// in one place rather than at each of the eight call sites it used to.
func BaseDomain(domain string) string { return strings.TrimPrefix(domain, "*.") }

// CertPaths are the files one domain's certificate occupies, in the
// certbot-compatible layout hz writes and reads.
//
// Certbot's layout is kept deliberately: the gateway may have been issued
// certificates by certbot before hz existed, and matching the layout means hz
// picks those up rather than reissuing over the top of them.
type CertPaths struct {
	LiveDir   string // <certDir>/live/<base>
	Fullchain string // the certificate, leaf first
	Privkey   string // the private key — the one file in here nothing may print
	Chain     string // the issuer chain, written only when the CA supplied one
}

// CertPathsFor computes where a domain's certificate lives under certDir.
//
// Uses path, not path/filepath: these are slash-separated strings being
// composed, not paths being resolved against a filesystem that may or may not
// have them. On the only OS hz runs on the two are the same function; taking
// the lexical one is what lets this stay on the pure side.
func CertPathsFor(certDir, domain string) CertPaths {
	dir := path.Join(certDir, "live", BaseDomain(domain))
	return CertPaths{
		LiveDir:   dir,
		Fullchain: path.Join(dir, "fullchain.pem"),
		Privkey:   path.Join(dir, "privkey.pem"),
		Chain:     path.Join(dir, "chain.pem"),
	}
}

// HAProxyCertFile is the file name a domain's combined PEM takes in the HAProxy
// cert directory. HAProxy loads every file in that directory, so the name is
// also the identity used to decide what is an orphan.
func HAProxyCertFile(domain string) string { return BaseDomain(domain) + ".pem" }

// HAProxyCertPathFor is HAProxyCertFile placed in the HAProxy cert directory.
func HAProxyCertPathFor(haproxyCertDir, domain string) string {
	return path.Join(haproxyCertDir, HAProxyCertFile(domain))
}

// CombinePEM is the HAProxy bundle format: certificate then key, one file.
//
// It copies rather than appending to cert's backing array — `append(cert,
// key...)` writes into the caller's slice whenever it has spare capacity, and
// the caller here is holding a certificate it may use again.
func CombinePEM(cert, key []byte) []byte {
	out := make([]byte, 0, len(cert)+len(key))
	out = append(out, cert...)
	out = append(out, key...)
	return out
}

// RequestedSANs is exactly what gets asked of the CA: the primary domain plus
// the configured extras, in that order.
//
// The apex is included only when it is itself a configured domain
// (DeriveSSLDomains makes an explicit "" subzone the primary or an extra SAN);
// a bare "*" wildcard subzone does NOT pull in the apex.
func RequestedSANs(d DomainConfig) []string {
	domains := []string{d.Domain}
	domains = append(domains, d.ExtraSANs...)
	return domains
}

// DiffSANs compares a certificate's SANs against the configured set, both ways:
// missing are configured-but-absent, extra are on the certificate but no longer
// configured. Either one means the certificate needs re-requesting. Both are
// sorted.
//
// The expected set is exactly the configured domains, mirroring RequestedSANs.
// A wildcard primary does NOT imply its apex — the apex is expected only when
// it's a configured domain (an explicit "" subzone, which appears here as the
// primary or an extra SAN). Expecting an unconfigured apex would flag it as
// perpetually missing and trigger a re-request every renewal sweep.
//
// The check has to be symmetric or a removed SubZone never actually leaves the
// certificate — nothing else triggers a re-request, so the domain keeps a valid
// SAN (and with it an http->https redirect) until natural expiry, months later.
func DiffSANs(d DomainConfig, actual []string) (missing, extra []string) {
	expected := make(map[string]bool)
	expected[d.Domain] = true
	for _, san := range d.ExtraSANs {
		expected[san] = true
	}

	have := make(map[string]bool)
	for _, san := range actual {
		have[san] = true
	}

	for san := range expected {
		if !have[san] {
			missing = append(missing, san)
		}
	}
	for san := range have {
		if !expected[san] {
			extra = append(extra, san)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// DirEntry is the whole of what the prune decision needs to know about a file
// in the HAProxy cert directory: its name and whether it is a directory.
// Listing the directory is apply-side (see PruneHAProxyCerts); deciding is not.
type DirEntry struct {
	Name  string
	IsDir bool
}

// OrphanedCertFiles reports which entries in the HAProxy cert directory belong
// to no currently-configured domain, in listing order.
//
// HAProxy loads *every* file in `crt <dir>`, so a stale cert (e.g. from a
// subzone that was removed from the config) keeps being served via SNI — and
// once it expires it shadows the valid cert for any hostname listed in its
// SANs. Reconciling the directory to exactly the configured set prevents that.
//
// Only .pem files are candidates: anything else in there was put there by
// somebody, is not loaded by HAProxy as a certificate, and is not hz's to
// delete.
func OrphanedCertFiles(domains []DomainConfig, entries []DirEntry) []string {
	valid := make(map[string]bool, len(domains))
	for _, d := range domains {
		valid[HAProxyCertFile(d.Domain)] = true
	}

	var orphans []string
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".pem") || valid[e.Name] {
			continue
		}
		orphans = append(orphans, e.Name)
	}
	return orphans
}

// NeedsRenewalAt reports whether a certificate expiring at notAfter is within
// withinDays of now.
//
// Taking `now` as an argument rather than reading the clock is what makes the
// renewal boundary testable at all: this is the decision whose failure mode is
// silence until a certificate expires, so it has to be exercised on both sides
// of the window without waiting for one.
func NeedsRenewalAt(notAfter, now time.Time, withinDays int) bool {
	return notAfter.Sub(now) < time.Duration(withinDays)*24*time.Hour
}

// CertNotAfter reads the expiry out of a PEM certificate's bytes. ok is false
// when the bytes are not a certificate hz can parse — which every caller treats
// as "renew", because an unreadable certificate is not a working one.
//
// The bytes come from the caller; this never opens the file. It parses a
// CERTIFICATE, never a key: a fullchain.pem holds no key, and the combined
// HAProxy bundle is not what gets passed here.
func CertNotAfter(pemData []byte) (notAfter time.Time, ok bool) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return time.Time{}, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, false
	}
	return cert.NotAfter, true
}

// ParseCertInfo assembles a CertInfo from the raw output of the five `openssl
// x509` invocations GetCertInfo runs. Running them is apply-side; making sense
// of what they printed is not.
//
// Every field is best-effort, exactly as before: openssl that failed or printed
// something unexpected leaves that field empty rather than failing the whole
// read, because a cert whose issuer could not be parsed is still a cert whose
// expiry the operator wants to see.
func ParseCertInfo(subjectOut, sansOut, issuerOut, datesOut, serialOut string) *CertInfo {
	info := &CertInfo{}
	info.Domain = parseSubjectCN(subjectOut)
	info.SANs = parseSANs(sansOut)
	info.Issuer = parseIssuer(issuerOut)
	info.NotBefore, info.NotAfter = parseDates(datesOut)
	info.Serial = parseSerial(serialOut)
	return info
}

// parseSubjectCN extracts CN from "subject=CN = *.example.com" or
// "subject= /CN=*.example.com" — openssl has printed both shapes.
func parseSubjectCN(out string) string {
	subject := strings.TrimSpace(out)
	idx := strings.Index(subject, "CN")
	if idx == -1 {
		return ""
	}
	cn := subject[idx+2:]
	cn = strings.TrimPrefix(cn, " = ")
	cn = strings.TrimPrefix(cn, "=")
	cn = strings.TrimSpace(cn)
	if commaIdx := strings.Index(cn, ","); commaIdx != -1 {
		cn = cn[:commaIdx]
	}
	return cn
}

// parseSANs pulls the DNS names out of `-ext subjectAltName` output, which is
// a "DNS:*.example.com, DNS:example.com" list on its own line.
func parseSANs(out string) []string {
	var sans []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "DNS:") && !strings.Contains(line, "DNS:") {
			continue
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "DNS:") {
				sans = append(sans, strings.TrimPrefix(part, "DNS:"))
			}
		}
	}
	return sans
}

func parseIssuer(out string) string {
	issuer := strings.TrimSpace(out)
	issuer = strings.TrimPrefix(issuer, "issuer=")
	return strings.TrimSpace(issuer)
}

func parseDates(out string) (notBefore, notAfter string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "notBefore=") {
			notBefore = strings.TrimPrefix(line, "notBefore=")
		} else if strings.HasPrefix(line, "notAfter=") {
			notAfter = strings.TrimPrefix(line, "notAfter=")
		}
	}
	return notBefore, notAfter
}

func parseSerial(out string) string {
	serial := strings.TrimSpace(out)
	return strings.TrimPrefix(serial, "serial=")
}

// ParseEndDate turns `openssl x509 -enddate -noout` output into the expiry
// string DomainStatus carries.
func ParseEndDate(out string) string {
	return strings.TrimSpace(strings.TrimPrefix(out, "notAfter="))
}

// ParseAWSProfiles collects the profile names out of the contents of AWS
// credentials/config files, in first-seen order, with "default" guaranteed
// present.
//
// The config file spells sections "[profile name]" and the credentials file
// spells them "[name]"; both are accepted, which is why this takes contents
// rather than a parsed ini.
func ParseAWSProfiles(files [][]byte) []string {
	var profiles []string
	seen := make(map[string]bool)
	for _, data := range files {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
				continue
			}
			profile := strings.TrimPrefix(strings.TrimSuffix(line, "]"), "[")
			// Config file uses "profile name" format
			profile = strings.TrimPrefix(profile, "profile ")
			if profile != "" && !seen[profile] {
				profiles = append(profiles, profile)
				seen[profile] = true
			}
		}
	}

	// Always include "default" as an option
	if !seen["default"] {
		profiles = append([]string{"default"}, profiles...)
	}
	return profiles
}
