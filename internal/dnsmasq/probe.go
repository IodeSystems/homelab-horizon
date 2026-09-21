package dnsmasq

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// This file observes and never changes: it reads the host's interfaces and
// asks resolvers questions. It is neither half of the render/apply seam —
// it needs no root, and it has no desired state — but it does need the
// machine, which is why it is not in render.go. forward.go and stats.go are
// the same category.

// LocalBindCheck reports whether localIP is owned by one of ifaces. If not,
// owningIface is the actual interface (if any) that holds the IP — useful for
// proposing a fix. ok is true when the IP is bound by a configured interface
// (or localIP is empty, in which case the check is skipped).
type LocalBindCheck struct {
	OK            bool
	LocalIP       string
	BoundIPs      []string // IPs the configured interfaces actually have
	OwningIface   string   // empty if no interface has localIP
	ConfiguredIfs []string // interfaces dnsmasq is configured to bind to
}

// CheckLocalBind verifies that localIP lives on one of ifaces. dnsmasq with
// bind-dynamic only listens on IPs that belong to its configured interfaces,
// so this catches the case where local_interface points at a NIC that isn't
// in dnsmasq_interfaces.
func CheckLocalBind(localIP string, ifaces []string) LocalBindCheck {
	res := LocalBindCheck{LocalIP: localIP, ConfiguredIfs: ifaces}
	if localIP == "" {
		res.OK = true
		return res
	}

	for _, name := range ifaces {
		ips := interfaceIPv4s(name)
		res.BoundIPs = append(res.BoundIPs, ips...)
		for _, ip := range ips {
			if ip == localIP {
				res.OK = true
			}
		}
	}
	if res.OK {
		return res
	}

	// Find which interface (if any) actually owns localIP so callers can
	// propose adding it to the dnsmasq interface list.
	all, err := net.Interfaces()
	if err == nil {
		for _, ni := range all {
			for _, ip := range interfaceIPv4s(ni.Name) {
				if ip == localIP {
					res.OwningIface = ni.Name
					return res
				}
			}
		}
	}
	return res
}

func interfaceIPv4s(name string) []string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok {
			if v4 := ipNet.IP.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	return out
}

// ResolveWith resolves a hostname against a specific DNS server
func ResolveWith(hostname, dnsServer string) (string, error) {
	if !strings.Contains(dnsServer, ":") {
		dnsServer = dnsServer + ":53"
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "udp", dnsServer)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ips, err := resolver.LookupIP(ctx, "ip4", hostname)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no A record found")
	}
	return ips[0].String(), nil
}

// ResolveAllWith resolves multiple hostnames in parallel against a DNS server
func ResolveAllWith(hostnames []string, dnsServer string) map[string]string {
	results := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, h := range hostnames {
		wg.Add(1)
		go func(hostname string) {
			defer wg.Done()
			ip, err := ResolveWith(hostname, dnsServer)
			mu.Lock()
			if err != nil {
				results[hostname] = ""
			} else {
				results[hostname] = ip
			}
			mu.Unlock()
		}(h)
	}

	wg.Wait()
	return results
}
