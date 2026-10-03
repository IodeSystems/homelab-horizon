package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// handleAPIInstances lists the hz instances: this one first, then every HA
// peer in config.Peers (Hz Nodes), in config order, then every declared hz
// instance (config.HZInstance, a nested hz), by name.
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
	cfg := s.cfg()
	rows := instancesFrom(cfg, LocalMachineName(), s.version, s.peerSyncSnapshot())
	s.decorateNested(r, cfg, rows)
	writeJSON(w, rows)
}

// decorateNested adds what this hz knows of each nested hz from its own
// records (N4a): whether an instance token is minted, and when the instance
// last asked for a desired state — recorded on its pull, never by dialling it.
func (s *Server) decorateNested(r *http.Request, cfg *config.Config, rows []apitypes.InstanceResp) {
	var pulls map[string]db.InstancePull
	unknown := ""
	if s.users == nil {
		unknown = "identity store unavailable — pulls are recorded in hz.db"
	} else if p, err := s.users.InstancePulls(r.Context()); err != nil {
		unknown = err.Error()
	} else {
		pulls = p
	}
	for i := range rows {
		if rows[i].Role != apitypes.InstanceRoleNested {
			continue
		}
		h, ok := cfg.FindHZInstance(rows[i].Name)
		if !ok {
			continue
		}
		n := &apitypes.InstanceNestedResp{HasToken: h.TokenSHA256 != "", PullsUnknown: unknown, VPNClient: h.VPNClient}
		var pulled time.Time
		if p, ok := pulls[h.Name]; ok {
			pulled = p.At
			n.LastPullAt = p.At.Unix()
			n.LastPullRung = p.Project + "/" + p.Environment
		}
		grants := s.childGrantStore()
		expires, _ := grants.pending(h.Name)
		enrolled, _ := grants.enrolledAt(h.Name)
		n.State = nestedState(n.HasToken, expires, enrolled, pulled)
		n.GrantExpiresAt = unixOrZero(expires)
		n.EnrolledAt = unixOrZero(enrolled)
		rows[i].Nested = n
	}
}

// nestedState is the one state a nested instance shows, from what this hz
// holds. A zero time is "none": no live command, no enrolment remembered, no
// pull recorded. Compared in whole seconds: hz.db stores a pull to the second.
//
// A live command wins: the operator is provisioning (or re-provisioning) the
// box now. An enrolment newer than the last pull is a box that has not pulled
// YET — a re-provisioned box must not read as connected on the old box's pull.
func nestedState(hasToken bool, grantExpires, enrolledAt, lastPull time.Time) string {
	switch {
	case !grantExpires.IsZero():
		return apitypes.NestedStateCommandGenerated
	case !enrolledAt.IsZero() && (lastPull.IsZero() || lastPull.Unix() < enrolledAt.Unix()):
		return apitypes.NestedStateEnrolled
	case !lastPull.IsZero():
		return apitypes.NestedStateConnected
	case hasToken:
		return apitypes.NestedStateNeverConnected
	default:
		return apitypes.NestedStateDeclared
	}
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
	// Nested hz: a separate config layer, declared here as an HZInstance. It
	// is a statement from the records only — the URL is never dialled
	// (invariant 1) — so it carries no version, no sync, no peer or primary
	// ID: those are facts about THIS instance's cluster and a nested hz is not
	// in it.
	nested := make([]apitypes.InstanceResp, 0)
	for _, h := range cfg.HZInstances {
		nested = append(nested, apitypes.InstanceResp{
			Name:     h.Name,
			Address:  h.URL,
			Role:     apitypes.InstanceRoleNested,
			Project:  h.Project,
			Declared: true,
		})
	}
	sort.Slice(nested, func(i, j int) bool { return nested[i].Name < nested[j].Name })
	return append(out, nested...)
}
