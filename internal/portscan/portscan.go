// Package portscan reports which TCP ports on a host actually have something
// listening.
//
// hz's port map is derived from its own configuration: service backends, the
// proxy's own ports, WireGuard, dnsmasq. That answers "what has hz been told
// about", which is not the same question as "what is in use" — and the
// allocator was answering the first while being asked the second. It handed
// out a port a neighbouring daemon already held, the service that took the
// advice failed to bind, and systemd restart-looped it.
//
// So the suggestion is checked against the host before it is offered. A
// connect is the only thing that settles it from another machine: hz runs on
// a different box from most backends, so there is no /proc to read.
package portscan

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// DefaultTimeout is how long one port gets to answer. Short: these are hosts
// on the same LAN, and a scan that takes a minute is one nobody waits for.
const DefaultTimeout = 300 * time.Millisecond

// DefaultConcurrency bounds the dials in flight. High enough to make a
// hundred ports quick, low enough not to look like a flood to the host.
const DefaultConcurrency = 64

// MaxPorts caps one scan. A request for more is a mistake or an attempt to
// make hz do somebody's scanning for them.
const MaxPorts = 1024

// Observe reports which of the given ports accept a TCP connection.
//
// A refused connection means nothing is listening, which is the answer we
// want. A timeout is ambiguous — a filtered port looks the same as a silent
// one — and is reported as not-open, because treating unreachable ports as
// used would make a firewalled host look entirely full.
func Observe(ctx context.Context, host string, ports []int) map[int]bool {
	if len(ports) > MaxPorts {
		ports = ports[:MaxPorts]
	}
	open := make(map[int]bool, len(ports))

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, DefaultConcurrency)

	for _, p := range ports {
		select {
		case <-ctx.Done():
			return open
		default:
		}

		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			d := net.Dialer{Timeout: DefaultTimeout}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				return
			}
			_ = conn.Close()

			mu.Lock()
			open[port] = true
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return open
}

// Range is the ports from..to inclusive, bounded by MaxPorts.
func Range(from, to int) []int {
	if from < 1 {
		from = 1
	}
	if to > 65535 {
		to = 65535
	}
	if to < from {
		return nil
	}
	if to-from+1 > MaxPorts {
		to = from + MaxPorts - 1
	}
	out := make([]int, 0, to-from+1)
	for p := from; p <= to; p++ {
		out = append(out, p)
	}
	return out
}

// Sorted returns the open ports in order, for stable output.
func Sorted(open map[int]bool) []int {
	out := make([]int, 0, len(open))
	for p := range open {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}
