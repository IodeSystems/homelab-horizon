package acme

// This file is the PURE half of the package: text in, text out. It opens no
// socket, runs no command, reads no file, reads no clock, and — the rule that
// matters most in this package — never speaks to a certificate authority and
// never touches an account key.
//
// What is here is everything the ACME conversation DECIDES rather than
// performs: what the operator is told about a provider, what an obtain failure
// probably means, and whether a zone looks delegated. Those were interleaved
// with the exec and network calls that produced their inputs, which is why a
// delegation check could only be exercised by actually querying public DNS.
//
// Naming rule for this half: nothing in it may be called Obtain, Request,
// Register, Present or CleanUp. Those are verbs that talk to somebody.

import (
	"fmt"
	"strings"
)

// DNSProviderType identifies the DNS provider for ACME challenges
type DNSProviderType string

const (
	DNSProviderRoute53    DNSProviderType = "route53"
	DNSProviderNamecom    DNSProviderType = "namecom"
	DNSProviderCloudflare DNSProviderType = "cloudflare"
)

// DNSProviderConfig holds provider-specific credentials for ACME challenges.
//
// It carries API tokens and an AWS secret key, so nothing in the pure half
// prints a whole one — see ProviderSummary, which reports only the fields that
// are identifiers rather than credentials.
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

// ProviderName returns the name of the provider for a given config
func ProviderName(cfg *DNSProviderConfig) string {
	if cfg == nil {
		return "unknown"
	}
	return string(cfg.Type)
}

// ZoneVerifiable reports whether there is a hosted zone to go and look up.
// Only Route53 with an explicit zone id gets verified; everything else has
// nothing to check before the challenge starts.
func ZoneVerifiable(cfg *DNSProviderConfig) bool {
	return cfg != nil && cfg.Type == DNSProviderRoute53 && cfg.AWSHostedZoneID != ""
}

// ProviderSummary is what the operator is told about the provider a
// certificate is being requested through.
//
// It returns two blocks because one network call sits between them: head is
// logged, then the hosted zone is verified (if ZoneVerifiable), then tail. The
// split point is exactly where the impure step goes, so the order of the lines
// is fixed here rather than reconstructed at the call site.
//
// EVERY field it prints is an identifier — profile name, zone id, region. The
// access key, the secret key and both API tokens are in the same struct and
// none of them appear, which is a property this function makes checkable
// rather than a habit spread over a switch statement.
func ProviderSummary(cfg *DNSProviderConfig) (head, tail []string) {
	if cfg == nil {
		return nil, nil
	}
	switch cfg.Type {
	case DNSProviderRoute53:
		if cfg.AWSProfile != "" {
			head = append(head, fmt.Sprintf("  AWS Profile: %s", cfg.AWSProfile))
		}
		if cfg.AWSHostedZoneID != "" {
			head = append(head, fmt.Sprintf("  AWS Hosted Zone ID: %s", cfg.AWSHostedZoneID))
		} else {
			head = append(head, "  ⚠ No AWS Hosted Zone ID configured - Lego will try to auto-detect")
		}
		if cfg.AWSRegion != "" {
			tail = append(tail, fmt.Sprintf("  AWS Region: %s", cfg.AWSRegion))
		}
	case DNSProviderCloudflare:
		if cfg.CloudflareZoneID != "" {
			tail = append(tail, fmt.Sprintf("  Cloudflare Zone ID: %s", cfg.CloudflareZoneID))
		}
	}
	return head, tail
}

// NormalizeZoneID strips the "/hostedzone/" prefix the AWS console shows, so a
// zone id pasted from either place works.
func NormalizeZoneID(zoneID string) string {
	return strings.TrimPrefix(zoneID, "/hostedzone/")
}

// ParseZoneName reads a zone name out of `aws route53 get-hosted-zone` output,
// dropping the trailing dot DNS writes and the shell does not.
func ParseZoneName(out string) string {
	return strings.TrimSuffix(strings.TrimSpace(out), ".")
}

// ObtainFailureHints turns a lego error string into the plain-language lines
// shown under a failed issuance. Empty when the error is not one of the four
// that have a known cause — a guess dressed as a diagnosis is worse than no
// line at all.
//
// Kept pure and separate because this is the text an operator reads at 2am
// about a certificate that did not renew, and it is the one part of the failure
// path that can be exercised without failing an issuance.
func ObtainFailureHints(errStr string) []string {
	switch {
	case strings.Contains(errStr, "NXDOMAIN"):
		return []string{"  ✗ DNS record not found - check that the zone ID is correct"}
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "Timeout"):
		return []string{"  ✗ DNS propagation timeout - the TXT record may not have propagated in time"}
	case strings.Contains(errStr, "unauthorized"):
		return []string{"  ✗ Authorization failed - Let's Encrypt could not verify domain ownership"}
	case strings.Contains(errStr, "rateLimited"):
		return []string{"  ✗ Rate limited - too many certificate requests, try again later"}
	}
	return nil
}

// SOAReport reads `dig +short SOA <domain>` output and says what it means.
//
// needNS is true when there was no SOA at all, which is the only case that
// justifies a second lookup: the caller then runs the NS query and passes its
// output to NSReport. Splitting it this way keeps the two DNS queries in
// apply.go and both verdicts here, where they can be tested against captured
// output instead of against the internet.
func SOAReport(soaOut string) (lines []string, needNS bool) {
	soa := strings.TrimSpace(soaOut)
	if soa == "" {
		return nil, true
	}
	// Parse SOA to show primary nameserver
	if parts := strings.Fields(soa); len(parts) >= 1 {
		return []string{fmt.Sprintf("  ✓ SOA record found (primary NS: %s)", parts[0])}, false
	}
	return nil, false
}

// NSReport is the verdict when a domain had no SOA record: either it does not
// exist in public DNS at all, or it is delegated somewhere that is not
// answering authoritatively. Both are fatal to a DNS-01 challenge, which is why
// both return an error rather than a warning — issuing would fail anyway, after
// burning a rate-limited attempt.
//
// The lines name the exact next action, because the operator reading them is
// looking at a certificate that did not renew and does not yet know why.
func NSReport(domain, nsOut string) (lines []string, err error) {
	lines = append(lines, fmt.Sprintf("  ✗ No SOA record found for %s - domain is not delegated to Route53", domain))

	ns := strings.TrimSpace(nsOut)
	if ns == "" {
		lines = append(lines,
			"  ✗ No NS records found - domain does not exist in public DNS",
			"  → Update nameservers at your domain registrar to point to Route53",
			"  → Run: aws route53 get-hosted-zone --id <zone-id> --query DelegationSet.NameServers")
		return lines, fmt.Errorf("domain %s is not delegated - no SOA/NS records in public DNS", domain)
	}

	lines = append(lines,
		fmt.Sprintf("  Current NS records: %s", strings.ReplaceAll(ns, "\n", ", ")),
		"  → These should be Route53 nameservers (ns-*.awsdns-*.com/net/org/co.uk)")
	return lines, fmt.Errorf("domain %s has NS records but no SOA - delegation may be incomplete", domain)
}

// ChallengeRecordName is the DNS name a DNS-01 challenge is answered at.
// One line, but it is the one string the challenge provider and its log
// messages must agree on.
func ChallengeRecordName(domain string) string {
	return fmt.Sprintf("_acme-challenge.%s", domain)
}
