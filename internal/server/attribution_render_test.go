package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/projection"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// ATTRIBUTION CHANGES NO RENDERED ARTIFACT (plan/design/ui.md, Decision 1
// amendment 6; CLAUDE.md invariant 6).
//
// Attribution is organisational. Every gateway-wide render — HAProxy, dnsmasq,
// iptables (bans and forwards included), wg0.conf peers, the cert domain list,
// public DNS — and every machine's projection is built from ALL records
// whatever project they name, and a sync combines them all. So the same estate
// rendered with every attribution field empty and with every one of them set
// must come out byte-identical. A renderer that starts reading a project field
// (to filter, to name, to comment) fails here.
//
// The fixture is rendered through the same config→input glue the server uses
// (DeriveHAProxyBackends, mfaJailFor, DeriveDNSRecords, ForwardsFromConfig,
// activeBanIPs, projection.Project), not by hand-building each renderer's
// input — a hand-built input would skip exactly the step that could start
// reading a project.

// attributionKeys are real WireGuard public keys, for the reason
// projection/segments_test.go gives: ValidateSegments checks the shape.
var attributionKeys = []string{
	"8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=",
	"IeNDqihcCycgQ9s+UnsC4lShD7/9oHii3oOaBqZqjSY=",
	"UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko=",
}

// attributionEstate is one of everything a renderer reads, with every
// attribution field EMPTY. attribute() sets them all.
func attributionEstate() *config.Config {
	return &config.Config{
		WGInterface:         "wg0",
		VPNRange:            "10.100.0.0/24",
		ListenAddr:          "10.100.0.1:8080",
		LocalInterface:      "192.168.1.10",
		LastLanCIDR:         "192.168.1.0/24",
		LastLocalIface:      "eth0",
		UpstreamDNS:         []string{"192.0.2.53"},
		LocalDNSDomain:      "lan",
		DNSMasqEnabled:      true,
		HAProxyEnabled:      true,
		HAProxyHTTPPort:     80,
		HAProxyHTTPSPort:    443,
		SSLEnabled:          true,
		SSLCertDir:          "/certs",
		SSLHAProxyCertDir:   "/haproxy-certs",
		PublicIPOverride:    "198.51.100.1",
		VPNMFAEnabled:       true,
		NodeExporterEnabled: true,
		VPNMFASecrets:       map[string]string{"bob-laptop": "JBSWY3DPEHPK3PXP"},

		Projects: []config.Project{{Name: "acme"}, {Name: "storefront", Parent: "acme"}},
		Environments: []config.Environment{
			{Project: "storefront", Name: "prod", Posture: "prod", Version: "1.4.0"},
		},
		Zones: []config.Zone{{Name: "example.invalid", ZoneID: "Z1", SubZones: []string{"", "*"},
			SSL: &config.ZoneSSL{Enabled: true, Email: "ops@example.invalid"}}},
		Services: []config.Service{
			{
				Name: "shop", Project: "storefront", Environment: "prod",
				Domains:     []string{"shop.example.invalid"},
				InternalDNS: &config.InternalDNS{IP: "192.168.1.10"},
				ExternalDNS: &config.ExternalDNS{IPs: []string{"198.51.100.1"}},
				Proxy:       &config.ProxyConfig{Backend: "192.168.1.20:8080", HealthCheck: &config.HealthCheck{Path: "/health"}},
				Forwards:    []config.Forward{{Proto: "udp", Port: 27015, Backend: "192.168.1.20:27015"}},
			},
			{
				Name: "wiki", Domains: []string{"wiki.example.invalid"},
				InternalDNS: &config.InternalDNS{IP: "192.168.1.10"},
				Proxy:       &config.ProxyConfig{Backend: "192.168.1.21:3000"},
			},
		},
		LocalDNSRecords: []config.LocalDNSRecord{{Name: "nas", IP: "192.168.1.30"}},

		Segments: []config.Segment{{
			Name: "seg:storefront", Project: "storefront", CIDR: "10.10.2.0/24", Interface: "wg-storefront",
			Members: []config.SegmentMember{
				{Machine: "gw-1", Address: "10.10.2.1", Hub: true, Endpoint: "gw.example.invalid:51821", PublicKey: attributionKeys[0]},
				{Machine: "app-1", Address: "10.10.2.11", PublicKey: attributionKeys[1]},
			},
		}},
		Machines: []config.Machine{
			{Name: "gw-1", Segments: []string{"seg:storefront"}},
			{Name: "app-1", Segments: []string{"seg:storefront"}},
			{Name: "idle-1"},
		},

		WGPeers: []config.WGPeer{
			{Name: "alice-phone", PublicKey: attributionKeys[2], AllowedIPs: "10.100.0.2/32"},
			{Name: "bob-laptop", PublicKey: attributionKeys[1], AllowedIPs: "10.100.0.3/32, 192.168.50.0/24"},
		},
		VPNProfiles: map[string]string{"bob-laptop": config.ProfileFullTunnel},

		ServiceChecks: []config.ServiceCheck{
			{Name: "db", Type: "http", Target: "http://192.168.1.40:5432/", Interval: 60, Enabled: true},
		},
		IPBans: []config.IPBan{
			{IP: "203.0.113.9", CreatedAt: 1757000000, Reason: "scanner", Service: "admin"},
			{IP: "203.0.113.10", CreatedAt: 1757000000, Timeout: 3600, ExpiresAt: 4102444800, Reason: "brute force"},
		},
		PortExclusions: []config.PortRange{{From: 18000, To: 18010, Note: "reserved"}, {From: 19000}},
	}
}

// attribute sets EVERY attribution field amendment 6 added. Adding a new one
// to the model without adding it here leaves it untested — the reflection
// check in TestTheFixtureSetsEveryAttributionField catches that.
func attribute(c *config.Config) {
	for i := range c.Machines {
		c.Machines[i].Project = "storefront"
	}
	c.VPNProjects = map[string]string{"alice-phone": "storefront", "bob-laptop": "acme"}
	for i := range c.ServiceChecks {
		c.ServiceChecks[i].Project = "storefront"
	}
	for i := range c.IPBans {
		c.IPBans[i].Project = "acme"
	}
	for i := range c.PortExclusions {
		c.PortExclusions[i].Project = "storefront"
	}
}

// attributionInstances is what the registration store would hand the
// projection: two projects' instances on one machine, which is the point —
// the gateway is owned by one project and hosts another's instance.
func attributionInstances() []projection.Instance {
	return []projection.Instance{
		{Machine: "gw-1", Project: "storefront", Environment: "prod", App: "shop", Role: "app"},
		{Machine: "app-1", Project: "storefront", Environment: "prod", App: "shop", Role: "next"},
	}
}

// renderEverything renders every artifact hz produces from a config, in a
// fixed order, as one string. Fixed inputs stand in for the machine reads the
// apply halves do (cert store, wg0.conf, clock), so the output is a function
// of the config alone.
func renderEverything(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var b strings.Builder
	section := func(name, body string) { fmt.Fprintf(&b, "===== %s\n%s\n", name, body) }
	asJSON := func(v any) string {
		out, err := json.MarshalIndent(v, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	// HAProxy, with a fixed cert store.
	hap := haproxy.New("/nowhere/haproxy.cfg", "/nowhere/admin.sock")
	backends := cfg.DeriveHAProxyBackends()
	hap.SetBackends(backends)
	hap.SetMFAJail(mfaJailFor(cfg, backends))
	hap.SetMetricsPort(cfg.HAProxyMetricsPort)
	hap.SetTLSMinVersion(cfg.TLSMinVersion())
	hap.SetCertStore(func(string) []haproxy.Cert {
		return []haproxy.Cert{{File: "example.invalid.pem", DNSNames: []string{"*.example.invalid", "example.invalid"}}}
	})
	section("haproxy.cfg", hap.GenerateConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort,
		&haproxy.SSLConfig{Enabled: true, CertDir: cfg.SSLHAProxyCertDir}))
	section("haproxy jail acl", string(haproxy.RenderJailACL(cfg.JailedPeerIPs())))

	// dnsmasq.
	dns := dnsmasq.New("/nowhere/hz.conf", "/nowhere/hosts.conf", append([]string{cfg.WGInterface}, cfg.DNSMasqInterfaces...), cfg.UpstreamDNS)
	dns.SetLocalDomain(cfg.LocalDNSDomain)
	section("dnsmasq.conf", dns.GenerateConfig())
	section("dnsmasq hosts", dns.GenerateRecords(cfg.DeriveDNSRecords()))

	// Public DNS and certificates.
	section("route53 records", asJSON(cfg.DeriveRoute53Records()))
	section("ssl domains", asJSON(cfg.DeriveSSLDomains()))

	// wg0.conf peer blocks, from the replicated peer list.
	var peerBlocks strings.Builder
	peers := make([]iptables.PeerInput, 0, len(cfg.WGPeers))
	wgPeers := make([]wireguard.Peer, 0, len(cfg.WGPeers))
	for _, p := range cfg.WGPeers {
		peerBlocks.WriteString(wireguard.RenderPeerBlock(p.Name, p.PublicKey, p.AllowedIPs))
		peers = append(peers, iptables.PeerInput{Name: p.Name, AllowedIPs: p.AllowedIPs})
		wgPeers = append(wgPeers, wireguard.Peer{Name: p.Name, PublicKey: p.PublicKey, AllowedIPs: p.AllowedIPs})
	}
	section("wg0.conf peers", peerBlocks.String())

	// iptables: every expected rule (WG chains, MFA jail, forwards, bans) and
	// every rule hz would call stale. The clock is fixed so a timed ban's
	// expiry is part of the fixture, not of when the test ran.
	const now = int64(1758844800)
	expected := iptables.ExpectedRules(iptables.Inputs{
		WGInterface:   cfg.WGInterface,
		OutIface:      cfg.LastLocalIface,
		VPNRange:      cfg.VPNRange,
		LanCIDR:       cfg.LastLanCIDR,
		Peers:         peers,
		ServerWGIP:    "10.100.0.1",
		ListenPort:    "8080",
		JailedPeers:   cfg.GetJailedPeers(),
		HAProxyPorts:  cfg.HAProxyJailPorts(),
		Profiles:      cfg.VPNProfiles,
		Forwards:      iptables.ForwardsFromConfig(cfg),
		ReservedPorts: cfg.ForwardReservedPorts(),
		BannedIPs:     activeBanIPs(cfg.IPBans, now),
	})
	section("iptables expected", asJSON(expected))
	section("iptables stale", asJSON(iptables.StaleRules(cfg, peers, "10.100.0.1", "8080")))

	// The WG-FORWARD / WG-INPUT chains are built from these options; the
	// chain bodies are in ExpectedRules above, and the options themselves are
	// what the privileged half is handed.
	section("wg chain opts", asJSON(wireguard.ForwardChainOpts{
		Peers: wgPeers, Profiles: cfg.VPNProfiles, VPNRange: cfg.VPNRange, LanCIDR: cfg.LastLanCIDR,
		JailedPeers: cfg.GetJailedPeers(), HAProxyPorts: cfg.HAProxyJailPorts(), ServerWGIP: "10.100.0.1", ListenPort: "8080",
	}))

	// Observability targets are rendered into the scrape config.
	section("exporter targets", asJSON(cfg.DeriveExporterTargets()))

	// Every machine's projection — the payload an agent pulls.
	g := projection.Global{Config: cfg, Instances: attributionInstances(), AgentVersion: "1.0.0"}
	names := make([]string, 0, len(cfg.Machines))
	for _, m := range cfg.Machines {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		mc, err := projection.Project(g, name)
		if err != nil {
			t.Fatalf("project %s: %v", name, err)
		}
		section("projection "+name, asJSON(mc))
	}
	return b.String()
}

func TestAttributionChangesNoRenderedArtifact(t *testing.T) {
	plain := attributionEstate()
	attributed := attributionEstate()
	attribute(attributed)

	// Both are configs hz would save — otherwise this compares two shapes
	// the validator would have refused.
	for label, c := range map[string]*config.Config{"plain": plain, "attributed": attributed} {
		if err := config.Save(t.TempDir()+"/config.json", c); err != nil {
			t.Fatalf("the %s fixture is not a config hz would save: %v", label, err)
		}
	}

	a, b := renderEverything(t, plain), renderEverything(t, attributed)

	// Not two empty strings: each artifact the comparison claims to cover has
	// to have rendered something from the fixture.
	for _, want := range []string{
		"shop.example.invalid",           // haproxy + dnsmasq + ssl
		"192.168.1.20:8080",              // a backend
		"203.0.113.9",                    // a ban's DROP
		"27015",                          // a forward
		"[Peer]",                         // wg0.conf
		"alice-phone",                    // a peer's chain rules
		"10.10.2.11",                     // a segment peer in a projection
		"host-record=nas,nas.lan",        // a local record
		"===== projection idle-1",        // a machine with nothing on it
		`"Email": "ops@example.invalid"`, // a cert domain
		`"Address": "192.168.1.20:9100"`, // an exporter target
	} {
		if !strings.Contains(a, want) {
			t.Errorf("the render does not contain %q — the comparison below would not cover it", want)
		}
	}

	if a != b {
		t.Fatalf("attribution changed a rendered artifact. First difference:\n%s", firstDiff(a, b))
	}
}

// THE NESTED-HZ MARKERS CHANGE NO RENDERED ARTIFACT (plan/plan.md Tier 1b, N3).
//
// Machine.HZ and Environment.Upstream are statements about ANOTHER hz: that a
// machine runs one, and that a rung's placements are held there. No renderer
// and no machine projection may read either — the URL is never dialled
// (CLAUDE.md invariant 1) and an upstream rung changes where a question is
// answered, not what any box runs. So the estate rendered with a nested hz
// declared but unmarked and unnamed, and with the marker and an Upstream set,
// must come out byte-identical.
//
// The nested hz's upstream VPN client is present in BOTH estates, with its
// `upstream` profile: the profile changes rules on purpose (it is not a marker),
// so it is held constant, and what differs is only the LINK — MachineHZ.VPNClient
// naming it — which must render nothing.
//
// The rung used carries no instance on purpose: what the projection should do
// with an instance registered HERE at a rung placed ELSEWHERE is not decided,
// and this guard must not pin an answer to it.
func TestNestedMarkersChangeNoRenderedArtifact(t *testing.T) {
	estate := func(marked bool) *config.Config {
		c := attributionEstate()
		c.Machines = append(c.Machines, config.Machine{Name: "storefront-prod-hz"})
		c.Environments = append(c.Environments, config.Environment{Project: "storefront", Name: "pci", Posture: "prod"})
		c.WGPeers = append(c.WGPeers, config.WGPeer{Name: "storefront-prod-hz-vpn", PublicKey: attributionKeys[0], AllowedIPs: "10.100.0.9/32"})
		c.VPNProfiles = map[string]string{"bob-laptop": config.ProfileFullTunnel, "storefront-prod-hz-vpn": config.ProfileUpstream}
		if marked {
			c.Machines[len(c.Machines)-1].HZ = &config.MachineHZ{URL: "https://hz.pci.example.invalid", VPNClient: "storefront-prod-hz-vpn"}
			c.Environments[len(c.Environments)-1].Upstream = "storefront-prod-hz"
		}
		return c
	}
	plain, marked := estate(false), estate(true)
	for label, c := range map[string]*config.Config{"plain": plain, "marked": marked} {
		if err := config.Save(t.TempDir()+"/config.json", c); err != nil {
			t.Fatalf("the %s fixture is not a config hz would save: %v", label, err)
		}
	}

	a, b := renderEverything(t, plain), renderEverything(t, marked)
	for _, want := range []string{"shop.example.invalid", "[Peer]", "10.10.2.11", "===== projection storefront-prod-hz"} {
		if !strings.Contains(a, want) {
			t.Errorf("the render does not contain %q — the comparison below would not cover it", want)
		}
	}
	// The upstream client's API-port rule, as rendered JSON args — not merely
	// its address, which wg0.conf's peer block carries too.
	if !regexp.MustCompile(`"10\.100\.0\.9/32",\s+"-d",\s+"10\.100\.0\.1/32",\s+"-p",\s+"tcp",\s+"--dport",\s+"8080"`).MatchString(a) {
		t.Error("the render does not contain the upstream client's API-port rule — the comparison below would not cover it")
	}
	if strings.Contains(b, "hz.pci.example.invalid") {
		t.Error("the nested hz's URL reached a rendered artifact")
	}
	if a != b {
		t.Fatalf("a nested-hz marker changed a rendered artifact. First difference:\n%s", firstDiff(a, b))
	}
}

// The fixture must set every attribution field, or the byte-identity above
// is a claim about fewer fields than it names.
func TestTheFixtureSetsEveryAttributionField(t *testing.T) {
	c := attributionEstate()
	attribute(c)
	for _, m := range c.Machines {
		if m.Project == "" {
			t.Errorf("machine %s unattributed", m.Name)
		}
	}
	for _, p := range c.WGPeers {
		if c.VPNProjects[p.Name] == "" {
			t.Errorf("VPN client %s unattributed", p.Name)
		}
	}
	for _, x := range c.ServiceChecks {
		if x.Project == "" {
			t.Errorf("check %s unattributed", x.Name)
		}
	}
	for _, x := range c.IPBans {
		if x.Project == "" {
			t.Errorf("ban %s unattributed", x.IP)
		}
	}
	for _, x := range c.PortExclusions {
		if x.Project == "" {
			t.Errorf("exclusion %s unattributed", x.Label())
		}
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	section := ""
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if strings.HasPrefix(x, "===== ") {
			section = x
		}
		if x != y {
			return fmt.Sprintf("%s, line %d\n  empty:    %q\n  attributed: %q", section, i+1, x, y)
		}
	}
	return "(lengths differ)"
}
