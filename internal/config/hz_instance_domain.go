package config

import (
	"fmt"
	"slices"
	"strings"
)

// An hz instance's ONE registered domain (N4j, operator 2026-10-07: "the child
// instance SHOULD have a domain registered with it", refined: "shouldn't the
// instance name have a root domain: loadtest.redline.example.com, making
// kiosk: kiosk.hz.loadtest.redline.example.com?").
//
// Every name the instance has DERIVES from HZInstance.Domain (CLAUDE.md #8 — a
// stored derivation is a second answer free to disagree):
//
//	<domain>            public    the app's name; the child asks for its A record
//	kiosk.hz.<domain>   public    the MFA kiosk and the human VPN endpoint
//	hz.<domain>         VPN-ONLY  the child hz's UI/API; only the child's dnsmasq
//	                              answers it, with its wg0 address
//
// The certificate this hz issues covers all three (DNS-01 needs a TXT record
// only, so a VPN-only name can be issued), and the DNS grant is
// [<domain>, *.<domain>].
//
// hz.<domain> is NEVER published publicly: a request for it is refused
// (ApplyInstanceDNSRequest), a record for it is refused at the write
// (InstanceRecordPublishable, which ValidateInstanceDNS and the publish run),
// and a hand-declared record there is refused too (no hot-fix exception).
//
// An instance with no Domain keeps the explicit Domains and DNSGrant it had
// before N4j — never guessed into a domain; the instance page nudges "set a
// domain".

// Instance name labels under the domain.
const (
	instanceKioskLabel = "kiosk.hz."
	instanceUILabel    = "hz."
)

// InstanceKioskName is kiosk.hz.<domain>: the kiosk and the VPN endpoint.
func InstanceKioskName(domain string) string { return instanceKioskLabel + domain }

// InstanceUIName is hz.<domain>: the child hz's UI, VPN-only.
func InstanceUIName(domain string) string { return instanceUILabel + domain }

// NormalizeInstanceDomain trims, lower-cases and drops a trailing dot.
func NormalizeInstanceDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

// CheckInstanceDomain refuses a domain that is not one lower-case hostname.
// Where it may be (a zone this hz owns with a provider, not delegated, not an
// apex, clear of services) is the TLS and grant rules', run on the derived
// names.
func CheckInstanceDomain(d string) error {
	if d == "" || strings.HasPrefix(d, "*.") || d != NormalizeInstanceDomain(d) || !instanceDomainRe.MatchString(d) {
		return fmt.Errorf("domain %q is not one lower-case hostname (no wildcard: an instance registers one domain, e.g. loadtest.redline.example.com)", d)
	}
	return nil
}

// DerivedInstanceDomains is the TLS name set of a domain, in certificate
// order (the first is the primary).
func DerivedInstanceDomains(domain string) []string {
	return []string{domain, InstanceKioskName(domain), InstanceUIName(domain)}
}

// DerivedInstanceGrant is the DNS grant of a domain.
func DerivedInstanceGrant(domain string) []string {
	return []string{domain, "*." + domain}
}

// TLSDomains is the names this hz issues the instance's certificate for:
// derived from Domain when set, else the explicit pre-N4j Domains.
func (h HZInstance) TLSDomains() []string {
	if h.Domain != "" {
		return DerivedInstanceDomains(h.Domain)
	}
	return append([]string(nil), h.Domains...)
}

// Grant is where this hz publishes DNS the instance asks for: derived from
// Domain when set, else the explicit pre-N4j DNSGrant.
func (h HZInstance) Grant() []string {
	if h.Domain != "" {
		return DerivedInstanceGrant(h.Domain)
	}
	return append([]string(nil), h.DNSGrant...)
}

// KioskName is kiosk.hz.<domain>, or "" with no domain.
func (h HZInstance) KioskName() string {
	if h.Domain == "" {
		return ""
	}
	return InstanceKioskName(h.Domain)
}

// UIName is hz.<domain>, or "" with no domain.
func (h HZInstance) UIName() string {
	if h.Domain == "" {
		return ""
	}
	return InstanceUIName(h.Domain)
}

// IsVPNOnly reports whether name is one of the instance's VPN-only names:
// hz.<domain>. A record there is never published, by anyone.
func (h HZInstance) IsVPNOnly(name string) bool {
	return h.Domain != "" && NormalizeInstanceDomain(name) == h.UIName()
}

// VPNOnlyName is the instance whose VPN-only name name is, if any.
func (c *Config) VPNOnlyName(name string) (instance string, ok bool) {
	for _, h := range c.HZInstances {
		if h.IsVPNOnly(name) {
			return h.Name, true
		}
	}
	return "", false
}

// validateInstanceDomainField holds the rules Domain obeys beside the TLS and
// grant rules its derived names go through: one hostname, and no explicit
// Domains or DNSGrant that DISAGREES with what it derives (#8). An explicit
// value equal to the derivation is accepted (a config written by hand) and
// cleared at the next edit.
func (c *Config) validateInstanceDomainField() error {
	for _, h := range c.HZInstances {
		if h.Domain == "" {
			continue
		}
		if err := CheckInstanceDomain(h.Domain); err != nil {
			return fmt.Errorf("hz instance %q: %w", h.Name, err)
		}
		if len(h.Domains) > 0 && !slices.Equal(h.Domains, DerivedInstanceDomains(h.Domain)) {
			return fmt.Errorf("hz instance %q: domains %s disagree with domain %s, which derives %s — with a domain set the names are derived, never stored; remove domains",
				h.Name, strings.Join(h.Domains, ", "), h.Domain, strings.Join(DerivedInstanceDomains(h.Domain), ", "))
		}
		if len(h.DNSGrant) > 0 && !slices.Equal(h.DNSGrant, DerivedInstanceGrant(h.Domain)) {
			return fmt.Errorf("hz instance %q: dns_grant %s disagrees with domain %s, which derives the grant %s — with a domain set the grant is derived, never stored; remove dns_grant",
				h.Name, strings.Join(h.DNSGrant, ", "), h.Domain, strings.Join(DerivedInstanceGrant(h.Domain), ", "))
		}
	}
	return nil
}

// clearDerivedInstanceFields drops an explicit Domains and DNSGrant from an
// instance with a Domain: they are derived. Run at every write, so a stored
// copy that agreed does not outlive the edit that would make it disagree.
func clearDerivedInstanceFields(h *HZInstance) {
	if h.Domain == "" {
		return
	}
	if slices.Equal(h.Domains, DerivedInstanceDomains(h.Domain)) {
		h.Domains = nil
	}
	if slices.Equal(h.DNSGrant, DerivedInstanceGrant(h.Domain)) {
		h.DNSGrant = nil
	}
}
