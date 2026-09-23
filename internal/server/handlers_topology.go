package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
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
	exclusions := cfg.ScrapeExclusions
	if exclusions == nil {
		exclusions = []string{}
	}
	resp := apitypes.TopologyResp{
		Hosts:            make([]apitypes.HostDecl, 0, len(cfg.Hosts)),
		SelfHost:         hostDeclToAPI(cfg.SelfHostDecl()),
		Exporters:        make([]apitypes.Exporter, 0, len(cfg.Exporters)),
		Targets:          []apitypes.ExporterTargetResp{},
		KnownHosts:       cfg.DeriveKnownHostIPs(),
		ScrapeExclusions: exclusions,
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
	// A whole-list replace is how a removal AND a rename both arrive, and
	// neither says so. Checking the proposed list against what the config
	// references refuses either while something still resolves through the
	// name, and names every dependant — the shape `hz project rm` uses.
	if err := s.cfg().ValidateHostsAgainst(hosts); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.Hosts = hosts }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	writeJSONOK(w)
}

// handleAPITopologyHostShow returns one declared host and every record that
// resolves through it, grouped nowhere — the CLI groups. It is the question hz
// could not answer before references existed: what points at this box, and so
// what breaks if it moves. Admin-only.
func (s *Server) handleAPITopologyHostShow(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	cfg := s.cfg()
	// "@self" is not declared and never will be, but it is the reference an
	// operator most needs to interrogate: it is how hz points at itself, and
	// "what points at this gateway" is the question a move actually asks.
	var h *config.HostDecl
	if name == config.HostRefSelfName {
		self := cfg.SelfHostDecl()
		h = &self
	} else if h = cfg.HostDeclByName(name); h == nil {
		writeJSONError(w, http.StatusNotFound, "host not found: "+name)
		return
	}
	resp := apitypes.HostShowResp{Host: hostDeclToAPI(*h), References: []apitypes.HostReferenceResp{}}
	for _, ref := range cfg.HostReferences(name) {
		resp.References = append(resp.References, apitypes.HostReferenceResp{
			Kind: ref.Kind, Owner: ref.Owner, Field: ref.Field, Value: ref.Value,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleAPITopologyHostSet repoints a declared host at a new address, leaving
// every referencing record untouched. This is the one-edit move: the address
// lives in one record, so changing it moves the proxy backends, the forwards,
// the DNS answers and the scrape targets together, in one config write and one
// sync. Admin-only.
func (s *Server) handleAPITopologyHostSet(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.HostSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.IP = strings.TrimSpace(req.IP)
	if req.Name == "" || req.IP == "" {
		writeJSONError(w, http.StatusBadRequest, "name and ip are required")
		return
	}
	// The declaration is the bottom of the chain, so its address is an address.
	if config.IsHostRef(req.IP) {
		writeJSONError(w, http.StatusBadRequest, "a declared host's ip is the address itself, not another reference")
		return
	}
	if net.ParseIP(req.IP) == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid IP: "+req.IP)
		return
	}
	cfg := s.cfg()
	if req.Name == config.HostRefSelfName {
		writeJSONError(w, http.StatusBadRequest,
			"@self is this instance's own address, not a declared host; it is per-instance (a peer resolves it to its own), and it changes by setting local_interface on the Settings page")
		return
	}
	if cfg.HostDeclByName(req.Name) == nil {
		writeJSONError(w, http.StatusNotFound, "host not found: "+req.Name)
		return
	}
	for _, h := range cfg.Hosts {
		if h.Name != req.Name && h.IP == req.IP {
			writeJSONError(w, http.StatusBadRequest, "host ip already declared by "+h.Name+": "+req.IP)
			return
		}
	}
	moved := apitypes.HostShowResp{References: []apitypes.HostReferenceResp{}}
	for _, ref := range cfg.HostReferences(req.Name) {
		moved.References = append(moved.References, apitypes.HostReferenceResp{
			Kind: ref.Kind, Owner: ref.Owner, Field: ref.Field, Value: ref.Value,
		})
	}
	if err := s.updateConfig(func(c *config.Config) {
		for i := range c.Hosts {
			if c.Hosts[i].Name == req.Name {
				c.Hosts[i].IP = req.IP
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	moved.Host = apitypes.HostDecl{Name: req.Name, IP: req.IP}
	if h := s.cfg().HostDeclByName(req.Name); h != nil {
		moved.Host = hostDeclToAPI(*h)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(moved)
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
	next := *s.cfg()
	next.Exporters = exporters
	if err := next.ValidateExporters(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.Exporters = exporters }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	writeJSONOK(w)
}

// handleAPITopologyScrapeExclusions replaces the scrape-exclusion list (IPs /
// CIDRs never emitted as scrape targets). Whole-list replace. Admin-only.
func (s *Server) handleAPITopologyScrapeExclusions(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.TopologyScrapeExclusionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	out := make([]string, 0, len(req.ScrapeExclusions))
	for _, e := range req.ScrapeExclusions {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid CIDR: "+e)
				return
			}
		} else if config.IsHostRef(e) {
			// An exclusion names a machine, so a reference is the right way to
			// write one — it keeps excluding the same box after it moves.
			if _, err := s.cfg().ResolveHostRef(e); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		} else if net.ParseIP(e) == nil {
			writeJSONError(w, http.StatusBadRequest, "invalid IP: "+e)
			return
		}
		out = append(out, e)
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.ScrapeExclusions = out }); err != nil {
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
	// Resolved: the scan dials each slot.
	cfg := s.cfg()
	slots := []slot{{"", cfg.ServiceBackend(svc)}}
	if next := cfg.ServiceNextBackend(svc); next != "" {
		slots = []slot{{"current", cfg.ServiceBackend(svc)}, {"next", next}}
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
