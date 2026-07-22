package main

import (
	"flag"
	"fmt"
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
		return fmt.Errorf("host subcommand required: list | add | rm")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return hostList(c)
	case "add", "create":
		return hostAdd(c, rest)
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
	if len(topo.Hosts) == 0 {
		fmt.Println("No hosts.")
		return nil
	}
	fmt.Printf("%-20s  %-16s  %s\n", "NAME", "IP", "LABELS")
	for _, h := range topo.Hosts {
		fmt.Printf("%-20s  %-16s  %s\n", h.Name, h.IP, formatLabels(h.Labels))
	}
	return nil
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
		fmt.Printf("%-16s  %-24s  %-20s  %s\n", "JOB", "TARGETS/PORT", "HOSTS", "PATH")
		for _, e := range topo.Exporters {
			tp := "-"
			if len(e.Targets) > 0 {
				tp = strings.Join(e.Targets, ",")
			} else if e.Port != 0 {
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
			fmt.Printf("%-16s  %-24s  %-20s  %s\n", e.Job, tp, hosts, path)
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
	var targets multiFlag
	fs.Var(&targets, "target", "explicit host:port target (repeatable)")
	port := fs.Int("port", 0, "port to expand across --host entries")
	var hosts multiFlag
	fs.Var(&hosts, "host", "host name/IP to expand --port across (repeatable); '*' = all known hosts")
	path := fs.String("path", "", "metrics path (default /metrics)")
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
	if len(targets) == 0 && (*port == 0 || len(hosts) == 0) {
		return fmt.Errorf("need --target, or --port with at least one --host")
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
