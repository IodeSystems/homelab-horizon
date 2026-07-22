package server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

// metricsCandidates builds the per-slot Prometheus targets from the service
// registry: any service with a (non-disabled) metrics integration and a proxy
// backend contributes one target per slot. Slot labels are set only when the
// service runs the blue-green two-slot model; single-backend services get an
// empty slot. The detector then probes these and keeps only the ones that
// actually respond (observed compatibility).
func (s *Server) metricsCandidates() []integration.Target {
	var out []integration.Target
	for _, svc := range s.cfg().Services {
		if svc.Integrations == nil || svc.Integrations.Metrics == nil {
			continue
		}
		m := svc.Integrations.Metrics
		if m.Disabled || svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}
		path, bearer := m.MetricsPath(), m.Bearer
		if svc.Proxy.Deploy != nil && svc.Proxy.Deploy.NextBackend != "" {
			out = append(out,
				integration.Target{Service: svc.Name, Slot: "current", Address: svc.Proxy.Backend, MetricsPath: path, Bearer: bearer},
				integration.Target{Service: svc.Name, Slot: "next", Address: svc.Proxy.Deploy.NextBackend, MetricsPath: path, Bearer: bearer},
			)
			continue
		}
		out = append(out, integration.Target{Service: svc.Name, Address: svc.Proxy.Backend, MetricsPath: path, Bearer: bearer})
	}
	return out
}

// refreshMetricsTargets re-probes the metrics candidates and updates the detector.
// Called from the health loop, so detection runs on the same 60s cadence. It also
// probes exporter targets for status — that result annotates the topology view but
// does NOT gate serving (exporters are always emitted; Prometheus owns up/down).
func (s *Server) refreshMetricsTargets() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.metrics.Refresh(ctx, s.metricsCandidates())
	s.refreshExporterStatus(ctx)
}

// refreshExporterStatus probes every configured exporter target concurrently and
// replaces the liveness map. Status-only: a dead target stays in the served
// scrape config so Prometheus can alert on up==0.
func (s *Server) refreshExporterStatus(ctx context.Context) {
	targets := s.cfg().DeriveExporterTargets()
	next := make(map[string]bool, len(targets))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, t := range targets {
		wg.Add(1)
		go func(t config.ExporterTarget) {
			defer wg.Done()
			alive := s.metrics.Probe(ctx, integration.Target{Address: t.Address, MetricsPath: t.Path, Bearer: t.Bearer})
			mu.Lock()
			next[t.Address] = alive
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	s.exporterMu.Lock()
	s.exporterAlive = next
	s.exporterMu.Unlock()
}

// exporterAliveFor returns the last probe result for an exporter target address
// (false if never probed).
func (s *Server) exporterAliveFor(addr string) bool {
	s.exporterMu.RLock()
	defer s.exporterMu.RUnlock()
	return s.exporterAlive[addr]
}

// scrapeJobs assembles the full set of Prometheus jobs hz serves: probed-healthy
// service-metrics jobs (reachable-only) followed by exporter jobs derived from
// the topology (always emitted — Prometheus owns up/down). Exporters sort after
// services and among themselves by job then address for stable output.
func (s *Server) scrapeJobs() []integration.ScrapeJob {
	jobs := integration.ServiceJobs(s.metrics.Healthy())
	jobs = append(jobs, s.exporterJobs()...)
	return jobs
}

// exporterJobs expands the configured exporters into ScrapeJobs, one job per
// exporter job_name, grouping that job's expanded targets. Target order follows
// DeriveExporterTargets (job, then address).
func (s *Server) exporterJobs() []integration.ScrapeJob {
	var jobs []integration.ScrapeJob
	idx := map[string]int{}
	for _, t := range s.cfg().DeriveExporterTargets() {
		i, ok := idx[t.Job]
		if !ok {
			i = len(jobs)
			idx[t.Job] = i
			jobs = append(jobs, integration.ScrapeJob{Name: t.Job, MetricsPath: t.Path, Bearer: t.Bearer})
		}
		jobs[i].Targets = append(jobs[i].Targets, integration.ScrapeTarget{Address: t.Address, Labels: t.Labels})
	}
	return jobs
}

// handleIntegrationPromScrape serves a Prometheus scrape_configs document for all
// currently-compatible services plus configured exporters. Network-restricted
// (local/VPN/admin).
func (s *Server) handleIntegrationPromScrape(w http.ResponseWriter, r *http.Request) {
	if !s.isLocalOrAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(integration.ScrapeYAML(s.scrapeJobs())))
}

// handleIntegrationPromTargets serves the Prometheus http_sd_config JSON for all
// currently-compatible services plus configured exporters. Network-restricted
// (local/VPN/admin).
func (s *Server) handleIntegrationPromTargets(w http.ResponseWriter, r *http.Request) {
	if !s.isLocalOrAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	body, err := integration.HTTPSDTargets(s.scrapeJobs())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "render error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// isLocalOrAdmin allows a request from the internal network (loopback, the VPN
// range, a trusted proxy, or an RFC1918 LAN address) or from an authenticated
// admin. This is the "no per-service config" network restriction: a central
// Prometheus on the LAN/VPN can pull the discovery config without a token, but it
// is never exposed to the public internet.
func (s *Server) isLocalOrAdmin(r *http.Request) bool {
	if s.isAdmin(r) {
		return true
	}
	ip := s.getClientIP(r)
	if ip == "" {
		return false
	}
	if s.isTrustedProxy(ip) || s.isInVPNRange(ip) {
		return true
	}
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsPrivate()
}
