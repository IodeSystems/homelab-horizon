package integration

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/server"
)

// sprinkConfigJSON is the motivating deployment: sprink's HTTP stays behind
// HAProxy, and its WebTransport (QUIC over UDP) port — which HAProxy cannot
// carry — is a layer-4 forward on the gateway.
const sprinkConfigJSON = `{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "vpn_range": "10.100.0.0/24",
  "server_endpoint": "vpn.iodesystems.com:51820",
  "local_interface": "192.168.1.160",
  "last_local_iface": "enx00051b94b7cc",
  "last_lan_cidr": "192.168.1.0/24",
  "haproxy_enabled": true,
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,
  "zones": [{"name": "iodesystems.com", "zone_id": "Z1"}],
  "services": []
}`

// TestDryRunSprinkForward runs the sprink forward through config load,
// validation, a dry-run server, the port map and the generator.
func TestDryRunSprinkForward(t *testing.T) {
	cfg, err := config.LoadFromJSON([]byte(sprinkConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	sprink := config.Service{
		Name:        "sprink",
		Domains:     []string{"sprink.iodesystems.com"},
		InternalDNS: &config.InternalDNS{IP: "192.168.1.160"},
		Proxy:       &config.ProxyConfig{Backend: "192.168.1.76:20200"},
		Forwards: []config.Forward{
			{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433", Name: "webtransport"},
		},
	}

	// The unsafe variants are refused before anything is stored.
	for _, bad := range []config.Forward{
		{Proto: "tcp", Port: 22, Backend: "192.168.1.76:22"},
		{Proto: "tcp", Port: 443, Backend: "192.168.1.76:443"},
		{Proto: "udp", Port: 51820, Backend: "192.168.1.76:51820"},
	} {
		probe := sprink
		probe.Forwards = []config.Forward{bad}
		if err := cfg.AddService(probe); err == nil {
			t.Fatalf("AddService accepted %s/%d", bad.Proto, bad.Port)
		}
	}
	if err := cfg.AddService(sprink); err != nil {
		t.Fatalf("AddService(sprink): %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.GetService("sprink").Forwards; len(got) != 1 || got[0].Port != 4433 {
		t.Fatalf("forward did not survive save/load: %+v", got)
	}

	srv, err := server.NewWithConfig(reloaded, configPath, true, "test")
	if err != nil || srv == nil {
		t.Fatalf("dry-run server: %v", err)
	}

	// Port map: the gateway's public port and the backend's port are both taken.
	pm := reloaded.DeriveHostPortMap()
	for host, port := range map[string]string{"192.168.1.160": "4433", "192.168.1.76": "4433"} {
		ok := slices.ContainsFunc(pm.Hosts[host], func(e config.HostPortEntry) bool {
			return e.Port == port && e.Proto == "udp" && e.Forward
		})
		if !ok {
			t.Errorf("port map lacks udp/%s on %s: %+v", port, host, pm.Hosts[host])
		}
	}

	// Generator, fed the way the server's reconciler feeds it.
	expected := iptables.ExpectedRules(iptables.Inputs{
		WGInterface:   reloaded.WGInterface,
		OutIface:      reloaded.LastLocalIface,
		VPNRange:      reloaded.VPNRange,
		LanCIDR:       reloaded.LastLanCIDR,
		ServerWGIP:    "10.100.0.1",
		ListenPort:    "8080",
		HAProxyPorts:  reloaded.HAProxyJailPorts(),
		Forwards:      iptables.ForwardsFromConfig(reloaded),
		ReservedPorts: reloaded.ForwardReservedPorts(),
	})
	var got []string
	for _, r := range expected {
		s := r.String()
		if strings.Contains(s, "HZ-") {
			got = append(got, s)
		}
	}
	want := []string{
		"-t nat -A PREROUTING -m addrtype --dst-type LOCAL -j HZ-PREROUTING",
		"-t nat -A POSTROUTING -j HZ-POSTROUTING",
		"-t filter -A FORWARD -j HZ-FORWARD",
		"-t nat -A HZ-PREROUTING -p udp --dport 4433 -j DNAT --to-destination 192.168.1.76:4433",
		"-t nat -A HZ-POSTROUTING -d 192.168.1.76/32 -o enx00051b94b7cc -p udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE",
		"-t filter -A HZ-FORWARD -d 192.168.1.76/32 -i enx00051b94b7cc -p udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
		"-t filter -A HZ-FORWARD -s 192.168.1.76/32 -o enx00051b94b7cc -p udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sprink forward rules\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, r := range expected {
		if r.Chain == "INPUT" && strings.Contains(strings.Join(r.Args, " "), "4433") {
			t.Errorf("forward leaked into INPUT: %s", r)
		}
	}
}
