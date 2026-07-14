package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// GET /api/v1/zones/records?zone=<name>
//
// Lists every record live at the provider for the zone, tagging each with
// Managed=true when the zone declares it in Zone.Records (so the UI can show
// HZ-owned records alongside ones added out-of-band).
func (s *Server) handleAPIZoneRecords(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	zoneName := r.URL.Query().Get("zone")
	if zoneName == "" {
		writeJSONError(w, http.StatusBadRequest, "zone required")
		return
	}

	zone := s.cfg().GetZone(zoneName)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}

	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		writeJSONError(w, http.StatusBadRequest, "No DNS provider configured for zone")
		return
	}

	provider, err := dns.NewProvider(providerCfg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Provider error: "+err.Error())
		return
	}

	live, err := provider.ListRecords(zone.ZoneID)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Failed to list records: "+err.Error())
		return
	}

	// Index declared records by (name|TYPE|value) so live records can be tagged.
	managed := make(map[string]bool, len(zone.Records))
	for _, rec := range zone.Records {
		managed[recordKey(rec.Name, rec.NormalizedType(), rec.Value)] = true
	}

	resp := apitypes.ZoneRecordsResponse{Zone: zone.Name}
	for _, rec := range live {
		resp.Records = append(resp.Records, apitypes.DNSRecordResp{
			Name:    rec.Name,
			Type:    rec.Type,
			Value:   rec.Value,
			TTL:     rec.TTL,
			Managed: managed[recordKey(rec.Name, strings.ToUpper(rec.Type), rec.Value)],
		})
	}
	writeJSON(w, resp)
}

func recordKey(name, recordType, value string) string {
	return strings.TrimSuffix(name, ".") + "|" + recordType + "|" + value
}

// recordMutation is the shared request for add/edit/delete of a single value
// within a (name, type) record set.
type recordMutation struct {
	Zone         string   `json:"zone"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Value        string   `json:"value"`        // add: new value; edit: new value; delete: value to remove
	OldValue     string   `json:"oldValue"`     // edit only: the value being replaced
	TTL          int      `json:"ttl"`          // applied to the added/edited value
	ExpectedFrom []string `json:"expectedFrom"` // values the UI last saw live for (name,type); drift guard
}

const (
	recordOpAdd    = "add"
	recordOpEdit   = "edit"
	recordOpDelete = "delete"
)

// POST /api/v1/zones/records/add|edit|delete
//
// Set-based, drift-safe. Publishing is anchored to the LIVE record set: the
// client sends ExpectedFrom (the values it saw), and if that no longer matches
// what's live at the provider the mutation is refused (409, drift) rather than
// clobbering an out-of-band change. Unmanaged sibling values at the same
// (name,type) ride along untouched — only the targeted value is added/changed/
// removed. Config Zone.Records is updated to track HZ-managed values.
func (s *Server) handleAPIRecordAdd(w http.ResponseWriter, r *http.Request) {
	s.applyRecordMutation(w, r, recordOpAdd)
}

func (s *Server) handleAPIRecordEdit(w http.ResponseWriter, r *http.Request) {
	s.applyRecordMutation(w, r, recordOpEdit)
}

func (s *Server) handleAPIRecordDelete(w http.ResponseWriter, r *http.Request) {
	s.applyRecordMutation(w, r, recordOpDelete)
}

func (s *Server) applyRecordMutation(w http.ResponseWriter, r *http.Request, op string) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.dnsSyncBlocked() {
		writeDNSDriftBlocked(w, s)
		return
	}

	var req recordMutation
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	name := strings.TrimSuffix(strings.TrimSpace(req.Name), ".")
	recType := strings.ToUpper(strings.TrimSpace(req.Type))
	value := strings.TrimSpace(req.Value)
	oldValue := strings.TrimSpace(req.OldValue)

	if req.Zone == "" || name == "" || recType == "" {
		writeJSONError(w, http.StatusBadRequest, "zone, name and type are required")
		return
	}
	if op != recordOpDelete && value == "" {
		writeJSONError(w, http.StatusBadRequest, "value is required")
		return
	}
	if op == recordOpDelete && value == "" {
		value = oldValue // tolerate delete carrying the value in either field
	}
	if op == recordOpEdit && oldValue == "" {
		writeJSONError(w, http.StatusBadRequest, "oldValue is required for edit")
		return
	}

	zone := s.cfg().GetZone(req.Zone)
	if zone == nil {
		writeJSONError(w, http.StatusNotFound, "Zone not found")
		return
	}
	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		writeJSONError(w, http.StatusBadRequest, "No DNS provider configured for zone")
		return
	}
	provider, err := dns.NewProvider(providerCfg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Provider error: "+err.Error())
		return
	}

	// Fetch the live set for (name, type) as the publish base (preserves TTLs of
	// unmanaged siblings).
	live, err := provider.ListRecords(zone.ZoneID)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Failed to read live records: "+err.Error())
		return
	}
	var base []dns.Record
	var liveValues []string
	for _, rec := range live {
		if strings.TrimSuffix(rec.Name, ".") == name && strings.ToUpper(rec.Type) == recType {
			base = append(base, rec)
			liveValues = append(liveValues, rec.Value)
		}
	}

	// Drift guard: what we're about to change must match what the client saw.
	if !valueSetsEqual(liveValues, req.ExpectedFrom) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"drift": true,
			"error": "record set changed at the provider since it was loaded; refresh and retry",
			"live":  liveValues,
		})
		return
	}

	// Compute the desired set from the live base + the delta.
	desired, err := mutateRecordSet(op, base, name, recType, value, oldValue, req.TTL, zone.ZoneID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Update config Zone.Records to track HZ-managed values.
	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name != zone.Name {
				continue
			}
			cfg.Zones[i].Records = applyRecordToConfig(cfg.Zones[i].Records, op, name, recType, value, oldValue, req.TTL)
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Publish the whole set atomically (delete the set if now empty).
	if len(desired) == 0 {
		if err := provider.DeleteRecord(zone.ZoneID, name, recType); err != nil {
			writeJSONError(w, http.StatusBadGateway, "Deleted from config but provider delete failed: "+err.Error())
			return
		}
	} else if _, err := provider.SyncRecordSet(zone.ZoneID, desired); err != nil {
		writeJSONError(w, http.StatusBadGateway, "Saved to config but provider publish failed: "+err.Error())
		return
	}

	desiredValues := make([]string, len(desired))
	for i, rec := range desired {
		desiredValues[i] = rec.Value
	}
	writeJSON(w, map[string]any{"ok": true, "values": desiredValues})
}

// mutateRecordSet applies op to the live base set and returns the desired set.
func mutateRecordSet(op string, base []dns.Record, name, recType, value, oldValue string, ttl int, zoneID string) ([]dns.Record, error) {
	newRec := dns.Record{Name: name, Type: recType, Value: value, TTL: ttl, ZoneID: zoneID}
	switch op {
	case recordOpAdd:
		for _, rec := range base {
			if rec.Value == value {
				return nil, errors.New("record already exists")
			}
		}
		return append(base, newRec), nil
	case recordOpEdit:
		out := make([]dns.Record, 0, len(base))
		found := false
		for _, rec := range base {
			if rec.Value == oldValue {
				found = true
				out = append(out, newRec)
				continue
			}
			out = append(out, rec)
		}
		if !found {
			return nil, errors.New("record to edit not found in live set")
		}
		return out, nil
	case recordOpDelete:
		out := make([]dns.Record, 0, len(base))
		found := false
		for _, rec := range base {
			if rec.Value == value {
				found = true
				continue
			}
			out = append(out, rec)
		}
		if !found {
			return nil, errors.New("record to delete not found in live set")
		}
		return out, nil
	default:
		return nil, errors.New("unknown op")
	}
}

// applyRecordToConfig mirrors the mutation into the zone's declared records so
// the managed-tag and scheduled sync stay in step with what was published.
func applyRecordToConfig(records []config.DNSRecord, op, name, recType, value, oldValue string, ttl int) []config.DNSRecord {
	switch op {
	case recordOpAdd:
		return append(records, config.DNSRecord{Name: name, Type: recType, Value: value, TTL: ttl})
	case recordOpEdit:
		for i := range records {
			if recordMatches(records[i], name, recType, oldValue) {
				records[i].Value = value
				records[i].TTL = ttl
				return records
			}
		}
		// Wasn't tracked before (adopting an unmanaged record): add it.
		return append(records, config.DNSRecord{Name: name, Type: recType, Value: value, TTL: ttl})
	case recordOpDelete:
		out := records[:0]
		for _, rec := range records {
			if recordMatches(rec, name, recType, value) {
				continue
			}
			out = append(out, rec)
		}
		return out
	default:
		return records
	}
}

func recordMatches(rec config.DNSRecord, name, recType, value string) bool {
	return strings.TrimSuffix(rec.Name, ".") == name &&
		rec.NormalizedType() == recType && rec.Value == value
}

// valueSetsEqual reports whether two value slices contain the same values
// (order-independent, multiset).
func valueSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}

// zoneRecordSet is the desired state for one (name, type) record set within a
// zone: all declared values published atomically (libdns SetRecords replaces
// the whole set).
type zoneRecordSet struct {
	Records []dns.Record // same Name+Type, one entry per declared value
}

// buildZoneRecordSets groups a zone's declared records by (name, type) so each
// group can be published as a single atomic record set. Order is preserved:
// names appear in first-seen order, values within a name in declared order.
// Records that fail validation are skipped and returned in the errs slice.
func buildZoneRecordSets(zone config.Zone) (sets []zoneRecordSet, errs []error) {
	index := make(map[string]int) // "name|TYPE" -> position in sets
	for _, rec := range zone.Records {
		if err := rec.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		dnsRec := dns.Record{
			Name:   rec.Name,
			Type:   rec.NormalizedType(),
			Value:  rec.Value,
			TTL:    rec.EffectiveTTL(),
			ZoneID: zone.ZoneID,
		}
		key := rec.Name + "|" + dnsRec.Type
		if i, ok := index[key]; ok {
			sets[i].Records = append(sets[i].Records, dnsRec)
		} else {
			index[key] = len(sets)
			sets = append(sets, zoneRecordSet{Records: []dns.Record{dnsRec}})
		}
	}
	return sets, errs
}

// syncZoneRecords publishes every zone's statically-declared records
// (Zone.Records) to its DNS provider. For each (name, type) it owns the full
// record set, replacing any undeclared values at that name/type. Returns the
// number of record sets that changed and the number that failed.
func (s *Server) syncZoneRecords(run *dnsSyncRun) (updated, failed int, err error) {
	for _, zone := range s.cfg().Zones {
		if len(zone.Records) == 0 {
			continue
		}

		sets, errs := buildZoneRecordSets(zone)
		for _, e := range errs {
			slog.Warn("invalid zone DNS record", "zone", zone.Name, "err", e)
			failed++
		}
		if len(sets) == 0 {
			continue
		}

		providerCfg := zone.GetDNSProvider()
		if providerCfg == nil {
			slog.Warn("zone has records but no DNS provider", "zone", zone.Name)
			failed += len(sets)
			continue
		}

		provider, perr := dns.NewProvider(providerCfg)
		if perr != nil {
			slog.Error("zone DNS provider error", "zone", zone.Name, "err", perr)
			failed += len(sets)
			continue
		}

		for _, set := range sets {
			rec := set.Records[0]
			changed, serr := run.publish(provider, zone, rec.Name, rec.Type, set.Records)
			if errors.Is(serr, errDNSDriftBlocked) {
				return updated, failed, serr // abort the whole run
			}
			if serr != nil {
				slog.Error("zone record sync failed", "zone", zone.Name,
					"name", rec.Name, "type", rec.Type, "err", serr)
				failed++
				continue
			}
			if changed {
				slog.Info("zone record published", "zone", zone.Name,
					"name", rec.Name, "type", rec.Type, "values", len(set.Records))
				updated++
			}
		}
	}
	return updated, failed, nil
}

// --- Drift-safe publishing (Phase 3) --------------------------------------
//
// All DNS publishing on the modern dns.Provider path routes through
// dnsSyncRun.publish. Before writing a (name,type) set it compares three
// states: live (at the provider), expected (LastPublishedRecords — what hz last
// wrote), desired (what hz wants now).
//   - live == desired            → already correct; no write.
//   - live == expected / first   → intentional change; publish, record baseline.
//   - live != expected & desired → OUT-OF-BAND DRIFT: sync-back baseline to live,
//                                  set DNSDriftBlocked, ntfy-alert, abort the run.
// Once blocked, every sync entrypoint refuses until an operator clears the drift
// (POST /api/v1/dns/drift/clear). See plan/dns-records.md Phase 3.

// errDNSDriftBlocked aborts a sync run: drift was just detected, or a prior
// block is still in effect.
var errDNSDriftBlocked = errors.New("dns sync halted: out-of-band change detected at a DNS provider; clear the drift to resume")

func driftKey(zoneName, name, recType string) string {
	return zoneName + "|" + strings.TrimSuffix(name, ".") + "|" + strings.ToUpper(recType)
}

// dnsSyncRun holds per-run state so live records are fetched once per zone and
// reused across that zone's record sets.
type dnsSyncRun struct {
	s          *Server
	liveByZone map[string][]dns.Record // zone.Name -> live records
}

func (s *Server) newDNSSyncRun() *dnsSyncRun {
	return &dnsSyncRun{s: s, liveByZone: make(map[string][]dns.Record)}
}

func (r *dnsSyncRun) live(provider dns.Provider, zone config.Zone) ([]dns.Record, error) {
	if recs, ok := r.liveByZone[zone.Name]; ok {
		return recs, nil
	}
	recs, err := provider.ListRecords(zone.ZoneID)
	if err != nil {
		return nil, err
	}
	r.liveByZone[zone.Name] = recs
	return recs, nil
}

// publish drift-checks and publishes the desired set for (name,type). Returns
// whether it changed. On drift it records the block + alerts and returns
// errDNSDriftBlocked so the caller aborts the whole run.
func (r *dnsSyncRun) publish(provider dns.Provider, zone config.Zone, name, recType string, desired []dns.Record) (bool, error) {
	name = strings.TrimSuffix(name, ".")
	recType = strings.ToUpper(recType)
	key := driftKey(zone.Name, name, recType)

	liveAll, err := r.live(provider, zone)
	if err != nil {
		return false, err
	}
	var liveVals []string
	for _, rec := range liveAll {
		if strings.TrimSuffix(rec.Name, ".") == name && strings.ToUpper(rec.Type) == recType {
			liveVals = append(liveVals, rec.Value)
		}
	}
	desiredVals := make([]string, len(desired))
	for i, rec := range desired {
		desiredVals[i] = rec.Value
	}
	expected := r.s.cfg().LastPublishedRecords[key]

	switch classifyDrift(liveVals, expected, desiredVals) {
	case driftNoop:
		// Already correct at the provider: nothing to write; keep baseline honest.
		r.s.setLastPublished(key, desiredVals)
		return false, nil

	case driftPublish:
		// Intentional change: provider still holds what we last published (or this
		// is the first publish of this set). Safe to write.
		if len(desired) == 0 {
			if err := provider.DeleteRecord(zone.ZoneID, name, recType); err != nil {
				return false, err
			}
		} else if _, err := provider.SyncRecordSet(zone.ZoneID, desired); err != nil {
			return false, err
		}
		r.s.setLastPublished(key, desiredVals)
		r.liveByZone[zone.Name] = replaceLiveSet(liveAll, name, recType, desired)
		return true, nil

	default: // driftDrift
		// Live matches neither what we published nor what we want. Sync-back the
		// baseline to live, block, alert, and abort.
		r.s.recordDNSDrift(config.DNSDriftInfo{
			Zone:       zone.Name,
			Name:       name,
			Type:       recType,
			Expected:   expected,
			Live:       liveVals,
			DetectedAt: time.Now().Unix(),
		})
		return false, errDNSDriftBlocked
	}
}

type driftDecision int

const (
	driftNoop    driftDecision = iota // live already equals desired
	driftPublish                      // safe to write (live == last-published, or first run)
	driftDrift                        // out-of-band change: live differs from both
)

// classifyDrift decides how to reconcile a (name,type) set from the three
// observed value sets. All comparisons are order-independent.
func classifyDrift(live, expected, desired []string) driftDecision {
	if valueSetsEqual(live, desired) {
		return driftNoop
	}
	if len(expected) == 0 || valueSetsEqual(live, expected) {
		return driftPublish
	}
	return driftDrift
}

// replaceLiveSet returns liveAll with the (name,type) entries swapped for
// desired, so later sets published in the same run see current state.
func replaceLiveSet(liveAll []dns.Record, name, recType string, desired []dns.Record) []dns.Record {
	out := make([]dns.Record, 0, len(liveAll)+len(desired))
	for _, rec := range liveAll {
		if strings.TrimSuffix(rec.Name, ".") == name && strings.ToUpper(rec.Type) == recType {
			continue
		}
		out = append(out, rec)
	}
	return append(out, desired...)
}

// setLastPublished records the values hz published for a (name,type) key.
func (s *Server) setLastPublished(key string, values []string) {
	current := s.cfg().LastPublishedRecords[key]
	if valueSetsEqual(current, values) {
		return // no persist needed
	}
	_ = s.updateConfig(func(cfg *config.Config) {
		if cfg.LastPublishedRecords == nil {
			cfg.LastPublishedRecords = make(map[string][]string)
		}
		cfg.LastPublishedRecords[key] = values
	})
}

// recordDNSDrift persists the drift block + detail (syncing the baseline back to
// live so we stop fighting it) and fires an ntfy alert.
func (s *Server) recordDNSDrift(info config.DNSDriftInfo) {
	key := driftKey(info.Zone, info.Name, info.Type)
	_ = s.updateConfig(func(cfg *config.Config) {
		cfg.DNSDriftBlocked = true
		d := info
		cfg.DNSDriftDetail = &d
		if cfg.LastPublishedRecords == nil {
			cfg.LastPublishedRecords = make(map[string][]string)
		}
		cfg.LastPublishedRecords[key] = info.Live // sync-back: adopt live as baseline
	})
	slog.Error("DNS drift detected; halting all DNS sync",
		"zone", info.Zone, "name", info.Name, "type", info.Type,
		"expected", info.Expected, "live", info.Live)
	s.notifyNtfy(
		"⚠️ DNS drift — sync halted",
		fmt.Sprintf("%s %s in zone %s changed out-of-band.\n\nhz published: %v\nnow live:     %v\n\nAll DNS sync is halted until you clear the drift.",
			info.Type, info.Name, info.Zone, info.Expected, info.Live),
		"warning", "high",
	)
}

// notifyNtfy posts a best-effort notification to the configured ntfy topic.
func (s *Server) notifyNtfy(title, message, tags, priority string) {
	url := s.cfg().NtfyURL
	if url == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(message))
	if err != nil {
		return
	}
	req.Header.Set("Title", title)
	if tags != "" {
		req.Header.Set("Tags", tags)
	}
	if priority != "" {
		req.Header.Set("Priority", priority)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// dnsSyncBlocked reports whether DNS sync is currently halted by drift.
func (s *Server) dnsSyncBlocked() bool {
	return s.cfg().DNSDriftBlocked
}

// writeDNSDriftBlocked responds 409 with the drift detail that halted sync.
func writeDNSDriftBlocked(w http.ResponseWriter, s *Server) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      false,
		"blocked": true,
		"error":   "DNS sync is halted: an out-of-band change was detected at a DNS provider. Review and clear the drift to resume.",
		"drift":   s.cfg().DNSDriftDetail,
	})
}

// GET /api/v1/dns/drift — current drift/block status for the UI banner.
func (s *Server) handleAPIDNSDriftStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	resp := apitypes.DNSDriftStatusResponse{Blocked: s.cfg().DNSDriftBlocked}
	if d := s.cfg().DNSDriftDetail; d != nil {
		resp.Detail = &apitypes.DNSDriftInfoResp{
			Zone: d.Zone, Name: d.Name, Type: d.Type,
			Expected: d.Expected, Live: d.Live, DetectedAt: d.DetectedAt,
		}
	}
	writeJSON(w, resp)
}

// POST /api/v1/dns/drift/clear — operator accepts the current live state and
// resumes DNS sync. The baseline was already synced-back to live on detection,
// so on the next sync hz re-asserts declared config over anything still drifted.
func (s *Server) handleAPIClearDNSDrift(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.DNSDriftBlocked = false
		cfg.DNSDriftDetail = nil
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONOK(w)
}
