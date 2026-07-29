package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// HTTPS coverage is not a service field: it lives on the zone as a SubZone
// entry (zone.sub_zones), which becomes a SAN on that zone's certificate.
// "Enable HTTPS for a domain" therefore means "add the SubZone that covers it",
// which is what /api/v1/domains/ssl/{add,remove} do. These helpers are shared by
// `hz domain`, `hz service --https`, and `hz setup`.

func runDomain(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("domain subcommand required: list | ssl")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return domainList(c, rest)
	case "ssl", "https":
		return domainSSL(c, rest)
	default:
		return fmt.Errorf("unknown domain subcommand: %s", sub)
	}
}

func fetchDomains(c *client) (*apitypes.DomainsResponse, error) {
	var out apitypes.DomainsResponse
	if err := c.do("GET", "/api/v1/domains", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// fetchDomainMap indexes the domain analysis by domain name. Domains hz knows
// nothing about (brand-new ones) are simply absent.
func fetchDomainMap(c *client) (map[string]*apitypes.DomainResp, error) {
	resp, err := fetchDomains(c)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*apitypes.DomainResp, len(resp.Domains))
	for i := range resp.Domains {
		m[resp.Domains[i].Domain] = &resp.Domains[i]
	}
	return m, nil
}

func domainList(c *client, args []string) error {
	fs := flag.NewFlagSet("domain list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := fetchDomains(c)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(resp.Domains) == 0 {
		fmt.Println("No domains.")
		return nil
	}
	sort.Slice(resp.Domains, func(i, j int) bool { return resp.Domains[i].Domain < resp.Domains[j].Domain })
	fmt.Printf("%-38s  %-28s  %-18s  %-6s  %s\n", "DOMAIN", "SERVICE", "ZONE", "HTTPS", "CERT")
	for _, d := range resp.Domains {
		svc := d.ServiceName
		if svc == "" {
			svc = "-"
		}
		zone := d.ZoneName
		if zone == "" {
			zone = "-"
		}
		https := "no"
		if d.HasSSLCoverage {
			https = "yes"
			if d.CoveredBy != "" {
				https = "wild"
			}
		}
		cert := "-"
		if d.CertExists {
			cert = d.CertExpiry
			if cert == "" {
				cert = "present"
			}
		} else if d.HasSSLCoverage {
			cert = "pending"
		}
		fmt.Printf("%-38s  %-28s  %-18s  %-6s  %s\n", d.Domain, svc, zone, https, cert)
	}
	if len(resp.SSLGaps) > 0 {
		fmt.Printf("\n%d service domain(s) without HTTPS coverage:\n", len(resp.SSLGaps))
		for _, g := range resp.SSLGaps {
			fmt.Printf("  %s (zone %s) — %s\n", g.Domain, g.ZoneName, g.Reason)
		}
		fmt.Println("\nRun 'hz domain ssl add <domain>' to cover one.")
	}
	return nil
}

func domainSSL(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("domain ssl subcommand required: add | rm")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add", "enable":
		return domainSSLAdd(c, rest)
	case "rm", "remove", "delete", "disable":
		return domainSSLRemove(c, rest)
	default:
		return fmt.Errorf("unknown domain ssl subcommand: %s", sub)
	}
}

// splitDomainArgs pulls every leading non-flag token out as domains, leaving the
// rest for flag parsing (Go's flag package stops at the first positional).
func splitDomainArgs(args []string) (domains, rest []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(rest) > 0 {
			rest = append(rest, a)
			continue
		}
		domains = append(domains, a)
	}
	return domains, rest
}

func domainSSLAdd(c *client, args []string) error {
	domains, rest := splitDomainArgs(args)
	fs := flag.NewFlagSet("domain ssl add", flag.ContinueOnError)
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("usage: hz domain ssl add <domain>... [--sync]")
	}
	dm, err := fetchDomainMap(c)
	if err != nil {
		return err
	}
	for _, d := range domains {
		if err := enableDomainHTTPS(c, d, dm[d]); err != nil {
			return err
		}
	}
	return maybeSync(c, *doSync)
}

func domainSSLRemove(c *client, args []string) error {
	domains, rest := splitDomainArgs(args)
	fs := flag.NewFlagSet("domain ssl rm", flag.ContinueOnError)
	confirm := fs.Bool("confirm", false, "confirm dropping HTTPS from domains that already have it")
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("usage: hz domain ssl rm <domain>... --confirm [--sync]")
	}
	if !*confirm {
		return fmt.Errorf("removing HTTPS coverage changes existing state: pass --confirm")
	}
	dm, err := fetchDomainMap(c)
	if err != nil {
		return err
	}
	for _, d := range domains {
		if err := disableDomainHTTPS(c, d, dm[d]); err != nil {
			return err
		}
	}
	return maybeSync(c, *doSync)
}

// enableDomainHTTPS adds SSL coverage for one domain. dr is its current analysis
// (nil when hz doesn't know the domain yet); a domain that is already covered is
// a no-op, and so is the 409 the server returns when the SubZone already exists.
func enableDomainHTTPS(c *client, domain string, dr *apitypes.DomainResp) error {
	if dr != nil && dr.HasSSLCoverage {
		fmt.Printf("  = https  %s (already covered%s)\n", domain, coveredBySuffix(dr))
		return nil
	}
	var out apitypes.DomainSSLAddResponse
	err := c.do("POST", "/api/v1/domains/ssl/add", apitypes.DomainSSLAddRequest{Domain: domain}, &out)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Printf("  = https  %s (already covered)\n", domain)
			return nil
		}
		return fmt.Errorf("enabling HTTPS for %s: %w", domain, err)
	}
	fmt.Printf("  + https  %s (zone %s, SubZone %q)\n", domain, out.Zone, out.SubZone)
	if dr != nil && dr.HasZone && !dr.ZoneHasSSL {
		fmt.Printf("  ! zone %s has SSL disabled — no certificate will be issued until you enable it\n", dr.ZoneName)
	}
	return nil
}

// disableDomainHTTPS removes SSL coverage for one domain. Coverage inherited
// from a wildcard SubZone can't be dropped per-domain — that would need the
// wildcard itself removed, which affects every domain under it.
func disableDomainHTTPS(c *client, domain string, dr *apitypes.DomainResp) error {
	if dr != nil && !dr.HasSSLCoverage {
		fmt.Printf("  = http   %s (no coverage)\n", domain)
		return nil
	}
	if dr != nil && dr.CoveredBy != "" {
		return fmt.Errorf("%s stays HTTPS: covered by wildcard %s — remove that SubZone from zone %s to drop it",
			domain, dr.CoveredBy, dr.ZoneName)
	}
	req := apitypes.DomainSSLRemoveRequest{Domain: domain, Force: true}
	if err := c.do("POST", "/api/v1/domains/ssl/remove", req, nil); err != nil {
		return fmt.Errorf("disabling HTTPS for %s: %w", domain, err)
	}
	fmt.Printf("  - https  %s\n", domain)
	return nil
}

func coveredBySuffix(dr *apitypes.DomainResp) string {
	if dr.CoveredBy != "" {
		return " by " + dr.CoveredBy
	}
	return ""
}

// --- HTTPS planning for service mutations ---

// httpsChange is one domain whose HTTPS state a service mutation would flip.
type httpsChange struct {
	domain string
	enable bool // true: gaining HTTPS, false: losing it
}

// planHTTPS diffs the desired per-domain HTTPS state against current coverage.
// existing is the service's domain set before the mutation: flipping one of
// those, or dropping coverage from any domain, changes state that is already
// live and so needs --confirm. Enabling HTTPS on a domain the service is only
// now gaining is all-new state and needs no confirmation.
func planHTTPS(domains []string, want map[string]bool, existing map[string]bool, dm map[string]*apitypes.DomainResp) (changes, needConfirm []httpsChange) {
	for _, d := range domains {
		dr := dm[d]
		has := dr != nil && dr.HasSSLCoverage
		if want[d] == has {
			continue
		}
		ch := httpsChange{domain: d, enable: want[d]}
		changes = append(changes, ch)
		if !ch.enable || existing[d] {
			needConfirm = append(needConfirm, ch)
		}
	}
	return changes, needConfirm
}

// applyHTTPS executes a plan against fresh coverage data — the service mutation
// has landed by now, so domains created by it are finally visible.
func applyHTTPS(c *client, domains []string, want map[string]bool) error {
	dm, err := fetchDomainMap(c)
	if err != nil {
		return err
	}
	for _, d := range domains {
		dr := dm[d]
		has := dr != nil && dr.HasSSLCoverage
		switch {
		case want[d] && !has:
			if err := enableDomainHTTPS(c, d, dr); err != nil {
				return err
			}
		case !want[d] && has:
			if err := disableDomainHTTPS(c, d, dr); err != nil {
				return err
			}
		}
	}
	return nil
}

func domainsOf(changes []httpsChange) string {
	out := make([]string, 0, len(changes))
	for _, ch := range changes {
		verb := "→ https"
		if !ch.enable {
			verb = "→ http"
		}
		out = append(out, ch.domain+" "+verb)
	}
	return strings.Join(out, ", ")
}
