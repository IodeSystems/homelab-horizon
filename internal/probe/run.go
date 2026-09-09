package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// certWarningDays is how close to expiry a certificate starts warning. The
// same seven days the local TLS check uses: Let's Encrypt renews at 30 days
// left, so a week out means renewal is broken rather than pending.
const certWarningDays = 7

// defaultTimeout applies when the target set does not set one.
const defaultTimeout = 10 * time.Second

// Run executes every probe the target asks for and returns one result each.
// It never returns an error: a failed probe is a result, which is the whole
// point of running it.
func Run(ctx context.Context, ts TargetSet, t Target) []Result {
	timeout := defaultTimeout
	if ts.Timeout > 0 {
		timeout = time.Duration(ts.Timeout) * time.Second
	}

	kinds := t.Kinds
	if len(kinds) == 0 {
		kinds = []string{KindDNS, KindHTTPS}
	}

	out := make([]Result, 0, len(kinds))
	for _, kind := range kinds {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		switch kind {
		case KindDNS:
			out = append(out, probeDNS(ctx, ts, t))
		case KindHTTPS:
			out = append(out, probeHTTPS(ctx, t, timeout))
		case KindTCP:
			out = append(out, probeTCP(ctx, t))
		default:
			out = append(out, result(t, kind, 0, StatusFailed, "", fmt.Errorf("unknown probe kind %q", kind)))
		}
		cancel()
	}
	return out
}

// result builds a Result, deriving the status from the error when one is set.
func result(t Target, kind string, latency time.Duration, status, detail string, err error) Result {
	r := Result{
		Target:    t.Name,
		Host:      t.Host,
		Kind:      kind,
		At:        time.Now().UTC(),
		Status:    status,
		LatencyMS: latency.Milliseconds(),
		Detail:    detail,
	}
	if err != nil {
		r.Error = err.Error()
		if status == StatusOK {
			r.Status = StatusFailed
		}
	}
	return r
}

// resolverFor builds the resolver the set asks for. Named resolvers are what
// make a DNS answer meaningful from outside: the agent's own system resolver
// may be a caching forwarder with its own stale view, so an operator who
// cares which resolvers agree lists them.
func resolverFor(ts TargetSet, i int) *net.Resolver {
	if len(ts.Resolvers) == 0 {
		return net.DefaultResolver
	}
	addr := ts.Resolvers[i%len(ts.Resolvers)]
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, network, addr)
		},
	}
}

// probeDNS resolves the target and compares the answer to what hz expects.
//
// An answer that contains none of the expected addresses is a failure: the
// name points somewhere else, which from outside is indistinguishable from
// the service being gone. An answer that is missing some of them warns —
// partial propagation is real and usually resolves itself, but it is also
// what a half-finished record update looks like.
func probeDNS(ctx context.Context, ts TargetSet, t Target) Result {
	start := time.Now()
	var (
		addrs []string
		err   error
	)
	// Each resolver in turn until one answers, so a single dead resolver in
	// the list does not read as a DNS outage.
	for i := range max(len(ts.Resolvers), 1) {
		addrs, err = resolverFor(ts, i).LookupHost(ctx, t.Host)
		if err == nil {
			break
		}
	}
	latency := time.Since(start)
	if err != nil {
		return result(t, KindDNS, latency, StatusFailed, "", err)
	}
	sort.Strings(addrs)
	status, detail, verdictErr := dnsVerdict(t, addrs)
	return result(t, KindDNS, latency, status, detail, verdictErr)
}

// dnsVerdict judges an answer against what hz expects. Split out from the
// lookup so the classification is testable without a resolver.
func dnsVerdict(t Target, addrs []string) (status, detail string, err error) {
	detail = strings.Join(addrs, ", ")
	if len(t.ExpectIPs) == 0 {
		return StatusOK, detail, nil
	}

	got := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		got[a] = true
	}
	var missing []string
	hits := 0
	for _, want := range t.ExpectIPs {
		if got[want] {
			hits++
		} else {
			missing = append(missing, want)
		}
	}
	switch {
	case hits == 0:
		return StatusFailed, detail,
			fmt.Errorf("resolved to %s, expected %s", detail, strings.Join(t.ExpectIPs, ", "))
	case len(missing) > 0:
		return StatusWarning, detail,
			fmt.Errorf("missing expected address %s", strings.Join(missing, ", "))
	default:
		return StatusOK, detail, nil
	}
}

// hostPort is the target's address, defaulting the port.
func hostPort(t Target, def int) string {
	port := t.Port
	if port == 0 {
		port = def
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// probeHTTPS completes a real request, not just a handshake: a certificate
// can be perfect while HAProxy has no backend behind it, and from outside
// those are the same outage.
func probeHTTPS(ctx context.Context, t Target, timeout time.Duration) Result {
	path := t.Path
	if path == "" {
		path = "/"
	}
	port := t.Port
	if port == 0 {
		port = 443
	}
	url := "https://" + t.Host
	if port != 443 {
		url += ":" + strconv.Itoa(port)
	}
	url += path

	var state *tls.ConnectionState
	client := &http.Client{
		Timeout: timeout,
		// The redirect chain is somebody else's health, not this target's.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result(t, KindHTTPS, 0, StatusFailed, "", err)
	}
	req.Header.Set("User-Agent", "hz-probe")
	resp, err := client.Do(req)
	if err != nil {
		return result(t, KindHTTPS, time.Since(start), StatusFailed, "", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain, bounded: the body is not the signal, but leaving it unread
	// breaks connection reuse and skews the next round's latency.
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	latency := time.Since(start)
	state = resp.TLS

	detail := strconv.Itoa(resp.StatusCode)
	if state != nil && len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		left := time.Until(leaf.NotAfter)
		detail += fmt.Sprintf(", cert %dd left", int(left.Hours()/24))
		if left < certWarningDays*24*time.Hour {
			return result(t, KindHTTPS, latency, StatusWarning, detail,
				fmt.Errorf("certificate expires %s", leaf.NotAfter.Format(time.RFC3339)))
		}
	}

	// 5xx is the edge admitting it is broken. 4xx is not: a 401 or 404 on the
	// probe path still proves DNS, TLS and the proxy all worked.
	if resp.StatusCode >= 500 {
		return result(t, KindHTTPS, latency, StatusFailed, detail,
			fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	return result(t, KindHTTPS, latency, StatusOK, detail, nil)
}

// probeTCP is the bare reachability question, for a port with no HTTP on it.
func probeTCP(ctx context.Context, t Target) Result {
	addr := hostPort(t, 443)
	start := time.Now()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return result(t, KindTCP, latency, StatusFailed, addr, err)
	}
	_ = conn.Close()
	return result(t, KindTCP, latency, StatusOK, addr, nil)
}
