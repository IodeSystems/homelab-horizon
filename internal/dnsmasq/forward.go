package dnsmasq

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Does the forwarder forward?
//
// Answers() proves a socket replies, and every name hz resolves elsewhere is
// one dnsmasq answers itself from an `address=` line. Both stay green when the
// upstream servers are unreachable, when `no-resolv` is set with no `server=`
// left, or when the box has no route out — and in that state a VPN client
// resolves every internal name perfectly and nothing else at all. This is the
// check for that, and it deliberately asks for a name dnsmasq cannot answer
// from its own config.

// DefaultProbeName is the name resolved to prove forwarding works. example.com
// is reserved by IANA (RFC 2606) and has stable A records, so it is a question
// with an answer that no operator has to maintain.
const DefaultProbeName = "example.com"

// ForwardResult is one address's answer to "can you resolve a name you do not
// serve yourself".
type ForwardResult struct {
	Addr string   `json:"addr"`           // "10.100.0.1:53"
	Name string   `json:"name"`           // the probe name
	OK   bool     `json:"ok"`             // NOERROR with at least one A record
	IPs  []string `json:"ips,omitempty"`  // what came back, for the dashboard
	Err  string   `json:"err,omitempty"`  // why not, when OK is false
	Code string   `json:"code,omitempty"` // DNS rcode when the server said no
}

// Forwards asks the resolver at addr for name's A records.
//
// A timeout and a SERVFAIL are both failures here, but they are different
// stories — a timeout usually means nothing is listening, SERVFAIL means
// dnsmasq tried its upstreams and got nowhere — so the reason is kept.
func Forwards(addr, name string) ForwardResult {
	if strings.TrimSpace(name) == "" {
		name = DefaultProbeName
	}
	res := ForwardResult{Addr: addr, Name: name}
	if strings.TrimSpace(addr) == "" {
		res.Err = "no address to query"
		return res
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "53")
		res.Addr = addr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	m.RecursionDesired = true

	c := &dns.Client{Timeout: 3 * time.Second}
	resp, _, err := c.ExchangeContext(ctx, m, addr)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	if resp.Rcode != dns.RcodeSuccess {
		res.Code = dns.RcodeToString[resp.Rcode]
		res.Err = "resolver answered " + res.Code
		return res
	}
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			res.IPs = append(res.IPs, a.A.String())
		}
	}
	if len(res.IPs) == 0 {
		res.Err = "answered with no A record — the name resolves to nothing"
		return res
	}
	res.OK = true
	return res
}

// ProbeConflict reports why probeName cannot prove anything against this set of
// locally served domains, or "".
//
// A probe name that dnsmasq serves from an `address=` line is answered from the
// config and never touches an upstream, so the check would pass on a box with
// no internet at all. Better to say the check is useless than to report a green
// row that means nothing.
func ProbeConflict(probeName string, servedDomains []string) string {
	probe := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(probeName), "."))
	if probe == "" {
		return ""
	}
	for _, d := range servedDomains {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		if probe == d || strings.HasSuffix(probe, "."+d) {
			return fmt.Sprintf("probe name %q is served locally (matches %q), so it never reaches an upstream", probeName, d)
		}
	}
	return ""
}
