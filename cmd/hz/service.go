package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func runService(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service subcommand required: list | show | create | edit | delete")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return serviceList(c)
	case "show", "get":
		return serviceShow(c, rest)
	case "create", "add":
		return serviceCreate(c, rest)
	case "edit", "update":
		return serviceEdit(c, rest)
	case "delete", "rm", "remove":
		return serviceDelete(c, rest)
	default:
		return fmt.Errorf("unknown service subcommand: %s", sub)
	}
}

// splitNameArgs pulls the first non-flag token out as the positional <name>,
// returning the remaining tokens (order preserved) for flag parsing. Go's flag
// package stops at the first positional, so without this a flag placed after
// <name> (the natural CLI order) would be silently ignored.
func splitNameArgs(args []string) (name string, rest []string) {
	for _, a := range args {
		if name == "" && !strings.HasPrefix(a, "-") {
			name = a
			continue
		}
		rest = append(rest, a)
	}
	return name, rest
}

func fetchServices(c *client) ([]apitypes.ServiceResp, error) {
	var out []apitypes.ServiceResp
	if err := c.do("GET", "/api/v1/services", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func findService(list []apitypes.ServiceResp, name string) *apitypes.ServiceResp {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func serviceList(c *client) error {
	list, err := fetchServices(c)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No services.")
		return nil
	}
	fmt.Printf("%-20s  %-32s  %-22s  %-8s  %s\n", "NAME", "DOMAIN", "TARGET", "ACCESS", "PROXY")
	for _, s := range list {
		domain := ""
		if len(s.Domains) > 0 {
			domain = s.Domains[0]
		}
		target, access, proxy := "-", "-", "-"
		if s.Proxy != nil {
			switch {
			case s.Proxy.Backend != "":
				target = s.Proxy.Backend
			case s.Proxy.StaticRoot != "":
				target = "static:" + s.Proxy.StaticRoot
			case s.Proxy.Self:
				target = "self"
			}
			if s.Proxy.InternalOnly {
				access = "internal"
			} else {
				access = "public"
			}
			proxy = s.Status.ProxyState
			if proxy == "" {
				proxy = boolWord(s.Status.ProxyUp, "up", "down")
			}
		}
		fmt.Printf("%-20s  %-32s  %-22s  %-8s  %s\n", s.Name, domain, target, access, proxy)
	}
	return nil
}

func boolWord(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func serviceShow(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	if name == "" {
		return fmt.Errorf("usage: hz service show <name> [--json]")
	}
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	list, err := fetchServices(c)
	if err != nil {
		return err
	}
	svc := findService(list, name)
	if svc == nil {
		return fmt.Errorf("service not found: %s", name)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(svc, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("Name:     %s\n", svc.Name)
	fmt.Printf("Domains:  %s\n", strings.Join(svc.Domains, ", "))
	if svc.InternalDNS != nil {
		fmt.Printf("InternalDNS: %s\n", svc.InternalDNS.IP)
	}
	if svc.ExternalDNS != nil {
		ips := svc.ExternalDNS.ConfiguredIPs
		src := "explicit"
		if len(ips) == 0 {
			ips = svc.ExternalDNS.IPs
			src = "host-fallback"
		}
		fmt.Printf("ExternalDNS: %s (ttl=%d, %s)\n", strings.Join(ips, ","), svc.ExternalDNS.TTL, src)
	}
	if p := svc.Proxy; p != nil {
		fmt.Println("Proxy:")
		if p.Backend != "" {
			fmt.Printf("  backend:      %s\n", p.Backend)
		}
		if p.StaticRoot != "" {
			fmt.Printf("  staticRoot:   %s (spa=%v)\n", p.StaticRoot, p.SPA)
		}
		if p.Self {
			fmt.Printf("  self:         true\n")
		}
		fmt.Printf("  access:       %s\n", boolWord(p.InternalOnly, "internal-only", "public"))
		if p.HealthCheck != nil {
			fmt.Printf("  healthCheck:  %s\n", p.HealthCheck.Path)
		}
		if p.Deploy != nil {
			fmt.Printf("  deploy:       next=%s activeSlot=%s balance=%s\n", p.Deploy.NextBackend, p.Deploy.ActiveSlot, p.Deploy.Balance)
		}
		if t := p.Timeouts; t != nil {
			fmt.Printf("  timeouts:     connect=%d server=%d tunnel=%d\n", t.ConnectSeconds, t.ServerSeconds, t.TunnelSeconds)
		}
		state := svc.Status.ProxyState
		if state == "" {
			state = boolWord(svc.Status.ProxyUp, "up", "down")
		}
		fmt.Printf("  status:       %s%s\n", state, errSuffix(svc.Status.ProxyError))
	}
	return nil
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}

// serviceFlags holds the flag values plus a record of which were explicitly set,
// so edit only touches fields the user named.
type serviceFlags struct {
	fs  *flag.FlagSet
	set map[string]bool

	name        string
	domains     multiFlag
	domainsCSV  string
	backend     string
	staticRoot  string
	static      bool
	self        bool
	spa         bool
	internal    bool
	public      bool
	healthCheck string
	deployNext  string
	balance     string
	intDNSIP    string
	extDNSIP    multiFlag
	ttl         int
	tConnect    int
	tServer     int
	tTunnel     int
	sync        bool
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func newServiceFlags(name string) *serviceFlags {
	sf := &serviceFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	f := sf.fs
	f.StringVar(&sf.name, "name", "", "service name")
	f.Var(&sf.domains, "domain", "domain (repeatable)")
	f.StringVar(&sf.domainsCSV, "domains", "", "comma-separated domains")
	f.StringVar(&sf.backend, "backend", "", "proxy backend host:port")
	f.StringVar(&sf.staticRoot, "static-root", "", "static folder to serve")
	f.BoolVar(&sf.static, "static", false, "serve an hz-managed static folder (auto path when --static-root omitted)")
	f.BoolVar(&sf.self, "self", false, "route to hz's own admin UI")
	f.BoolVar(&sf.spa, "spa", false, "static: serve index.html for unknown paths")
	f.BoolVar(&sf.internal, "internal-only", false, "not publicly reachable")
	f.BoolVar(&sf.public, "public", false, "publicly reachable")
	f.StringVar(&sf.healthCheck, "health-check", "", "backend health-check path")
	f.StringVar(&sf.deployNext, "deploy-next-backend", "", "standby backend host:port (blue-green)")
	f.StringVar(&sf.balance, "balance", "", "first | roundrobin")
	f.StringVar(&sf.intDNSIP, "internal-dns-ip", "", "internal (dnsmasq) A record IP")
	f.Var(&sf.extDNSIP, "external-dns-ip", "external A record IP (repeatable)")
	f.IntVar(&sf.ttl, "ttl", 300, "external DNS TTL seconds")
	f.IntVar(&sf.tConnect, "timeout-connect", 0, "HAProxy connect timeout override (s)")
	f.IntVar(&sf.tServer, "timeout-server", 0, "HAProxy server timeout override (s)")
	f.IntVar(&sf.tTunnel, "timeout-tunnel", 0, "HAProxy tunnel timeout override (s)")
	f.BoolVar(&sf.sync, "sync", false, "trigger a global sync after the mutation")
	f.Usage = func() {
		_, _ = fmt.Fprintf(f.Output(), "Flags for 'hz service %s':\n", name)
		f.PrintDefaults()
		_, _ = fmt.Fprintln(f.Output(), "\nRun 'hz schema service' for the full request schema.")
	}
	return sf
}

func (sf *serviceFlags) parse(args []string) error {
	if err := sf.fs.Parse(args); err != nil {
		return err
	}
	sf.set = map[string]bool{}
	sf.fs.Visit(func(fl *flag.Flag) { sf.set[fl.Name] = true })
	return nil
}

func (sf *serviceFlags) allDomains() []string {
	var out []string
	out = append(out, sf.domains...)
	for _, d := range strings.Split(sf.domainsCSV, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// buildRequest constructs a ServiceRequest from scratch (create).
func (sf *serviceFlags) buildRequest() (apitypes.ServiceRequest, error) {
	req := apitypes.ServiceRequest{
		Name:    sf.name,
		Domains: sf.allDomains(),
	}
	if req.Name == "" {
		return req, fmt.Errorf("--name is required")
	}
	if len(req.Domains) == 0 {
		return req, fmt.Errorf("at least one --domain is required")
	}
	if sf.intDNSIP != "" {
		req.InternalDNS = &apitypes.ServiceRequestInternalDNS{IP: sf.intDNSIP}
	}
	if len(sf.extDNSIP) > 0 {
		req.ExternalDNS = &apitypes.ServiceRequestExternalDNS{IPs: sf.extDNSIP, TTL: sf.ttl}
	}
	proxy := sf.buildProxy()
	if proxy != nil {
		req.Proxy = proxy
	}
	return req, nil
}

func (sf *serviceFlags) buildProxy() *apitypes.ServiceRequestProxy {
	if sf.backend == "" && sf.staticRoot == "" && !sf.self && !sf.static {
		return nil
	}
	p := &apitypes.ServiceRequestProxy{
		Backend:      sf.backend,
		StaticRoot:   sf.staticRoot,
		Static:       sf.static,
		Self:         sf.self,
		SPA:          sf.spa,
		InternalOnly: sf.internal,
	}
	if sf.healthCheck != "" {
		p.HealthCheck = &apitypes.ServiceRequestHealthCheck{Path: sf.healthCheck}
	}
	if sf.deployNext != "" {
		p.Deploy = &apitypes.ServiceRequestDeploy{NextBackend: sf.deployNext, Balance: sf.balance}
	}
	if sf.tConnect > 0 || sf.tServer > 0 || sf.tTunnel > 0 {
		p.Timeouts = &apitypes.ServiceRequestTimeouts{
			ConnectSeconds: sf.tConnect,
			ServerSeconds:  sf.tServer,
			TunnelSeconds:  sf.tTunnel,
		}
	}
	return p
}

func serviceCreate(c *client, args []string) error {
	sf := newServiceFlags("create")
	if err := sf.parse(args); err != nil {
		return err
	}
	req, err := sf.buildRequest()
	if err != nil {
		return err
	}
	if err := c.do("POST", "/api/v1/services/add", req, nil); err != nil {
		return err
	}
	fmt.Printf("Created service %q.\n", req.Name)
	return maybeSync(c, sf.sync)
}

// respToRequest converts an existing ServiceResp into a ServiceRequest so an
// edit round-trips unchanged fields. Uses ConfiguredIPs (not resolved IPs) for
// external DNS, matching the server's edit semantics.
func respToRequest(s *apitypes.ServiceResp) apitypes.ServiceRequest {
	req := apitypes.ServiceRequest{
		OriginalName: s.Name,
		Name:         s.Name,
		Domains:      append([]string(nil), s.Domains...),
	}
	if s.InternalDNS != nil {
		req.InternalDNS = &apitypes.ServiceRequestInternalDNS{IP: s.InternalDNS.IP}
	}
	if s.ExternalDNS != nil {
		req.ExternalDNS = &apitypes.ServiceRequestExternalDNS{
			IPs: append([]string(nil), s.ExternalDNS.ConfiguredIPs...),
			TTL: s.ExternalDNS.TTL,
		}
	}
	if p := s.Proxy; p != nil {
		rp := &apitypes.ServiceRequestProxy{
			Backend:      p.Backend,
			StaticRoot:   p.StaticRoot,
			Self:         p.Self,
			SPA:          p.SPA,
			InternalOnly: p.InternalOnly,
		}
		if p.HealthCheck != nil {
			rp.HealthCheck = &apitypes.ServiceRequestHealthCheck{Path: p.HealthCheck.Path}
		}
		if p.Deploy != nil {
			rp.Deploy = &apitypes.ServiceRequestDeploy{NextBackend: p.Deploy.NextBackend, Balance: p.Deploy.Balance}
		}
		if t := p.Timeouts; t != nil {
			rp.Timeouts = &apitypes.ServiceRequestTimeouts{
				ConnectSeconds: t.ConnectSeconds,
				ServerSeconds:  t.ServerSeconds,
				TunnelSeconds:  t.TunnelSeconds,
			}
		}
		req.Proxy = rp
	}
	return req
}

func serviceEdit(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	if name == "" {
		return fmt.Errorf("usage: hz service edit <name> [flags]")
	}
	sf := newServiceFlags("edit")
	if err := sf.parse(rest); err != nil {
		return err
	}

	list, err := fetchServices(c)
	if err != nil {
		return err
	}
	svc := findService(list, name)
	if svc == nil {
		return fmt.Errorf("service not found: %s", name)
	}
	req := respToRequest(svc)

	// Apply only explicitly-set flags.
	set := sf.set
	if set["name"] {
		req.Name = sf.name
	}
	if set["domain"] || set["domains"] {
		req.Domains = sf.allDomains()
	}
	if set["internal-dns-ip"] {
		if sf.intDNSIP == "" {
			req.InternalDNS = nil
		} else {
			req.InternalDNS = &apitypes.ServiceRequestInternalDNS{IP: sf.intDNSIP}
		}
	}
	if set["external-dns-ip"] || set["ttl"] {
		if req.ExternalDNS == nil {
			req.ExternalDNS = &apitypes.ServiceRequestExternalDNS{TTL: sf.ttl}
		}
		if set["external-dns-ip"] {
			req.ExternalDNS.IPs = sf.extDNSIP
		}
		if set["ttl"] {
			req.ExternalDNS.TTL = sf.ttl
		}
	}

	// Proxy edits: mutate the existing proxy (or create one) field-by-field.
	if proxyFlagSet(set) {
		if req.Proxy == nil {
			req.Proxy = &apitypes.ServiceRequestProxy{}
		}
		p := req.Proxy
		if set["backend"] {
			p.Backend = sf.backend
		}
		if set["static-root"] {
			p.StaticRoot = sf.staticRoot
		}
		if set["static"] {
			p.Static = sf.static
		}
		if set["self"] {
			p.Self = sf.self
		}
		if set["spa"] {
			p.SPA = sf.spa
		}
		if set["internal-only"] {
			p.InternalOnly = sf.internal
		}
		if set["public"] && sf.public {
			p.InternalOnly = false
		}
		if set["health-check"] {
			if sf.healthCheck == "" {
				p.HealthCheck = nil
			} else {
				p.HealthCheck = &apitypes.ServiceRequestHealthCheck{Path: sf.healthCheck}
			}
		}
		if set["deploy-next-backend"] {
			if sf.deployNext == "" {
				p.Deploy = nil
			} else {
				p.Deploy = &apitypes.ServiceRequestDeploy{NextBackend: sf.deployNext, Balance: sf.balance}
			}
		} else if set["balance"] && p.Deploy != nil {
			p.Deploy.Balance = sf.balance
		}
		if set["timeout-connect"] || set["timeout-server"] || set["timeout-tunnel"] {
			if p.Timeouts == nil {
				p.Timeouts = &apitypes.ServiceRequestTimeouts{}
			}
			if set["timeout-connect"] {
				p.Timeouts.ConnectSeconds = sf.tConnect
			}
			if set["timeout-server"] {
				p.Timeouts.ServerSeconds = sf.tServer
			}
			if set["timeout-tunnel"] {
				p.Timeouts.TunnelSeconds = sf.tTunnel
			}
		}
	}

	if err := c.do("POST", "/api/v1/services/edit", req, nil); err != nil {
		return err
	}
	fmt.Printf("Edited service %q.\n", name)
	return maybeSync(c, sf.sync)
}

func proxyFlagSet(set map[string]bool) bool {
	for _, k := range []string{"backend", "static-root", "static", "self", "spa", "internal-only",
		"public", "health-check", "deploy-next-backend", "balance",
		"timeout-connect", "timeout-server", "timeout-tunnel"} {
		if set[k] {
			return true
		}
	}
	return false
}

func serviceDelete(c *client, args []string) error {
	name, rest := splitNameArgs(args)
	if name == "" {
		return fmt.Errorf("usage: hz service delete <name> [--yes] [--sync]")
	}
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	doSync := fs.Bool("sync", false, "trigger a global sync after delete")
	yes := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(rest); err != nil {
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
	body := map[string]string{"name": name}
	if err := c.do("POST", "/api/v1/services/delete", body, nil); err != nil {
		return err
	}
	fmt.Printf("Deleted service %q.\n", name)
	return maybeSync(c, *doSync)
}

// --- sync / pending ---

func maybeSync(c *client, do bool) error {
	if !do {
		return nil
	}
	return runSync(c, []string{"--wait"})
}

func runSync(c *client, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	wait := fs.Bool("wait", false, "block until the sync finishes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var out apitypes.TriggerSyncResponse
	if err := c.do("POST", "/api/v1/services/sync", nil, &out); err != nil {
		return err
	}
	if !out.Started {
		fmt.Println("A sync is already running.")
	} else {
		fmt.Println("Sync started.")
	}
	if !*wait {
		return nil
	}
	return waitForSync(c)
}

func waitForSync(c *client) error {
	fmt.Print("Waiting for sync")
	for i := 0; i < 120; i++ {
		var st struct {
			Running bool `json:"running"`
		}
		if err := c.do("GET", "/api/v1/services/sync/status", nil, &st); err != nil {
			fmt.Println()
			return err
		}
		if !st.Running {
			fmt.Println(" done.")
			return nil
		}
		fmt.Print(".")
		sleep1s()
	}
	fmt.Println()
	return fmt.Errorf("sync still running after 120s")
}

func runPending(c *client, _ []string) error {
	var pc apitypes.PendingChanges
	if err := c.do("GET", "/api/v1/sync/pending", nil, &pc); err != nil {
		return err
	}
	if !pc.HasPending {
		fmt.Println("No pending changes — everything is synced.")
		return nil
	}
	fmt.Printf("%d pending change(s):\n", pc.Count)
	sort.Slice(pc.Items, func(i, j int) bool {
		if pc.Items[i].Kind != pc.Items[j].Kind {
			return pc.Items[i].Kind < pc.Items[j].Kind
		}
		return pc.Items[i].Name < pc.Items[j].Name
	})
	for _, it := range pc.Items {
		fmt.Printf("  %-8s %-8s %s\n", it.Change, it.Kind, it.Name)
		for _, f := range it.Fields {
			fmt.Printf("      %s: %s -> %s\n", f.Path, f.Before, f.After)
		}
	}
	fmt.Println("\nRun 'hz sync' to publish.")
	return nil
}
