package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	fillShowOccurrences(cfg, &resp)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// fillShowOccurrences attaches the second list — the records carrying this
// host's address as a literal — to `hz host show`.
//
// It is the half the command could not answer: the live gateway's config names
// its own address dozens of times and every one of them reported zero
// dependants, which is an honest answer to "what references this" and the wrong
// answer to "what breaks if I move it". The two lists stay separate here for
// the same reason they do everywhere else: a reference follows the host, an
// occurrence does not.
func fillShowOccurrences(cfg *config.Config, resp *apitypes.HostShowResp) {
	addr := strings.TrimSpace(resp.Host.IP)
	resp.Occurrences = []apitypes.HostOccurrenceResp{}
	if addr == "" {
		resp.OccurrencesUnknownWhy = "hz does not know this host's address, so it could not look for " +
			"records carrying it. This list is empty because the search could not run."
		return
	}
	resp.OccurrencesKnown = true
	if plan, err := cfg.PlanAddressAdoption(addr); err == nil {
		for _, a := range plan.Adopt {
			resp.Occurrences = append(resp.Occurrences, adoptionToAPI(a))
		}
		for _, r := range plan.Refused {
			resp.Occurrences = append(resp.Occurrences, adoptionToAPI(r))
		}
		sortOccurrenceResps(resp.Occurrences)
		return
	}
	// Nothing declares the address, so there is nothing to adopt into. The
	// occurrences are still real.
	for _, o := range cfg.AddressOccurrences(addr) {
		resp.Occurrences = append(resp.Occurrences, apitypes.HostOccurrenceResp{
			Kind: o.Kind, Owner: o.Owner, Field: o.Field, Value: o.Value,
		})
	}
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

// handleAPITopologyHostAdopt rewrites every literal occurrence of a host's
// address into a reference to it. Admin-only.
//
// DRY RUN BY DEFAULT — the listing is the product and --confirm is the
// afterthought, the discipline `hz project rm` and `hz segment rm` use. This is
// a config rewrite on a live gateway, touching dozens of records at once; an
// operator gets to read every line before any of it is written.
//
// The plan and the write are one computation (config.adoptAddress), so the
// confirmed run cannot do something the dry run did not describe.
func (s *Server) handleAPITopologyHostAdopt(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.HostAdoptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	cfg := s.cfg()
	addr, err := adoptAddressForHost(cfg, req.Name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	plan, err := cfg.PlanAddressAdoption(addr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := adoptPlanToAPI(plan)
	if !req.Confirm {
		writeJSON(w, resp)
		return
	}

	// Rehearse on a DEEP copy and validate it before anything is stored.
	// updateConfig's copy is shallow — the slices and the *ProxyConfig
	// pointers adoption writes through are shared with the live config — so a
	// rewrite that turned out invalid would already be in memory. A JSON round
	// trip is the cheap honest copy.
	rehearsal, err := deepCopyConfig(cfg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not rehearse the rewrite: "+err.Error())
		return
	}
	if _, err := rehearsal.AdoptAddress(addr); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := rehearsal.ValidateHostRefs(); err != nil {
		writeJSONError(w, http.StatusBadRequest,
			"the rewrite would leave a config hz cannot save, so nothing was written: "+err.Error())
		return
	}

	var written *config.AdoptionPlan
	if err := s.updateConfig(func(c *config.Config) {
		written, _ = c.AdoptAddress(addr)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickExporterStatus()
	resp = adoptPlanToAPI(written)
	resp.Confirmed = true
	writeJSON(w, resp)
}

// adoptAddressForHost turns the host name the caller named into the address to
// scan for. "self" is the reserved pseudo-host and the case that dominates a
// gateway, so it is resolved here rather than being a name lookup that fails.
func adoptAddressForHost(cfg *config.Config, name string) (string, error) {
	if name == config.HostRefSelfName {
		self := strings.TrimSpace(cfg.LocalInterface)
		if self == "" {
			return "", fmt.Errorf("hz has not detected this instance's own LAN address, so it cannot say which literals are %s; local_interface on the Settings page is what fills it in", config.SelfRef)
		}
		return self, nil
	}
	h := cfg.HostDeclByName(name)
	if h == nil {
		return "", fmt.Errorf("host not found: %s", name)
	}
	if strings.TrimSpace(h.IP) == "" {
		return "", fmt.Errorf("host %q carries no address, so there is nothing to look for", name)
	}
	return strings.TrimSpace(h.IP), nil
}

// deepCopyConfig round-trips a config through JSON. It is used to rehearse a
// rewrite: the result shares no slice or pointer with the live config, which a
// struct copy does not give.
func deepCopyConfig(cfg *config.Config) (*config.Config, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return config.LoadFromJSON(raw)
}

func adoptPlanToAPI(p *config.AdoptionPlan) apitypes.HostAdoptResp {
	resp := apitypes.HostAdoptResp{
		Address: p.Address,
		Ref:     p.Ref,
		RefWhy:  p.RefWhy,
		Adopt:   make([]apitypes.HostOccurrenceResp, 0, len(p.Adopt)),
		Refused: make([]apitypes.HostOccurrenceResp, 0, len(p.Refused)),
		Written: p.Written,
	}
	for _, a := range p.Adopt {
		resp.Adopt = append(resp.Adopt, adoptionToAPI(a))
	}
	for _, r := range p.Refused {
		resp.Refused = append(resp.Refused, adoptionToAPI(r))
	}
	return resp
}
