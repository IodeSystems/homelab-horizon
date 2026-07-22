// Package integration auto-detects per-service observability integrations and
// serves their discovery configs.
//
// Pull-style integrations (Prometheus) are detected by probing each service and
// exposed as a scrape / service-discovery config that a central consumer pulls
// from hz at /integration/<tool>/...; hz runs nothing per-host for these.
// Push-style integrations (Loki via grafana-alloy) run a per-host agent that
// pushes to a central sink and are handled elsewhere.
//
// This package is config-agnostic: callers build []Target from their own config
// and hand them to a Detector. Compatibility is observed (the probe must pass),
// not merely declared.
package integration

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Target is one scrapeable endpoint — a single service slot.
type Target struct {
	Service     string // service name, e.g. "ragtag"
	Slot        string // "current" | "next" | "" (no slot model)
	Address     string // host:port of the backend
	MetricsPath string // e.g. "/metrics"
	Bearer      string // optional token, sent as "Authorization: Bearer <token>"
}

// key uniquely identifies a target across refreshes.
func (t Target) key() string { return t.Service + "|" + t.Slot }

// Detector probes candidate targets and caches the set that currently responds
// with valid Prometheus exposition. The healthy set is what the served scrape
// config exposes, so a service that stops responding drops out on the next
// refresh and a newly-deployed slot appears once it answers.
type Detector struct {
	client  *http.Client
	mu      sync.RWMutex
	healthy map[string]Target
}

// NewDetector returns a Detector with a short probe timeout (internal network).
func NewDetector() *Detector {
	return &Detector{
		client:  &http.Client{Timeout: 4 * time.Second},
		healthy: map[string]Target{},
	}
}

// Probe reports whether a target's metrics endpoint responds 200 with content
// that looks like Prometheus exposition. It is deliberately lenient on format
// (exporters vary) but strict on reachability.
func (d *Detector) Probe(ctx context.Context, t Target) bool {
	url := "http://" + t.Address + t.MetricsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	if t.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+t.Bearer)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	// Verify the body actually looks like Prometheus exposition — never trust
	// the status or Content-Type alone. A catchall/SPA that answers 200 with
	// HTML (or a plain-text "ok") must NOT be mistaken for a metrics endpoint;
	// this matters especially for multi-path resolution, where telling a real
	// /metrics from a catchall is the whole job.
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return looksLikeExposition(string(buf))
}

// metricSampleLine matches a single Prometheus exposition sample:
//
//	metric_name{label="v",...}? <value>
//
// where value is a float, NaN, or ±Inf. Anchored so a stray word won't match.
var metricSampleLine = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[^}]*\})?[ \t]+-?([0-9][0-9.eE+-]*|\+?Inf|NaN)([ \t]+[0-9]+)?$`)

// looksLikeExposition reports whether body is plausibly Prometheus text
// exposition: it carries HELP/TYPE markers or at least one metric-sample line,
// and is not obviously HTML/JSON. Deliberately lenient on exporter quirks but
// strict enough to reject catchall pages.
func looksLikeExposition(body string) bool {
	s := strings.TrimSpace(body)
	if s == "" {
		return false
	}
	if s[0] == '<' || s[0] == '{' || s[0] == '[' { // HTML / JSON
		return false
	}
	if strings.Contains(body, "# TYPE ") || strings.Contains(body, "# HELP ") {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if metricSampleLine.MatchString(line) {
			return true
		}
	}
	return false
}

// Refresh probes all candidates concurrently and replaces the healthy set with
// those that pass. Candidates that fail are dropped (no stale entries).
func (d *Detector) Refresh(ctx context.Context, candidates []Target) {
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	next := make(map[string]Target, len(candidates))
	for _, c := range candidates {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			if d.Probe(ctx, t) {
				mu.Lock()
				next[t.key()] = t
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()

	d.mu.Lock()
	d.healthy = next
	d.mu.Unlock()
}

// Healthy returns the currently-compatible targets, sorted by service then slot
// for stable output.
func (d *Detector) Healthy() []Target {
	d.mu.RLock()
	out := make([]Target, 0, len(d.healthy))
	for _, t := range d.healthy {
		out = append(out, t)
	}
	d.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}
