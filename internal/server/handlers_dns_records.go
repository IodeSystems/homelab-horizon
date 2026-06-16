package server

import (
	"log/slog"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

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
func (s *Server) syncZoneRecords() (updated, failed int) {
	for _, zone := range s.cfg().Zones {
		if len(zone.Records) == 0 {
			continue
		}

		sets, errs := buildZoneRecordSets(zone)
		for _, err := range errs {
			slog.Warn("invalid zone DNS record", "zone", zone.Name, "err", err)
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

		provider, err := dns.NewProvider(providerCfg)
		if err != nil {
			slog.Error("zone DNS provider error", "zone", zone.Name, "err", err)
			failed += len(sets)
			continue
		}

		for _, set := range sets {
			rec := set.Records[0]
			changed, err := provider.SyncRecordSet(zone.ZoneID, set.Records)
			if err != nil {
				slog.Error("zone record sync failed", "zone", zone.Name,
					"name", rec.Name, "type", rec.Type, "err", err)
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
	return updated, failed
}
