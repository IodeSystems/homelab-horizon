package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// fetchTopology fetches the current observability topology (declared hosts,
// exporter jobs, and the expanded/probed targets derived from them).
func fetchTopology(c *client) (*apitypes.TopologyResp, error) {
	var out apitypes.TopologyResp
	if err := c.do("GET", "/api/v1/topology", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// parseLabels turns repeated "--label k=v" flags into a labels map.
func parseLabels(pairs multiFlag) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --label %q (want key=value)", p)
		}
		m[k] = v
	}
	return m, nil
}

// formatLabels renders a labels map as a sorted "k=v,k=v" string for table
// output (or "-" when empty).
func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// --- hz host ---

func runHost(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("host subcommand required: list | show | adopt | add | set | rm")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return hostList(c)
	case "show":
		return hostShow(c, rest)
	case "adopt":
		return hostAdopt(c, rest)
	case "add", "create":
		return hostAdd(c, rest)
	case "set":
		return hostSet(c, rest)
	case "rm", "remove", "delete":
		return hostRm(c, rest)
	default:
		return fmt.Errorf("unknown host subcommand: %s", sub)
	}
}

func hostList(c *client) error {
	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	fmt.Printf("%-20s  %-16s  %s\n", "NAME", "IP", "LABELS")
	// @self first: it is not a declaration and cannot be edited, but it is the
	// reference that matters most — it is how hz points at itself, and it is
	// the only spelling that stays right on a peer.
	fmt.Printf("%-20s  %-16s  %s\n", "@self (this hz)", topo.SelfHost.IP, "-")
	for _, h := range topo.Hosts {
		fmt.Printf("%-20s  %-16s  %s\n", h.Name, h.IP, formatLabels(h.Labels))
	}
	if len(topo.Hosts) == 0 {
		fmt.Println("\nNo hosts declared besides @self.")
		fmt.Println("`hz host add --name <name> --ip <ip>` declares one.")
	}
	fmt.Println("\nWrite @<name> (or @<name>:<port>) wherever an address goes to point a record")
	fmt.Println("at one of these, and @self for this gateway's own address. `hz host show <name>`")
	fmt.Println("lists what already does; `hz host show self` covers this gateway.")
	return nil
}

const hostShowUsage = `usage: hz host show <name>

Shows one declared host and everything that depends on it, as TWO lists:

  REFERENCED BY   records written @<name>. They FOLLOW the host, so moving it
                  is one edit: hz host set <name> <new ip>
  CARRIES ITS ADDRESS
                  records holding the address as a plain string. They follow
                  nothing, and they are what breaks when the box moves.
                  hz host adopt <name> turns them into references.

Both are grouped by kind: proxy backends, deploy slots, port forwards, DNS
answers, scrape targets.
`

func hostShow(c *client, args []string) error {
	fs := flag.NewFlagSet("host show", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, hostShowUsage) }
	name, rest := splitNameArgs(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("%s", hostShowUsage)
	}

	var out apitypes.HostShowResp
	if err := c.do("GET", "/api/v1/topology/hosts/show?name="+url.QueryEscape(name), nil, &out); err != nil {
		return err
	}

	fmt.Printf("Host:   %s\n", out.Host.Name)
	fmt.Printf("IP:     %s\n", out.Host.IP)
	fmt.Printf("Labels: %s\n", formatLabels(out.Host.Labels))
	fmt.Println()

	self := name == "self"
	if len(out.References) == 0 {
		fmt.Println("Nothing references it.")
		fmt.Printf("Write @%s (or @%s:<port>) in a backend, a forward, a DNS record or a scrape\n", out.Host.Name, out.Host.Name)
		if self {
			fmt.Printf("target and it resolves to whichever instance is running, which is also the\n")
			fmt.Printf("only spelling that stays correct on a peer.\n")
		} else {
			fmt.Printf("target and it resolves through this declaration instead.\n")
		}
	} else {
		printHostReferences(out.References)
		if self {
			fmt.Printf("\n%d record(s) resolve to THIS instance's own address. They follow it: each\n", len(out.References))
			fmt.Println("instance resolves @self to its own local_interface, so nothing here has to be")
			fmt.Println("rewritten when the gateway moves, and a peer does not inherit this box's address.")
		} else {
			fmt.Printf("\n%d record(s) resolve through this host. `hz host set %s <new ip>` moves them\n", len(out.References), out.Host.Name)
			fmt.Println("all at once — one edit, one sync. Removing or renaming the host is refused")
			fmt.Println("while any of them still points at it.")
		}
	}

	printHostOccurrences(out.Occurrences, out.OccurrencesKnown, out.OccurrencesUnknownWhy, out.Host.Name)
	return nil
}

// printHostOccurrences prints the SECOND list — records carrying the address as
// a plain string — under its own heading, never merged into the references.
//
// The two are opposite behaviours in the same shape of record, and the number
// an operator actually needs before a move is this one: a reference follows the
// box, an occurrence breaks. Printing them as one list would hand back the
// reassuring total that made `hz host show` answer "0 dependants" for a gateway
// 47 records depend on.
func printHostOccurrences(occs []apitypes.HostOccurrenceResp, known bool, unknownWhy, name string) {
	fmt.Println()
	if !known {
		fmt.Println("CARRIES ITS ADDRESS — NOT SCANNED")
		fmt.Printf("  %s\n", unknownWhy)
		return
	}
	if len(occs) == 0 {
		fmt.Println("CARRIES ITS ADDRESS")
		fmt.Println("  Nothing. No record holds this address as a plain string, so the list above is")
		fmt.Println("  the whole dependency: every one of them follows the host.")
		return
	}

	adoptable := 0
	fmt.Println("CARRIES ITS ADDRESS AS A PLAIN STRING")
	kind := ""
	for _, o := range occs {
		if o.Kind != kind {
			kind = o.Kind
			fmt.Printf("\n  %s\n", strings.ToUpper(kind))
		}
		owner := o.Owner
		if owner == "" {
			owner = "(config)"
		}
		after := o.Ref
		if after == "" {
			after = "— not adoptable"
		} else {
			adoptable++
		}
		fmt.Printf("    %-24s  %-28s  %-24s  %s\n", owner, o.Field, o.Value, after)
	}
	for _, o := range occs {
		if o.Ref == "" && o.WhyNotAdoptable != "" {
			fmt.Printf("\n  %s (%s) is not adoptable: %s\n", o.Kind, o.Field, o.WhyNotAdoptable)
		}
	}

	fmt.Printf("\n%d record(s) carry this address rather than referencing it. They do NOT follow\n", len(occs))
	fmt.Println("the host: moving the box breaks every one of them, and that is what this list is")
	fmt.Println("for. It is a different list from the references above on purpose.")
	if adoptable > 0 {
		fmt.Printf("\n`hz host adopt %s` rewrites %d of them into references (dry run first).\n", name, adoptable)
	}
}

// printHostReferences prints the dependants grouped by kind, in the order the
// server returned them (already sorted by kind, owner, field).
func printHostReferences(refs []apitypes.HostReferenceResp) {
	fmt.Println("REFERENCED BY")
	kind := ""
	for _, r := range refs {
		if r.Kind != kind {
			kind = r.Kind
			fmt.Printf("\n  %s\n", strings.ToUpper(kind))
		}
		owner := r.Owner
		if owner == "" {
			owner = "(config)"
		}
		fmt.Printf("    %-24s  %-28s  %s\n", owner, r.Field, r.Value)
	}
}

const hostSetUsage = `usage: hz host set <name> <ip> [--sync]

Repoints a declared host at a new address. Every record written @<name> follows
it — proxy backends, deploy slots, port forwards, DNS answers, scrape targets —
in one config write.

This is the command a machine move needs. ` + "`hz host show <name>`" + ` lists what will
move before you run it.
`

func hostSet(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	ip, rest := splitNameArgs(rest)
	fs := flag.NewFlagSet("host set", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, hostSetUsage) }
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" || ip == "" {
		return fmt.Errorf("%s", hostSetUsage)
	}

	var out apitypes.HostShowResp
	if err := c.do("PUT", "/api/v1/topology/hosts/set", apitypes.HostSetRequest{Name: name, IP: ip}, &out); err != nil {
		return err
	}
	fmt.Printf("Host %q is now %s.\n", out.Host.Name, out.Host.IP)
	if len(out.References) == 0 {
		fmt.Printf("\nNothing referenced it, so nothing else moved. Records that should follow this\n")
		fmt.Printf("host need to be written @%s rather than carrying the address themselves.\n", out.Host.Name)
	} else {
		fmt.Printf("\n%d record(s) moved with it:\n\n", len(out.References))
		printHostReferences(out.References)
	}
	if !*doSync {
		fmt.Println("\nNothing is rendered until the next sync. `hz sync` applies it now.")
	}
	return maybeSync(c, *doSync)
}

const hostAdoptUsage = `usage: hz host adopt <name> [--confirm] [--sync]

Rewrites every record that carries this host's address as a plain string into a
reference to the host, so the next move is ONE edit instead of dozens.

Prints what it would change and writes NOTHING unless --confirm is given. The
listing is the point: this rewrites config records across the whole gateway at
once, and each line names the record, the literal it holds now, and the
reference it would hold after.

  <name>      a declared host, or 'self' for this gateway's own address.
              'self' is the case that matters on a gateway: @self resolves per
              instance, so it stays correct on a peer, where a declared host
              naming THIS box's address does not.
  --confirm   actually rewrite them.
  --sync      trigger a global sync afterwards.

Adoption changes how the config is WRITTEN, never what it renders: every
reference resolves back to the literal it replaced, so haproxy.cfg, the dnsmasq
answers, the iptables rules and the scrape targets come out byte for byte the
same. Records hz will not rewrite are listed with the reason.
`

// hostAdopt is the command that makes the move one edit.
//
// DRY RUN BY DEFAULT, the discipline `hz project rm` uses and for a stronger
// reason: this touches dozens of unrelated records in one write, on a live
// gateway. The dry run is the product; --confirm is the afterthought.
func hostAdopt(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	fs := flag.NewFlagSet("host adopt", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, hostAdoptUsage) }
	confirm := fs.Bool("confirm", false, "actually rewrite the records; without this it is a dry run")
	doSync := fs.Bool("sync", false, "trigger a global sync after the rewrite")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("%s", hostAdoptUsage)
	}

	var out apitypes.HostAdoptResp
	req := apitypes.HostAdoptRequest{Name: name, Confirm: *confirm}
	if err := c.do("POST", "/api/v1/topology/hosts/adopt", req, &out); err != nil {
		return err
	}
	return renderAdoption(name, *confirm, out, c, *doSync)
}

// renderAdoption prints the one answer both runs give: what carries this
// address, what each record would become, and what hz refuses to touch.
func renderAdoption(name string, confirm bool, out apitypes.HostAdoptResp, c *client, doSync bool) error {
	fmt.Printf("Host:    %s\n", name)
	fmt.Printf("Address: %s\n", out.Address)
	fmt.Printf("Adopts to: %s\n", out.Ref)
	if out.RefWhy != "" {
		fmt.Printf("\nWhy %s and not the other spelling:\n  %s\n", out.Ref, out.RefWhy)
	}

	if len(out.Adopt) == 0 {
		fmt.Println("\nNothing to adopt: no record carries this address as a plain string.")
		if len(out.Refused) == 0 {
			fmt.Printf("Records that should follow this host are already written %s.\n", out.Ref)
		}
	} else {
		verb := "would be rewritten"
		if confirm {
			verb = "were rewritten"
		}
		fmt.Printf("\n%d record(s) %s:\n\n", len(out.Adopt), verb)
		fmt.Printf("  %-22s  %-16s  %-26s  %-24s  %s\n", "KIND", "OWNER", "FIELD", "NOW", "AFTER")
		for _, a := range out.Adopt {
			owner := a.Owner
			if owner == "" {
				owner = "(config)"
			}
			fmt.Printf("  %-22s  %-16s  %-26s  %-24s  %s\n", a.Kind, owner, a.Field, a.Value, a.Ref)
		}
	}

	if len(out.Refused) > 0 {
		fmt.Printf("\n%d record(s) carry the address and hz will NOT rewrite:\n", len(out.Refused))
		for _, r := range out.Refused {
			owner := r.Owner
			if owner == "" {
				owner = "(config)"
			}
			fmt.Printf("\n  %s %s (%s = %s)\n", r.Kind, owner, r.Field, r.Value)
			fmt.Printf("    %s\n", r.WhyNotAdoptable)
		}
	}

	if !confirm {
		fmt.Println("\nDry run: nothing was written.")
		if len(out.Adopt) > 0 {
			fmt.Printf("Re-run with --confirm to rewrite the %d record(s) above.\n", len(out.Adopt))
			fmt.Println("Rendered output does not change — each reference resolves to the literal it")
			fmt.Println("replaced — so this is safe to apply and sync.")
		}
		return nil
	}

	if !out.Confirmed {
		// A confirmed run that reported no write and named no reason would
		// otherwise read as success.
		return fmt.Errorf("the server did not confirm the rewrite and gave no reason — treat nothing as written")
	}
	if out.Written != len(out.Adopt) {
		return fmt.Errorf("hz listed %d record(s) to rewrite and wrote %d; the config on disk may be half adopted, so check `hz host show %s`",
			len(out.Adopt), out.Written, name)
	}
	fmt.Printf("\nRewrote %d record(s). Moving this host is now one edit", out.Written)
	if out.Ref == "@self" {
		fmt.Println(":\n  local_interface, on the Settings page (per instance — a peer keeps its own).")
	} else {
		fmt.Printf(":\n  hz host set %s <new ip>\n", name)
	}
	if !doSync {
		fmt.Println("\nRendered output is unchanged, so a sync is not urgent. `hz sync` applies it.")
	}
	return maybeSync(c, doSync)
}

func hostAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("host add", flag.ContinueOnError)
	name := fs.String("name", "", "host name")
	ip := fs.String("ip", "", "host IP")
	var labels multiFlag
	fs.Var(&labels, "label", "label key=value (repeatable)")
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *ip == "" {
		return fmt.Errorf("--name and --ip are required")
	}
	lbls, err := parseLabels(labels)
	if err != nil {
		return err
	}

	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	for _, h := range topo.Hosts {
		if h.Name == *name {
			return fmt.Errorf("host name already exists: %s", *name)
		}
		if h.IP == *ip {
			return fmt.Errorf("host ip already exists: %s", *ip)
		}
	}
	topo.Hosts = append(topo.Hosts, apitypes.HostDecl{Name: *name, IP: *ip, Labels: lbls})
	if err := c.do("PUT", "/api/v1/topology/hosts", apitypes.TopologyHostsRequest{Hosts: topo.Hosts}, nil); err != nil {
		return err
	}
	fmt.Printf("Added host %q (%s).\n", *name, *ip)
	return maybeSync(c, *doSync)
}

func hostRm(c *client, args []string) error {
	key, rest := splitNameArgs(args)
	if key == "" {
		return fmt.Errorf("usage: hz host rm <name|ip> [--sync]")
	}
	fs := flag.NewFlagSet("host rm", flag.ContinueOnError)
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	var removed bool
	newHosts := make([]apitypes.HostDecl, 0, len(topo.Hosts))
	for _, h := range topo.Hosts {
		if h.Name == key || h.IP == key {
			removed = true
			continue
		}
		newHosts = append(newHosts, h)
	}
	if !removed {
		return fmt.Errorf("host not found: %s", key)
	}
	if err := c.do("PUT", "/api/v1/topology/hosts", apitypes.TopologyHostsRequest{Hosts: newHosts}, nil); err != nil {
		return err
	}
	fmt.Printf("Removed host %q.\n", key)
	return maybeSync(c, *doSync)
}

// --- hz exporter ---

func runExporter(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("exporter subcommand required: list | add | rm")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return exporterList(c)
	case "add", "create":
		return exporterAdd(c, rest)
	case "rm", "remove", "delete":
		return exporterRm(c, rest)
	default:
		return fmt.Errorf("unknown exporter subcommand: %s", sub)
	}
}

// exporterList shows the configured jobs first, then the expanded live
// targets with probe status — the payoff view.
func exporterList(c *client) error {
	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	if len(topo.Exporters) == 0 {
		fmt.Println("No exporters.")
	} else {
		fmt.Printf("%-16s  %-8s  %-24s  %-20s  %s\n", "JOB", "MODE", "TARGETS/PORT", "HOSTS", "PATH")
		for _, e := range topo.Exporters {
			mode := e.Mode
			if mode == "" {
				mode = "port"
			}
			tp := "-"
			switch {
			case len(e.Targets) > 0:
				tp = strings.Join(e.Targets, ",")
			case e.Port != 0:
				tp = fmt.Sprintf("%d", e.Port)
			}
			hosts := "-"
			if len(e.Hosts) > 0 {
				hosts = strings.Join(e.Hosts, ",")
			}
			path := e.Path
			if path == "" {
				path = "-"
			}
			fmt.Printf("%-16s  %-8s  %-24s  %-20s  %s\n", e.Job, mode, tp, hosts, path)
		}
	}
	fmt.Println()
	if len(topo.Targets) == 0 {
		fmt.Println("No live targets.")
		return nil
	}
	fmt.Printf("%-16s  %-24s  %-6s  %s\n", "JOB", "ADDRESS", "ALIVE", "LABELS")
	for _, t := range topo.Targets {
		fmt.Printf("%-16s  %-24s  %-6s  %s\n", t.Job, t.Address, boolWord(t.Alive, "up", "down"), formatLabels(t.Labels))
	}
	return nil
}

func exporterAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("exporter add", flag.ContinueOnError)
	job := fs.String("job", "", "exporter job name")
	mode := fs.String("mode", "", "port | service | static (inferred if omitted)")
	var targets multiFlag
	fs.Var(&targets, "target", "static mode: explicit host:port target (repeatable)")
	port := fs.Int("port", 0, "port mode: port to expand across --host entries")
	var hosts multiFlag
	fs.Var(&hosts, "host", "port mode: host name/IP to expand --port across (repeatable); '*' = all known hosts")
	path := fs.String("path", "", "metrics path(s); CSV probes candidates in order, e.g. /metrics,/api/metrics")
	bearer := fs.String("bearer", "", "optional bearer token")
	var labels multiFlag
	fs.Var(&labels, "label", "label key=value (repeatable)")
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *job == "" {
		return fmt.Errorf("--job is required")
	}
	// Infer mode when omitted, then validate the fields that mode needs.
	m := *mode
	if m == "" {
		switch {
		case len(targets) > 0:
			m = "static"
		case *port > 0:
			m = "port"
		default:
			return fmt.Errorf("need --mode, or one of: --target (static), --port+--host (port)")
		}
	}
	switch m {
	case "port":
		if *port == 0 {
			return fmt.Errorf("port mode needs --port")
		}
		if len(hosts) == 0 {
			hosts = multiFlag{"*"} // default: all known hosts
		}
	case "service":
		// generated from service backends; no port/hosts/targets needed
	case "static":
		if len(targets) == 0 {
			return fmt.Errorf("static mode needs at least one --target")
		}
	default:
		return fmt.Errorf("invalid --mode %q (want port|service|static)", m)
	}
	lbls, err := parseLabels(labels)
	if err != nil {
		return err
	}

	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	for _, e := range topo.Exporters {
		if e.Job == *job {
			return fmt.Errorf("exporter job already exists: %s", *job)
		}
	}
	topo.Exporters = append(topo.Exporters, apitypes.Exporter{
		Job:     *job,
		Mode:    m,
		Targets: targets,
		Port:    *port,
		Hosts:   hosts,
		Path:    *path,
		Bearer:  *bearer,
		Labels:  lbls,
	})
	if err := c.do("PUT", "/api/v1/topology/exporters", apitypes.TopologyExportersRequest{Exporters: topo.Exporters}, nil); err != nil {
		return err
	}
	fmt.Printf("Added exporter %q.\n", *job)
	return maybeSync(c, *doSync)
}

func exporterRm(c *client, args []string) error {
	job, rest := splitNameArgs(args)
	if job == "" {
		return fmt.Errorf("usage: hz exporter rm <job> [--sync]")
	}
	fs := flag.NewFlagSet("exporter rm", flag.ContinueOnError)
	doSync := fs.Bool("sync", false, "trigger a global sync after the mutation")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	topo, err := fetchTopology(c)
	if err != nil {
		return err
	}
	var removed bool
	newExp := make([]apitypes.Exporter, 0, len(topo.Exporters))
	for _, e := range topo.Exporters {
		if e.Job == job {
			removed = true
			continue
		}
		newExp = append(newExp, e)
	}
	if !removed {
		return fmt.Errorf("exporter not found: %s", job)
	}
	if err := c.do("PUT", "/api/v1/topology/exporters", apitypes.TopologyExportersRequest{Exporters: newExp}, nil); err != nil {
		return err
	}
	fmt.Printf("Removed exporter %q.\n", job)
	return maybeSync(c, *doSync)
}
