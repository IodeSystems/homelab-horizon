package server

import (
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// handleAPIInstances lists the hz instances: this one first, then every HA
// peer in config.Peers, in config order.
//
// It is a read of what hz already holds. It does NOT contact a peer (CLAUDE.md
// invariant 1 — hz reaches off-box to read only through the subsystems that
// already do): a peer row carries what this instance's config says about it,
// and nothing observed. Self's name is LocalMachineName(), the same call
// `machines/add` with self=true makes, so the project join below and a
// "put this box in a project" from the UI agree on the name by construction.
// GET /api/v1/instances
func (s *Server) handleAPIInstances(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeJSON(w, instancesFrom(s.cfg(), LocalMachineName(), s.version, s.peerSyncSnapshot()))
}

// instancesFrom is the whole decision, pure so it is tested without a server.
func instancesFrom(cfg *config.Config, selfName, version string, snap PeerSyncStatusSnapshot) []apitypes.InstanceResp {
	standalone := len(cfg.Peers) == 0

	primaryID := ""
	switch {
	case standalone:
	case cfg.ConfigPrimary:
		primaryID = cfg.PeerID
	default:
		if p := cfg.PrimaryPeer(); p != nil {
			primaryID = p.ID
		}
	}

	role := func(isPrimary bool) string {
		switch {
		case standalone:
			return apitypes.InstanceRoleStandalone
		case isPrimary:
			return apitypes.InstanceRolePrimary
		default:
			return apitypes.InstanceRoleReplica
		}
	}

	join := func(row *apitypes.InstanceResp) {
		if m, ok := cfg.FindMachine(row.Name); ok {
			row.Declared = true
			row.Project = m.Project
		}
	}

	self := apitypes.InstanceResp{
		Name:      selfName,
		Self:      true,
		Address:   strings.TrimSpace(cfg.LocalInterface),
		Role:      role(cfg.ConfigPrimary),
		PeerID:    cfg.PeerID,
		PrimaryID: primaryID,
		Version:   version,
	}
	join(&self)
	if self.Role == apitypes.InstanceRoleReplica {
		self.Sync = &apitypes.InstanceSyncResp{
			PullCount:     snap.PullCount,
			LastSuccessAt: unixOrZero(snap.LastSuccessAt),
			LastError:     snap.LastError,
		}
	}

	out := make([]apitypes.InstanceResp, 0, 1+len(cfg.Peers))
	out = append(out, self)
	for _, p := range cfg.Peers {
		row := apitypes.InstanceResp{
			Name:      p.ID,
			Address:   p.WGAddr,
			Role:      role(p.Primary),
			PeerID:    p.ID,
			PrimaryID: primaryID,
		}
		join(&row)
		out = append(out, row)
	}
	return out
}
