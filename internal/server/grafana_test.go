package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestDashboardAdaptsToDeployment: panels for software that isn't installed
// would render "No data" forever, and a dashboard with dead panels teaches
// people to ignore panels.
func TestDashboardAdaptsToDeployment(t *testing.T) {
	bare := mustJSON(t, buildDashboard(&config.Config{}))
	if strings.Contains(bare, "node_cpu_seconds_total") {
		t.Error("host panels must be absent without node-exporter")
	}
	if strings.Contains(bare, "hz_dnsmasq_") {
		t.Error("dnsmasq panels must be absent when dnsmasq is off")
	}
	if strings.Contains(bare, "haproxy_frontend_") {
		t.Error("HAProxy exporter panels must be absent when its port is disabled")
	}
	// hz's own metrics are always available, so they are never conditional.
	if !strings.Contains(bare, "hz_vpn_mfa_jailed_peers") {
		t.Error("hz's own panels should always be present")
	}

	full := mustJSON(t, buildDashboard(&config.Config{
		DNSMasqEnabled:      true,
		NodeExporterEnabled: true,
		HAProxyMetricsPort:  8405,
	}))
	for _, want := range []string{"node_cpu_seconds_total", "hz_dnsmasq_cache_hits_total", "haproxy_frontend_http_responses_total"} {
		if !strings.Contains(full, want) {
			t.Errorf("a fully-equipped deployment should graph %s", want)
		}
	}
}

// Grafana silently misrenders panels that share an ID, and overlapping grid
// positions stack panels on top of each other.
func TestDashboardLayoutIsSane(t *testing.T) {
	d := buildDashboard(&config.Config{DNSMasqEnabled: true, NodeExporterEnabled: true, HAProxyMetricsPort: 8405})

	seen := map[int]bool{}
	type cell struct{ x, y int }
	occupied := map[cell]string{}

	for _, p := range d.Panels {
		if p.ID == 0 {
			t.Errorf("panel %q has no id", p.Title)
		}
		if seen[p.ID] {
			t.Errorf("panel %q reuses id %d", p.Title, p.ID)
		}
		seen[p.ID] = true

		if p.GridPos.X+p.GridPos.W > 24 {
			t.Errorf("panel %q runs past the 24-column grid (x=%d w=%d)", p.Title, p.GridPos.X, p.GridPos.W)
		}
		for x := p.GridPos.X; x < p.GridPos.X+p.GridPos.W; x++ {
			for y := p.GridPos.Y; y < p.GridPos.Y+p.GridPos.H; y++ {
				if other, clash := occupied[cell{x, y}]; clash {
					t.Errorf("panel %q overlaps %q at (%d,%d)", p.Title, other, x, y)
				}
				occupied[cell{x, y}] = p.Title
			}
		}
	}
}

// Every query must go through the datasource template variable. A hardcoded
// UID is the classic reason a shared dashboard imports empty: it refers to a
// datasource that exists only on the machine that generated it.
func TestDashboardUsesDatasourceVariable(t *testing.T) {
	d := buildDashboard(&config.Config{DNSMasqEnabled: true})

	if len(d.Templating.List) == 0 || d.Templating.List[0].Type != "datasource" {
		t.Fatal("dashboard must declare a datasource variable")
	}
	for _, p := range d.Panels {
		if p.Datasource.UID != "${datasource}" {
			t.Errorf("panel %q hardcodes datasource %q", p.Title, p.Datasource.UID)
		}
		for _, tg := range p.Targets {
			if tg.Datasource.UID != "${datasource}" {
				t.Errorf("target in %q hardcodes datasource %q", p.Title, tg.Datasource.UID)
			}
			if strings.TrimSpace(tg.Expr) == "" {
				t.Errorf("panel %q has an empty query", p.Title)
			}
			if tg.RefID == "" {
				t.Errorf("target in %q has no refId; Grafana needs one per query", p.Title)
			}
		}
	}
}

// Panels sharing a refId within one panel silently drop queries.
func TestDashboardRefIDsUniqueWithinPanel(t *testing.T) {
	d := buildDashboard(&config.Config{DNSMasqEnabled: true, NodeExporterEnabled: true, HAProxyMetricsPort: 8405})
	for _, p := range d.Panels {
		seen := map[string]bool{}
		for _, tg := range p.Targets {
			if seen[tg.RefID] {
				t.Errorf("panel %q reuses refId %q", p.Title, tg.RefID)
			}
			seen[tg.RefID] = true
		}
	}
}

// A Prometheus query in a table panel needs format:"table". Without it Grafana
// renders the result as time-series shaped data — Time and Value columns, every
// label collapsed into the series name — so a per-label table shows none of its
// labels. The panel still "works", which is why this shipped once already.
func TestTablePanelsRequestTableFormat(t *testing.T) {
	d := buildDashboard(&config.Config{})
	found := false
	for _, p := range d.Panels {
		if p.Type != "table" {
			continue
		}
		found = true
		for _, tg := range p.Targets {
			if tg.Format != "table" {
				t.Errorf("table panel %q queries without format:table", p.Title)
			}
			if !tg.Instant {
				t.Errorf("table panel %q should use an instant query", p.Title)
			}
		}
	}
	if !found {
		t.Error("expected at least one table panel")
	}
}

// Someone hunting for compliance state searches for "PCI", not "controls".
func TestControlsPanelIsFindable(t *testing.T) {
	d := buildDashboard(&config.Config{})
	for _, p := range d.Panels {
		if strings.Contains(p.Title, "PCI") {
			return
		}
	}
	t.Error("no panel title mentions PCI; the control table is what people come looking for")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("dashboard must marshal: %v", err)
	}
	return string(b)
}

// The per-service table appears only when something is scoped in. An empty
// compliance table on a deployment that never opted into PCI is another dead
// panel, and dead panels are how people learn to ignore panels.
func TestServiceScopePanelOnlyWhenScoped(t *testing.T) {
	bare := mustJSON(t, buildDashboard(&config.Config{}))
	if strings.Contains(bare, "hz_service_control_state") {
		t.Error("no scoped services should mean no per-service panel")
	}

	scoped := mustJSON(t, buildDashboard(&config.Config{
		Services: []config.Service{{Name: "shop", PCIScope: config.PCIScopeCDE}},
	}))
	if !strings.Contains(scoped, "hz_service_control_state") {
		t.Error("a scoped service should add the per-service panel")
	}
}
