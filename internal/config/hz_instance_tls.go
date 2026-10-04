package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// TLS for a nested hz's own hostnames (operator, 2026-10-03: "the domain owner
// needs to issue with full view and child needs to pull the cert and dnsmasq
// fix").
//
// THIS hz owns the zone and its DNS credential, so THIS hz issues. The child
// receives ISSUED material — a certificate and its key for exactly the names
// listed in HZInstance.Domains — and never the power to issue: the ACME
// account key and the DNS provider credential stay here (CLAUDE.md #3, "Cert
// material and the two channels").
//
// ONE CERTIFICATE PER INSTANCE. A child's names are never added to this hz's
// own certificates (DeriveSSLDomains reads zones' sub_zones only), and this
// hz's certificates are never offered to a child: the instance certificate is
// filed under its own directory (letsencrypt.InstanceCertPaths), which the
// pull endpoint is the only reader of.

// instanceDomainRe is a hostname, optionally a leading "*." wildcard.
var instanceDomainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeInstanceDomains trims, lower-cases and drops empties, keeping the
// declared order (the first name is the certificate's primary).
func NormalizeInstanceDomains(in []string) []string {
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// instanceDomainZone is the one zone every domain of h is in, or an error
// saying which name cannot be issued here and why.
func (c *Config) instanceDomainZone(h HZInstance) (*Zone, error) {
	var zone *Zone
	for _, d := range h.Domains {
		bare := strings.TrimPrefix(d, "*.")
		z := c.owningZone(bare)
		if z == nil {
			return nil, fmt.Errorf("hz instance %q: domain %s is outside every zone this hz owns, so this hz cannot answer its DNS-01 challenge and cannot issue it — declare the zone here, or let the zone's owner issue it", h.Name, d)
		}
		if z.GetDNSProvider() == nil {
			return nil, fmt.Errorf("hz instance %q: domain %s is in zone %s, which has no dns_provider, so this hz cannot write its DNS-01 challenge and cannot issue it", h.Name, d, z.Name)
		}
		if zone != nil && zone.Name != z.Name {
			return nil, fmt.Errorf("hz instance %q: domains %s and %s are in different zones (%s, %s); one instance certificate is answered through one zone's DNS provider — list names from one zone", h.Name, h.Domains[0], d, zone.Name, z.Name)
		}
		zone = z
	}
	return zone, nil
}

// ValidateInstanceDomains holds the rules HZInstance.Domains obey:
//
//   - each is a hostname (a leading "*." is allowed), listed once;
//   - each is inside a zone this hz owns, with a DNS provider — this hz
//     answers the DNS-01 challenge, so it must be able to write the zone;
//   - all are in ONE zone (one certificate, one provider conversation);
//   - none is at or below a delegated name — its challenge would be written
//     to a zone no resolver reads for it;
//   - none is a domain of a service here, nor a name in this hz's own
//     certificates — a name has one TLS terminator;
//   - no two instances list the same name.
func (c *Config) ValidateInstanceDomains() error {
	owner := map[string]string{}
	services := map[string]string{}
	for _, svc := range c.Services {
		for _, d := range svc.Domains {
			services[strings.ToLower(strings.TrimSpace(d))] = svc.Name
		}
	}
	office := map[string]bool{}
	for _, dc := range c.DeriveSSLDomains() {
		for _, san := range letsencrypt.RequestedSANs(dc) {
			office[strings.ToLower(san)] = true
		}
	}
	for _, h := range c.HZInstances {
		seen := map[string]bool{}
		for _, d := range h.Domains {
			if d != strings.ToLower(strings.TrimSpace(d)) || !instanceDomainRe.MatchString(d) {
				return fmt.Errorf("hz instance %q: domain %q is not a lower-case hostname (a leading \"*.\" is allowed)", h.Name, d)
			}
			if seen[d] {
				return fmt.Errorf("hz instance %q lists domain %s twice", h.Name, d)
			}
			seen[d] = true
			if other, ok := owner[d]; ok {
				return fmt.Errorf("domain %s is listed by hz instances %q and %q; a name has one TLS terminator", d, other, h.Name)
			}
			owner[d] = h.Name
			if svc, ok := services[d]; ok {
				return fmt.Errorf("hz instance %q: domain %s is also a domain of service %s on this hz; a name has one TLS terminator — remove it from one of them", h.Name, d, svc)
			}
			if office[d] {
				return fmt.Errorf("hz instance %q: domain %s is a name in this hz's own certificate (a zone's sub_zones); the instance gets a certificate of its own — remove the sub_zone", h.Name, d)
			}
			if z := c.owningZone(strings.TrimPrefix(d, "*.")); z != nil {
				if del, ok := z.DelegationCovering(strings.TrimPrefix(d, "*.")); ok {
					return fmt.Errorf("hz instance %q: domain %s is at or below a delegated name; %s — its DNS-01 challenge would be written to %s, where resolvers never look, so the delegated zone's owner issues it", h.Name, d, del.describe(), z.Name)
				}
			}
		}
		if _, err := c.instanceDomainZone(h); err != nil {
			return err
		}
	}
	return nil
}

// InstanceCertRequest is the ONE certificate this hz asks the CA for on behalf
// of an instance: exactly its declared domains, the first as the primary, the
// owning zone's DNS provider and ACME email. ok is false when the instance
// declares no domains. It never reads, and never returns, a sub_zone of this
// hz's own certificates.
func (c *Config) InstanceCertRequest(name string) (dc letsencrypt.DomainConfig, ok bool, err error) {
	h, found := c.FindHZInstance(name)
	if !found {
		return dc, false, fmt.Errorf("no hz instance %q%s", name, c.hzInstanceHint())
	}
	if len(h.Domains) == 0 {
		return dc, false, nil
	}
	zone, err := c.instanceDomainZone(h)
	if err != nil {
		return dc, false, err
	}
	email := ""
	if zone.SSL != nil {
		email = strings.TrimSpace(zone.SSL.Email)
	}
	if email == "" {
		for i := range c.Zones {
			if z := c.Zones[i]; z.SSL != nil && strings.TrimSpace(z.SSL.Email) != "" {
				email = strings.TrimSpace(z.SSL.Email)
				break
			}
		}
	}
	if email == "" {
		return dc, false, fmt.Errorf("hz instance %q: no zone here has an ssl.email, and the CA needs a contact address to issue — set ssl.email on zone %s", name, zone.Name)
	}
	dc = letsencrypt.DomainConfig{
		Domain:      h.Domains[0],
		ExtraSANs:   append([]string(nil), h.Domains[1:]...),
		Email:       email,
		DNSProvider: zoneLEProvider(zone),
	}
	return dc, true, nil
}

// HasInstanceDomains reports whether any instance asks this hz for a cert.
func (c *Config) HasInstanceDomains() bool {
	for _, h := range c.HZInstances {
		if len(h.Domains) > 0 {
			return true
		}
	}
	return false
}

// zoneLEProvider is a zone's DNS provider in the letsencrypt package's shape,
// with the zone id filled in from the zone when the provider leaves it empty.
// The one conversion, shared by the office's certificates and an instance's.
func zoneLEProvider(zone *Zone) *letsencrypt.DNSProviderConfig {
	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		return nil
	}
	awsHostedZoneID := providerCfg.AWSHostedZoneID
	if awsHostedZoneID == "" && providerCfg.Type == "route53" {
		awsHostedZoneID = zone.ZoneID
	}
	cloudflareZoneID := providerCfg.CloudflareZoneID
	if cloudflareZoneID == "" && providerCfg.Type == "cloudflare" {
		cloudflareZoneID = zone.ZoneID
	}
	return &letsencrypt.DNSProviderConfig{
		Type:               letsencrypt.DNSProviderType(providerCfg.Type),
		AWSAccessKeyID:     providerCfg.AWSAccessKeyID,
		AWSSecretAccessKey: providerCfg.AWSSecretAccessKey,
		AWSRegion:          providerCfg.AWSRegion,
		AWSHostedZoneID:    awsHostedZoneID,
		AWSProfile:         providerCfg.AWSProfile,
		NamecomUsername:    providerCfg.NamecomUsername,
		NamecomAPIToken:    providerCfg.NamecomAPIToken,
		CloudflareAPIToken: providerCfg.CloudflareAPIToken,
		CloudflareZoneID:   cloudflareZoneID,
	}
}

// selfAddresses is every address this resolver already treats as THIS hz:
// its public address, its LAN and VPN gateway addresses, and every address a
// local answer names.
func (c *Config) selfAddresses(answers map[string]string) map[string]bool {
	self := map[string]bool{}
	for _, ip := range []string{c.EffectivePublicIP(), c.PublicIP, c.LocalInterface, c.GetWGGatewayIP()} {
		if ip = strings.TrimSpace(ip); ip != "" {
			self[ip] = true
		}
	}
	for _, ip := range answers {
		self[ip] = true
	}
	return self
}

// coveredByWildcard reports whether a broader `address=` answer would capture
// name: a wildcard record at a proper parent of it.
func coveredByWildcard(name string, wildcards map[string]bool) bool {
	for parent := range wildcards {
		if strings.HasSuffix(name, "."+parent) {
			return true
		}
	}
	return false
}

// externalNameForwards is the dnsmasq carve-out for names that must resolve
// PUBLICLY although a broader local answer covers them: every name declared
// as a zone record (A, AAAA, CNAME) that points somewhere other than this hz,
// and every hz instance domain. Without it, `address=/parent/<this hz>` from a
// service domain answers for them too — measured 2026-10-03: a child's name,
// declared as a zone A record, resolved to the office on the office LAN.
//
// A name this resolver answers itself is left alone (its own answer is the
// intended one), and a name no wildcard covers needs no carve-out: dnsmasq
// already forwards it.
func (c *Config) externalNameForwards(answers map[string]string, wildcards map[string]bool) []dnsmasq.Record {
	self := c.selfAddresses(answers)
	why := map[string]string{}
	add := func(name, reason string) {
		name = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(name), "*."), "."))
		if name == "" {
			return
		}
		if _, local := answers[name]; local {
			return
		}
		if !coveredByWildcard(name, wildcards) {
			return
		}
		if _, dup := why[name]; !dup {
			why[name] = reason
		}
	}
	for i := range c.Zones {
		z := &c.Zones[i]
		for _, r := range z.Records {
			t := r.NormalizedType()
			name, _ := normalizeRecordKey(z.qualify(r.Name), t)
			v := strings.TrimSpace(r.Value)
			switch t {
			case "A", "AAAA":
				if ip := net.ParseIP(v); ip == nil || self[ip.String()] || self[v] {
					continue
				}
			case "CNAME":
				target := strings.ToLower(strings.TrimSuffix(v, "."))
				if _, local := answers[target]; local {
					continue
				}
			default:
				continue
			}
			add(name, "zone "+z.Name+" declares "+t+" "+name+" -> "+v+", not this hz; resolved upstream, not answered here")
		}
	}
	for _, h := range c.HZInstances {
		for _, d := range h.Domains {
			add(d, "hz instance "+h.Name+" terminates TLS for "+d+"; resolved upstream, not answered here")
		}
	}
	names := make([]string, 0, len(why))
	for n := range why {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]dnsmasq.Record, 0, len(names))
	for _, n := range names {
		out = append(out, dnsmasq.Record{Name: n, ForwardUpstream: true, Comment: why[n]})
	}
	return out
}
