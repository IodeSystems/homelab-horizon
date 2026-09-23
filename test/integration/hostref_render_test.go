package integration

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// updateGolden regenerates testdata/legacy_render.golden.
//
// The golden was produced by running this file against the tree BEFORE host
// references existed, which is what makes it a compatibility proof rather than
// a description of current behaviour: the bytes in it are the old code's
// output. Regenerating it from the new code would prove nothing, so do not,
// unless the representative config below changes on purpose.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/legacy_render.golden from this run")

// legacyConfigJSON is a config as every config written before host references
// was written: every address a literal string, no "@" anywhere. It covers each
// record kind that can now hold a reference — proxy backend, blue-green next
// backend, L4 forward, local DNS record, service internal DNS, exporter static
// target, exporter port-mode host list, scrape exclusion — so the golden
// exercises every code path resolution was threaded through.
const legacyConfigJSON = `{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "vpn_range": "10.100.0.0/24",
  "server_endpoint": "vpn.example.net:51820",
  "local_interface": "192.168.1.1",
  "last_local_iface": "eth0",
  "last_lan_cidr": "192.168.1.0/24",
  "haproxy_enabled": true,
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,
  "dnsmasq_enabled": true,
  "zones": [{"name": "example.net", "zone_id": "Z1"}],
  "hosts": [
    {"name": "nas", "ip": "192.168.1.160", "labels": {"role": "storage"}},
    {"name": "db", "ip": "192.168.1.60"}
  ],
  "services": [
    {
      "name": "app",
      "domains": ["app.example.net"],
      "internal_dns": {"ip": "192.168.1.160"},
      "proxy": {
        "backend": "192.168.1.160:8080",
        "health_check": {"path": "/health"},
        "deploy": {"next_backend": "192.168.1.160:8081", "token": "t", "active_slot": "a"}
      },
      "forwards": [
        {"proto": "udp", "port": 4433, "backend": "192.168.1.160:4433", "name": "webtransport"}
      ]
    },
    {
      "name": "files",
      "domains": ["files.example.net"],
      "proxy": {"backend": "192.168.1.160:445", "internal_only": true}
    }
  ],
  "local_dns_records": [
    {"name": "nas", "ip": "192.168.1.160", "comment": "the box in the cupboard"},
    {"name": "desktop", "ip": "192.168.1.90"}
  ],
  "exporters": [
    {"job": "node", "mode": "port", "port": 9100, "hosts": ["192.168.1.160", "192.168.1.60"]},
    {"job": "pg", "mode": "static", "targets": ["192.168.1.60:9187"], "labels": {"db": "main"}},
    {"job": "app", "mode": "service", "path": "/metrics"}
  ],
  "scrape_exclusions": ["192.168.1.90"]
}`

// renderAll is every artifact the config produces that an address can reach:
// haproxy.cfg, the dnsmasq records file, the iptables rule set, the Prometheus
// scrape targets, and the derived host/port map. One string, so the comparison
// is one comparison.
func renderAll(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var b strings.Builder

	b.WriteString("=== haproxy.cfg ===\n")
	b.WriteString(haproxy.RenderConfig(haproxy.ConfigInput{
		HTTPPort:  cfg.HAProxyHTTPPort,
		HTTPSPort: cfg.HAProxyHTTPSPort,
		Backends:  cfg.DeriveHAProxyBackends(),
	}))

	b.WriteString("\n=== dnsmasq records ===\n")
	b.WriteString(dnsmasq.RenderHosts(dnsmasq.HostsInput{Records: cfg.DeriveDNSRecords()}))

	b.WriteString("\n=== iptables ===\n")
	rules := iptables.ExpectedRules(iptables.Inputs{
		WGInterface:   cfg.WGInterface,
		OutIface:      cfg.LastLocalIface,
		VPNRange:      cfg.VPNRange,
		LanCIDR:       cfg.LastLanCIDR,
		ServerWGIP:    "10.100.0.1",
		ListenPort:    "8080",
		Forwards:      iptables.ForwardsFromConfig(cfg),
		ReservedPorts: cfg.ForwardReservedPorts(),
	})
	for _, r := range rules {
		b.WriteString(r.Canonical())
		b.WriteString("\n")
	}

	b.WriteString("\n=== prometheus targets ===\n")
	for _, tgt := range cfg.DeriveExporterTargets() {
		keys := make([]string, 0, len(tgt.Labels))
		for k := range tgt.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+tgt.Labels[k])
		}
		fmt.Fprintf(&b, "%s %s %s {%s}\n", tgt.Job, tgt.Address, tgt.Path, strings.Join(pairs, ","))
	}

	b.WriteString("\n=== host/port map ===\n")
	m := cfg.DeriveHostPortMap()
	hosts := make([]string, 0, len(m.Hosts))
	for h := range m.Hosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		entries := m.Hosts[h]
		lines := make([]string, 0, len(entries))
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("%s/%s %s", e.Proto, e.Port, e.Service))
		}
		sort.Strings(lines)
		fmt.Fprintf(&b, "%s\n", h)
		for _, l := range lines {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}

	b.WriteString("\n=== known host ips ===\n")
	b.WriteString(strings.Join(cfg.DeriveKnownHostIPs(), "\n"))
	b.WriteString("\n")

	return b.String()
}

// TestLegacyConfigRendersByteIdentically is the compatibility rule, checked
// rather than asserted: a config written before host references existed must
// still render exactly the bytes it rendered then.
//
// The golden file was generated from the pre-change tree. Nothing in the
// legacy config carries the "@" sigil, so resolution never fires on it and the
// bytes cannot move — but "cannot" is a claim, and this is the measurement.
func TestLegacyConfigRendersByteIdentically(t *testing.T) {
	cfg, err := config.LoadFromJSON([]byte(legacyConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := renderAll(t, cfg)

	path := filepath.Join("testdata", "legacy_render.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with -update-golden on the PRE-change tree)", err)
	}
	if got != string(want) {
		t.Errorf("rendered output changed for a config that uses no host references")
		for i, line := range diffLines(string(want), got) {
			if i > 40 {
				t.Errorf("  ... and more")
				break
			}
			t.Errorf("  %s", line)
		}
	}
}

// referencedConfigJSON is legacyConfigJSON with every address that belongs to
// the declared host "nas" (and one that belongs to "db") written as a
// reference instead. The host declarations are untouched, so it describes the
// same network — which is the claim the next test measures.
const referencedConfigJSON = `{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "vpn_range": "10.100.0.0/24",
  "server_endpoint": "vpn.example.net:51820",
  "local_interface": "192.168.1.1",
  "last_local_iface": "eth0",
  "last_lan_cidr": "192.168.1.0/24",
  "haproxy_enabled": true,
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,
  "dnsmasq_enabled": true,
  "zones": [{"name": "example.net", "zone_id": "Z1"}],
  "hosts": [
    {"name": "nas", "ip": "192.168.1.160", "labels": {"role": "storage"}},
    {"name": "db", "ip": "192.168.1.60"}
  ],
  "services": [
    {
      "name": "app",
      "domains": ["app.example.net"],
      "internal_dns": {"ip": "@nas"},
      "proxy": {
        "backend": "@nas:8080",
        "health_check": {"path": "/health"},
        "deploy": {"next_backend": "@nas:8081", "token": "t", "active_slot": "a"}
      },
      "forwards": [
        {"proto": "udp", "port": 4433, "backend": "@nas:4433", "name": "webtransport"}
      ]
    },
    {
      "name": "files",
      "domains": ["files.example.net"],
      "proxy": {"backend": "@nas:445", "internal_only": true}
    }
  ],
  "local_dns_records": [
    {"name": "nas", "ip": "@nas", "comment": "the box in the cupboard"},
    {"name": "desktop", "ip": "192.168.1.90"}
  ],
  "exporters": [
    {"job": "node", "mode": "port", "port": 9100, "hosts": ["@nas", "@db"]},
    {"job": "pg", "mode": "static", "targets": ["@db:9187"], "labels": {"db": "main"}},
    {"job": "app", "mode": "service", "path": "/metrics"}
  ],
  "scrape_exclusions": ["192.168.1.90"]
}`

// TestReferencesRenderTheSameBytesAsLiterals is the other half of the
// compatibility claim: a reference is not a different mechanism with similar
// results, it resolves to the literal and everything downstream is unchanged.
// Same golden, so the two configs are proved equal to each other AND to the
// pre-change output at once.
func TestReferencesRenderTheSameBytesAsLiterals(t *testing.T) {
	cfg, err := config.LoadFromJSON([]byte(referencedConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "legacy_render.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := renderAll(t, cfg)
	if got != string(want) {
		t.Errorf("a referenced config rendered differently from the literal config it describes")
		for i, line := range diffLines(string(want), got) {
			if i > 40 {
				t.Errorf("  ... and more")
				break
			}
			t.Errorf("  %s", line)
		}
	}
}

// TestOneEditMovesEveryReference is the office move: the box gets a new
// address, one record changes, and every artifact follows. Nothing may still
// name the old address, which is the failure mode the literal config has — a
// copy nobody found.
func TestOneEditMovesEveryReference(t *testing.T) {
	const oldIP, newIP = "192.168.1.160", "192.168.1.211"

	cfg, err := config.LoadFromJSON([]byte(referencedConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// What `hz host show nas` lists, and what `hz host set` will move.
	refs := cfg.HostReferences("nas")
	if len(refs) < 6 {
		t.Fatalf("expected every nas-referencing record to be listed, got %d: %+v", len(refs), refs)
	}
	kinds := map[string]bool{}
	for _, r := range refs {
		kinds[r.Kind] = true
	}
	for _, want := range []string{
		config.HostRefKindServiceBackend,
		config.HostRefKindDeployNext,
		config.HostRefKindForward,
		config.HostRefKindLocalDNS,
		config.HostRefKindInternalDNS,
		config.HostRefKindExporterHost,
	} {
		if !kinds[want] {
			t.Errorf("host show missed the %q dependants", want)
		}
	}

	// The whole edit: one field, in one record.
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Name == "nas" {
			cfg.Hosts[i].IP = newIP
		}
	}

	out := renderAll(t, cfg)
	if strings.Contains(out, oldIP) {
		t.Errorf("the old address survives somewhere in the rendered artifacts:\n%s", linesContaining(out, oldIP))
	}
	for _, want := range []string{
		newIP + ":8080",            // haproxy backend
		newIP + ":8081",            // haproxy standby slot
		newIP + ":4433",            // iptables DNAT
		newIP + ":9100",            // prometheus node target
		"host-record=nas," + newIP, // dnsmasq answer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the rendered artifacts after the move", want)
		}
	}
}

// gatewayConfigJSON models what the live gateway actually looks like, which is
// not "records pointing at some other box" at all: 192.168.1.160 IS the
// gateway, and every occurrence is hz pointing at ITSELF — services' dnsmasq A
// records, backends of processes running on it, and their standby slots.
//
// %s is the address, so the same config can be rendered with the literal and
// with "@self" and the two compared.
const gatewayConfigJSON = `{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "vpn_range": "10.100.0.0/24",
  "server_endpoint": "vpn.example.net:51820",
  "local_interface": "192.168.1.160",
  "last_local_iface": "eth0",
  "last_lan_cidr": "192.168.1.0/24",
  "haproxy_enabled": true,
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,
  "dnsmasq_enabled": true,
  "zones": [{"name": "example.net", "zone_id": "Z1"}],
  "services": [
    {
      "name": "app",
      "domains": ["app.example.net"],
      "internal_dns": {"ip": "%[1]s"},
      "proxy": {
        "backend": "%[2]s:8080",
        "health_check": {"path": "/health"},
        "deploy": {"next_backend": "%[2]s:8081", "token": "t", "active_slot": "a"}
      }
    },
    {
      "name": "wiki",
      "domains": ["wiki.example.net"],
      "internal_dns": {"ip": "%[1]s"},
      "proxy": {"backend": "%[2]s:3000"}
    }
  ],
  "exporters": [
    {"job": "node", "mode": "port", "port": 9100, "hosts": ["%[1]s"]}
  ]
}`

// TestSelfReferenceRendersTheSameBytesAsTheGatewaysOwnLiteralAddress is the
// compatibility proof for the case that actually dominates the live config:
// rewriting the gateway's own address to @self must change nothing.
func TestSelfReferenceRendersTheSameBytesAsTheGatewaysOwnLiteralAddress(t *testing.T) {
	literal, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, "192.168.1.160", "192.168.1.160")))
	if err != nil {
		t.Fatalf("load literal: %v", err)
	}
	selfRef, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, "@self", "@self")))
	if err != nil {
		t.Fatalf("load @self: %v", err)
	}
	want, got := renderAll(t, literal), renderAll(t, selfRef)
	if want != got {
		t.Errorf("@self did not render what the gateway's own literal address renders")
		for i, line := range diffLines(want, got) {
			if i > 40 {
				t.Errorf("  ... and more")
				break
			}
			t.Errorf("  %s", line)
		}
	}
}

// TestTheGatewayMovesItself is the office move as it really is: hz's own
// address changes. With literals that is every occurrence, by hand; with
// @self it is the one field hz already keeps per instance.
func TestTheGatewayMovesItself(t *testing.T) {
	const oldIP, newIP = "192.168.1.160", "192.168.1.211"

	// The literal config is the problem being solved: it still names the old
	// address everywhere after the move, and hz cannot say where.
	literal, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, oldIP, oldIP)))
	if err != nil {
		t.Fatalf("load literal: %v", err)
	}
	literal.LocalInterface = newIP
	if out := renderAll(t, literal); !strings.Contains(out, oldIP) {
		t.Fatal("expected the literal config to still carry the old address — if it does not, this test no longer models the problem")
	}

	selfRef, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, "@self", "@self")))
	if err != nil {
		t.Fatalf("load @self: %v", err)
	}

	// `hz host show self`: what points at this gateway. Six records here —
	// two dnsmasq answers, two backends, one standby slot, and one exporter
	// host list.
	refs := selfRef.HostReferences(config.HostRefSelfName)
	if len(refs) != 6 {
		t.Fatalf("want 6 self-references, got %d: %+v", len(refs), refs)
	}

	// The whole edit. LocalInterface is per instance and never replicated, so
	// this is one field on one box.
	selfRef.LocalInterface = newIP

	out := renderAll(t, selfRef)
	if strings.Contains(out, oldIP) {
		t.Errorf("the gateway's old address survives:\n%s", linesContaining(out, oldIP))
	}
	for _, want := range []string{
		newIP + ":8080",
		newIP + ":8081",
		newIP + ":3000",
		"address=/app.example.net/" + newIP,
		"address=/wiki.example.net/" + newIP,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q after the gateway moved", want)
		}
	}
}

func linesContaining(s, needle string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			out = append(out, "  "+l)
		}
	}
	return strings.Join(out, "\n")
}

// diffLines is a line-level diff good enough to point at the first divergence.
func diffLines(want, got string) []string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	var out []string
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			out = append(out, fmt.Sprintf("line %d:\n    want %q\n    got  %q", i+1, wl, gl))
		}
	}
	return out
}

// --- adoption: the rewrite must change nothing --------------------------------

// TestAdoptionRendersByteIdenticallyToTheLiteralConfig is the whole safety
// claim of `hz host adopt`, measured rather than asserted.
//
// Adoption changes how a config is WRITTEN — 47 copies of an address become 47
// references — and must change nothing about what it PRODUCES. The config here
// is the legacy one, entirely literals, which is the state the live gateway is
// in; after adopting "nas" every record that carried 192.168.1.160 is written
// "@nas", and the bytes are compared against the SAME golden the pre-change
// tree generated. So adoption is proved equal to the literal config and to the
// output of the code that existed before references did, at once.
func TestAdoptionRendersByteIdenticallyToTheLiteralConfig(t *testing.T) {
	cfg, err := config.LoadFromJSON([]byte(legacyConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	plan, err := cfg.AdoptAddress("192.168.1.160")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if plan.Ref != "@nas" {
		t.Fatalf("adopted to %q; 192.168.1.160 is declared as nas and is not this instance's address", plan.Ref)
	}
	// If this rewrote nothing the byte comparison below would pass for the
	// wrong reason — a golden test of an untouched config.
	if plan.Written < 6 {
		t.Fatalf("adoption rewrote %d record(s); the legacy config carries the address in more than that: %+v", plan.Written, plan.Adopt)
	}

	want, err := os.ReadFile(filepath.Join("testdata", "legacy_render.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := renderAll(t, cfg)
	if got != string(want) {
		t.Errorf("adoption changed what the config renders — it may only change how the config is written")
		for i, line := range diffLines(string(want), got) {
			if i > 40 {
				t.Errorf("  ... and more")
				break
			}
			t.Errorf("  %s", line)
		}
	}
}

// TestAdoptionMakesTheDeclaredMoveOneEdit is the payoff: before adoption the
// literal config still names the old address everywhere after the box moves;
// after adoption the same move is one field.
func TestAdoptionMakesTheDeclaredMoveOneEdit(t *testing.T) {
	const oldIP, newIP = "192.168.1.160", "192.168.1.211"

	// The problem, so the test fails if it stops modelling it.
	before, err := config.LoadFromJSON([]byte(legacyConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := range before.Hosts {
		if before.Hosts[i].Name == "nas" {
			before.Hosts[i].IP = newIP
		}
	}
	if out := renderAll(t, before); !strings.Contains(out, oldIP) {
		t.Fatal("the literal config no longer carries the old address after the move — this test models nothing")
	}

	after, err := config.LoadFromJSON([]byte(legacyConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := after.AdoptAddress(oldIP); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	for i := range after.Hosts {
		if after.Hosts[i].Name == "nas" {
			after.Hosts[i].IP = newIP
		}
	}
	out := renderAll(t, after)
	if strings.Contains(out, oldIP) {
		t.Errorf("the old address survives after adopting and moving:\n%s", linesContaining(out, oldIP))
	}
	for _, want := range []string{
		newIP + ":8080",
		newIP + ":8081",
		newIP + ":4433",
		newIP + ":9100",
		"host-record=nas," + newIP,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q after the adopted host moved", want)
		}
	}
}

// TestAdoptingTheGatewaysOwnAddressRendersByteIdentically is the case the live
// config is actually in: 192.168.1.160 IS the gateway, and adoption must write
// @self — the spelling that is correct on a peer — without moving a byte.
//
// It renders the gateway config with its literal address, adopts it, and
// compares against the untouched literal render. Same comparison as above, on
// the shape that dominates the real config: internal_dns answers, the backends
// of processes on this box, and their standby slots.
func TestAdoptingTheGatewaysOwnAddressRendersByteIdentically(t *testing.T) {
	const gwIP = "192.168.1.160"

	literal, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, gwIP, gwIP)))
	if err != nil {
		t.Fatalf("load literal: %v", err)
	}
	want := renderAll(t, literal)

	adopted, err := config.LoadFromJSON([]byte(fmt.Sprintf(gatewayConfigJSON, gwIP, gwIP)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	plan, err := adopted.AdoptAddress(gwIP)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if plan.Ref != config.SelfRef {
		t.Fatalf("adopted the gateway's own address to %q, want %s", plan.Ref, config.SelfRef)
	}
	if plan.Written == 0 {
		t.Fatal("adoption rewrote nothing, so the comparison below proves nothing")
	}
	// local_interface still carries the address: it is what @self resolves to.
	if adopted.LocalInterface != gwIP {
		t.Errorf("local_interface was rewritten to %q; it is the declaration @self resolves to, and rewriting it is a cycle", adopted.LocalInterface)
	}

	if got := renderAll(t, adopted); got != want {
		t.Errorf("adopting the gateway's own address to @self changed what it renders")
		for i, line := range diffLines(want, got) {
			if i > 40 {
				t.Errorf("  ... and more")
				break
			}
			t.Errorf("  %s", line)
		}
	}

	// And the move is now one field, on this box only.
	adopted.LocalInterface = "192.168.1.211"
	out := renderAll(t, adopted)
	if strings.Contains(out, gwIP) {
		t.Errorf("the gateway's old address survives after the one-field move:\n%s", linesContaining(out, gwIP))
	}
}

// TestAdoptionCoversEveryRecordKindTheGoldenExercises pins the scope: the
// record kinds adoption rewrites are the kinds the golden config uses, so a
// kind quietly dropped from the walker shows up here rather than as a record
// nobody adopted.
func TestAdoptionCoversEveryRecordKindTheGoldenExercises(t *testing.T) {
	cfg, err := config.LoadFromJSON([]byte(legacyConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	plan, err := cfg.PlanAddressAdoption("192.168.1.160")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	kinds := map[string]int{}
	for _, a := range plan.Adopt {
		kinds[a.Kind]++
	}
	for _, want := range []string{
		config.HostRefKindServiceBackend,
		config.HostRefKindDeployNext,
		config.HostRefKindForward,
		config.HostRefKindLocalDNS,
		config.HostRefKindInternalDNS,
		config.HostRefKindExporterHost,
	} {
		if kinds[want] == 0 {
			t.Errorf("no %q record was adopted; the golden config has one", want)
		}
	}

	// The declaration itself is listed and refused, never adopted.
	refusedFields := map[string]bool{}
	for _, r := range plan.Refused {
		refusedFields[r.Field] = true
		if r.WhyNot == "" {
			t.Errorf("%s/%s was refused with no reason given", r.Kind, r.Field)
		}
	}
	if !refusedFields["hosts[0].ip"] {
		t.Error("the nas declaration's own ip was not listed as a refused occurrence")
	}
}
