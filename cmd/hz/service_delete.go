package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Deleting a service is not self-contained. Two pieces of state outlive it: the
// zone SubZone that gives its domain HTTPS (it lives on the zone, so it stays
// behind as a cert SAN plus an http->https redirect for a host nothing serves)
// and the record published at the DNS provider (the external-DNS sync is
// upsert-only, so nothing ever retracts it). Both are invisible from
// `hz service list` afterwards, which is exactly how they get forgotten.
//
// So the CLI refuses to guess: when a delete would strand either, it prints the
// full picture and makes the operator say --delete-orphans or --keep-orphans.

func serviceDelete(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	if name == "" {
		return fmt.Errorf("usage: hz service delete <name> [--delete-orphans|--keep-orphans] [--yes] [--sync]")
	}
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	doSync := fs.Bool("sync", false, "trigger a global sync after delete")
	yes := fs.Bool("yes", false, "skip confirmation")
	delOrphans := fs.Bool("delete-orphans", false, "also retract the SubZone and DNS record the delete would strand")
	keepOrphans := fs.Bool("keep-orphans", false, "leave the stranded SubZone and DNS record in place")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *delOrphans && *keepOrphans {
		return fmt.Errorf("--delete-orphans and --keep-orphans are mutually exclusive")
	}

	preview, err := fetchDeletePreview(c, name)
	if err != nil {
		return err
	}
	actionable := actionableOrphans(preview.Orphans)

	if len(preview.Orphans) > 0 {
		fmt.Printf("Deleting %q leaves behind:\n\n", name)
		printOrphans(preview.Orphans)
		fmt.Println()
	}
	if err := requireOrphanDecision(actionable, *delOrphans, *keepOrphans); err != nil {
		return err
	}

	if !*yes {
		fmt.Printf("Delete service %q? [y/N] ", name)
		var ans string
		_, _ = fmt.Fscanln(os.Stdin, &ans)
		if !strings.EqualFold(strings.TrimSpace(ans), "y") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := c.do("POST", "/api/v1/services/delete", map[string]string{"name": name}, nil); err != nil {
		return err
	}
	fmt.Printf("Deleted service %q.\n", name)

	// Orphans are retracted after the service is gone: with no service owning
	// the domain, removing its SubZone can't strip coverage from something live.
	if *delOrphans {
		if err := applyOrphanDeletion(c, actionable); err != nil {
			return err
		}
	} else if len(actionable) > 0 {
		fmt.Printf("Kept %d orphaned item(s) — 'hz domain list' shows the SubZone with no service.\n", len(actionable))
	}

	return maybeSync(c, *doSync)
}

// requireOrphanDecision enforces the explicit pick. Nothing stranded means
// nothing to decide, so a plain delete stays a plain delete.
func requireOrphanDecision(actionable []apitypes.ServiceDeleteOrphan, del, keep bool) error {
	if len(actionable) == 0 || del || keep {
		return nil
	}
	return fmt.Errorf("this delete strands %d item(s) listed above — re-run with --delete-orphans or --keep-orphans",
		len(actionable))
}

func actionableOrphans(orphans []apitypes.ServiceDeleteOrphan) []apitypes.ServiceDeleteOrphan {
	var out []apitypes.ServiceDeleteOrphan
	for _, o := range orphans {
		if o.Action == apitypes.OrphanActionDelete {
			out = append(out, o)
		}
	}
	return out
}

// orphanLabel splits into two aligned columns: the subsystem, and a marker for
// whether this entry needs a decision.
func orphanLabel(o apitypes.ServiceDeleteOrphan) (kind, marker string) {
	kind = "dns"
	if o.Kind == apitypes.OrphanKindHTTPS {
		kind = "https"
	}
	switch o.Action {
	case apitypes.OrphanActionAuto:
		return kind, "."
	case apitypes.OrphanActionKeep:
		return kind, "="
	default:
		return kind, "!"
	}
}

func printOrphans(orphans []apitypes.ServiceDeleteOrphan) {
	for _, o := range orphans {
		kind, marker := orphanLabel(o)
		fmt.Printf("  %s %-5s %-30s %s\n", marker, kind, o.Domain, o.Detail)
	}
	fmt.Println("\n  ! needs a decision   . goes away on sync   = shared, left alone")
}

// applyOrphanDeletion retracts each stranded item, reporting per item. One
// failure doesn't abort the rest — a registrar hiccup shouldn't leave the
// SubZone behind too.
func applyOrphanDeletion(c *client, orphans []apitypes.ServiceDeleteOrphan) error {
	var failures []string
	for _, o := range orphans {
		var err error
		switch o.Kind {
		case apitypes.OrphanKindHTTPS:
			err = c.do("POST", "/api/v1/domains/ssl/remove",
				apitypes.DomainSSLRemoveRequest{Domain: o.Domain, Force: true}, nil)
			if err == nil {
				fmt.Printf("  - https  %s\n", o.Domain)
			}
		case apitypes.OrphanKindExternalDNS:
			err = deleteExternalRecord(c, o)
			if err == nil {
				fmt.Printf("  - dns    %s (%s %s)\n", o.Domain, o.RecordType, strings.Join(o.Values, ", "))
			}
		default:
			continue
		}
		if err != nil {
			fmt.Printf("  ! %s: %s\n", o.Domain, err)
			failures = append(failures, o.Domain)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("service deleted, but %d orphan(s) could not be retracted: %s",
			len(failures), strings.Join(failures, ", "))
	}
	return nil
}

// deleteExternalRecord removes the published record. A round-robin set is one
// record per value, so each value needs its own delete.
func deleteExternalRecord(c *client, o apitypes.ServiceDeleteOrphan) error {
	values := o.Values
	if len(values) == 0 {
		values = []string{""} // let the server drop the whole (name, type) set
	}
	for _, v := range values {
		body := map[string]any{
			"zone":  o.Zone,
			"name":  o.Domain,
			"type":  o.RecordType,
			"value": v,
		}
		if err := c.do("POST", "/api/v1/zones/records/delete", body, nil); err != nil {
			return err
		}
	}
	return nil
}

// fetchDeletePreview asks the server, which owns the full config and so is the
// only place that can tell a shared SubZone from a stranded one. Servers
// predating the endpoint fall back to a client-side reconstruction.
func fetchDeletePreview(c *client, name string) (*apitypes.ServiceDeletePreviewResponse, error) {
	var out apitypes.ServiceDeletePreviewResponse
	err := c.do("POST", "/api/v1/services/delete/preview",
		apitypes.ServiceDeletePreviewRequest{Name: name}, &out)
	if err == nil {
		return &out, nil
	}
	if se, ok := err.(*apiStatusError); ok && se.Code == 404 && strings.Contains(se.Msg, "Service not found") {
		return nil, fmt.Errorf("service not found: %s", name)
	}
	return localDeletePreview(c, name)
}

// localDeletePreview reconstructs the orphan set from /services and /domains.
// It is deliberately more conservative than the server: without the zone's
// SubZone list it treats any wildcard-covered domain as shared, so it can
// under-report an actionable SubZone but never proposes deleting a shared one.
func localDeletePreview(c *client, name string) (*apitypes.ServiceDeletePreviewResponse, error) {
	services, err := fetchServices(c)
	if err != nil {
		return nil, err
	}
	svc := findService(services, name)
	if svc == nil {
		return nil, fmt.Errorf("service not found: %s", name)
	}
	dm, err := fetchDomainMap(c)
	if err != nil {
		return nil, err
	}

	out := &apitypes.ServiceDeletePreviewResponse{Service: svc.Name, Domains: svc.Domains}
	for _, domain := range svc.Domains {
		dr := dm[domain]
		zoneName := ""
		if dr != nil {
			zoneName = dr.ZoneName
		}

		if dr != nil && dr.HasSSLCoverage {
			if dr.CoveredBy != "" {
				out.Orphans = append(out.Orphans, apitypes.ServiceDeleteOrphan{
					Kind: apitypes.OrphanKindHTTPS, Action: apitypes.OrphanActionKeep,
					Domain: domain, Zone: zoneName,
					Detail: fmt.Sprintf("covered by wildcard %s — shared, left in place", dr.CoveredBy),
				})
			} else {
				out.Orphans = append(out.Orphans, apitypes.ServiceDeleteOrphan{
					Kind: apitypes.OrphanKindHTTPS, Action: apitypes.OrphanActionDelete,
					Domain: domain, Zone: zoneName,
					Detail: "own SubZone on zone " + zoneName + " — keeps a cert SAN and an http->https redirect for a host nothing serves",
				})
			}
		}

		if svc.ExternalDNS != nil {
			ips := svc.ExternalDNS.IPs
			if len(ips) == 0 && svc.ExternalDNS.IP != "" {
				ips = []string{svc.ExternalDNS.IP}
			}
			detail := "A record at the DNS provider — stays live and keeps resolving after the delete"
			if len(ips) > 0 {
				detail = fmt.Sprintf("A %s at the DNS provider — stays live and keeps resolving after the delete",
					strings.Join(ips, ", "))
			}
			out.Orphans = append(out.Orphans, apitypes.ServiceDeleteOrphan{
				Kind: apitypes.OrphanKindExternalDNS, Action: apitypes.OrphanActionDelete,
				Domain: domain, Zone: zoneName, RecordType: "A", Values: ips, Detail: detail,
			})
		}

		if svc.InternalDNS != nil && svc.InternalDNS.IP != "" {
			out.Orphans = append(out.Orphans, apitypes.ServiceDeleteOrphan{
				Kind: apitypes.OrphanKindInternalDNS, Action: apitypes.OrphanActionAuto,
				Domain: domain,
				Detail: fmt.Sprintf("dnsmasq A %s — removed automatically on the next sync", svc.InternalDNS.IP),
			})
		}
	}
	return out, nil
}
