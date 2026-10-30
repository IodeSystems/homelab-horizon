package server

import (
	"errors"
	"fmt"

	"github.com/iodesystems/homelab-horizon/internal/dns"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// externalDNSResult is one record set the external-DNS publish wrote, or
// tried to.
type externalDNSResult struct {
	Name    string
	Values  []string
	Profile string // the zone's AWS profile, for the log line
	Changed bool
	Err     error
}

// publishExternalDNS writes the services' external DNS (DeriveRoute53Plan's
// records) through each zone's dns_provider — run.publish, the path zone
// records and sync-all use, with its drift check and read-back.
//
// Found live 2026-10-30: these records went out through the AWS CLI even when
// the zone had a dns_provider, and on a box without the CLI every one failed
// with `exec: "aws": executable file not found in $PATH` while the zone's own
// records published. There is no CLI path now: DeriveRoute53Plan leaves out a
// zone with no provider and says why.
//
// Returns errDNSDriftBlocked as soon as a set drifts; the caller stops there.
func (s *Server) publishExternalDNS(run *dnsSyncRun, records []route53.Record) ([]externalDNSResult, error) {
	type setKey struct{ name, typ, zone string }
	sets := make(map[setKey][]route53.Record)
	var order []setKey
	for _, rec := range records {
		k := setKey{rec.Name, rec.Type, rec.ZoneName}
		if _, ok := sets[k]; !ok {
			order = append(order, k)
		}
		sets[k] = append(sets[k], rec)
	}

	cfg := s.cfg()
	var out []externalDNSResult
	for _, k := range order {
		rs := sets[k]
		res := externalDNSResult{Name: k.name, Profile: rs[0].AWSProfile}
		for _, rec := range rs {
			res.Values = append(res.Values, rec.Value)
		}

		zone := cfg.GetZone(k.zone)
		if zone == nil || zone.GetDNSProvider() == nil {
			// Unreachable while the plan skips these, and checked anyway: a
			// write with no provider has nowhere to go.
			res.Err = fmt.Errorf("zone %s has no dns_provider", k.zone)
			out = append(out, res)
			continue
		}
		provider, err := s.dnsProvider(zone.GetDNSProvider())
		if err != nil {
			res.Err = err
			out = append(out, res)
			continue
		}
		desired := make([]dns.Record, len(rs))
		for i, rec := range rs {
			desired[i] = dns.Record{Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, ZoneID: zone.ZoneID}
		}
		res.Changed, res.Err = run.publish(provider, *zone, k.name, k.typ, desired)
		out = append(out, res)
		if errors.Is(res.Err, errDNSDriftBlocked) {
			return out, res.Err
		}
	}
	return out, nil
}
