package config

import (
	"strings"
	"testing"
)

func forwardTestConfig() *Config {
	return &Config{
		Zones:            []Zone{{Name: "example.com", ZoneID: "Z1"}},
		ListenAddr:       ":8080",
		HAProxyEnabled:   true,
		HAProxyHTTPPort:  80,
		HAProxyHTTPSPort: 443,
		ServerEndpoint:   "vpn.example.com:51821",
		LocalInterface:   "192.168.1.160",
		LastLanCIDR:      "192.168.1.0/24",
		Services: []Service{{
			Name:     "voice",
			Domains:  []string{"voice.example.com"},
			Forwards: []Forward{{Proto: "udp", Port: 5000, Backend: "192.168.1.50:5000"}},
		}},
	}
}

func sprinkForward() Forward {
	return Forward{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433", Name: "webtransport"}
}

func TestValidateForwards(t *testing.T) {
	with := func(mut func(*Forward)) []Forward {
		f := sprinkForward()
		mut(&f)
		return []Forward{f}
	}
	cases := []struct {
		name     string
		forwards []Forward
		wantErr  string // substring; "" = valid
	}{
		{"sprink", []Forward{sprinkForward()}, ""},
		{"same port other proto is a different forward", with(func(f *Forward) { f.Proto, f.Port = "tcp", 5000 }), ""},
		{"ssh tcp/22", with(func(f *Forward) { f.Proto, f.Port = "tcp", 22 }), "reserved for ssh"},
		{"ssh udp/22", with(func(f *Forward) { f.Port = 22 }), "reserved for ssh"},
		{"dns 53", with(func(f *Forward) { f.Port = 53 }), "reserved"},
		{"http 80", with(func(f *Forward) { f.Proto, f.Port = "tcp", 80 }), "reserved"},
		{"https 443", with(func(f *Forward) { f.Port = 443 }), "reserved"},
		{"horizon listen port", with(func(f *Forward) { f.Proto, f.Port = "tcp", 8080 }), "reserved for homelab-horizon"},
		{"wireguard endpoint port", with(func(f *Forward) { f.Port = 51821 }), "reserved for wireguard"},
		{"wireguard default port", with(func(f *Forward) { f.Port = 51820 }), "reserved for wireguard"},
		{"duplicate across services", with(func(f *Forward) { f.Port = 5000 }), `already forwarded by service "voice"`},
		{"duplicate within service", []Forward{sprinkForward(), sprinkForward()}, "forwarded twice"},
		{"bad proto", with(func(f *Forward) { f.Proto = "icmp" }), "proto"},
		{"uppercase proto is not normalised here", with(func(f *Forward) { f.Proto = "UDP" }), "proto"},
		{"port zero", with(func(f *Forward) { f.Port = 0 }), "1-65535"},
		{"port too high", with(func(f *Forward) { f.Port = 65536 }), "1-65535"},
		{"backend hostname", with(func(f *Forward) { f.Backend = "game.lan:4433" }), "IPv4"},
		{"backend without port", with(func(f *Forward) { f.Backend = "192.168.1.76" }), "IPv4"},
		{"backend port zero", with(func(f *Forward) { f.Backend = "192.168.1.76:0" }), "IPv4"},
		{"backend IPv6", with(func(f *Forward) { f.Backend = "[fd00::76]:4433" }), "IPv4"},
		{"backend loopback", with(func(f *Forward) { f.Backend = "127.0.0.1:4433" }), "loopback"},
		{"backend is the gateway", with(func(f *Forward) { f.Backend = "192.168.1.160:4433" }), "gateway itself"},
		{"backend outside LAN", with(func(f *Forward) { f.Backend = "10.100.0.5:4433" }), "outside the gateway's LAN"},
		{"multi-line name", with(func(f *Forward) { f.Name = "a\nb" }), "one line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := forwardTestConfig().ValidateForwards(c.forwards, "sprink")
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// Without a detected LAN CIDR the backend check cannot run, and a forward
// whose return path is unverifiable is refused rather than assumed fine.
func TestValidateForwardsNeedsLanCIDR(t *testing.T) {
	cfg := forwardTestConfig()
	cfg.LastLanCIDR = ""
	if err := cfg.ValidateForwards([]Forward{sprinkForward()}, "sprink"); err == nil || !strings.Contains(err.Error(), "LAN CIDR") {
		t.Fatalf("want LAN CIDR error, got %v", err)
	}
}

// A service re-validated under its own name must not collide with itself.
func TestValidateForwardsExcludesTheServiceBeingEdited(t *testing.T) {
	cfg := forwardTestConfig()
	if err := cfg.ValidateForwards(cfg.Services[0].Forwards, "voice"); err != nil {
		t.Fatalf("editing voice with its own forward: %v", err)
	}
}

func TestForwardReservedPortsFollowsConfig(t *testing.T) {
	cfg := forwardTestConfig()
	cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, cfg.HAProxyMetricsPort = 8081, 8444, 8405
	cfg.SetListenOverride("127.0.0.1:9443")
	got := cfg.ForwardReservedPorts()
	for _, p := range []int{22, 53, 80, 443, 8080, 9443, 8081, 8444, 8405, 51821, 51820} {
		if _, ok := got[p]; !ok {
			t.Errorf("port %d should be reserved, got %v", p, got)
		}
	}
}

func TestAddServiceValidatesForwards(t *testing.T) {
	cfg := forwardTestConfig()
	bad := Service{Name: "sprink", Domains: []string{"sprink.example.com"},
		Forwards: []Forward{{Proto: "tcp", Port: 22, Backend: "192.168.1.76:22"}}}
	if err := cfg.AddService(bad); err == nil {
		t.Fatal("AddService accepted a forward of port 22")
	}
	good := Service{Name: "sprink", Domains: []string{"sprink.example.com"}, Forwards: []Forward{sprinkForward()}}
	if err := cfg.AddService(good); err != nil {
		t.Fatalf("AddService rejected sprink: %v", err)
	}
}

func TestDeriveHostPortMapReservesForwardPorts(t *testing.T) {
	cfg := forwardTestConfig()
	cfg.Services = append(cfg.Services, Service{Name: "sprink", Domains: []string{"sprink.example.com"}, Forwards: []Forward{sprinkForward()}})
	m := cfg.DeriveHostPortMap()

	has := func(host, port, proto string) bool {
		for _, e := range m.Hosts[host] {
			if e.Port == port && e.Proto == proto && e.Forward {
				return true
			}
		}
		return false
	}
	if !has("192.168.1.160", "4433", "udp") {
		t.Errorf("gateway public port not reserved: %v", m.Hosts["192.168.1.160"])
	}
	if !has("192.168.1.76", "4433", "udp") {
		t.Errorf("backend port not reserved: %v", m.Hosts["192.168.1.76"])
	}
}
