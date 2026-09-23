package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The Segment record's surface (plan/architecture.md, phase 4 item 15).
//
// Same layering as handlers_api_machines.go and for the same reason: every
// decision lives in internal/config beside the validator it has to satisfy, and
// these handlers are transport. The one thing computed here is the JOIN a
// client must not re-derive — each member's peers, and which machines name the
// segment without being addressed on it.

// handleAPISegments returns the declared segments, sorted by name.
// GET /api/v1/segments
func (s *Server) handleAPISegments(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	cfg := s.cfg()
	out := make([]apitypes.SegmentResp, 0, len(cfg.Segments))
	for _, seg := range cfg.Segments {
		out = append(out, segmentResp(cfg, seg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAPISegmentAdd declares a segment.
// POST /api/v1/segments/add
func (s *Server) handleAPISegmentAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.SegmentAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	seg := config.Segment{
		Name:      req.Name,
		Project:   req.Project,
		CIDR:      req.CIDR,
		Interface: req.Interface,
		Note:      req.Note,
	}
	for _, m := range req.Members {
		seg.Members = append(seg.Members, config.SegmentMember{
			Machine:   m.Machine,
			Address:   m.Address,
			PublicKey: m.PublicKey,
			Hub:       m.Hub,
			Endpoint:  m.Endpoint,
		})
	}

	next := *s.cfg()
	if err := next.AddSegment(seg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	saved, _ := next.FindSegment(req.Name)
	writeJSON(w, segmentResp(&next, saved))
}

// handleAPISegmentRm removes a segment, or — without confirm — says what that
// would take.
//
// Every machine naming the segment is a dependant: leaving one behind would
// leave a membership resolving to nothing, which Save refuses, and the operator
// would meet that as a validation error about a machine they never mentioned.
// POST /api/v1/segments/rm
func (s *Server) handleAPISegmentRm(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.SegmentRmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	removes, blocked, err := next.SegmentRemoval(req.Name, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := apitypes.RemovalResp{Removes: dependantsResp(removes), Blocked: dependantsResp(blocked)}
	if len(blocked) > 0 || !req.Confirm {
		writeJSON(w, out)
		return
	}
	if _, err := next.RemoveSegment(req.Name, req.Cascade); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	out.OK = true
	writeJSON(w, out)
}

// segmentResp renders one segment for the wire, joining the two facts a client
// would otherwise have to work out for itself: who each member peers with, and
// which machines are IN the segment without being addressed on it.
func segmentResp(cfg *config.Config, seg config.Segment) apitypes.SegmentResp {
	out := apitypes.SegmentResp{
		Name:      seg.Name,
		Project:   seg.Project,
		CIDR:      seg.CIDR,
		Interface: seg.Interface,
		Note:      seg.Note,
	}
	for _, m := range seg.Members {
		mem := apitypes.SegmentMemberResp{
			Machine:   m.Machine,
			Address:   m.Address,
			PublicKey: m.PublicKey,
			Hub:       m.Hub,
			Endpoint:  m.Endpoint,
		}
		for _, p := range seg.PeersOf(m.Machine) {
			mem.Peers = append(mem.Peers, p.Machine)
		}
		out.Members = append(out.Members, mem)
	}
	for _, machine := range cfg.Machines {
		for _, name := range machine.Segments {
			if name != seg.Name {
				continue
			}
			if _, addressed := seg.Member(machine.Name); !addressed {
				out.Unaddressed = append(out.Unaddressed, machine.Name)
			}
		}
	}
	sort.Strings(out.Unaddressed)
	return out
}
