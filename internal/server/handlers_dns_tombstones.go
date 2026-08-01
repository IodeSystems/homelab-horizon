package server

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

// retractTombstones drives pending deletions to completion at each provider.
//
// A tombstone is only forgotten once the provider says the value is gone, so a
// retraction that fails — permissions, a network blip, the process dying
// mid-sync — is retried on the next run instead of being silently lost. That
// durability is the whole point: the previous behaviour issued one best-effort
// delete and dropped the intent on the floor if it failed.
//
// Retraction routes through dnsSyncRun.publish rather than calling DeleteRecord
// directly, so it inherits the same drift protection as publishing: the desired
// set is "live minus the tombstoned values", and if live matches neither what hz
// last published nor what it now wants, the run blocks instead of overwriting an
// out-of-band change.
func (s *Server) retractTombstones(run *dnsSyncRun) (retracted, failed int, err error) {
	for _, zone := range s.cfg().Zones {
		if len(zone.Tombstones) == 0 {
			continue
		}

		providerCfg := zone.GetDNSProvider()
		if providerCfg == nil {
			slog.Warn("zone has tombstones but no DNS provider", "zone", zone.Name)
			failed += len(zone.Tombstones)
			continue
		}
		provider, perr := dns.NewProvider(providerCfg)
		if perr != nil {
			slog.Error("tombstone provider error", "zone", zone.Name, "err", perr)
			failed += len(zone.Tombstones)
			continue
		}

		zr, zf, zerr := s.retractZoneTombstones(run, provider, zone)
		retracted += zr
		failed += zf
		if zerr != nil {
			return retracted, failed, zerr
		}
	}
	return retracted, failed, nil
}

// retractZoneTombstones is the per-zone half, split out so tests can drive it
// with an injected provider the way the drift tests drive publish.
func (s *Server) retractZoneTombstones(run *dnsSyncRun, provider dns.Provider, zone config.Zone) (retracted, failed int, err error) {
	{
		// Group by (name, type): a record set is published atomically, so every
		// tombstone on the same set has to be applied in one write.
		type setKey struct{ name, recType string }
		grouped := map[setKey][]config.DNSTombstone{}
		var order []setKey
		for _, t := range zone.Tombstones {
			n := strings.TrimSuffix(t.Name, ".")
			k := setKey{n, strings.ToUpper(t.Type)}
			if _, seen := grouped[k]; !seen {
				order = append(order, k)
			}
			grouped[k] = append(grouped[k], t)
		}

		for _, k := range order {
			// Interlock: if hz derives or declares this record again, the
			// declaration is the newer intent and supersedes the pending
			// deletion. Without this the two fight — publish writes it, the
			// tombstone retracts it, and every sync flaps the record.
			if s.cfg().OwnsRecord(zone.Name, k.name, k.recType) {
				slog.Info("tombstone superseded by a live declaration",
					"zone", zone.Name, "name", k.name, "type", k.recType)
				s.forgetTombstones(zone.Name, grouped[k])
				continue
			}

			doomed := map[string]bool{}
			for _, t := range grouped[k] {
				doomed[t.Value] = true
			}

			liveAll, lerr := run.live(provider, zone)
			if lerr != nil {
				slog.Error("tombstone live read failed", "zone", zone.Name, "err", lerr)
				failed += len(grouped[k])
				continue
			}

			// Keep the siblings hz was not asked to remove.
			var remaining []dns.Record
			var liveVals []string
			var present bool
			for _, rec := range liveAll {
				if strings.TrimSuffix(rec.Name, ".") != k.name ||
					strings.ToUpper(rec.Type) != k.recType {
					continue
				}
				liveVals = append(liveVals, rec.Value)
				if doomed[rec.Value] {
					present = true
					continue
				}
				remaining = append(remaining, rec)
			}

			if !present {
				// Already gone — someone deleted it by hand, or a previous run
				// succeeded and died before clearing the tombstone. Either way
				// the intent is satisfied.
				s.forgetTombstones(zone.Name, grouped[k])
				retracted += len(grouped[k])
				continue
			}

			// The tombstone is itself the claim on this name: an operator asked
			// for these exact values to go. Adopt live as the baseline so the
			// removal classifies as an intentional change rather than a
			// takeover of a name hz has no publish history with — otherwise
			// retracting a record hz never published would block on the very
			// rule meant to protect other people's records. Nothing is lost:
			// `remaining` is computed from live, so a value added out of band
			// survives the write.
			s.setLastPublished(driftKey(zone.Name, k.name, k.recType), liveVals)

			_, perr := run.publish(provider, zone, k.name, k.recType, remaining)
			if errors.Is(perr, errDNSDriftBlocked) {
				return retracted, failed, perr // abort the run; tombstones persist
			}
			if perr != nil {
				slog.Error("tombstone retraction failed", "zone", zone.Name,
					"name", k.name, "type", k.recType, "err", perr)
				failed += len(grouped[k])
				continue
			}

			slog.Info("tombstone retracted", "zone", zone.Name,
				"name", k.name, "type", k.recType, "values", len(grouped[k]))
			s.forgetTombstones(zone.Name, grouped[k])
			retracted += len(grouped[k])
		}
	}
	return retracted, failed, nil
}

// forgetTombstones drops confirmed-gone entries in a single config write.
func (s *Server) forgetTombstones(zoneName string, done []config.DNSTombstone) {
	if err := s.updateConfig(func(cfg *config.Config) {
		for _, t := range done {
			cfg.DropTombstone(zoneName, t.Name, t.Type, t.Value)
		}
	}); err != nil {
		slog.Warn("dropping tombstones failed", "zone", zoneName, "err", err)
	}
}

// ingestObservedRecords adopts everything live at the provider that hz does not
// own, so the zone's real contents are visible rather than invisible.
//
// Track-only, deliberately: ingesting never grants hz authority to publish or
// delete these, so absorbing an MX or a third-party CNAME cannot put it at risk.
// It exists so an operator can see the whole zone in one place, and so hz can
// tell "someone else's record" from "a record I forgot about" without ever
// having to claim the zone.
//
// The set is rebuilt from scratch each run. These records change out of band by
// definition — that's what makes them not hz's — so a cache that only ever grew
// would drift into fiction.
func (s *Server) ingestObservedRecords(run *dnsSyncRun) (ingested int, err error) {
	observed := map[string][]config.DNSRecord{}

	for _, zone := range s.cfg().Zones {
		providerCfg := zone.GetDNSProvider()
		if providerCfg == nil {
			continue
		}
		provider, perr := dns.NewProvider(providerCfg)
		if perr != nil {
			slog.Warn("ingest provider error", "zone", zone.Name, "err", perr)
			continue
		}
		recs, lerr := s.ingestZoneRecords(run, provider, zone)
		if lerr != nil {
			slog.Warn("ingest live read failed", "zone", zone.Name, "err", lerr)
			continue
		}
		observed[zone.Name] = recs
		ingested += len(recs)
	}

	if uerr := s.updateConfig(func(cfg *config.Config) {
		for i := range cfg.Zones {
			cfg.Zones[i].ObservedRecords = observed[cfg.Zones[i].Name]
		}
	}); uerr != nil {
		return ingested, uerr
	}
	return ingested, nil
}

// ingestZoneRecords returns the track-only inventory for one zone: everything
// live that hz neither owns nor is retracting, minus the provider's own
// delegation records. Split out so tests can inject a provider.
func (s *Server) ingestZoneRecords(run *dnsSyncRun, provider dns.Provider, zone config.Zone) ([]config.DNSRecord, error) {
	liveAll, err := run.live(provider, zone)
	if err != nil {
		return nil, err
	}

	cfg := s.cfg()
	var out []config.DNSRecord
	for _, rec := range liveAll {
		name := strings.TrimSuffix(rec.Name, ".")
		if !cfg.ShouldIngest(zone.Name, name, rec.Type) {
			continue
		}
		out = append(out, config.DNSRecord{
			Name:  name,
			Type:  strings.ToUpper(rec.Type),
			Value: rec.Value,
			TTL:   rec.TTL,
		})
	}
	return out, nil
}
