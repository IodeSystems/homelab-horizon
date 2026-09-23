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

// handleAPISegmentSet changes a segment that already exists, and the
// memberships on it — the verb `ls|show|add|rm` left out, and the first thing
// real use hits: re-addressing one member meant `rm --cascade` and re-declaring
// the whole segment.
//
// It goes through config.SetSegment, which runs the SAME validator AddSegment
// runs, over the whole model. That is the point of not writing the record here:
// a second write path that validated differently is how a config becomes
// unloadable, and the failure would surface from a later, unrelated writer.
//
// Two shapes of run, and the difference is whether the change drops a record the
// operator did not name:
//
//	strands nobody  writes immediately, the way `hz env set` does.
//	strands members a new CIDR putting an existing address outside itself
//	                BLOCKS, naming each one; --cascade opts in to unaddressing
//	                them and is a DRY RUN until --confirm.
//
// POST /api/v1/segments/set
func (s *Server) handleAPISegmentSet(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.SegmentSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	patch := config.SegmentPatch{
		Project:   req.Project,
		CIDR:      req.CIDR,
		Interface: req.Interface,
		Note:      req.Note,
		Hub:       req.Hub,
		Unaddress: req.Unaddress,
	}
	for _, m := range req.Members {
		patch.Members = append(patch.Members, config.SegmentMemberPatch{
			Machine:   m.Machine,
			Address:   m.Address,
			PublicKey: m.PublicKey,
			Endpoint:  m.Endpoint,
		})
	}

	// The blockers first, structured: SetSegment collapses them into one error
	// string, and a client that had to parse that back apart would be re-deriving
	// a judgement hz already made.
	_, blocked, err := s.cfg().SegmentSet(req.Name, patch, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(blocked) > 0 {
		writeJSON(w, apitypes.SegmentSetResp{Blocked: dependantsResp(blocked)})
		return
	}

	// Applied to a COPY, whatever confirm says. A dry run that skipped the write
	// would be a dry run that skipped the validation too, and the operator would
	// meet the refusal on the confirmed run instead — which is the one thing a
	// dry run exists to prevent.
	next := *s.cfg()
	change, err := next.SetSegment(req.Name, patch, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, _ := next.FindSegment(req.Name)
	resp := apitypes.SegmentSetResp{
		Changes: change.Fields,
		Strands: dependantsResp(change.Strands),
		HubMove: hubMoveResp(change.HubMove),
	}
	segResp := segmentResp(&next, saved)
	resp.Segment = &segResp

	// Confirm gates only the destructive half. A set that strands nobody writes
	// immediately, because it is an edit to values and `hz segment set` can put
	// every one of them back.
	if len(change.Strands) > 0 && !req.Confirm {
		writeJSON(w, resp)
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	resp.OK = true
	writeJSON(w, resp)
}

func hubMoveResp(move *config.SegmentHubMove) *apitypes.SegmentHubMoveResp {
	if move == nil {
		return nil
	}
	out := &apitypes.SegmentHubMoveResp{From: move.From, To: move.To}
	for _, p := range move.Peers {
		out.Peers = append(out.Peers, apitypes.SegmentPeerChangeResp{
			Machine: p.Machine, Before: p.Before, After: p.After,
		})
	}
	return out
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
