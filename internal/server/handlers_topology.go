package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

// --- config <-> apitypes mapping (plain data, field-for-field) ---------------

func hostDeclToAPI(h config.HostDecl) apitypes.HostDecl {
	return apitypes.HostDecl{Name: h.Name, IP: h.IP, Labels: h.Labels}
}

func hostDeclFromAPI(h apitypes.HostDecl) config.HostDecl {
	return config.HostDecl{Name: h.Name, IP: h.IP, Labels: h.Labels}
}

func exporterToAPI(e config.Exporter) apitypes.Exporter {
	return apitypes.Exporter{
		Job: e.Job, Mode: e.EffectiveMode(), Targets: e.Targets, Port: e.Port, Hosts: e.Hosts,
		Path: e.Path, Bearer: e.Bearer, Labels: e.Labels,
	}
}

func exporterFromAPI(e apitypes.Exporter) config.Exporter {
	return config.Exporter{
		Job: e.Job, Mode: e.Mode, Targets: e.Targets, Port: e.Port, Hosts: e.Hosts,
		Path: e.Path, Bearer: e.Bearer, Labels: e.Labels,
	}
}

// handleAPITopology returns the observability topology: declared hosts and
// exporters (for editing), plus the fully-expanded targets with liveness and the
// known-host population that "*" expands over. Admin-only.
func (s *Server) handleAPITopology(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.buildTopologyResp())
}

// buildTopologyResp assembles the topology read view from current config and the
// last exporter-probe results. Shared by GET /topology and the reprobe handler.
func (s *Server) buildTopologyResp() apitypes.TopologyResp {
	cfg := s.cfg()
	resp := apitypes.TopologyResp{
		Hosts:      make([]apitypes.HostDecl, 0, len(cfg.Hosts)),
		Exporters:  make([]apitypes.Exporter, 0, len(cfg.Exporters)),
		Targets:    []apitypes.ExporterTargetResp{},
		KnownHosts: cfg.DeriveKnownHostIPs(),
	}
	for _, h := range cfg.Hosts {
		resp.Hosts = append(resp.Hosts, hostDeclToAPI(h))
	}
	for _, e := range cfg.Exporters {
		resp.Exporters = append(resp.Exporters, exporterToAPI(e))
	}
	for _, t := range cfg.DeriveExporterTargets() {
		pr := s.exporterProbeFor(t.Job, t.Address, t.Path)
		resp.Targets = append(resp.Targets, apitypes.ExporterTargetResp{
			Job:     t.Job,
			Address: t.Address,
			Path:    pr.Path,
			Paths:   t.Paths,
			Labels:  t.Labels,
			Alive:   pr.Alive,
		})
	}
	return resp
}

// handleAPITopologyReprobe forces a synchronous exporter re-probe (rather than
// waiting for the 60s background loop) and returns the refreshed topology.
// Admin-only.
func (s *Server) handleAPITopologyReprobe(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	s.refreshExporterStatus(ctx)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.buildTopologyResp())
}

// handleAPITopologyHosts replaces the declared-host list (read-modify-write from
// the client, like the service editor). Admin-only.
func (s *Server) handleAPITopologyHosts(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.TopologyHostsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	hosts := make([]config.HostDecl, 0, len(req.Hosts))
	for _, h := range req.Hosts {
		if h.IP == "" {
			writeJSONError(w, http.StatusBadRequest, "each host requires an ip")
			return
		}
		hosts = append(hosts, hostDeclFromAPI(h))
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.Hosts = hosts }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	writeJSONOK(w)
}

// handleAPITopologyExporters replaces the exporter list. Admin-only.
func (s *Server) handleAPITopologyExporters(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.TopologyExportersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	exporters := make([]config.Exporter, 0, len(req.Exporters))
	for _, e := range req.Exporters {
		if e.Job == "" {
			writeJSONError(w, http.StatusBadRequest, "each exporter requires a job")
			return
		}
		exporters = append(exporters, exporterFromAPI(e))
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.Exporters = exporters }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	writeJSONOK(w)
}

// metricsPathCandidates is the ordered set the service metrics-path scan tries.
var metricsPathCandidates = []string{"/metrics", "/api/metrics"}

// handleAPIServiceScanMetrics discovers a service's metrics path by probing its
// backend slot(s) at the candidate paths in order; the first path any slot
// answers on is suggested. Admin-only.
func (s *Server) handleAPIServiceScanMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.ServiceScanMetricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	var svc *config.Service
	for i := range s.cfg().Services {
		if s.cfg().Services[i].Name == req.Name {
			svc = &s.cfg().Services[i]
			break
		}
	}
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, "service not found")
		return
	}
	if svc.Proxy == nil || svc.Proxy.Backend == "" {
		writeJSONError(w, http.StatusBadRequest, "service has no proxy backend to scan")
		return
	}

	// Slots to probe: single backend, or blue-green current+next.
	type slot struct{ name, addr string }
	slots := []slot{{"", svc.Proxy.Backend}}
	if svc.Proxy.Deploy != nil && svc.Proxy.Deploy.NextBackend != "" {
		slots = []slot{{"current", svc.Proxy.Backend}, {"next", svc.Proxy.Deploy.NextBackend}}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	resp := apitypes.ServiceScanMetricsResp{Name: svc.Name, Candidates: metricsPathCandidates}
	for _, path := range metricsPathCandidates {
		var slotResults []apitypes.ServiceScanSlot
		anyOK := false
		for _, sl := range slots {
			ok := s.metrics.Probe(ctx, integration.Target{Address: sl.addr, MetricsPath: path})
			anyOK = anyOK || ok
			slotResults = append(slotResults, apitypes.ServiceScanSlot{Slot: sl.name, Address: sl.addr, Path: path, OK: ok})
		}
		if anyOK {
			resp.SuggestedPath = path
			resp.Slots = slotResults
			break
		}
		// Keep the last attempt's detail if nothing ever responds.
		resp.Slots = slotResults
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// kickExporterStatus refreshes exporter liveness off the request path so a newly
// added target's status shows up without waiting for the 60s health loop.
func (s *Server) kickExporterStatus() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.refreshExporterStatus(ctx)
	}()
}
