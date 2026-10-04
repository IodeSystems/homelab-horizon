package config

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// DNS for a nested hz (N4h, operator 2026-10-03: "if a parent OWNS a domain,
// then the child SYNC needs to push that sync to the parent").
//
// THE PARENT OWNS THE ZONE, SO THE PARENT WRITES. A child serving a name in a
// zone this hz owns cannot publish it: the DNS provider credential is this
// hz's and never crosses (CLAUDE.md #3). So the child sends the records it
// wants (POST /api/v1/hz-instances/<name>/dns) and this hz publishes them,
// through its own drift-safe path.
//
// BOUNDED BY A GRANT THE PARENT SETS. HZInstance.DNSGrant is the only place an
// instance's request can land: exact names and "*.suffix" subtrees, in zones
// this hz owns with a provider. A request outside it is refused with the
// reason, and a record outside it is never written — refused at the request
// (ApplyInstanceDNSRequest), refused at the write (ValidateInstanceDNS, which
// Save runs), and skipped at publish (InstanceRecordPublishable).
//
// ACCEPTED RECORDS ARE DECLARED STATE, ATTRIBUTED. They are zone records
// (Zone.Records) with DNSRecord.Instance set: the parent's DNS sync publishes
// them like any declared record, the records view shows "from <instance>",
// and no admin record edit touches them — revoking or narrowing the grant is
// the parent's control, and it removes them.

// instanceDNSTypes are the record types an instance may ask for.
var instanceDNSTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true}

// grantEntryCovers reports whether one grant entry covers name. An exact
// entry covers that name only. "*.s" covers every name strictly below s —
// "x.s", "a.x.s", the wildcard record "*.s" itself and "*.x.s" — and not s.
func grantEntryCovers(entry, name string) bool {
	entry = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if entry == "" || name == "" {
		return false
	}
	if entry == name {
		return true
	}
	suffix, wild := strings.CutPrefix(entry, "*.")
	if !wild {
		return false
	}
	return strings.HasSuffix(strings.TrimPrefix(name, "*."), "."+suffix)
}

// GrantCovers reports whether any entry of grant covers name.
func GrantCovers(grant []string, name string) bool {
	for _, g := range grant {
		if grantEntryCovers(g, name) {
			return true
		}
	}
	return false
}

// grantsOverlap reports whether two grant entries share any name.
func grantsOverlap(a, b string) bool { return grantEntryCovers(a, b) || grantEntryCovers(b, a) }

// MigrateInstanceDNSGrants gives every instance that declares TLS domains and
// no grant the grant of exactly those names. Before N4h the operator pointed a
// child's name at it by hand; the grant is what lets the child ask instead, and
// starting it at the names this hz already issues for is the narrowest grant
// that keeps a live instance working. Said LOUD, once, at the write.
func (c *Config) MigrateInstanceDNSGrants() {
	for i := range c.HZInstances {
		h := &c.HZInstances[i]
		if len(h.Domains) == 0 || len(h.DNSGrant) > 0 {
			continue
		}
		h.DNSGrant = append([]string(nil), h.Domains...)
		slog.Warn("LOUD: hz instance "+h.Name+" had TLS domains and no DNS grant; its grant is now exactly its domains — widen it on the instance page if the child serves more",
			"instance", h.Name, "grant", strings.Join(h.DNSGrant, ","))
	}
}

// ValidateInstanceDNS holds the rules HZInstance.DNSGrant and the
// instance-owned records obey:
//
//   - a grant entry is a lower-case hostname, optionally "*.", listed once;
//   - inside a zone this hz owns WITH a DNS provider;
//   - never the zone apex, and never "*.<apex>" (the whole zone);
//   - never at or below a delegated name;
//   - never covering a service domain here, a sub_zones name, or a record
//     this hz declares itself — EXCEPT a record at one of the instance's own
//     TLS domains: that is the pre-N4h hand-declared pointer to the child,
//     which the child's first identical request converts (hot fix);
//   - never overlapping another instance's grant;
//   - every TLS domain of the instance is inside its grant;
//   - an instance-owned record names a declared instance, has a type an
//     instance may ask for, and is inside that instance's grant.
func (c *Config) ValidateInstanceDNS() error {
	type svcDomain struct{ svc, domain string }
	var services []svcDomain
	for _, s := range c.Services {
		for _, d := range s.Domains {
			services = append(services, svcDomain{s.Name, strings.ToLower(strings.TrimSpace(d))})
		}
	}
	type owned struct{ instance, entry string }
	var claimed []owned
	for _, h := range c.HZInstances {
		seen := map[string]bool{}
		domains := map[string]bool{}
		for _, d := range h.Domains {
			domains[strings.ToLower(d)] = true
		}
		for _, g := range h.DNSGrant {
			if g != strings.ToLower(strings.TrimSpace(g)) || !instanceDomainRe.MatchString(g) {
				return fmt.Errorf("hz instance %q: DNS grant entry %q is not a lower-case hostname or \"*.\" subtree", h.Name, g)
			}
			if seen[g] {
				return fmt.Errorf("hz instance %q lists DNS grant entry %s twice", h.Name, g)
			}
			seen[g] = true
			bare, wild := strings.CutPrefix(g, "*.")
			z := c.owningZone(bare)
			if z == nil {
				return fmt.Errorf("hz instance %q: DNS grant %s is outside every zone this hz owns, so this hz cannot publish there — grant names in a zone declared here", h.Name, g)
			}
			if z.GetDNSProvider() == nil {
				return fmt.Errorf("hz instance %q: DNS grant %s is in zone %s, which has no dns_provider, so this hz cannot publish there", h.Name, g, z.Name)
			}
			if strings.EqualFold(bare, z.Name) {
				if wild {
					return fmt.Errorf("hz instance %q: DNS grant %s covers the whole zone %s; grant the subtree the instance serves, or delegate the zone to it", h.Name, g, z.Name)
				}
				return fmt.Errorf("hz instance %q: DNS grant %s is the apex of zone %s; the apex holds the zone's NS and SOA and stays this hz's", h.Name, g, z.Name)
			}
			if del, ok := z.DelegationCovering(bare); ok {
				return fmt.Errorf("hz instance %q: DNS grant %s is at or below a delegated name; %s — a record there is written to %s, where resolvers never look", h.Name, g, del.describe(), z.Name)
			}
			for _, sd := range services {
				if grantEntryCovers(g, sd.domain) {
					return fmt.Errorf("hz instance %q: DNS grant %s covers %s, a domain of service %s on this hz; a name is published by one hz — narrow the grant", h.Name, g, sd.domain, sd.svc)
				}
			}
			for i := range c.Zones {
				zz := &c.Zones[i]
				for _, sub := range zz.SubZones {
					fqdn := strings.ToLower(zz.SubZoneFQDN(sub))
					if grantEntryCovers(g, fqdn) {
						return fmt.Errorf("hz instance %q: DNS grant %s covers %s, a name in this hz's own certificate (zone %s sub_zones %q) — narrow the grant or remove the sub_zone", h.Name, g, fqdn, zz.Name, sub)
					}
				}
				for _, r := range zz.Records {
					if r.Instance != "" {
						continue
					}
					n, t := normalizeRecordKey(zz.qualify(r.Name), r.Type)
					if !grantEntryCovers(g, n) {
						continue
					}
					if domains[n] && instanceDNSTypes[t] {
						continue // the hand-declared pointer to the child; converted on its first identical request
					}
					return fmt.Errorf("hz instance %q: DNS grant %s covers %s %s -> %s, a record this hz declares on zone %s; delete that record or narrow the grant", h.Name, g, t, n, r.Value, zz.Name)
				}
			}
			for _, o := range claimed {
				if grantsOverlap(o.entry, g) {
					return fmt.Errorf("DNS grant %s of hz instance %q overlaps grant %s of hz instance %q; a name is published for one instance", g, h.Name, o.entry, o.instance)
				}
			}
			claimed = append(claimed, owned{h.Name, g})
		}
		for _, d := range h.Domains {
			if !GrantCovers(h.DNSGrant, d) {
				return fmt.Errorf("hz instance %q: TLS domain %s is outside its DNS grant (%s); the child must be able to ask for the name it serves — add it to the grant", h.Name, d, grantList(h.DNSGrant))
			}
		}
	}
	for i := range c.Zones {
		z := &c.Zones[i]
		for _, r := range z.Records {
			if r.Instance == "" {
				continue
			}
			if why, ok := c.InstanceRecordPublishable(z.Name, r); !ok {
				return fmt.Errorf("zone %s: %s", z.Name, why)
			}
		}
	}
	return nil
}

func grantList(grant []string) string {
	if len(grant) == 0 {
		return "none"
	}
	return strings.Join(grant, ", ")
}

// InstanceRecordPublishable is the last check before a write: an
// instance-owned record may be published only when its instance is declared,
// its type is one an instance may ask for, and its name is inside that
// instance's grant. A plain record is always ok here.
func (c *Config) InstanceRecordPublishable(zoneName string, r DNSRecord) (string, bool) {
	if r.Instance == "" {
		return "", true
	}
	z := c.GetZone(zoneName)
	name, t := normalizeRecordKey(r.Name, r.Type)
	if z != nil {
		name, t = normalizeRecordKey(z.qualify(r.Name), r.Type)
	}
	h, ok := c.FindHZInstance(r.Instance)
	if !ok {
		return fmt.Sprintf("record %s %s is owned by hz instance %q, which is not declared", t, name, r.Instance), false
	}
	if !instanceDNSTypes[t] {
		return fmt.Sprintf("record %s %s owned by hz instance %q has a type an instance may not ask for", t, name, h.Name), false
	}
	if !GrantCovers(h.DNSGrant, name) {
		return fmt.Sprintf("record %s %s owned by hz instance %q is outside its DNS grant (%s)", t, name, h.Name, grantList(h.DNSGrant)), false
	}
	return "", true
}

// HasInstanceRecords reports whether any zone holds a record published for an
// hz instance.
func (c *Config) HasInstanceRecords() bool {
	for i := range c.Zones {
		for _, r := range c.Zones[i].Records {
			if r.Instance != "" {
				return true
			}
		}
	}
	return false
}

// InstanceGrantCovering is the instance whose grant covers name, if any — the
// admin record edits refuse to write there: the instance publishes there.
func (c *Config) InstanceGrantCovering(name string) (instance, entry string, ok bool) {
	for _, h := range c.HZInstances {
		for _, g := range h.DNSGrant {
			if grantEntryCovers(g, name) {
				return h.Name, g, true
			}
		}
	}
	return "", "", false
}

// InstanceOwnerOf is the instance owning any declared value at (name, type)
// in a zone, "" for none.
func (c *Config) InstanceOwnerOf(zoneName, name, recType string) string {
	z := c.GetZone(zoneName)
	if z == nil {
		return ""
	}
	rn, rt := normalizeRecordKey(name, recType)
	for _, r := range z.Records {
		n, t := normalizeRecordKey(z.qualify(r.Name), r.Type)
		if n == rn && t == rt && r.Instance != "" {
			return r.Instance
		}
	}
	return ""
}

// InstanceDNSAsk is one record set an instance asks for.
type InstanceDNSAsk struct {
	Name   string
	Type   string
	Values []string
	TTL    int
}

// InstanceDNSVerdict is this hz's answer to one ask.
type InstanceDNSVerdict struct {
	Zone      string
	Name      string
	Type      string
	Values    []string
	Accepted  bool
	Reason    string
	Converted bool
}

// InstanceDNSRemoval is a record set an instance owned and no longer has.
type InstanceDNSRemoval struct {
	Zone   string
	Name   string
	Type   string
	Values []string
}

// InstanceTombstoneReason prefixes the tombstone of every record removed from
// an instance, so its retraction can be driven for that instance alone.
func InstanceTombstoneReason(instance string) string { return "hz instance " + instance + ": " }

// copyZonesForWrite gives the receiver fresh Zones, Records and Tombstones,
// for the reason copyForWrite gives.
func (c *Config) copyZonesForWrite() {
	zones := make([]Zone, len(c.Zones))
	for i, z := range c.Zones {
		z.Records = cloneNonNil(z.Records)
		z.Tombstones = cloneNonNil(z.Tombstones)
		zones[i] = z
	}
	if c.Zones == nil {
		zones = nil
	}
	c.Zones = zones
}

// dropInstanceRecords removes every record instance owns for which keep is
// false, tombstoning each value so the next sync retracts it. Only records
// with Instance == instance are ever touched. The receiver must already hold
// zones of its own (copyZonesForWrite).
func (c *Config) dropInstanceRecords(instance string, keep func(zone, name, recType string) bool, why string, nowUnix int64) []InstanceDNSRemoval {
	var out []InstanceDNSRemoval
	index := map[string]int{}
	for i := range c.Zones {
		z := &c.Zones[i]
		var kept []DNSRecord
		for _, r := range z.Records {
			if r.Instance != instance {
				kept = append(kept, r)
				continue
			}
			n, t := normalizeRecordKey(z.qualify(r.Name), r.Type)
			if keep(z.Name, n, t) {
				kept = append(kept, r)
				continue
			}
			v := CanonicalRecordValue(t, r.Value)
			key := z.Name + "|" + n + "|" + t
			if j, ok := index[key]; ok {
				out[j].Values = append(out[j].Values, v)
			} else {
				index[key] = len(out)
				out = append(out, InstanceDNSRemoval{Zone: z.Name, Name: n, Type: t, Values: []string{v}})
			}
			c.AddTombstone(z.Name, DNSTombstone{Name: n, Type: t, Value: v, CreatedAt: nowUnix, Reason: InstanceTombstoneReason(instance) + why})
		}
		z.Records = kept
	}
	return out
}

// ApplyInstanceDNSRequest is the parent's whole decision on one request: the
// instance's FULL desired set. It returns the config to store (the receiver is
// not modified), one verdict per ask in order, and every record set the
// instance owned that the set no longer lists (removed, tombstoned).
//
// Per ask: refused when the type is not A/AAAA/CNAME/TXT, the name is
// outside the grant, a value does not parse, or the set would break the
// zone's record rules; refused when another instance owns the name; when this
// hz declares the name itself (by hand) — CONVERTED to the instance's when the
// values are identical, refused naming the record when they differ. Accepted
// asks replace what the instance owned at that (name, type).
//
// A refused ask keeps what the instance already owned at that name: it is in
// the set, so it is not a removal request.
func (c *Config) ApplyInstanceDNSRequest(instance string, asks []InstanceDNSAsk, nowUnix int64) (*Config, []InstanceDNSVerdict, []InstanceDNSRemoval, error) {
	h, ok := c.FindHZInstance(instance)
	if !ok {
		return nil, nil, nil, fmt.Errorf("no hz instance %q%s", instance, c.hzInstanceHint())
	}
	next := *c
	next.copyZonesForWrite()
	listed := map[string]bool{} // zone|name|TYPE the set names, accepted or not
	asked := map[string]bool{}  // name|TYPE, to refuse a duplicate ask
	verdicts := make([]InstanceDNSVerdict, 0, len(asks))
	for _, a := range asks {
		name, t := normalizeRecordKey(a.Name, a.Type)
		v := InstanceDNSVerdict{Name: name, Type: t}
		var values []string
		for _, raw := range a.Values {
			if cv := CanonicalRecordValue(t, raw); cv != "" && !containsString(values, cv) {
				values = append(values, cv)
			}
		}
		sort.Strings(values)
		v.Values = values
		refuse := func(format string, args ...any) {
			v.Reason = fmt.Sprintf(format, args...)
			verdicts = append(verdicts, v)
		}
		if name == "" {
			refuse("a record with no name")
			continue
		}
		if asked[name+"|"+t] {
			refuse("%s %s is listed twice in the request; send each (name, type) once with all its values", t, name)
			continue
		}
		asked[name+"|"+t] = true
		if !instanceDNSTypes[t] {
			refuse("type %q is not one an instance may ask for; A, AAAA, CNAME or TXT", t)
			continue
		}
		if !GrantCovers(h.DNSGrant, name) {
			refuse("%s is outside hz instance %s's DNS grant (%s); the parent's operator widens the grant", name, h.Name, grantList(h.DNSGrant))
			continue
		}
		zone := next.owningZone(strings.TrimPrefix(name, "*."))
		if zone == nil || zone.GetDNSProvider() == nil {
			refuse("%s is in no zone this hz publishes through a DNS provider", name)
			continue
		}
		v.Zone = zone.Name
		listed[zone.Name+"|"+name+"|"+t] = true
		if len(values) == 0 {
			refuse("%s %s has no values; to remove it, leave it out of the set", t, name)
			continue
		}
		if a.TTL < 0 || a.TTL > 86400 {
			refuse("ttl %d for %s %s is outside 0..86400 seconds", a.TTL, t, name)
			continue
		}
		var bad error
		for _, val := range values {
			if err := (DNSRecord{Name: name, Type: t, Value: val}).Validate(); err != nil {
				bad = err
				break
			}
		}
		if bad != nil {
			refuse("%s %s: %v", t, name, bad)
			continue
		}
		var mine, theirs, other []DNSRecord
		var rest []DNSRecord
		for _, r := range zone.Records {
			n, rt := normalizeRecordKey(zone.qualify(r.Name), r.Type)
			if n != name || rt != t {
				rest = append(rest, r)
				continue
			}
			switch r.Instance {
			case h.Name:
				mine = append(mine, r)
			case "":
				theirs = append(theirs, r)
			default:
				other = append(other, r)
			}
		}
		if len(other) > 0 {
			refuse("%s %s is owned by hz instance %s", t, name, other[0].Instance)
			continue
		}
		ttl := a.TTL
		if len(theirs) > 0 {
			have := make([]string, len(theirs))
			for i, r := range theirs {
				have[i] = CanonicalRecordValue(t, r.Value)
			}
			sort.Strings(have)
			if !equalStrings(have, values) {
				refuse("this hz declares %s %s -> %s by hand, which differs from the requested %s; the parent's operator decides — delete that record (the next request then publishes yours) or keep it and drop the name from the request",
					t, name, strings.Join(have, ", "), strings.Join(values, ", "))
				continue
			}
			v.Converted = true
			if ttl == 0 {
				ttl = theirs[0].TTL
			}
		}
		if ttl == 0 && len(mine) > 0 {
			ttl = mine[0].TTL
		}
		recs := make([]DNSRecord, 0, len(values))
		for _, val := range values {
			recs = append(recs, DNSRecord{Name: name, Type: t, Value: val, TTL: ttl, Instance: h.Name,
				Note: "requested by hz instance " + h.Name})
		}
		candidate := *zone
		candidate.Records = append(append([]DNSRecord(nil), rest...), recs...)
		if err := candidate.ValidateRecords(); err != nil {
			refuse("%s %s would break zone %s: %v", t, name, zone.Name, err)
			continue
		}
		zone.Records = candidate.Records
		next.ClearTombstonesForSet(zone.Name, name, t)
		v.Accepted = true
		if v.Converted {
			v.Reason = fmt.Sprintf("converted: this hz declared %s %s -> %s by hand; it is now owned by hz instance %s", t, name, strings.Join(values, ", "), h.Name)
		}
		verdicts = append(verdicts, v)
	}
	removed := next.dropInstanceRecords(h.Name, func(zone, name, recType string) bool {
		return listed[zone+"|"+name+"|"+recType]
	}, "no longer requested", nowUnix)
	if err := next.ValidateInstanceDNS(); err != nil {
		return nil, nil, nil, err
	}
	return &next, verdicts, removed, nil
}

func containsString(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ParentDNSAsks is what a NESTED hz asks its parent to publish: one A record
// set per domain of each service with external_dns that no zone here
// publishes (no zone with a DNS provider contains it), valued with the
// service's publishable addresses — its pin, else this hz's public IP.
//
// unasked is set, and asks nil, when a needed address is unknown or stale:
// the set is the WHOLE desired state, and a name left out of it reads to the
// parent as a removal (CLAUDE.md #2 — unknown is not empty).
func (c *Config) ParentDNSAsks() (asks []InstanceDNSAsk, unasked string) {
	seen := map[string]bool{}
	for i := range c.Services {
		svc := &c.Services[i]
		if svc.ExternalDNS == nil {
			continue
		}
		for _, d := range svc.Domains {
			name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
			if name == "" || seen[name] {
				continue
			}
			if z := c.owningZone(strings.TrimPrefix(name, "*.")); z != nil && z.GetDNSProvider() != nil {
				continue // this hz publishes it itself
			}
			ips := c.PublishablePublicIPs(svc)
			if len(ips) == 0 {
				return nil, "this hz's public IP is unknown or stale, so the A record for " + name + " (service " + svc.Name + ") has no value; nothing is asked of the parent until it is detected — a name left out would read as a removal"
			}
			seen[name] = true
			ttl := svc.ExternalDNS.TTL
			if ttl < 0 {
				ttl = 0
			}
			asks = append(asks, InstanceDNSAsk{Name: name, Type: "A", Values: append([]string(nil), ips...), TTL: ttl})
		}
	}
	sort.Slice(asks, func(i, j int) bool { return asks[i].Name < asks[j].Name })
	return asks, ""
}
