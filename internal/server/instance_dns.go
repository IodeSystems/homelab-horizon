package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// DNS for a nested hz (N4h, operator 2026-10-03: "if a parent OWNS a domain,
// then the child SYNC needs to push that sync to the parent").
//
//	POST /api/v1/hz-instances/<name>/dns   <name>'s instance token only
//
// PARENT SIDE. The child sends its full desired set; config decides it
// (ApplyInstanceDNSRequest: inside the grant or refused, the hot-fix
// conversion, removals of what the instance owns). Accepted records are
// zone records attributed to the instance, published through the SAME
// drift-safe path as every declared record (dnsSyncRun.publish) — here, right
// after a request that changed something, and by the parent's own syncs.
// Nothing outside the instance's grant is ever written, even when the config
// says to: InstanceRecordPublishable is asked again at the write.
//
// CHILD SIDE. internal/nested posts and keeps the answer; the child's sync
// output and its self row (ParentDNSResp) show it.

// instanceDNSMemo serialises requests (a request reads the config, decides,
// and writes it back) and keeps each instance's last request for its page.
type instanceDNSMemo struct {
	mu   sync.Mutex
	rows map[string]instanceDNSRow
	// failedAt throttles a publish that keeps failing at the provider: the
	// child re-sends every minute while a record is pending.
	failedAt map[string]time.Time
}

type instanceDNSRow struct {
	at       time.Time
	publicIP string
	refused  []apitypes.InstanceDNSRecordStatus
}

func (m *instanceDNSMemo) get(name string) instanceDNSRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[name]
}

// instanceDNSPublishBackoff is how long a failed publish for one instance is
// not retried from its requests. The parent's own syncs still retry it.
const instanceDNSPublishBackoff = 5 * time.Minute

// handleAPIHZInstanceDNS takes one instance's full desired DNS set.
func (s *Server) handleAPIHZInstanceDNS(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	caller, ok := s.instanceCaller(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized — an instance's DNS request is taken from that instance's token only")
		return
	}
	if caller != name {
		writeJSONError(w, http.StatusForbidden, "instance:"+caller+" may ask for its own DNS only, not "+name+"'s")
		return
	}
	if !issuesHere(s.cfg()) {
		writeJSONError(w, http.StatusServiceUnavailable, "this hz is an HA replica; instance DNS is published by the config primary — ask the primary")
		return
	}
	var req apitypes.InstanceDNSReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	asks := make([]config.InstanceDNSAsk, 0, len(req.Records))
	for _, rec := range req.Records {
		asks = append(asks, config.InstanceDNSAsk{Name: rec.Name, Type: rec.Type, Values: rec.Values, TTL: rec.TTL})
	}

	s.instanceDNS.mu.Lock()
	cur := s.cfg()
	next, verdicts, removed, err := cur.ApplyInstanceDNSRequest(name, asks, time.Now().Unix())
	if err != nil {
		s.instanceDNS.mu.Unlock()
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	changed := zonesJSON(cur) != zonesJSON(next)
	if changed {
		if err := s.updateConfig(func(c *config.Config) { c.Zones = next.Zones }); err != nil {
			s.instanceDNS.mu.Unlock()
			writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
			return
		}
	}
	var refused []apitypes.InstanceDNSRecordStatus
	for _, v := range verdicts {
		if !v.Accepted {
			refused = append(refused, apitypes.InstanceDNSRecordStatus{Name: v.Name, Type: v.Type, Values: nonNil(v.Values), Status: apitypes.InstanceDNSRefused, Reason: v.Reason})
		}
	}
	if s.instanceDNS.rows == nil {
		s.instanceDNS.rows = map[string]instanceDNSRow{}
	}
	s.instanceDNS.rows[name] = instanceDNSRow{at: time.Now(), publicIP: strings.TrimSpace(req.PublicIP), refused: refused}
	s.instanceDNS.mu.Unlock()

	for _, v := range verdicts {
		switch {
		case v.Converted:
			slog.Warn("LOUD: "+v.Reason, "instance", name, "zone", v.Zone)
		case !v.Accepted:
			slog.Info("instance-dns: refused", "instance", name, "name", v.Name, "type", v.Type, "reason", v.Reason)
		}
	}
	for _, rm := range removed {
		slog.Info("instance-dns: removed (no longer requested); retracted at the provider", "instance", name, "zone", rm.Zone, "name", rm.Name, "type", rm.Type)
	}

	// Publish now when the set changed, or when something accepted is still
	// not published — the same drift-safe path the parent's syncs use.
	if changed || s.anyInstanceRecordPending(name) {
		s.publishInstanceDNSFromRequest(name)
	}

	cfg := s.cfg()
	out := apitypes.InstanceDNSResp{Instance: name, Records: make([]apitypes.InstanceDNSRecordStatus, 0, len(verdicts))}
	for _, v := range verdicts {
		st := apitypes.InstanceDNSRecordStatus{Name: v.Name, Type: v.Type, Values: nonNil(v.Values), Converted: v.Converted, Reason: v.Reason}
		if !v.Accepted {
			st.Status = apitypes.InstanceDNSRefused
		} else {
			status, why := s.instanceRecordStatus(cfg, v.Zone, v.Name, v.Type, v.Values)
			st.Status = status
			if why != "" && !v.Converted {
				st.Reason = why
			}
		}
		out.Records = append(out.Records, st)
	}
	for _, rm := range removed {
		out.Removed = append(out.Removed, apitypes.InstanceDNSRemoved{Name: rm.Name, Type: rm.Type, Values: nonNil(rm.Values)})
	}
	writeJSON(w, out)
}

func zonesJSON(c *config.Config) string {
	b, _ := json.Marshal(c.Zones)
	return string(b)
}

// publishInstanceDNSFromRequest is the publish a request triggers, unless a
// recent one failed (instanceDNSPublishBackoff) or DNS sync is halted.
func (s *Server) publishInstanceDNSFromRequest(name string) {
	if s.dnsSyncBlocked() {
		return
	}
	s.instanceDNS.mu.Lock()
	if at, ok := s.instanceDNS.failedAt[name]; ok && time.Since(at) < instanceDNSPublishBackoff {
		s.instanceDNS.mu.Unlock()
		return
	}
	s.instanceDNS.mu.Unlock()
	_, failed, err := s.publishInstanceDNS(s.newDNSSyncRun(), name)
	s.instanceDNS.mu.Lock()
	defer s.instanceDNS.mu.Unlock()
	if s.instanceDNS.failedAt == nil {
		s.instanceDNS.failedAt = map[string]time.Time{}
	}
	if (err != nil && !errors.Is(err, errDNSDriftBlocked)) || failed > 0 {
		s.instanceDNS.failedAt[name] = time.Now()
		slog.Error("instance-dns: publish failed; the next sync retries it", "instance", name, "failed", failed, "err", err)
		return
	}
	delete(s.instanceDNS.failedAt, name)
}

// publishInstanceDNS publishes every record set instance owns, through the
// drift-safe path, and retracts the ones removed from it. A record that is
// not publishable (outside the grant, an undeclared instance) is NEVER
// written: it is skipped, said LOUD, and counted failed.
func (s *Server) publishInstanceDNS(run *dnsSyncRun, instance string) (updated, failed int, err error) {
	cfg := s.cfg()
	if cfg.DNSDriftBlocked {
		return 0, 0, errDNSDriftBlocked
	}
	prefix := config.InstanceTombstoneReason(instance)
	for _, zone := range cfg.Zones {
		own := zone
		own.Records = nil
		for _, r := range zone.Records {
			if r.Instance != instance {
				continue
			}
			if why, ok := cfg.InstanceRecordPublishable(zone.Name, r); !ok {
				slog.Error("LOUD: instance-dns: NOT publishing a record outside the grant", "zone", zone.Name, "reason", why)
				failed++
				continue
			}
			own.Records = append(own.Records, r)
		}
		var tombs []config.DNSTombstone
		for _, t := range zone.Tombstones {
			if strings.HasPrefix(t.Reason, prefix) {
				tombs = append(tombs, t)
			}
		}
		if len(own.Records) == 0 && len(tombs) == 0 {
			continue
		}
		providerCfg := zone.GetDNSProvider()
		if providerCfg == nil {
			failed += len(own.Records) + len(tombs)
			continue
		}
		provider, perr := s.dnsProvider(providerCfg)
		if perr != nil {
			slog.Error("instance-dns: provider error", "zone", zone.Name, "err", perr)
			failed += len(own.Records) + len(tombs)
			continue
		}
		sets, errs := buildZoneRecordSets(own)
		failed += len(errs)
		for _, set := range sets {
			rec := set.Records[0]
			changed, serr := run.publish(provider, zone, rec.Name, rec.Type, set.Records)
			if errors.Is(serr, errDNSDriftBlocked) {
				return updated, failed, serr
			}
			if serr != nil {
				slog.Error("instance-dns: publish failed", "instance", instance, "zone", zone.Name, "name", rec.Name, "type", rec.Type, "err", serr)
				failed++
				continue
			}
			s.markSetSynced(zone.Name, strings.TrimSuffix(rec.Name, "."), rec.Type)
			if changed {
				slog.Info("instance-dns: published", "instance", instance, "zone", zone.Name, "name", rec.Name, "type", rec.Type, "values", len(set.Records))
				updated++
			}
		}
		if len(tombs) > 0 {
			tz := zone
			tz.Tombstones = tombs
			n, f, terr := s.retractZoneTombstones(run, provider, tz)
			updated += n
			failed += f
			if terr != nil {
				return updated, failed, terr
			}
		}
	}
	return updated, failed, nil
}

// publishAllInstanceDNS is the parent's sync step: every instance's records.
func (s *Server) publishAllInstanceDNS(run *dnsSyncRun) (updated, failed int, err error) {
	cfg := s.cfg()
	names := map[string]bool{}
	for _, h := range cfg.HZInstances {
		names[h.Name] = true
	}
	for _, z := range cfg.Zones {
		for _, r := range z.Records {
			if r.Instance != "" {
				names[r.Instance] = true // an undeclared owner is reported, never written
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		u, f, perr := s.publishInstanceDNS(run, n)
		updated += u
		failed += f
		if perr != nil {
			return updated, failed, perr
		}
	}
	return updated, failed, nil
}

// instanceRecordStatus is published when this hz's last publish of the set
// wrote exactly these values, else pending with the reason it knows.
func (s *Server) instanceRecordStatus(cfg *config.Config, zone, name, recType string, values []string) (string, string) {
	if valueSetsEqual(cfg.LastPublishedRecords[driftKey(zone, name, recType)], canonicalValues(recType, values)) {
		return apitypes.InstanceDNSPublished, ""
	}
	if cfg.DNSDriftBlocked {
		where := ""
		if d := cfg.DNSDriftDetail; d != nil {
			where = fmt.Sprintf(" (%s %s in zone %s)", d.Type, d.Name, d.Zone)
		}
		return apitypes.InstanceDNSPending, "DNS sync on the parent is halted by an out-of-band change" + where + "; the parent's operator reviews and clears it"
	}
	return apitypes.InstanceDNSPending, "stored on the parent; its DNS sync has not written it yet"
}

// instanceOwnedSets groups an instance's records by (zone, name, type).
func instanceOwnedSets(cfg *config.Config, instance string) []apitypes.InstanceOwnedRecordResp {
	var out []apitypes.InstanceOwnedRecordResp
	index := map[string]int{}
	for i := range cfg.Zones {
		z := &cfg.Zones[i]
		for _, r := range z.Records {
			if r.Instance != instance {
				continue
			}
			name := strings.ToLower(z.Qualify(r.Name))
			t := r.NormalizedType()
			k := z.Name + "|" + name + "|" + t
			v := config.CanonicalRecordValue(t, r.Value)
			if j, ok := index[k]; ok {
				out[j].Values = append(out[j].Values, v)
				continue
			}
			index[k] = len(out)
			out = append(out, apitypes.InstanceOwnedRecordResp{Zone: z.Name, Name: name, Type: t, Values: []string{v}, TTL: r.EffectiveTTL()})
		}
	}
	return out
}

func (s *Server) anyInstanceRecordPending(instance string) bool {
	cfg := s.cfg()
	for _, set := range instanceOwnedSets(cfg, instance) {
		if st, _ := s.instanceRecordStatus(cfg, set.Zone, set.Name, set.Type, set.Values); st != apitypes.InstanceDNSPublished {
			return true
		}
	}
	return false
}

// instanceDNSStatus is the parent's instance page: the grant, the records the
// instance owns here with their status, and its last request.
func (s *Server) instanceDNSStatus(cfg *config.Config, h config.HZInstance) *apitypes.InstanceDNSStatusResp {
	out := &apitypes.InstanceDNSStatusResp{Grant: append([]string{}, h.DNSGrant...), Records: []apitypes.InstanceOwnedRecordResp{}}
	for _, set := range instanceOwnedSets(cfg, h.Name) {
		set.Status, set.Reason = s.instanceRecordStatus(cfg, set.Zone, set.Name, set.Type, set.Values)
		out.Records = append(out.Records, set)
	}
	row := s.instanceDNS.get(h.Name)
	out.LastRequestAt = unixOrZero(row.at)
	out.LastPublicIP = row.publicIP
	out.LastRefused = row.refused
	return out
}

// parentDNSRequest is the child's request, built from its live config.
func (s *Server) parentDNSRequest() (apitypes.InstanceDNSReq, string) {
	cfg := s.cfg()
	asks, unasked := cfg.ParentDNSAsks()
	if unasked != "" {
		return apitypes.InstanceDNSReq{}, unasked
	}
	req := apitypes.InstanceDNSReq{Records: make([]apitypes.InstanceDNSRecordReq, 0, len(asks)), PublicIP: cfg.EffectivePublicIP()}
	for _, a := range asks {
		req.Records = append(req.Records, apitypes.InstanceDNSRecordReq{Name: a.Name, Type: a.Type, Values: a.Values, TTL: a.TTL})
	}
	return req, ""
}

// parentDNSResp is the child's self-row view of its parent's DNS answer.
func (s *Server) parentDNSResp(cfg *config.Config) *apitypes.ParentDNSResp {
	if cfg.Upstream == nil || s.child == nil {
		return nil
	}
	st := s.child.DNSStatus()
	return &st
}

// syncPublishableZone drops, LOUD, every instance-owned record the config
// holds but may not publish — the guard at the write, whatever got into the
// config.
func syncPublishableZone(cfg *config.Config, zone config.Zone) config.Zone {
	out := zone
	out.Records = nil
	for _, r := range zone.Records {
		if why, ok := cfg.InstanceRecordPublishable(zone.Name, r); !ok {
			slog.Error("LOUD: zone record sync: NOT publishing a record outside its instance's grant", "zone", zone.Name, "reason", why)
			continue
		}
		out.Records = append(out.Records, r)
	}
	return out
}
