package server

import (
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The model's READ surface for a screen: one machine's projection, served to
// an admin browser instead of to an agent.
//
// # Why this is not GET /api/v1/agent/desired
//
// That route answers the MACHINE, authenticated by the machine's own agent
// credential, and it carries the composed payload — hz's rendered HAProxy
// config, its dnsmasq records, its wg0.conf read back, its certificate
// bundles. Those sections exist only for the box hz runs on and are attached
// at the call site (handlers_agent.go, desiredFor). An operator asking "what
// does hz say about app-1" is asking a different question with a different
// answer, and the honest answer is the PURE projection: what hz computes from
// its own records for any machine, gateway included, with no local files in
// it.
//
// So this handler calls projection.Project directly and composes nothing. The
// gateway is machine #1 here exactly as it is there: no branch.
//
// # Read-only, and that is a rule rather than a phase
//
// hz publishes; the agent collects. There is no apply here, no push, and no
// endpoint next to this one that reaches a machine — see plan/ui-redesign.md,
// "No Apply button, no push, no reach into a machine".

// handleAPIMachineProjection serves projection.Project for one machine.
//
// GET /api/v1/machines/projection?machine=<name>
//
// A MACHINE HZ DOES NOT DECLARE IS A 200, NOT A 404, and that is the whole
// point of the shape. projection.Project answers for an undeclared machine
// with an empty projection plus a Gap on the "machine" section saying no
// record declares it and naming the command that would. Refusing with a 404
// would throw that sentence away and leave a screen with nothing to render but
// an error — which is the same screen it would show for a typo, a permissions
// problem or hz being down. The only 400 is an empty machine name, because
// there is no machine to have an opinion about.
func (s *Server) handleAPIMachineProjection(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	machine := r.URL.Query().Get("machine")
	mc, err := projection.Project(s.projectionGlobal(s.cfg()), machine)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// no-store for the same reason version-drift sets it: every field here is
	// hz's current opinion, and a cached opinion rendered beside a live
	// observation is a diff against a memory on the hz side of the pair.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, machineProjectionResp(mc))
}

// machineProjectionResp copies a MachineConfig onto its wire mirror.
//
// EXPLICIT, FIELD BY FIELD, rather than a re-marshal through JSON or a type
// alias. The mirror exists because tygo reads only internal/apitypes
// (apitypes/projection_view.go says why), and a copy that is written out is a
// copy a reflection test can check against the original — which
// handlers_api_model_test.go does, in both directions, so a field added to
// either side fails the build rather than vanishing off a screen.
//
// The slices are built with make(..., 0, n) and never left nil: a nil slice
// marshals to null, and null on this type would be a third state beside "empty
// because nothing is wanted" and "empty because hz does not know". There are
// exactly two and the type refuses to grow a third.
func machineProjectionResp(mc projection.MachineConfig) apitypes.MachineProjectionResp {
	out := apitypes.MachineProjectionResp{
		Machine:    mc.Machine,
		Serial:     mc.Serial,
		Segments:   make([]apitypes.ProjectionSegment, 0, len(mc.Segments)),
		Forwards:   make([]apitypes.ProjectionForward, 0, len(mc.Forwards)),
		Hosts:      make([]apitypes.ProjectionHostEntry, 0, len(mc.Hosts)),
		Packages:   make([]apitypes.ProjectionPackage, 0, len(mc.Packages)),
		Feeds:      make([]apitypes.ProjectionFeed, 0, len(mc.Feeds)),
		Units:      make([]apitypes.ProjectionUnit, 0, len(mc.Units)),
		Unresolved: make([]apitypes.ProjectionGap, 0, len(mc.Unresolved)),
	}
	for _, seg := range mc.Segments {
		out.Segments = append(out.Segments, apitypes.ProjectionSegment{
			Name:      seg.Name,
			Interface: seg.Interface,
			Address:   seg.Address,
			Peers:     seg.Peers,
			Resolved:  seg.Resolved,
		})
	}
	for _, f := range mc.Forwards {
		out.Forwards = append(out.Forwards, apitypes.ProjectionForward{From: f.From, To: f.To, Reason: f.Reason})
	}
	for _, h := range mc.Hosts {
		out.Hosts = append(out.Hosts, apitypes.ProjectionHostEntry{Name: h.Name, Address: h.Address})
	}
	for _, p := range mc.Packages {
		out.Packages = append(out.Packages, apitypes.ProjectionPackage{Name: p.Name, Version: p.Version, Hold: p.Hold})
	}
	for _, f := range mc.Feeds {
		out.Feeds = append(out.Feeds, apitypes.ProjectionFeed{
			From: f.From, URL: f.URL, Suite: f.Suite, Component: f.Component, KeyID: f.KeyID,
		})
	}
	for _, u := range mc.Units {
		out.Units = append(out.Units, apitypes.ProjectionUnit{Name: u.Name, Enabled: u.Enabled})
	}
	for _, g := range mc.Unresolved {
		out.Unresolved = append(out.Unresolved, apitypes.ProjectionGap{Section: g.Section, Reason: g.Reason, Why: g.Why})
	}
	return out
}
