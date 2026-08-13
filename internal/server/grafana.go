package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A ready-to-import Grafana dashboard for everything hz publishes.
//
// Generated rather than shipped as a static file so it matches the box: a
// deployment without node-exporter shouldn't get a row of "No data" CPU
// panels, and one without dnsmasq shouldn't get cache-hit graphs that will
// never fill in. A dashboard with dead panels teaches people to ignore panels.
//
// Every metric named here is asserted to exist by bin/e2e — the names come
// from a scrape of a running instance, not from memory.

// grafanaSchemaVersion targets a broadly compatible schema. Grafana upgrades
// dashboards on import, so aiming low is safer than aiming current.
const grafanaSchemaVersion = 39

type gfDashboard struct {
	Title         string       `json:"title"`
	UID           string       `json:"uid"`
	Tags          []string     `json:"tags"`
	Timezone      string       `json:"timezone"`
	SchemaVersion int          `json:"schemaVersion"`
	Refresh       string       `json:"refresh"`
	Time          gfTime       `json:"time"`
	Templating    gfTemplating `json:"templating"`
	Panels        []gfPanel    `json:"panels"`
}

type gfTime struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type gfTemplating struct {
	List []gfTemplateVar `json:"list"`
}

// gfTemplateVar is the datasource picker. Without it the dashboard hardcodes a
// datasource UID from whatever instance generated it, which never matches the
// importer's — the classic reason a shared dashboard imports empty.
type gfTemplateVar struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Query   string `json:"query"`
	Current struct {
		Text  string `json:"text"`
		Value string `json:"value"`
	} `json:"current"`
	Hide int `json:"hide"`
}

type gfPanel struct {
	Type        string       `json:"type"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Datasource  gfDatasource `json:"datasource"`
	GridPos     gfGridPos    `json:"gridPos"`
	Targets     []gfTarget   `json:"targets,omitempty"`
	FieldConfig *gfFieldCfg  `json:"fieldConfig,omitempty"`
	Options     any          `json:"options,omitempty"`
	Collapsed   *bool        `json:"collapsed,omitempty"`
	SubPanels   []gfPanel    `json:"panels,omitempty"`
	ID          int          `json:"id"`
}

type gfDatasource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type gfGridPos struct {
	H int `json:"h"`
	W int `json:"w"`
	X int `json:"x"`
	Y int `json:"y"`
}

type gfTarget struct {
	Expr         string       `json:"expr"`
	LegendFormat string       `json:"legendFormat,omitempty"`
	RefID        string       `json:"refId"`
	Datasource   gfDatasource `json:"datasource"`
	Instant      bool         `json:"instant,omitempty"`
}

type gfFieldCfg struct {
	Defaults  gfFieldDefaults `json:"defaults"`
	Overrides []any           `json:"overrides"`
}

type gfFieldDefaults struct {
	Unit       string       `json:"unit,omitempty"`
	Min        *float64     `json:"min,omitempty"`
	Decimals   *int         `json:"decimals,omitempty"`
	Mappings   []any        `json:"mappings"`
	Thresholds *gfThreshold `json:"thresholds,omitempty"`
}

type gfThreshold struct {
	Mode  string         `json:"mode"`
	Steps []gfThreshStep `json:"steps"`
}

type gfThreshStep struct {
	Color string   `json:"color"`
	Value *float64 `json:"value"`
}

// dashboardBuilder lays panels out left to right, wrapping rows, so callers
// describe what they want rather than doing grid arithmetic.
type dashboardBuilder struct {
	panels []gfPanel
	x, y   int
	rowH   int
	nextID int
}

const gridWidth = 24

func (b *dashboardBuilder) add(p gfPanel, w, h int) {
	if b.x+w > gridWidth {
		b.x = 0
		b.y += b.rowH
		b.rowH = 0
	}
	b.nextID++
	p.ID = b.nextID
	p.GridPos = gfGridPos{H: h, W: w, X: b.x, Y: b.y}
	p.Datasource = dsRef()
	for i := range p.Targets {
		p.Targets[i].Datasource = dsRef()
		if p.Targets[i].RefID == "" {
			p.Targets[i].RefID = "A"
		}
	}
	b.panels = append(b.panels, p)
	b.x += w
	if h > b.rowH {
		b.rowH = h
	}
}

// newRow starts a fresh line even if the current one has space.
func (b *dashboardBuilder) newRow() {
	if b.x != 0 {
		b.x = 0
		b.y += b.rowH
		b.rowH = 0
	}
}

func dsRef() gfDatasource {
	return gfDatasource{Type: "prometheus", UID: "${datasource}"}
}

func f64(v float64) *float64 { return &v }

func thresholds(steps ...gfThreshStep) *gfThreshold {
	return &gfThreshold{Mode: "absolute", Steps: steps}
}

// stat is a single big number.
func stat(title, desc, expr, unit string, th *gfThreshold) gfPanel {
	return gfPanel{
		Type: "stat", Title: title, Description: desc,
		Targets: []gfTarget{{Expr: expr, Instant: true}},
		FieldConfig: &gfFieldCfg{
			Defaults:  gfFieldDefaults{Unit: unit, Mappings: []any{}, Thresholds: th},
			Overrides: []any{},
		},
		Options: map[string]any{
			"colorMode":     "value",
			"graphMode":     "area",
			"reduceOptions": map[string]any{"calcs": []string{"lastNotNull"}},
		},
	}
}

// series is a time series with one or more queries.
func series(title, desc, unit string, targets ...gfTarget) gfPanel {
	return gfPanel{
		Type: "timeseries", Title: title, Description: desc,
		Targets: targets,
		FieldConfig: &gfFieldCfg{
			Defaults:  gfFieldDefaults{Unit: unit, Min: f64(0), Mappings: []any{}},
			Overrides: []any{},
		},
	}
}

func target(expr, legend, refID string) gfTarget {
	return gfTarget{Expr: expr, LegendFormat: legend, RefID: refID}
}

// buildDashboard assembles the dashboard for this deployment.
func buildDashboard(cfg *config.Config) gfDashboard {
	b := &dashboardBuilder{}

	okBad := thresholds(
		gfThreshStep{Color: "red", Value: nil},
		gfThreshStep{Color: "green", Value: f64(1)},
	)
	badWhenAny := thresholds(
		gfThreshStep{Color: "green", Value: nil},
		gfThreshStep{Color: "orange", Value: f64(1)},
	)

	// ---- top line: is anything wrong right now ----
	b.add(stat("hz", "1 when hz is serving the scrape.", "hz_up", "", okBad), 3, 4)
	b.add(stat("VPN peers", "Configured WireGuard peers.", "hz_vpn_peers", "", nil), 3, 4)
	b.add(stat("Peers online", "Handshaked within the last three minutes.",
		"hz_vpn_peers_recently_handshaked", "", nil), 3, 4)
	b.add(stat("Jailed by MFA", "Peers confined to the portal until they authenticate.",
		"hz_vpn_mfa_jailed_peers", "", nil), 3, 4)
	b.add(stat("MFA exceptions", "Live time-limited bypasses. Expected during an incident, worth asking about otherwise.",
		"hz_vpn_mfa_active_exceptions", "", badWhenAny), 4, 4)
	b.add(stat("Banned IPs", "Addresses blocked at the edge.", "hz_banned_ips", "", nil), 4, 4)
	b.add(stat("iptables drift", "Live rules hz does not recognise. Sustained non-zero means something else is editing the firewall.",
		`sum(hz_iptables_rules{state=~"unknown|stale"})`, "", badWhenAny), 4, 4)

	// ---- security posture ----
	b.newRow()
	b.add(series("MFA sessions and jailed peers", "", "",
		target("hz_vpn_mfa_active_sessions", "active sessions", "A"),
		target("hz_vpn_mfa_jailed_peers", "jailed", "B"),
	), 12, 8)
	b.add(series("Second factors enrolled", "Peers holding each kind of factor. A peer may hold both.", "",
		target("hz_vpn_mfa_enrolled_peers", "{{factor}}", "A"),
	), 12, 8)

	b.newRow()
	b.add(gfPanel{
		Type:  "table",
		Title: "Security controls",
		Description: "1 = the control is in its hardened setting. This describes hz's configuration; " +
			"whether it satisfies a requirement is an assessor's judgement over a defined scope.",
		Targets: []gfTarget{{Expr: "hz_control_state", LegendFormat: "{{control}}", Instant: true}},
		FieldConfig: &gfFieldCfg{
			Defaults:  gfFieldDefaults{Mappings: []any{}, Thresholds: okBad},
			Overrides: []any{},
		},
	}, 12, 8)
	b.add(series("iptables rules by classification", "Expected, stale, blessed and unknown, as the reconciler sees them.", "",
		target("hz_iptables_rules", "{{state}}", "A"),
	), 12, 8)

	// ---- edge ----
	b.newRow()
	b.add(series("HAProxy backends up", "1 when hz sees the backend as healthy.", "",
		target("hz_haproxy_backend_up", "{{backend}}", "A"),
	), 12, 8)

	if cfg.HAProxyMetricsPort > 0 {
		// From HAProxy's own exporter, which hz enables by generating the
		// listener. Absent from the dashboard when that port is disabled,
		// since the series would never appear.
		b.add(series("HTTP responses by code", "From HAProxy's built-in exporter.", "reqps",
			target(`sum by (code) (rate(haproxy_frontend_http_responses_total[5m]))`, "{{code}}", "A"),
		), 12, 8)
		b.newRow()
		b.add(series("Backend response time (p99)", "", "s",
			target(`histogram_quantile(0.99, sum by (le, proxy) (rate(haproxy_backend_response_time_seconds_bucket[5m])))`,
				"{{proxy}}", "A"),
		), 12, 8)
		b.add(series("Current sessions", "", "",
			target("haproxy_frontend_current_sessions", "{{proxy}}", "A"),
		), 12, 8)
	}

	// ---- DNS ----
	if cfg.DNSMasqEnabled {
		b.newRow()
		b.add(series("dnsmasq cache hit ratio", "Hits as a share of answered queries. A low ratio with rising evictions means the cache is undersized.", "percentunit",
			target(`rate(hz_dnsmasq_cache_hits_total[5m]) / clamp_min(rate(hz_dnsmasq_cache_hits_total[5m]) + rate(hz_dnsmasq_cache_misses_total[5m]), 0.001)`,
				"hit ratio", "A"),
		), 8, 8)
		b.add(series("dnsmasq cache churn", "Insertions and evictions per second.", "ops",
			target("rate(hz_dnsmasq_cache_insertions_total[5m])", "insertions", "A"),
			target("rate(hz_dnsmasq_cache_evictions_total[5m])", "evictions", "B"),
		), 8, 8)
		b.add(series("Upstream resolver failures", "Failures climbing on one server while its siblings stay flat is the signal worth alerting on.", "ops",
			target("rate(hz_dnsmasq_upstream_failures_total[5m])", "{{server}}", "A"),
		), 8, 8)
	}

	// ---- host ----
	if cfg.NodeExporterEnabled {
		// node-exporter's own metrics: hz does not reimplement these, it
		// installs the exporter and merges it into the scrape config.
		b.newRow()
		b.add(series("CPU utilisation", "From node-exporter.", "percentunit",
			target(`1 - avg without (cpu) (rate(node_cpu_seconds_total{mode="idle"}[5m]))`, "cpu", "A"),
		), 8, 8)
		b.add(series("Memory used", "From node-exporter.", "percentunit",
			target(`1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`, "memory", "A"),
		), 8, 8)
		b.add(series("Root filesystem used", "From node-exporter.", "percentunit",
			target(`1 - (node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`,
				"{{mountpoint}}", "A"),
		), 8, 8)
	}

	d := gfDashboard{
		Title:         "Homelab Horizon",
		UID:           "homelab-horizon",
		Tags:          []string{"homelab-horizon"},
		Timezone:      "browser",
		SchemaVersion: grafanaSchemaVersion,
		Refresh:       "30s",
		Time:          gfTime{From: "now-6h", To: "now"},
		Panels:        b.panels,
	}
	v := gfTemplateVar{
		Name: "datasource", Label: "Data source", Type: "datasource", Query: "prometheus",
	}
	v.Current.Text = "Prometheus"
	v.Current.Value = "Prometheus"
	d.Templating.List = []gfTemplateVar{v}
	return d
}

// handleIntegrationGrafanaDashboard serves the dashboard JSON for copy/paste
// into Grafana's import screen.
func (s *Server) handleIntegrationGrafanaDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminOrScrapeToken(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	d := buildDashboard(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	// Indented: a human is going to read this in a textarea before pasting it.
	enc.SetIndent("", "  ")
	_ = enc.Encode(d)
}
