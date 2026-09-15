package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Forward is a layer-4 port forward on the gateway: packets addressed to the
// gateway on Proto/Port are DNATed to Backend, a host on the gateway's LAN.
//
// It exists for traffic HAProxy cannot carry — UDP, and QUIC/WebTransport in
// particular. HAProxy stays the path for HTTP; a forward is a plain NAT hop
// with no TLS termination, no vhost routing and no health check.
//
// The rules live in horizon-owned chains (see internal/iptables) and are
// installed by the iptables reconciler, so a forward is applied on the next
// reconcile tick, not by Sync.
type Forward struct {
	Proto   string `json:"proto"`   // "udp" or "tcp"
	Port    int    `json:"port"`    // public port on the gateway
	Backend string `json:"backend"` // "ip:port" on the LAN

	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// BackendIPPort splits Backend into its IP and port. ok is false for anything
// that is not an IPv4 literal with a port in 1-65535: a DNAT target has to be
// an address, and horizon only manages the IPv4 table.
func (f Forward) BackendIPPort() (ip string, port int, ok bool) {
	host, p, err := net.SplitHostPort(f.Backend)
	if err != nil {
		return "", 0, false
	}
	parsed := net.ParseIP(host)
	if parsed == nil || parsed.To4() == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, false
	}
	return parsed.To4().String(), n, true
}

// forwardAlwaysReserved are ports a forward may never claim, whatever the
// config says. The DNAT matches every packet addressed to the gateway on that
// port, so forwarding one of these takes it away from the gateway itself — for
// 22 that is the SSH session the gateway is administered over.
var forwardAlwaysReserved = map[int]string{
	22:  "ssh",
	53:  "dns",
	80:  "http",
	443: "https",
}

// ForwardAlwaysReserved reports whether p is one of the ports no forward may
// use regardless of config. The iptables generator checks this itself, so a
// config that skipped validation still cannot produce such a rule.
func ForwardAlwaysReserved(p int) (string, bool) {
	why, ok := forwardAlwaysReserved[p]
	return why, ok
}

// ForwardReservedPorts returns every gateway port a forward may not claim,
// mapped to what owns it: the fixed set above plus horizon's own listeners
// (admin HTTP, HAProxy binds and metrics, WireGuard).
//
// Reserved regardless of protocol. Forwarding udp/443 while HAProxy holds
// tcp/443 is technically possible, but HTTP/3 puts both on one number and the
// safe rule is the one that needs no explanation.
func (c *Config) ForwardReservedPorts() map[int]string {
	out := make(map[int]string, len(forwardAlwaysReserved)+6)
	for p, why := range forwardAlwaysReserved {
		out[p] = why
	}
	addAddrPort := func(addr, why string) {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				out[n] = why
			}
		}
	}
	addAddrPort(c.ListenAddr, "homelab-horizon")
	addAddrPort(c.EffectiveListenAddr(), "homelab-horizon")
	for _, p := range []int{c.HAProxyHTTPPort, c.HAProxyHTTPSPort} {
		if p > 0 {
			out[p] = "haproxy"
		}
	}
	if c.HAProxyMetricsPort > 0 {
		out[c.HAProxyMetricsPort] = "haproxy metrics"
	}
	// WireGuard: the endpoint's port, and the default in case the endpoint
	// is unset or advertises a different port than wg0 listens on.
	addAddrPort(c.ServerEndpoint, "wireguard")
	if _, taken := out[51820]; !taken {
		out[51820] = "wireguard"
	}
	return out
}

// ValidateForwards checks a service's forwards against each other, against
// every other service's forwards, and against the gateway's own ports.
//
// exclude names the service being replaced (the original name on edit), so a
// service does not collide with its previous self.
func (c *Config) ValidateForwards(forwards []Forward, exclude string) error {
	reserved := c.ForwardReservedPorts()

	taken := map[string]string{} // "udp/4433" -> owning service
	for _, svc := range c.Services {
		if svc.Name == exclude {
			continue
		}
		for _, f := range svc.Forwards {
			taken[forwardKey(f.Proto, f.Port)] = svc.Name
		}
	}

	var lan *net.IPNet
	if c.LastLanCIDR != "" {
		_, lan, _ = net.ParseCIDR(c.LastLanCIDR)
	}

	seen := map[string]bool{}
	for i, f := range forwards {
		field := fmt.Sprintf("forwards[%d]", i)

		if f.Proto != "udp" && f.Proto != "tcp" {
			return &ValidationError{Field: field + ".proto", Message: `must be "udp" or "tcp"`}
		}
		if f.Port < 1 || f.Port > 65535 {
			return &ValidationError{Field: field + ".port", Message: "must be 1-65535"}
		}
		if why, ok := reserved[f.Port]; ok {
			return &ValidationError{Field: field + ".port", Message: fmt.Sprintf(
				"port %d is reserved for %s on the gateway; forwarding it would take it from the gateway itself", f.Port, why)}
		}

		key := forwardKey(f.Proto, f.Port)
		if seen[key] {
			return &ValidationError{Field: field, Message: fmt.Sprintf("%s is forwarded twice in this service", key)}
		}
		seen[key] = true
		if owner, ok := taken[key]; ok {
			return &ValidationError{Field: field, Message: fmt.Sprintf("%s is already forwarded by service %q", key, owner)}
		}

		ip, _, ok := f.BackendIPPort()
		if !ok {
			return &ValidationError{Field: field + ".backend", Message: "must be an IPv4 address and port, e.g. 192.168.1.76:4433"}
		}
		parsed := net.ParseIP(ip)
		if parsed.IsLoopback() || parsed.IsUnspecified() || parsed.IsMulticast() {
			return &ValidationError{Field: field + ".backend", Message: "must be a LAN host, not a loopback, unspecified or multicast address"}
		}
		if c.LocalInterface != "" && ip == c.LocalInterface {
			return &ValidationError{Field: field + ".backend", Message: "points at the gateway itself; a forward is for another host on the LAN"}
		}
		// The rules masquerade and accept on the default-route interface only,
		// so a backend reached any other way (a VPN peer, a docker bridge) would
		// get a DNAT and no working return path.
		if lan == nil {
			return &ValidationError{Field: field + ".backend", Message: "the gateway's LAN CIDR is not known yet (it is detected on the first iptables reconcile); retry in a minute"}
		}
		if !lan.Contains(parsed) {
			return &ValidationError{Field: field + ".backend", Message: fmt.Sprintf(
				"%s is outside the gateway's LAN %s; the forward's return path only works for hosts on that LAN", ip, lan)}
		}
		if strings.ContainsAny(f.Name+f.Description, "\n\r") {
			return &ValidationError{Field: field + ".name", Message: "name and description must be one line"}
		}
	}
	return nil
}

func forwardKey(proto string, port int) string {
	return proto + "/" + strconv.Itoa(port)
}
