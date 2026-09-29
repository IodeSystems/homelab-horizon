package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Pending-change detection. Config mutations apply to local subsystems
// (dnsmasq/HAProxy) immediately, but external DNS and SSL certs are only
// published by an explicit full Sync. To show the operator that a Sync is due
// — and what it will push — we keep a snapshot of the config as of the last
// successful sync in a sidecar file and diff the live config against it.

// syncedConfigPath is the sidecar holding the config snapshot as of the last
// successful full sync.
func (s *Server) syncedConfigPath() string {
	return s.configPath + ".synced"
}

// markSynced snapshots the current config as the synced baseline. Called after
// a successful full sync and after applying a pulled peer config. No-op in
// dry-run (no files should be written).
func (s *Server) markSynced() {
	if s.dryRun {
		return
	}
	syncedMu.Lock()
	defer syncedMu.Unlock()
	data, err := json.MarshalIndent(s.cfg(), "", "  ")
	if err != nil {
		slog.Error("markSynced: marshal config", "err", err)
		return
	}
	if err := os.WriteFile(s.syncedConfigPath(), data, 0600); err != nil {
		slog.Error("markSynced: write synced baseline", "err", err)
	}
}

// initSyncedBaseline writes the baseline on first run so a fresh install (or
// the first run after this feature ships) shows nothing pending until the next
// edit. An existing baseline is left untouched.
func (s *Server) initSyncedBaseline() {
	if s.dryRun {
		return
	}
	if _, err := os.Stat(s.syncedConfigPath()); err == nil {
		return
	}
	s.markSynced()
}

// syncedMu serialises writes to the synced baseline: a full sync's markSynced
// and a direct record publish's markSetSynced can finish at the same time.
var syncedMu sync.Mutex

// markSetSynced folds ONE record set that was just published straight to the
// provider (records/set, and the per-value add/edit/delete) into the synced
// baseline. Without it `hz pending` kept listing the zone as modified for a
// set that was already live — "(published)" from `hz dns record add`, and
// "not synced" from `hz pending`, about the same records.
//
// Only that (name, type) is copied, records and tombstones both: every other
// unsynced edit in the zone stays pending. A zone absent from the baseline is
// left alone — the zone itself has not been synced, so it stays "added".
// The baseline file is edited as JSON, not round-tripped through
// config.LoadFromJSON, so load-time defaults cannot leak into it.
func (s *Server) markSetSynced(zoneName, name, recType string) {
	if s.dryRun {
		return
	}
	syncedMu.Lock()
	defer syncedMu.Unlock()

	live := s.cfg().GetZone(zoneName)
	if live == nil {
		return
	}
	data, err := os.ReadFile(s.syncedConfigPath())
	if err != nil {
		return
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		slog.Warn("markSetSynced: parse synced baseline", "err", err)
		return
	}
	var zones []map[string]json.RawMessage
	if err := json.Unmarshal(top["zones"], &zones); err != nil {
		return
	}
	for i := range zones {
		var bz config.Zone
		if err := json.Unmarshal(mustMarshal(zones[i]), &bz); err != nil || bz.Name != zoneName {
			continue
		}
		inSet := func(d config.DNSRecord) bool {
			return strings.EqualFold(bz.Qualify(d.Name), name) && d.NormalizedType() == strings.ToUpper(recType)
		}
		var recs []config.DNSRecord
		for _, d := range bz.Records {
			if !inSet(d) {
				recs = append(recs, d)
			}
		}
		for _, d := range live.Records {
			if inSet(d) {
				recs = append(recs, d)
			}
		}
		var tombs []config.DNSTombstone
		for _, t := range bz.Tombstones {
			if !t.MatchesSet(name, recType) {
				tombs = append(tombs, t)
			}
		}
		for _, t := range live.Tombstones {
			if t.MatchesSet(name, recType) {
				tombs = append(tombs, t)
			}
		}
		setOrDrop(zones[i], "records", len(recs) > 0, recs)
		setOrDrop(zones[i], "tombstones", len(tombs) > 0, tombs)
		top["zones"] = mustMarshal(zones)
		out, err := json.MarshalIndent(top, "", "  ")
		if err != nil {
			return
		}
		if err := os.WriteFile(s.syncedConfigPath(), out, 0600); err != nil {
			slog.Error("markSetSynced: write synced baseline", "err", err)
		}
		return
	}
}

func setOrDrop(m map[string]json.RawMessage, key string, set bool, v any) {
	if !set {
		delete(m, key) // both fields are omitempty
		return
	}
	m[key] = mustMarshal(v)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// loadSyncedBaseline reads the last-synced snapshot, or nil if absent/unreadable.
func (s *Server) loadSyncedBaseline() *config.Config {
	data, err := os.ReadFile(s.syncedConfigPath())
	if err != nil {
		return nil
	}
	cfg, err := config.LoadFromJSON(data)
	if err != nil {
		slog.Warn("pending: parse synced baseline", "err", err)
		return nil
	}
	return cfg
}

// computePending diffs the live config against the synced baseline. With no
// baseline (dry-run or first boot before init) nothing is pending.
func (s *Server) computePending() apitypes.PendingChanges {
	baseline := s.loadSyncedBaseline()
	if baseline == nil {
		return apitypes.PendingChanges{Items: []apitypes.PendingItem{}}
	}
	items := diffConfig(baseline, s.cfg())
	return apitypes.PendingChanges{
		HasPending: len(items) > 0,
		Count:      len(items),
		Items:      items,
	}
}

func (s *Server) handleAPIPendingChanges(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.computePending())
}

// settingsExcluded lists the top-level config keys NOT counted as pending
// changes: runtime caches, locally-detected state, secrets, ban/MFA-session
// state, and fleet identity. These either mutate without a user edit (and would
// flag phantom pending) or aren't part of what a Sync publishes. services and
// zones are excluded here because they're diffed individually above. Anything
// not listed is a declarative setting, so new settings are covered by default.
var settingsExcluded = map[string]bool{
	"services": true, "zones": true,
	"admin_token":            true, // runtime secret, pinned per-instance
	"public_ip":              true, // runtime auto-detection cache
	"public_ip_last_checked": true, // runtime auto-detection cache
	"last_published_records": true, // DNS publish baseline, written by every publish, not an edit
	"last_local_iface":       true, // runtime interface-reconcile state
	"last_lan_cidr":          true, // runtime interface-reconcile state
	"blessed_iptables_rules": true, // local admin state, own UI, not sync-published
	"wg_peers":               true, // VPN peer state, own mutation flow
	"vpn_mfa_secrets":        true, // secrets
	"ntfy_token":             true, // secret, write-only across the API — the diff is served to the UI
	"vpn_mfa_sessions":       true, // runtime session expiries
	"ip_bans":                true, // ban state, mutated at runtime
	"peer_id":                true, // fleet identity, local
	"config_primary":         true, // fleet identity, local
	"peers":                  true, // fleet membership, local
	"vpn_projects":           true, // attribution only — see stripAttribution
}

// attributionOnly lists the per-element keys that are ATTRIBUTION: which
// project a record belongs to. Amendment 6 (plan/design/ui.md) pins that
// attribution changes no rendered artifact
// (TestAttributionChangesNoRenderedArtifact), so a Sync has nothing to publish
// for it, and counting it as pending told the operator to Sync for nothing —
// the operator's own report: "aw4 modified since the last Sync" for an edit
// that was only `project: → iode, environment: → dev`.
var attributionOnly = map[string][]string{
	"services":        {"project", "environment"},
	"machines":        {"project"},
	"service_checks":  {"project"},
	"port_exclusions": {"project"},
}

// elementSecrets lists per-element keys that are SECRETS, write-only across
// the API. The diff is served to the UI, and a list like remote_probes is
// diffed as one stringified array, so an unstripped secret appeared in full
// in the Before/After of every edit to its vantage. A Sync publishes none of
// them, so dropping them also stops a rotation reading as pending.
var elementSecrets = map[string][]string{
	"remote_probes": {"token", "ntfy_url", "ntfy_token"},
}

// stripAttribution removes the attribution keys from one marshalled element of
// `list` (a top-level config key), leaving everything a Sync can publish.
func stripAttribution(list string, elem []byte) []byte {
	keys := attributionOnly[list]
	if len(keys) == 0 {
		return elem
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(elem, &m); err != nil {
		return elem
	}
	for _, k := range keys {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return out
}

// diffConfig reports added/removed/modified services and zones plus a single
// "settings" item covering changed top-level settings (HAProxy/DNS/SSL toggles,
// ports, monitoring, VPN policy, ...). zones carry their own DNS records.
func diffConfig(base, cur *config.Config) []apitypes.PendingItem {
	items := diffSet("service", marshalServices(base), marshalServices(cur))
	items = append(items, diffSet("zone", marshalZones(base), marshalZones(cur))...)
	if fields := diffFields(settingsObject(base), settingsObject(cur)); len(fields) > 0 {
		items = append(items, apitypes.PendingItem{
			Kind: "settings", Name: "settings", Change: "modified", Fields: fields,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Name < items[j].Name
	})
	return items
}

// settingsObject marshals the config to its top-level JSON object with the
// excluded keys removed, so only declarative settings remain for diffing.
func settingsObject(c *config.Config) []byte {
	var m map[string]json.RawMessage
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &m)
	for k := range m {
		if settingsExcluded[k] {
			delete(m, k)
			continue
		}
		if _, ok := attributionOnly[k]; ok {
			var elems []json.RawMessage
			if json.Unmarshal(m[k], &elems) == nil {
				for i := range elems {
					elems[i] = stripAttribution(k, elems[i])
				}
				m[k], _ = json.Marshal(elems)
			}
		}
		if keys, ok := elementSecrets[k]; ok {
			var elems []json.RawMessage
			if json.Unmarshal(m[k], &elems) == nil {
				for i := range elems {
					elems[i] = dropKeys(elems[i], keys)
				}
				m[k], _ = json.Marshal(elems)
			}
		}
	}
	out, _ := json.Marshal(m)
	return out
}

// serviceServerHeld lists the per-service keys hz keeps for itself and never
// publishes. Service.Token authenticates calls INTO hz (handlers_deploy.go); a
// Sync writes it nowhere. It is also generated lazily — at startup
// (ensureServicesRunning) and on the first read of /api/v1/services/integration
// — so a service created and synced gained it afterwards and read as pending
// forever: "modified service bintag token: -> bd63...", with nothing a Sync
// could publish to clear it.
var serviceServerHeld = []string{"token"}

func marshalServices(c *config.Config) map[string][]byte {
	m := make(map[string][]byte, len(c.Services))
	for i := range c.Services {
		b, _ := json.Marshal(c.Services[i])
		m[c.Services[i].Name] = dropKeys(stripAttribution("services", b), serviceServerHeld)
	}
	return m
}

// dropKeys removes keys from one marshalled JSON object.
func dropKeys(elem []byte, keys []string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(elem, &m); err != nil {
		return elem
	}
	for _, k := range keys {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return out
}

// marshalZones sorts each zone's records and tombstones first. Their order
// publishes nothing, and a set folded into the baseline by markSetSynced lands
// at a different position than the live config's edit put it.
func marshalZones(c *config.Config) map[string][]byte {
	m := make(map[string][]byte, len(c.Zones))
	for i := range c.Zones {
		z := c.Zones[i]
		z.Records = append([]config.DNSRecord(nil), z.Records...)
		sort.SliceStable(z.Records, func(a, b int) bool { return recordKey(z.Records[a]) < recordKey(z.Records[b]) })
		z.Tombstones = append([]config.DNSTombstone(nil), z.Tombstones...)
		sort.SliceStable(z.Tombstones, func(a, b int) bool {
			ta, tb := z.Tombstones[a], z.Tombstones[b]
			return ta.Name+"\x00"+ta.Type+"\x00"+ta.Value < tb.Name+"\x00"+tb.Type+"\x00"+tb.Value
		})
		if len(z.Records) == 0 {
			z.Records = nil
		}
		if len(z.Tombstones) == 0 {
			z.Tombstones = nil
		}
		b, _ := json.Marshal(z)
		m[z.Name] = b
	}
	return m
}

func recordKey(d config.DNSRecord) string {
	return strings.ToLower(d.Name) + "\x00" + d.NormalizedType() + "\x00" + d.Value
}

func diffSet(kind string, base, cur map[string][]byte) []apitypes.PendingItem {
	names := make(map[string]bool, len(base)+len(cur))
	for n := range base {
		names[n] = true
	}
	for n := range cur {
		names[n] = true
	}

	items := make([]apitypes.PendingItem, 0)
	for name := range names {
		b, inB := base[name]
		c, inC := cur[name]
		switch {
		case inB && !inC:
			items = append(items, apitypes.PendingItem{Kind: kind, Name: name, Change: "removed"})
		case !inB && inC:
			items = append(items, apitypes.PendingItem{Kind: kind, Name: name, Change: "added"})
		case !bytes.Equal(b, c):
			items = append(items, apitypes.PendingItem{
				Kind: kind, Name: name, Change: "modified", Fields: diffFields(b, c),
			})
		}
	}
	return items
}

// diffFields flattens both JSON objects to dotted paths and reports the fields
// whose values differ.
func diffFields(before, after []byte) []apitypes.FieldChange {
	bf := flattenJSON(before)
	af := flattenJSON(after)

	keys := make(map[string]bool, len(bf)+len(af))
	for k := range bf {
		keys[k] = true
	}
	for k := range af {
		keys[k] = true
	}

	changes := make([]apitypes.FieldChange, 0)
	for k := range keys {
		if bf[k] != af[k] {
			changes = append(changes, apitypes.FieldChange{Path: k, Before: bf[k], After: af[k]})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func flattenJSON(b []byte) map[string]string {
	out := make(map[string]string)
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return out
	}
	flattenValue("", v, out)
	return out
}

// flattenValue walks objects into dotted paths; arrays and scalars are
// stringified as a single value at their path.
func flattenValue(prefix string, v any, out map[string]string) {
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenValue(key, val, out)
		}
		return
	}
	out[prefix] = stringifyValue(v)
}

func stringifyValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
