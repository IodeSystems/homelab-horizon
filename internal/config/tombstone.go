package config

import "strings"

// DNS deletion is not the absence of a declaration.
//
// hz publishes records derived from services and declared on zones. Removing
// either only stops hz from *wanting* the record — it says nothing to the
// provider, and the sync walks outward from config, so a name that is no longer
// derived is simply never visited again. The record stays live forever. That is
// how a deleted service left a public A record pointing at a homelab.
//
// The fix is to make deletion an explicit state rather than an inference:
//
//	declared -> tombstoned -> (provider confirms gone) -> forgotten
//
// A tombstone is durable, so a retraction that fails — permissions, a drift
// block, the box rebooting mid-sync — is retried rather than lost. And because
// hz only ever deletes what it has been explicitly told to delete, it never
// needs to claim authority over the whole zone; anything it finds and does not
// own is ingested as track-only instead.

// normalizeRecordKey canonicalizes the (name, type) identity used to compare
// records across config, tombstones, and whatever the provider reports. Names
// come back FQDN-with-trailing-dot from some providers and bare from others.
func normalizeRecordKey(name, recType string) (string, string) {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".")),
		strings.ToUpper(strings.TrimSpace(recType))
}

// Matches reports whether the tombstone identifies this record.
func (t DNSTombstone) Matches(name, recType, value string) bool {
	tn, tt := normalizeRecordKey(t.Name, t.Type)
	rn, rt := normalizeRecordKey(name, recType)
	return tn == rn && tt == rt && t.Value == value
}

// MatchesSet reports whether the tombstone targets this (name, type), ignoring
// value — used to find every tombstone applying to one record set.
func (t DNSTombstone) MatchesSet(name, recType string) bool {
	tn, tt := normalizeRecordKey(t.Name, t.Type)
	rn, rt := normalizeRecordKey(name, recType)
	return tn == rn && tt == rt
}

// AddTombstone records an intent to delete, replacing any identical entry so a
// repeated delete doesn't stack duplicates. Returns false if the zone is
// unknown.
func (c *Config) AddTombstone(zoneName string, t DNSTombstone) bool {
	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		for _, existing := range c.Zones[i].Tombstones {
			if existing.Matches(t.Name, t.Type, t.Value) {
				return true // already pending
			}
		}
		c.Zones[i].Tombstones = append(c.Zones[i].Tombstones, t)
		return true
	}
	return false
}

// DropTombstone forgets a tombstone once the provider has confirmed the value
// is gone — or because the record was re-declared, which supersedes the intent.
func (c *Config) DropTombstone(zoneName, name, recType, value string) bool {
	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		// Fresh slice: callers mutate a shallow copy of Config, so filtering in
		// place writes into an array the previous copy still points at.
		kept := make([]DNSTombstone, 0, len(c.Zones[i].Tombstones))
		var dropped bool
		for _, t := range c.Zones[i].Tombstones {
			if t.Matches(name, recType, value) {
				dropped = true
				continue
			}
			kept = append(kept, t)
		}
		c.Zones[i].Tombstones = kept
		if len(kept) == 0 {
			c.Zones[i].Tombstones = nil
		}
		return dropped
	}
	return false
}

// ClearTombstonesForSet drops every tombstone on a (name, type). Call it when
// the record is declared again: the new declaration is a later statement of
// intent than the pending deletion, and leaving both would have sync publish
// and retract the same record on alternating runs.
func (c *Config) ClearTombstonesForSet(zoneName, name, recType string) int {
	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		// Fresh slice: callers mutate a shallow copy of Config, so filtering in
		// place writes into an array the previous copy still points at.
		kept := make([]DNSTombstone, 0, len(c.Zones[i].Tombstones))
		cleared := 0
		for _, t := range c.Zones[i].Tombstones {
			if t.MatchesSet(name, recType) {
				cleared++
				continue
			}
			kept = append(kept, t)
		}
		c.Zones[i].Tombstones = kept
		if len(kept) == 0 {
			c.Zones[i].Tombstones = nil
		}
		return cleared
	}
	return 0
}

// OwnsRecord reports whether hz is the author of this record — either derived
// from a service's external DNS, or declared on the zone. Ingest uses it to
// leave hz's own output alone: adopting a derived record as "observed" would
// give it two owners and make the inventory lie.
func (c *Config) OwnsRecord(zoneName, name, recType string) bool {
	rn, rt := normalizeRecordKey(name, recType)

	// Service-derived records are always A records at a service domain.
	if rt == "A" {
		for i := range c.Services {
			if c.Services[i].ExternalDNS == nil {
				continue
			}
			for _, d := range c.Services[i].Domains {
				dn, _ := normalizeRecordKey(d, "A")
				if dn == rn {
					return true
				}
			}
		}
	}

	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		for _, rec := range c.Zones[i].Records {
			cn, ct := normalizeRecordKey(c.Zones[i].qualify(rec.Name), rec.Type)
			if cn == rn && ct == rt {
				return true
			}
		}
	}
	return false
}

// qualify expands a record name to an FQDN. Declared names may be an FQDN, the
// apex ("@" or the zone name), or a bare label relative to the zone.
func (z *Zone) qualify(name string) string {
	n := strings.TrimSuffix(strings.TrimSpace(name), ".")
	if n == "" || n == "@" || strings.EqualFold(n, z.Name) {
		return z.Name
	}
	if strings.HasSuffix(strings.ToLower(n), "."+strings.ToLower(z.Name)) {
		return n
	}
	return n + "." + z.Name
}

// Who authored a record live at the provider. A single "managed" bool could not
// say this: it was computed from Zone.Records alone, so a record hz publishes
// from a service read as un-owned, indistinguishable from a stray MX someone
// added by hand. These four states are what the operator actually needs to tell
// apart before touching anything.
const (
	RecordOwnerDerived    = "derived"    // published from a service's external DNS
	RecordOwnerDeclared   = "declared"   // listed in Zone.Records
	RecordOwnerObserved   = "observed"   // live, but not hz's — never written or deleted
	RecordOwnerTombstoned = "tombstoned" // hz's, on its way out; awaiting confirmation
)

// ClassifyRecord reports who owns a record live at the provider. Tombstoned
// wins over everything: a pending retraction is the most recent statement of
// intent, and the record is about to stop existing.
func (c *Config) ClassifyRecord(zoneName, name, recType, value string) string {
	rn, rt := normalizeRecordKey(name, recType)

	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		for _, t := range c.Zones[i].Tombstones {
			if t.Matches(rn, rt, value) {
				return RecordOwnerTombstoned
			}
		}
		for _, rec := range c.Zones[i].Records {
			cn, ct := normalizeRecordKey(c.Zones[i].qualify(rec.Name), rec.Type)
			if cn == rn && ct == rt && rec.Value == value {
				return RecordOwnerDeclared
			}
		}
	}

	if rt == "A" {
		for i := range c.Services {
			if c.Services[i].ExternalDNS == nil {
				continue
			}
			for _, d := range c.Services[i].Domains {
				if dn, _ := normalizeRecordKey(d, "A"); dn == rn {
					return RecordOwnerDerived
				}
			}
		}
	}

	return RecordOwnerObserved
}

// ingestSkippedTypes are never adopted into ObservedRecords. NS and SOA at the
// apex are the provider's own delegation records: they are not hz's to track,
// and surfacing them as inventory invites someone to "clean them up" and break
// the zone.
var ingestSkippedTypes = map[string]bool{"NS": true, "SOA": true}

// ShouldIngest reports whether a record live at the provider should be adopted
// as track-only inventory.
func (c *Config) ShouldIngest(zoneName, name, recType string) bool {
	_, rt := normalizeRecordKey(name, recType)
	if ingestSkippedTypes[rt] {
		return false
	}
	if c.OwnsRecord(zoneName, name, recType) {
		return false
	}
	// A pending retraction is hz's record on its way out, not someone else's.
	for i := range c.Zones {
		if c.Zones[i].Name != zoneName {
			continue
		}
		for _, t := range c.Zones[i].Tombstones {
			if t.MatchesSet(name, recType) {
				return false
			}
		}
	}
	return true
}
