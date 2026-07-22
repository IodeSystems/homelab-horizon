package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// toAPIRanges converts config port ranges to the API shape.
func toAPIRanges(rs []config.PortRange) []apitypes.PortRange {
	out := make([]apitypes.PortRange, len(rs))
	for i, r := range rs {
		out[i] = apitypes.PortRange{From: r.From, To: r.To, Note: r.Note}
	}
	return out
}

// GET /api/v1/ports — reserved ports per host, derived from the full config
// (service backends, blue-green deploy next-backends, HAProxy, WireGuard,
// dnsmasq, and the admin server). Authoritative source for port allocation:
// the hz CLI uses it so `ports next`/`ports list` never hand out a port that
// infra or a standby release already holds.
func (s *Server) handleAPIPorts(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	pm := s.cfg().DeriveHostPortMap()
	resp := apitypes.HostPortMapResponse{
		Hosts: make(map[string][]apitypes.HostPortEntry, len(pm.Hosts)),
		Exclusions: apitypes.PortExclusionsResp{
			Builtin: toAPIRanges(config.BuiltinPortExclusions()),
			Custom:  toAPIRanges(s.cfg().PortExclusions),
		},
	}
	for host, entries := range pm.Hosts {
		out := make([]apitypes.HostPortEntry, len(entries))
		for i, e := range entries {
			out[i] = apitypes.HostPortEntry{Port: e.Port, Proto: e.Proto, Service: e.Service, Domain: e.Domain}
		}
		resp.Hosts[host] = out
	}
	writeJSON(w, resp)
}

// PUT /api/v1/ports/exclusions — replace the operator's custom port-exclusion
// list (the built-in denylist is constant and not editable). Admin-only.
func (s *Server) handleAPIPortExclusions(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or PUT required")
		return
	}
	var req apitypes.PortExclusionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	custom := make([]config.PortRange, 0, len(req.Custom))
	for _, r := range req.Custom {
		if r.From <= 0 || r.From > 65535 || (r.To != 0 && (r.To < r.From || r.To > 65535)) {
			writeJSONError(w, http.StatusBadRequest, "each exclusion needs a valid from (1-65535) and optional to >= from")
			return
		}
		custom = append(custom, config.PortRange{From: r.From, To: r.To, Note: r.Note})
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.PortExclusions = custom }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONOK(w)
}
