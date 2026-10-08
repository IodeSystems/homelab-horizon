package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The Hz Instance record's surface (B3, operator 2026-10-02): a nested hz with
// its own config, declared here and NEVER a machine. Transport only — every
// decision is in internal/config/hz_instance.go beside its validator.

// hzInstanceResp renders one instance for the wire. The token digest never
// leaves; HasToken says one is minted.
func hzInstanceResp(h config.HZInstance) apitypes.HZInstanceResp {
	return apitypes.HZInstanceResp{Name: h.Name, Project: h.Project, URL: h.URL, VPNClient: h.VPNClient, Note: h.Note, HasToken: h.TokenSHA256 != "",
		Domain: h.Domain, Domains: append([]string{}, h.TLSDomains()...), DNSGrant: append([]string{}, h.Grant()...)}
}

// instanceAlias is the one instance name a request carries, accepting the
// pre-B3 `machine` spelling for one release. Both, different, is refused.
func instanceAlias(instance, machine string) (string, error) {
	instance, machine = strings.TrimSpace(instance), strings.TrimSpace(machine)
	switch {
	case instance != "" && machine != "" && instance != machine:
		return "", fmt.Errorf("instance %q and machine %q disagree — machine is the old spelling of instance; send one", instance, machine)
	case instance != "":
		return instance, nil
	}
	return machine, nil
}

// GET /api/v1/hz-instances
func (s *Server) handleAPIHZInstances(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	cfg := s.cfg()
	out := make([]apitypes.HZInstanceResp, 0, len(cfg.HZInstances))
	for _, h := range cfg.HZInstances {
		out = append(out, hzInstanceResp(h))
	}
	writeJSON(w, out)
}

// POST /api/v1/hz-instances/add
func (s *Server) handleAPIHZInstanceAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.HZInstanceAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	next := *s.cfg()
	if err := next.AddHZInstance(config.HZInstance{Name: req.Name, Project: req.Project, URL: req.URL, VPNClient: req.VPNClient, Note: req.Note, Domain: req.Domain, Domains: req.Domains, DNSGrant: req.DNSGrant}); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	h, _ := next.FindHZInstance(strings.TrimSpace(req.Name))
	if len(h.TLSDomains()) > 0 {
		s.kickInstanceCert(h.Name)
	}
	writeJSON(w, hzInstanceResp(h))
}

// POST /api/v1/hz-instances/set
func (s *Server) handleAPIHZInstanceSet(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.HZInstanceSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	next := *s.cfg()
	h, err := next.SetHZInstance(req.Name, config.HZInstancePatch{Project: req.Project, URL: req.URL, VPNClient: req.VPNClient, Note: req.Note, Domain: req.Domain, Domains: req.Domains, DNSGrant: req.DNSGrant})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	names := req.Domains != nil || req.Domain != nil
	if names && len(h.TLSDomains()) > 0 {
		s.kickInstanceCert(h.Name)
	}
	if req.DNSGrant != nil || req.Domain != nil {
		// A narrowed grant tombstoned what the instance owned outside it;
		// retract it now rather than at the next sync.
		s.publishInstanceDNSFromRequest(h.Name)
	}
	if names {
		// A changed name set changes what this hz's resolver must leave to
		// public DNS (server=/name/#), so the records file is rewritten.
		if err := s.applyLocalDNS(); err != nil {
			slog.Warn("dns records after an instance domain edit", "err", err)
		}
	}
	writeJSON(w, hzInstanceResp(h))
}

// handleAPIHZInstanceRm removes an hz instance, or — without confirm — says
// what that would take. Refused while a rung's Upstream names it or its VPN
// client exists; cascade CLEARS each Upstream (the rung is kept) and removes
// the client from wg0.conf. The instance token goes with the record.
// POST /api/v1/hz-instances/rm
func (s *Server) handleAPIHZInstanceRm(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.HZInstanceRmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	next := *s.cfg()
	removes, blocked, err := next.HZInstanceRemoval(req.Name, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := apitypes.RemovalResp{Removes: dependantsResp(removes), Blocked: dependantsResp(blocked)}
	if len(blocked) > 0 || !req.Confirm {
		writeJSON(w, out)
		return
	}
	// The VPN client — only reached with cascade, since without it the client
	// is a blocked dependant and the dry run returned above.
	h, _ := next.FindHZInstance(req.Name)
	vpnClient := h.VPNClient
	if _, err := next.RemoveHZInstance(req.Name, req.Cascade); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The client leaves wg0.conf BEFORE the config is written, not after: if
	// the removal fails nothing has changed, while the other order could
	// leave the client in wg0.conf with its profile entry deleted — and a
	// client with no profile entry renders as lan-access, which would widen
	// an orphaned nested hz's reach to the LAN.
	var wgPeers []config.WGPeer
	if vpnClient != "" && s.wg != nil {
		for _, p := range s.wg.GetPeers() {
			if p.Name != vpnClient {
				continue
			}
			if err := s.wg.RemovePeer(p.PublicKey); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "nothing was removed: the VPN client "+vpnClient+" could not be removed from wg0.conf: "+err.Error())
				return
			}
		}
		wgPeers = s.snapshotWGPeers()
	}
	if err := s.updateConfig(func(cfg *config.Config) {
		*cfg = next
		if vpnClient != "" {
			cfg.DeletePeerProfile(vpnClient)
			cfg.DeletePeerProject(vpnClient)
			cfg.DeleteMFAPeer(vpnClient)
			cfg.WGPeers = wgPeers
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	if vpnClient != "" && s.wg != nil {
		if err := s.wg.Reload(); err != nil {
			slog.Warn("wg.Reload", "err", err)
		}
		s.rebuildWGChains()
	}
	// Its records were tombstoned with it; retract them now.
	s.publishInstanceDNSFromRequest(req.Name)
	slog.Info("hz instance removed", "instance", req.Name, "cascade", req.Cascade, "by", s.adminActor(r))
	out.OK = true
	writeJSON(w, out)
}

// settleLegacyHZCarriers removes each machine the B3 migration left as a
// removal candidate (it carried a nested-hz marker and nothing else), unless
// hz holds an agent credential for it — then it is kept, said LOUD. The
// credential store is the server's, which is why config cannot decide this.
// An UNREADABLE store is not an empty one: every candidate is then kept.
func settleLegacyHZCarriers(cfg *config.Config, configPath string) (removed, kept []string) {
	if len(cfg.LegacyHZCarriers) == 0 {
		return nil, nil
	}
	creds, err := agent.CredentialStore{Path: configPath + agent.CredentialsSuffix}.Load()
	if err != nil {
		slog.Error("LOUD: the agent credential store is unreadable, so no migrated hz carrier machine is removed", "err", err)
		return cfg.SettleLegacyHZCarriers(nil)
	}
	held := map[string]bool{}
	for _, c := range creds {
		held[c.Machine] = true
	}
	return cfg.SettleLegacyHZCarriers(func(name string) bool { return held[name] })
}
