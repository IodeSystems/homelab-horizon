package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sort"

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
	"last_local_iface":       true, // runtime interface-reconcile state
	"last_lan_cidr":          true, // runtime interface-reconcile state
	"blessed_iptables_rules": true, // local admin state, own UI, not sync-published
	"wg_peers":               true, // VPN peer state, own mutation flow
	"vpn_mfa_secrets":        true, // secrets
	"vpn_mfa_sessions":       true, // runtime session expiries
	"ip_bans":                true, // ban state, mutated at runtime
	"peer_id":                true, // fleet identity, local
	"config_primary":         true, // fleet identity, local
	"peers":                  true, // fleet membership, local
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
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func marshalServices(c *config.Config) map[string][]byte {
	m := make(map[string][]byte, len(c.Services))
	for i := range c.Services {
		b, _ := json.Marshal(c.Services[i])
		m[c.Services[i].Name] = b
	}
	return m
}

func marshalZones(c *config.Config) map[string][]byte {
	m := make(map[string][]byte, len(c.Zones))
	for i := range c.Zones {
		b, _ := json.Marshal(c.Zones[i])
		m[c.Zones[i].Name] = b
	}
	return m
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
