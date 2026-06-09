package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"homelab-horizon/internal/integration"
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
// Called from the health loop, so detection runs on the same 60s cadence.
func (s *Server) refreshMetricsTargets() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.metrics.Refresh(ctx, s.metricsCandidates())
}

// handleIntegrationPromScrape serves a Prometheus scrape_configs document for all
// currently-compatible services. Network-restricted (local/VPN/admin).
func (s *Server) handleIntegrationPromScrape(w http.ResponseWriter, r *http.Request) {
	if !s.isLocalOrAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(integration.ScrapeYAML(s.metrics.Healthy())))
}

// handleIntegrationPromTargets serves the Prometheus http_sd_config JSON for all
// currently-compatible services. Network-restricted (local/VPN/admin).
func (s *Server) handleIntegrationPromTargets(w http.ResponseWriter, r *http.Request) {
	if !s.isLocalOrAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	body, err := integration.HTTPSDTargets(s.metrics.Healthy())
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
