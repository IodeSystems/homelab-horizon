package config

import (
	"fmt"
	"sort"
	"strings"
)

// DNS delegation: one owner per name.
//
// A zone may hand a subdomain to another owner — typically a nested hz with its
// own hosted zone — by declaring an NS record set at that name. From then on
// the name and everything below it is the other owner's: resolvers follow the
// NS and never read this zone for those names again. So hz refuses to declare
// anything there itself. A record, a service domain or a sub_zone at or below a
// delegated name would be written to a zone nobody consults, and two hz
// instances writing one name is exactly what the drift guard halts all DNS for.
//
// The apex is never a delegation: the apex NS set is the provider's own
// delegation of the whole zone, and replacing it would take the zone offline.

// Delegation is one NS record set declared below a zone's apex.
type Delegation struct {
	Name        string   // FQDN, lower case, no trailing dot
	NameServers []string // canonical (no trailing dot), in declared order
}

// Delegations returns the zone's declared NS sets, sorted by name.
func (z *Zone) Delegations() []Delegation {
	byName := map[string]*Delegation{}
	for _, r := range z.Records {
		if r.NormalizedType() != "NS" {
			continue
		}
		n, _ := normalizeRecordKey(z.qualify(r.Name), "NS")
		d := byName[n]
		if d == nil {
			d = &Delegation{Name: n}
			byName[n] = d
		}
		d.NameServers = append(d.NameServers, CanonicalRecordValue("NS", r.Value))
	}
	out := make([]Delegation, 0, len(byName))
	for _, d := range byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DelegationCovering returns the delegation that fqdn is at or below, if any.
func (z *Zone) DelegationCovering(fqdn string) (Delegation, bool) {
	n, _ := normalizeRecordKey(fqdn, "")
	for _, d := range z.Delegations() {
		if n == d.Name || strings.HasSuffix(n, "."+d.Name) {
			return d, true
		}
	}
	return Delegation{}, false
}

func (d Delegation) describe() string {
	return d.Name + " is delegated to " + strings.Join(d.NameServers, ", ")
}

// ValidateDelegationRecords holds the rules a declared NS set obeys on its own
// zone: it is below the apex, it is the only type at its name, every nameserver
// is outside the delegated name, and nothing is declared beneath it.
func (z *Zone) ValidateDelegationRecords() error {
	apex := strings.ToLower(z.Name)
	delegations := z.Delegations()
	for _, d := range delegations {
		if d.Name == apex {
			return fmt.Errorf("an NS record at the zone apex %s is not allowed; the apex NS set is the provider's delegation of the whole zone, and replacing it would take the zone offline", d.Name)
		}
		for _, ns := range d.NameServers {
			if n := strings.ToLower(ns); n == d.Name || strings.HasSuffix(n, "."+d.Name) {
				return fmt.Errorf("nameserver %s is inside the delegated name %s; it would need glue records in this zone, and records below a delegation belong to the delegated zone's owner", ns, d.Name)
			}
		}
	}
	for _, r := range z.Records {
		t := r.NormalizedType()
		n, _ := normalizeRecordKey(z.qualify(r.Name), t)
		for _, d := range delegations {
			switch {
			case n == d.Name && t != "NS":
				return fmt.Errorf("the %s record at %s cannot coexist with its NS records; %s, and a delegation point holds only NS records", t, n, d.describe())
			case strings.HasSuffix(n, "."+d.Name):
				return fmt.Errorf("the %s record %s is below a delegated name; %s — records below it belong to that zone's owner, not to %s", t, n, d.describe(), z.Name)
			}
		}
	}
	return nil
}

// owningZone is the most specific configured zone containing fqdn. A config may
// hold both a parent zone and the zone it delegates to (one hz owning both);
// a name below the delegation then belongs to the child zone, not the parent.
func (c *Config) owningZone(fqdn string) *Zone {
	var best *Zone
	for i := range c.Zones {
		if c.Zones[i].ContainsName(fqdn) && (best == nil || len(c.Zones[i].Name) > len(best.Name)) {
			best = &c.Zones[i]
		}
	}
	return best
}

// ServiceDomainDelegationError refuses a service domain at or below a delegated
// name in the zone that owns it. Nil when the domain is not delegated.
func (c *Config) ServiceDomainDelegationError(service, domain string) error {
	name := strings.TrimPrefix(strings.TrimSpace(domain), "*.")
	z := c.owningZone(name)
	if z == nil {
		return nil
	}
	d, ok := z.DelegationCovering(name)
	if !ok {
		return nil
	}
	return fmt.Errorf("service %s cannot use domain %s; %s — names at or below it belong to that zone's owner, so serve it from the hz that owns the delegated zone", service, domain, d.describe())
}

// SubZoneFQDN is the name a sub_zone entry stands for (see DeriveSSLDomains):
// "" is the apex, "*" the apex wildcard, anything else a label under the zone.
func (z *Zone) SubZoneFQDN(sub string) string {
	switch sub {
	case "":
		return z.Name
	case "*":
		return "*." + z.Name
	default:
		return sub + "." + z.Name
	}
}

// SubZoneDelegationError refuses a sub_zone whose certificate name is at or
// below a delegated name. Its DNS-01 challenge would be written into this zone,
// where no resolver looks for that name. Nil when not delegated.
func (z *Zone) SubZoneDelegationError(sub string) error {
	fqdn := z.SubZoneFQDN(sub)
	d, ok := z.DelegationCovering(strings.TrimPrefix(fqdn, "*."))
	if !ok {
		return nil
	}
	return fmt.Errorf("sub_zone %q of zone %s names %s; %s — its certificate's DNS-01 challenge would be written to %s, where resolvers never look, so the delegated zone's owner issues it", sub, z.Name, fqdn, d.describe(), z.Name)
}

// ValidateDelegations is the Save-time check that nothing hz declares sits at
// or below a name the zone delegated away. It checks the delegation rules only,
// not every record rule in ValidateRecords, so a config saved before NS was
// declarable still saves.
func (c *Config) ValidateDelegations() error {
	for i := range c.Zones {
		z := &c.Zones[i]
		if err := z.ValidateDelegationRecords(); err != nil {
			return fmt.Errorf("zone %s: %w", z.Name, err)
		}
		for _, sub := range z.SubZones {
			if err := z.SubZoneDelegationError(sub); err != nil {
				return err
			}
		}
	}
	for _, svc := range c.Services {
		for _, d := range svc.Domains {
			if err := c.ServiceDomainDelegationError(svc.Name, d); err != nil {
				return err
			}
		}
	}
	return nil
}
