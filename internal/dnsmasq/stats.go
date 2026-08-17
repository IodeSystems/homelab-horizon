package dnsmasq

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// dnsmasq publishes its own counters as TXT records in the CHAOS class —
// `dig +short chaos txt hits.bind @127.0.0.1` and friends. That is the same
// source google/dnsmasq_exporter reads.
//
// hz queries them directly rather than recommending that exporter: it isn't
// packaged for Debian or Ubuntu, so hz could not install it through the same
// vetted allowlist it uses for everything else, and hz already owns dnsmasq,
// knows where it listens, and has a DNS library in the tree. One fewer moving
// part, and the numbers land in the /metrics hz already serves.

// statsTimeout bounds the whole collection. These queries hit a loopback
// resolver, so anything slow means dnsmasq is wedged — in which case reporting
// nothing promptly beats blocking a scrape.
const statsTimeout = 2 * time.Second

// Stats is one snapshot of dnsmasq's internal counters.
//
// The cache counters are monotonic since dnsmasq started, so they are
// counters, not gauges. Leases and CacheSize are point-in-time.
type Stats struct {
	CacheSize  float64 // configured cache entries
	Insertions float64 // entries added since start
	Evictions  float64 // entries evicted to make room
	Hits       float64 // queries answered from cache
	Misses     float64 // queries that had to go upstream
	Servers    []ServerStats
}

// ServerStats is per-upstream, from servers.bind. Failures rising against one
// upstream while its siblings are flat is the signal worth alerting on.
type ServerStats struct {
	Address       string
	QueriesSent   float64
	QueriesFailed float64
}

// bindCounters maps the CHAOS record name to where its value lands.
var bindCounters = []struct {
	record string
	assign func(*Stats, float64)
}{
	{"cachesize.bind", func(s *Stats, v float64) { s.CacheSize = v }},
	{"insertions.bind", func(s *Stats, v float64) { s.Insertions = v }},
	{"evictions.bind", func(s *Stats, v float64) { s.Evictions = v }},
	{"hits.bind", func(s *Stats, v float64) { s.Hits = v }},
	{"misses.bind", func(s *Stats, v float64) { s.Misses = v }},
}

// ReadStats queries dnsmasq's CHAOS counters at addr ("127.0.0.1:53").
//
// A failure on any single record is not fatal: dnsmasq versions differ in
// which they publish, and a partial answer is more useful than none. An error
// comes back only when nothing at all could be read, which is what "dnsmasq is
// not answering" should look like to the caller.
func ReadStats(addr string) (*Stats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()

	client := &dns.Client{Timeout: statsTimeout}
	stats := &Stats{}
	answered := 0

	for _, c := range bindCounters {
		v, err := queryCounter(ctx, client, addr, c.record)
		if err != nil {
			continue
		}
		c.assign(stats, v)
		answered++
	}

	if servers, err := queryServers(ctx, client, addr); err == nil {
		stats.Servers = servers
		answered++
	}

	if answered == 0 {
		return nil, fmt.Errorf("dnsmasq at %s answered no CHAOS counters", addr)
	}
	return stats, nil
}

// queryCounter reads one numeric CHAOS TXT record.
func queryCounter(ctx context.Context, c *dns.Client, addr, record string) (float64, error) {
	txt, err := queryTXT(ctx, c, addr, record)
	if err != nil {
		return 0, err
	}
	if len(txt) == 0 {
		return 0, fmt.Errorf("%s: empty answer", record)
	}
	return strconv.ParseFloat(strings.TrimSpace(txt[0]), 64)
}

// queryServers parses servers.bind, whose TXT strings look like
// "10.0.0.1#53 1234 5" — address, queries sent, queries failed.
func queryServers(ctx context.Context, c *dns.Client, addr string) ([]ServerStats, error) {
	txt, err := queryTXT(ctx, c, addr, "servers.bind")
	if err != nil {
		return nil, err
	}
	out := make([]ServerStats, 0, len(txt))
	for _, entry := range txt {
		fields := strings.Fields(entry)
		if len(fields) < 3 {
			continue
		}
		sent, err1 := strconv.ParseFloat(fields[1], 64)
		failed, err2 := strconv.ParseFloat(fields[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, ServerStats{Address: fields[0], QueriesSent: sent, QueriesFailed: failed})
	}
	return out, nil
}

// queryTXT issues one CHAOS-class TXT query and returns the strings.
func queryTXT(ctx context.Context, c *dns.Client, addr, name string) ([]string, error) {
	m := new(dns.Msg)
	m.Question = []dns.Question{{
		Name:   dns.Fqdn(name),
		Qtype:  dns.TypeTXT,
		Qclass: dns.ClassCHAOS,
	}}
	m.RecursionDesired = false

	resp, _, err := c.ExchangeContext(ctx, m, addr)
	if err != nil {
		return nil, err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("%s: rcode %s", name, dns.RcodeToString[resp.Rcode])
	}
	var out []string
	for _, rr := range resp.Answer {
		if t, ok := rr.(*dns.TXT); ok {
			out = append(out, t.Txt...)
		}
	}
	return out, nil
}

// StatsAddr is where hz asks dnsmasq for its counters. Loopback rather than
// the WireGuard address: the counters are the local daemon's, and asking over
// the tunnel address would fail whenever wg0 is down — which is exactly when
// someone is reading the dashboard.
func StatsAddr() string {
	return net.JoinHostPort("127.0.0.1", "53")
}

// Answers reports whether a resolver at addr actually answers queries.
//
// CheckLocalBind proves the configuration is coherent — that local_interface's
// IP sits on an interface dnsmasq is told to bind. This proves the socket is
// there and responding, which is a different claim: with bind-dynamic dnsmasq
// starts happily and picks up addresses as they appear, so a correct config can
// still be answering on nothing yet.
//
// Queries the CHAOS record dnsmasq always serves rather than a real name, so
// the result does not depend on upstream reachability or a populated cache. A
// forwarding resolver with no upstream is still a working resolver for the
// question being asked here.
func Answers(addr string) bool {
	if strings.TrimSpace(addr) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &dns.Client{Timeout: 2 * time.Second}
	if _, err := queryTXT(ctx, c, addr, "cachesize.bind"); err != nil {
		return false
	}
	return true
}
