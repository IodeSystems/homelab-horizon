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
