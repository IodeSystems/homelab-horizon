package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// POST /api/v1/zones/records/set
//
// Declares the complete value set hz owns at one (name, type) and publishes it.
// This is what `hz dns record add|edit` call. The per-value add/edit/delete
// endpoints serve the UI, which edits one row of a live listing at a time; a
// declaration from the CLI names the whole set (`--value a --value b`), and
// building that from per-value calls would publish every intermediate state.
//
// The rule it keeps: hz replaces whole record sets, so it never writes a set
// that holds a value it did not publish and was not told to own. Such a value
// is refused by name (409) instead of silently deleted. Listing it in Values
// adopts it: the record becomes hz's, and if the provider already holds exactly
// the requested set nothing is written at all.
func (s *Server) handleAPIRecordSet(w http.ResponseWriter, r *http.Request) {
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

	var req apitypes.DNSRecordSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Name), "."))
	recType := strings.ToUpper(strings.TrimSpace(req.Type))
	if name == "" || recType == "" {
		writeJSONError(w, http.StatusBadRequest, "name and type are required")
		return
	}
	if !config.IsDeclarableRecordType(recType) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("record type %q cannot be declared; allowed: %s",
			recType, strings.Join(config.DeclarableRecordTypes, ", ")))
		return
	}
	values := canonicalValues(recType, req.Values)
	if len(values) == 0 {
		writeJSONError(w, http.StatusBadRequest, "at least one value is required; to remove a record use /api/v1/zones/records/delete")
		return
	}

	zone, err := s.zoneForRecord(req.Zone, name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerCfg := zone.GetDNSProvider()
	if providerCfg == nil {
		writeJSONError(w, http.StatusBadRequest, "No DNS provider configured for zone "+zone.Name)
		return
	}

	// Everything the zone declared before, minus this set.
	var others, previous []config.DNSRecord
	for _, d := range zone.Records {
		if strings.EqualFold(zone.Qualify(d.Name), name) && d.NormalizedType() == recType {
			previous = append(previous, d)
			continue
		}
		others = append(others, d)
	}

	provider, err := s.dnsProvider(providerCfg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Provider error: "+err.Error())
		return
	}
	live, err := provider.ListRecords(zone.ZoneID)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Failed to read live records: "+err.Error())
		return
	}
	var liveVals []string
	liveTTL := 0
	for _, rec := range live {
		if strings.EqualFold(strings.TrimSuffix(rec.Name, "."), name) && strings.EqualFold(rec.Type, recType) {
			liveVals = append(liveVals, rec.Value)
			liveTTL = rec.TTL
		}
	}

	if !valueSetsEqual(liveVals, canonicalValues(recType, req.ExpectedFrom)) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"drift": true,
			"error": "record set changed at the provider since it was read; re-read and retry",
			"live":  liveVals,
		})
		return
	}

	// A live value that hz neither declared nor was told to own is someone
	// else's. Publishing the set would delete it.
	var foreign []string
	for _, v := range liveVals {
		if containsValue(values, v) {
			continue
		}
		owned := false
		for _, d := range previous {
			if config.CanonicalRecordValue(recType, d.Value) == v {
				owned = true
				break
			}
		}
		if !owned {
			foreign = append(foreign, v)
		}
	}
	if len(foreign) > 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"ok":      false,
			"foreign": foreign,
			"error": fmt.Sprintf("%s %s holds value(s) hz did not publish: %s. Writing the set would delete them. "+
				"List them as values to adopt them, or remove them at the provider first",
				name, recType, strings.Join(foreign, ", ")),
		})
		return
	}

	// TTL: asked for, else what is declared, else what is live (so adopting a
	// record rewrites nothing), else the default.
	ttl := req.TTL
	if ttl <= 0 && len(previous) > 0 && previous[0].TTL > 0 {
		ttl = previous[0].TTL
	}
	if ttl <= 0 && liveTTL > 0 {
		ttl = liveTTL
	}
	note := req.Note
	if note == "" && len(previous) > 0 {
		note = previous[0].Note
	}

	declared := make([]config.DNSRecord, 0, len(values))
	for _, v := range values {
		declared = append(declared, config.DNSRecord{Name: name, Type: recType, Value: v, TTL: ttl, Note: note})
	}
	candidate := *zone
	candidate.Records = append(append([]config.DNSRecord(nil), others...), declared...)
	if err := candidate.ValidateRecords(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			if cfg.Zones[i].Name == zone.Name {
				cfg.Zones[i].Records = candidate.Records
			}
		}
		// A declaration is a later statement of intent than a pending
		// deletion; leaving both makes sync publish and retract on alternate runs.
		cfg.ClearTombstonesForSet(zone.Name, name, recType)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	desired := make([]dns.Record, len(declared))
	for i, d := range declared {
		desired[i] = dns.Record{Name: name, Type: recType, Value: d.Value, TTL: d.EffectiveTTL(), ZoneID: zone.ZoneID}
	}
	changed := false
	if !valueSetsEqual(liveVals, values) || liveTTL != desired[0].TTL {
		changed, err = provider.SyncRecordSet(zone.ZoneID, desired)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "Saved to config but provider publish failed (the next sync retries it): "+err.Error())
			return
		}
	}
	s.setLastPublished(driftKey(zone.Name, name, recType), values)
	s.markSetSynced(zone.Name, name, recType)

	writeJSON(w, apitypes.DNSRecordSetResponse{
		OK: true, Zone: zone.Name, Name: name, Type: recType, Values: values, Changed: changed,
	})
}

// zoneForRecord resolves the zone a record belongs to: the named one, which
// must contain the name, or else the managed zone that contains it. A name
// under no managed zone is refused — hz cannot publish where it holds no zone,
// and guessing one would write the record somewhere nobody asked.
func (s *Server) zoneForRecord(zoneName, name string) (*config.Zone, error) {
	if zoneName == "" {
		z := s.cfg().GetZoneForDomain(name)
		if z == nil {
			return nil, fmt.Errorf("no managed zone contains %s", name)
		}
		return z, nil
	}
	z := s.cfg().GetZone(zoneName)
	if z == nil {
		return nil, fmt.Errorf("zone %s not found", zoneName)
	}
	if !z.ContainsName(name) {
		return nil, fmt.Errorf("%s is not in zone %s", name, z.Name)
	}
	return z, nil
}

func writeJSONStatus(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
