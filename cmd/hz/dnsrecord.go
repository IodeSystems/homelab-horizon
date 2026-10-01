package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz dns record` declares records hz owns on a zone (Zone.Records) that no
// service derives: domain verification, DKIM, SPF, DMARC, a MAIL FROM MX.
//
// Ownership is the whole design. hz replaces whole (name, type) record sets at
// the provider, so it only ever writes a set whose every value it declared or
// was told to adopt, and it only ever deletes a value it declared that is still
// live exactly as declared. Anything else at the provider is someone else's and
// is shown, never touched.

func runDNS(c *client, args []string) error {
	if len(args) == 0 || args[0] != "record" && args[0] != "records" {
		return fmt.Errorf("dns subcommand required: record")
	}
	args = args[1:]
	if len(args) == 0 {
		return fmt.Errorf("dns record subcommand required: list | add | edit | rm")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return dnsRecordList(c, rest)
	case "add":
		return dnsRecordAdd(c, rest)
	case "edit", "set":
		return dnsRecordEdit(c, rest)
	case "rm", "remove", "delete":
		return dnsRecordRemove(c, rest)
	default:
		return fmt.Errorf("unknown dns record subcommand: %s", sub)
	}
}

// leadingName takes a positional <name> before the flags (Go's flag package
// stops at the first positional).
func leadingName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func normName(n string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), ".")) }

// zoneFor picks the managed zone a name is in: the longest zone name the FQDN
// ends with. The server checks the same thing; asking here too means the live
// read below goes to the right zone.
func zoneFor(zones []apitypes.ZoneResp, fqdn, explicit string) (string, error) {
	fqdn = normName(fqdn)
	if explicit != "" {
		for _, z := range zones {
			if z.Name == explicit {
				if fqdn != z.Name && !strings.HasSuffix(fqdn, "."+z.Name) {
					return "", fmt.Errorf("%s is not in zone %s", fqdn, explicit)
				}
				return z.Name, nil
			}
		}
		return "", fmt.Errorf("zone %s not found", explicit)
	}
	best := ""
	for _, z := range zones {
		if (fqdn == z.Name || strings.HasSuffix(fqdn, "."+z.Name)) && len(z.Name) > len(best) {
			best = z.Name
		}
	}
	if best == "" {
		names := make([]string, len(zones))
		for i, z := range zones {
			names[i] = z.Name
		}
		return "", fmt.Errorf("%s is not in any managed zone (%s); hz can only publish records in a zone it manages",
			fqdn, strings.Join(names, ", "))
	}
	return best, nil
}

func fetchZones(c *client) ([]apitypes.ZoneResp, error) {
	var zones []apitypes.ZoneResp
	if err := c.do("GET", "/api/v1/zones", nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

func fetchZoneRecords(c *client, zone string) (*apitypes.ZoneRecordsResponse, error) {
	var out apitypes.ZoneRecordsResponse
	if err := c.do("GET", "/api/v1/zones/records?zone="+zone, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// recordSet is what hz and the provider each hold at one (name, type).
type recordSet struct {
	live     []apitypes.DNSRecordResp         // every live value, any owner
	declared []apitypes.DeclaredDNSRecordResp // hz's declarations
}

func (rs recordSet) liveValues() []string {
	out := make([]string, 0, len(rs.live))
	for _, r := range rs.live {
		out = append(out, r.Value)
	}
	return out
}

func (rs recordSet) declaredValues() []string {
	out := make([]string, 0, len(rs.declared))
	for _, d := range rs.declared {
		out = append(out, d.Value)
	}
	return out
}

func setAt(resp *apitypes.ZoneRecordsResponse, name, typ string) recordSet {
	var rs recordSet
	for _, r := range resp.Records {
		if normName(r.Name) == name && strings.EqualFold(r.Type, typ) {
			rs.live = append(rs.live, r)
		}
	}
	for _, d := range resp.Declared {
		if normName(d.Name) == name && strings.EqualFold(d.Type, typ) {
			rs.declared = append(rs.declared, d)
		}
	}
	return rs
}

// --- list ---

func dnsRecordList(c *client, args []string) error {
	fs := flag.NewFlagSet("dns record list", flag.ContinueOnError)
	zoneFlag := fs.String("zone", "", "only this zone")
	all := fs.Bool("all", false, "every record live at the provider, with its owner — not only hz's")
	asJSON := fs.Bool("json", false, "output raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	zones, err := fetchZones(c)
	if err != nil {
		return err
	}
	var resps []*apitypes.ZoneRecordsResponse
	for _, z := range zones {
		if *zoneFlag != "" && z.Name != *zoneFlag {
			continue
		}
		resp, err := fetchZoneRecords(c, z.Name)
		if err != nil {
			return fmt.Errorf("zone %s: %w", z.Name, err)
		}
		resps = append(resps, resp)
	}
	if *zoneFlag != "" && len(resps) == 0 {
		return fmt.Errorf("zone %s not found", *zoneFlag)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(resps, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if *all {
		fmt.Printf("%-18s  %-50s  %-6s  %-10s  %s\n", "ZONE", "NAME", "TYPE", "OWNER", "VALUE")
		for _, resp := range resps {
			for _, r := range resp.Records {
				fmt.Printf("%-18s  %-50s  %-6s  %-10s  %s\n", resp.Zone, normName(r.Name), r.Type, r.Owner, r.Value)
			}
		}
		return nil
	}

	n := 0
	for _, resp := range resps {
		sort.SliceStable(resp.Declared, func(i, j int) bool {
			if resp.Declared[i].Name != resp.Declared[j].Name {
				return resp.Declared[i].Name < resp.Declared[j].Name
			}
			return resp.Declared[i].Type < resp.Declared[j].Type
		})
		for _, d := range resp.Declared {
			if n == 0 {
				fmt.Printf("%-18s  %-50s  %-6s  %-5s  %-5s  %-40s  %s\n", "ZONE", "NAME", "TYPE", "TTL", "LIVE", "VALUE", "NOTE")
			}
			n++
			live := "no"
			if d.Live {
				live = "yes"
			}
			fmt.Printf("%-18s  %-50s  %-6s  %-5d  %-5s  %-40s  %s\n", resp.Zone, d.Name, d.Type, d.TTL, live, d.Value, d.Note)
		}
		for _, t := range resp.Tombstones {
			fmt.Printf("  pending removal: %s %s %s (still live: %v)\n", t.Name, t.Type, t.Value, t.StillLive)
		}
	}
	if n == 0 {
		fmt.Println("No declared records. 'hz dns record list --all' shows what the providers hold.")
	}
	return nil
}

// --- add / edit ---

type recordFlags struct {
	fs     *flag.FlagSet
	name   string
	typ    string
	zone   string
	values multiFlag
	ttl    int
	note   string
}

func newRecordFlags(cmd string) *recordFlags {
	rf := &recordFlags{fs: flag.NewFlagSet(cmd, flag.ContinueOnError)}
	rf.fs.StringVar(&rf.name, "name", "", "record name (FQDN)")
	rf.fs.StringVar(&rf.typ, "type", "", "A | AAAA | CNAME | TXT | MX | NS")
	rf.fs.StringVar(&rf.zone, "zone", "", "zone (default: the managed zone the name is in)")
	rf.fs.Var(&rf.values, "value", "value (repeatable). TXT unquoted; MX \"<pref> <host>\"; NS one nameserver per --value")
	rf.fs.IntVar(&rf.ttl, "ttl", 0, "TTL seconds (default: declared, else live, else 300)")
	rf.fs.StringVar(&rf.note, "note", "", "why this record exists")
	return rf
}

// dnsRecordAdd declares values at (name, type), keeping any already declared
// there. A value the provider already holds unowned must be named to be
// adopted; the server refuses a write that would delete one.
func dnsRecordAdd(c *client, args []string) error {
	name, rest := leadingName(args)
	rf := newRecordFlags("dns record add")
	if err := rf.fs.Parse(rest); err != nil {
		return err
	}
	if rf.name == "" {
		rf.name = name
	}
	if rf.name == "" || rf.typ == "" || len(rf.values) == 0 {
		return fmt.Errorf("usage: hz dns record add --name N --type T --value V [--value V2] [--ttl S] [--note ...]")
	}
	return putRecordSet(c, rf, true)
}

// dnsRecordEdit replaces the declared value set at (name, type). With no
// --value it keeps the values and changes only --ttl / --note.
func dnsRecordEdit(c *client, args []string) error {
	name, rest := leadingName(args)
	rf := newRecordFlags("dns record edit")
	if err := rf.fs.Parse(rest); err != nil {
		return err
	}
	if rf.name == "" {
		rf.name = name
	}
	if rf.name == "" || rf.typ == "" {
		return fmt.Errorf("usage: hz dns record edit <name> --type T [--value V ...] [--ttl S] [--note ...]")
	}
	return putRecordSet(c, rf, false)
}

func putRecordSet(c *client, rf *recordFlags, add bool) error {
	name := normName(rf.name)
	typ := strings.ToUpper(strings.TrimSpace(rf.typ))
	zones, err := fetchZones(c)
	if err != nil {
		return err
	}
	zone, err := zoneFor(zones, name, rf.zone)
	if err != nil {
		return err
	}
	resp, err := fetchZoneRecords(c, zone)
	if err != nil {
		return err
	}
	rs := setAt(resp, name, typ)

	var values []string
	switch {
	case add:
		values = append(rs.declaredValues(), rf.values...)
	case len(rf.values) > 0:
		if len(rs.declared) == 0 {
			return fmt.Errorf("%s %s is not declared; use 'hz dns record add'", name, typ)
		}
		values = rf.values
	default:
		if len(rs.declared) == 0 {
			return fmt.Errorf("%s %s is not declared; use 'hz dns record add'", name, typ)
		}
		if rf.ttl == 0 && rf.note == "" {
			return fmt.Errorf("nothing to change: pass --value, --ttl or --note")
		}
		values = rs.declaredValues()
	}
	values = dedupe(values)

	var out apitypes.DNSRecordSetResponse
	err = c.do("POST", "/api/v1/zones/records/set", apitypes.DNSRecordSetRequest{
		Zone: zone, Name: name, Type: typ, Values: values, TTL: rf.ttl, Note: rf.note,
		ExpectedFrom: rs.liveValues(),
	}, &out)
	if err != nil {
		return err
	}
	state := "published"
	if !out.Changed {
		state = "already live; declared, nothing written"
	}
	fmt.Printf("%s %s in %s: %s (%s)\n", out.Name, out.Type, out.Zone, strings.Join(out.Values, " | "), state)
	return nil
}

func dedupe(vs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		k := strings.TrimSuffix(strings.TrimSpace(v), ".")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// --- rm ---

// dnsRecordRemove retracts declared values. It deletes a value at the provider
// only when that value is hz's and still live exactly as hz declared it; a
// value someone else holds at the same name is left alone and named. The
// server applies the same value and drift checks again (expectedFrom), so a
// change made between this read and the write is refused, not overwritten.
func dnsRecordRemove(c *client, args []string) error {
	name, rest := leadingName(args)
	fs := flag.NewFlagSet("dns record rm", flag.ContinueOnError)
	typ := fs.String("type", "", "A | AAAA | CNAME | TXT | MX | NS")
	zoneFlag := fs.String("zone", "", "zone (default: the managed zone the name is in)")
	var only multiFlag
	fs.Var(&only, "value", "remove only this declared value (repeatable; default: every declared value)")
	confirm := fs.Bool("confirm", false, "do it; without this, print what would be removed")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" || *typ == "" {
		return fmt.Errorf("usage: hz dns record rm <name> --type T [--value V] [--confirm]")
	}
	name = normName(name)
	t := strings.ToUpper(*typ)

	zones, err := fetchZones(c)
	if err != nil {
		return err
	}
	zone, err := zoneFor(zones, name, *zoneFlag)
	if err != nil {
		return err
	}
	resp, err := fetchZoneRecords(c, zone)
	if err != nil {
		return err
	}
	rs := setAt(resp, name, t)
	plan, keep, err := planRemoval(rs, only)
	if err != nil {
		return err
	}

	for _, p := range plan {
		how := "delete at provider"
		if !p.live {
			how = "not live; drop the declaration only"
		}
		fmt.Printf("  - %s %s %s (%s)\n", name, t, p.value, how)
	}
	for _, v := range keep {
		fmt.Printf("  = %s %s %s (not hz's; left alone)\n", name, t, v)
	}
	if !*confirm {
		fmt.Println("\nDry run. Re-run with --confirm to remove.")
		return nil
	}

	live := rs.liveValues()
	for _, p := range plan {
		var out struct {
			Values []string `json:"values"`
		}
		err := c.do("POST", "/api/v1/zones/records/delete", map[string]any{
			"zone": zone, "name": name, "type": t, "value": p.value, "expectedFrom": live,
		}, &out)
		if err != nil {
			return fmt.Errorf("removing %s: %w", p.value, err)
		}
		live = out.Values
	}
	fmt.Printf("Removed %d value(s).\n", len(plan))
	return nil
}

type removal struct {
	value string
	live  bool
}

// planRemoval decides what rm may touch: declared values only (filtered by
// --value), each marked live or not. Live values at the set that hz did not
// declare are returned as keep. Asking for a value hz did not declare is an
// error, not a skip — the operator believed it was hz's, and it is not.
func planRemoval(rs recordSet, only []string) ([]removal, []string, error) {
	if len(rs.declared) == 0 {
		if len(rs.live) > 0 {
			return nil, nil, fmt.Errorf("hz did not declare this record; its %d live value(s) belong to someone else and hz will not delete them", len(rs.live))
		}
		return nil, nil, fmt.Errorf("no such record")
	}
	want := map[string]bool{}
	for _, v := range only {
		want[strings.TrimSuffix(strings.TrimSpace(v), ".")] = true
	}
	var plan []removal
	declared := map[string]bool{}
	for _, d := range rs.declared {
		declared[d.Value] = true
		if len(want) > 0 && !want[d.Value] {
			continue
		}
		plan = append(plan, removal{value: d.Value, live: d.Live})
		delete(want, d.Value)
	}
	for v := range want {
		return nil, nil, fmt.Errorf("value %q is not declared by hz at this name; hz will not delete it", v)
	}
	var keep []string
	for _, r := range rs.live {
		if !declared[r.Value] {
			keep = append(keep, r.Value)
		}
	}
	return plan, keep, nil
}
