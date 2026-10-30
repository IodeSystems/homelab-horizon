package server

import (
	"errors"
	"fmt"

	"github.com/iodesystems/homelab-horizon/internal/dns"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// route53SyncRecordSet and route53Available are the legacy AWS CLI write and
// its presence check, variables so a test can see which sets take that path
// without reaching AWS or depending on the CLI being installed.
var (
	route53SyncRecordSet = route53.SyncRecordSet
	route53Available     = route53.Available
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
// records). A set whose zone has a dns_provider goes through that provider —
// run.publish, the path zone records and sync-all use, with its drift check
// and read-back. Only the legacy shape, a zone_id and no dns_provider, still
// shells out to the AWS CLI, and without the CLI that is an error naming the
// fix, never a silence.
//
// Found live 2026-10-30: a box with a route53 dns_provider and no aws CLI
// published its zone records and failed every service record with
// `exec: "aws": executable file not found in $PATH`.
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
		switch {
		case zone != nil && zone.GetDNSProvider() != nil:
			provider, err := s.dnsProvider(zone.GetDNSProvider())
			if err != nil {
				res.Err = err
				break
			}
			desired := make([]dns.Record, len(rs))
			for i, rec := range rs {
				desired[i] = dns.Record{Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, ZoneID: zone.ZoneID}
			}
			res.Changed, res.Err = run.publish(provider, *zone, k.name, k.typ, desired)
			if errors.Is(res.Err, errDNSDriftBlocked) {
				return append(out, res), res.Err
			}
		case !route53Available():
			res.Err = fmt.Errorf("zone %s has a zone_id and no dns_provider, which only the aws CLI can write, and it is not installed; add a dns_provider to the zone", k.zone)
		default:
			res.Changed, res.Err = route53SyncRecordSet(rs[0], res.Values)
		}
		out = append(out, res)
	}
	return out, nil
}
