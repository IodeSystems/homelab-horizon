package config

import "testing"

func findTarget(ts []ExporterTarget, addr string) *ExporterTarget {
	for i := range ts {
		if ts[i].Address == addr {
			return &ts[i]
		}
	}
	return nil
}

func TestDeriveExporterTargets(t *testing.T) {
	cfg := &Config{
		// One routed service so 192.168.1.76 is a derived (known) host.
		Services: []Service{
			{Name: "app", Proxy: &ProxyConfig{Backend: "192.168.1.76:8300"}},
		},
		Hosts: []HostDecl{
			{Name: "nas", IP: "192.168.1.50", Labels: map[string]string{"role": "storage"}},
			{Name: "db", IP: "192.168.1.60", Labels: map[string]string{"role": "pg"}},
		},
		Exporters: []Exporter{
			// Templated across all known hosts (derived + declared).
			{Job: "node", Port: 9100, Hosts: []string{"*"}},
			// Explicit target, extra static label.
			{Job: "postgres", Targets: []string{"192.168.1.60:9187"}, Labels: map[string]string{"db": "main"}},
			// Port expanded across a named host.
			{Job: "redis", Port: 6379, Hosts: []string{"nas"}, Path: "/scrape"},
		},
	}

	got := cfg.DeriveExporterTargets()

	t.Run("node templated over all known hosts", func(t *testing.T) {
		// Known hosts: 192.168.1.76 (derived), 192.168.1.50, 192.168.1.60 (declared).
		for _, ip := range []string{"192.168.1.76:9100", "192.168.1.50:9100", "192.168.1.60:9100"} {
			if findTarget(got, ip) == nil {
				t.Errorf("node target %s missing", ip)
			}
		}
	})

	t.Run("declared host labels merged", func(t *testing.T) {
		nas := findTarget(got, "192.168.1.50:9100")
		if nas == nil || nas.Labels["role"] != "storage" {
			t.Errorf("nas node target should carry role=storage, got %+v", nas)
		}
		derived := findTarget(got, "192.168.1.76:9100")
		if derived == nil || len(derived.Labels) != 0 {
			t.Errorf("derived host has no declared labels, got %+v", derived)
		}
	})

	t.Run("explicit target keeps its label and default path", func(t *testing.T) {
		pg := findTarget(got, "192.168.1.60:9187")
		if pg == nil || pg.Job != "postgres" || pg.Labels["db"] != "main" {
			t.Errorf("postgres target wrong: %+v", pg)
		}
		if pg != nil && pg.Path != "/metrics" {
			t.Errorf("default path should be /metrics, got %q", pg.Path)
		}
		// Explicit postgres target also picks up the declared db-host label.
		if pg != nil && pg.Labels["role"] != "pg" {
			t.Errorf("explicit target on a declared host should merge role=pg, got %+v", pg.Labels)
		}
	})

	t.Run("named host resolves to ip with custom path", func(t *testing.T) {
		redis := findTarget(got, "192.168.1.50:6379")
		if redis == nil || redis.Job != "redis" || redis.Path != "/scrape" {
			t.Errorf("redis target wrong: %+v", redis)
		}
	})

	t.Run("explicit label wins over host label", func(t *testing.T) {
		cfg2 := &Config{
			Hosts:     []HostDecl{{Name: "db", IP: "192.168.1.60", Labels: map[string]string{"role": "pg"}}},
			Exporters: []Exporter{{Job: "x", Targets: []string{"192.168.1.60:1"}, Labels: map[string]string{"role": "override"}}},
		}
		x := findTarget(cfg2.DeriveExporterTargets(), "192.168.1.60:1")
		if x == nil || x.Labels["role"] != "override" {
			t.Errorf("exporter label should win over host label, got %+v", x)
		}
	})

	t.Run("empty job is skipped", func(t *testing.T) {
		cfg3 := &Config{Exporters: []Exporter{{Job: "", Targets: []string{"1.2.3.4:9100"}}}}
		if len(cfg3.DeriveExporterTargets()) != 0 {
			t.Error("exporter with empty job should be skipped")
		}
	})
}

func TestDeriveExporterTargetsServiceMode(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			// opted-in per-service metrics -> skipped by the service rule
			{Name: "grafana", Proxy: &ProxyConfig{Backend: "192.168.1.76:3000"},
				Integrations: &Integrations{Metrics: &MetricsIntegration{Path: "/metrics"}}},
			// not opted-in, blue-green -> two slots
			{Name: "app", Proxy: &ProxyConfig{Backend: "10.0.0.1:8300", Deploy: &DeployConfig{NextBackend: "10.0.0.1:8301"}}},
			// not opted-in, single backend
			{Name: "api", Proxy: &ProxyConfig{Backend: "10.0.0.2:9000"}},
			// no backend -> ignored
			{Name: "static", Proxy: &ProxyConfig{StaticRoot: "/srv"}},
		},
		Exporters: []Exporter{
			{Job: "svc-metrics", Mode: "service", Path: "/api/metrics"},
		},
	}
	got := cfg.DeriveExporterTargets()

	if findTarget(got, "192.168.1.76:3000") != nil {
		t.Error("opted-in service must be skipped by the service rule")
	}
	cur := findTarget(got, "10.0.0.1:8300")
	if cur == nil || cur.Path != "/api/metrics" || cur.Labels["service"] != "app" || cur.Labels["slot"] != "current" {
		t.Errorf("blue-green current slot wrong: %+v", cur)
	}
	if nx := findTarget(got, "10.0.0.1:8301"); nx == nil || nx.Labels["slot"] != "next" {
		t.Errorf("blue-green next slot wrong: %+v", nx)
	}
	single := findTarget(got, "10.0.0.2:9000")
	if single == nil || single.Labels["service"] != "api" || single.Labels["slot"] != "" {
		t.Errorf("single-backend service target wrong: %+v", single)
	}
}

func TestExporterEffectiveModeInference(t *testing.T) {
	cases := []struct {
		e    Exporter
		want string
	}{
		{Exporter{Port: 9100}, "port"},                          // legacy port-only
		{Exporter{Targets: []string{"1.2.3.4:9187"}}, "static"}, // legacy explicit targets
		{Exporter{Mode: "service"}, "service"},
		{Exporter{}, "port"},
	}
	for _, c := range cases {
		if got := c.e.EffectiveMode(); got != c.want {
			t.Errorf("%+v EffectiveMode = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestDeriveKnownHostIPsIncludesDeclared(t *testing.T) {
	cfg := &Config{
		Services: []Service{{Name: "app", Proxy: &ProxyConfig{Backend: "192.168.1.76:8300"}}},
		Hosts:    []HostDecl{{Name: "nas", IP: "192.168.1.50"}},
	}
	ips := cfg.DeriveKnownHostIPs()
	seen := map[string]bool{}
	for _, ip := range ips {
		seen[ip] = true
	}
	if !seen["192.168.1.76"] || !seen["192.168.1.50"] {
		t.Errorf("known hosts should include derived + declared, got %v", ips)
	}
}

func TestScrapeExclusionsDropTargets(t *testing.T) {
	cfg := &Config{
		// same box reachable at LAN + VPN; VPN address excluded by CIDR.
		ScrapeExclusions: []string{"10.8.0.0/24", "192.168.1.99"},
		Exporters: []Exporter{
			{Job: "node", Mode: "static", Path: "/metrics", Targets: []string{
				"192.168.1.50:9100", // kept
				"10.8.0.50:9100",    // excluded by CIDR
				"192.168.1.99:9100", // excluded by exact IP
			}},
		},
	}
	got := cfg.DeriveExporterTargets()
	if len(got) != 1 {
		t.Fatalf("expected 1 target after exclusions, got %d: %+v", len(got), got)
	}
	if got[0].Address != "192.168.1.50:9100" {
		t.Errorf("wrong surviving target: %q", got[0].Address)
	}

	// '*' port expansion must skip excluded hosts too.
	cfg2 := &Config{
		Hosts:            []HostDecl{{Name: "nas", IP: "192.168.1.50"}, {Name: "nas-vpn", IP: "10.8.0.50"}},
		ScrapeExclusions: []string{"10.8.0.0/24"},
		Exporters:        []Exporter{{Job: "node", Mode: "port", Port: 9100, Hosts: []string{"*"}}},
	}
	for _, tg := range cfg2.DeriveExporterTargets() {
		if tg.Address == "10.8.0.50:9100" {
			t.Errorf("excluded VPN host must not be scraped: %+v", tg)
		}
	}
}

func TestExporterPathListMultiAndDedup(t *testing.T) {
	e := Exporter{Path: "/metrics, /api/metrics ,/metrics"}
	got := e.PathList()
	if len(got) != 2 || got[0] != "/metrics" || got[1] != "/api/metrics" {
		t.Errorf("PathList = %v, want [/metrics /api/metrics]", got)
	}
	if p := (&Exporter{}).PathList(); len(p) != 1 || p[0] != "/metrics" {
		t.Errorf("empty PathList should default to [/metrics], got %v", p)
	}
}

func TestDeriveExporterTargetsCarriesCandidatePaths(t *testing.T) {
	cfg := &Config{
		Services:  []Service{{Name: "app", Proxy: &ProxyConfig{Backend: "10.0.0.9:8300"}}},
		Exporters: []Exporter{{Job: "svc", Mode: "service", Path: "/metrics,/api/metrics"}},
	}
	got := cfg.DeriveExporterTargets()
	tg := findTarget(got, "10.0.0.9:8300")
	if tg == nil || len(tg.Paths) != 2 || tg.Path != "/metrics" {
		t.Errorf("target should carry both candidate paths, got %+v", tg)
	}
}
