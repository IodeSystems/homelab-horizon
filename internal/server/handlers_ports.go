package server

import (
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

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
	resp := apitypes.HostPortMapResponse{Hosts: make(map[string][]apitypes.HostPortEntry, len(pm.Hosts))}
	for host, entries := range pm.Hosts {
		out := make([]apitypes.HostPortEntry, len(entries))
		for i, e := range entries {
			out[i] = apitypes.HostPortEntry{Port: e.Port, Proto: e.Proto, Service: e.Service, Domain: e.Domain}
		}
		resp.Hosts[host] = out
	}
	writeJSON(w, resp)
}
