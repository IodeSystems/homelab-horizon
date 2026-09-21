package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The Machine record's surface, and hz becoming the ISSUER of agent
// credentials (plan/architecture.md, phase 4 item 13).
//
// Same layering as handlers_api_declare.go and for the same reason: the server
// owns the config, holds it in an atomic pointer, and is the only thing that
// writes config.json — so every decision lives in internal/config beside the
// validator it has to satisfy, and these handlers are transport.
//
// THE ONE THING THAT IS NOT TRANSPORT is handleAPIAgentEnroll. `hz-agent
// enroll` used to mint its own secret and write its own record into hz's
// store, which only worked because hz and the agent were the same root on one
// box; a remote agent doing that would be writing itself in. So hz mints, hz
// writes CredentialStore, and a machine hz does not declare is refused. What
// did NOT change: the store format, the header, the hashing, Server.agentCaller
// and the poll — none of them know where a credential came from
// (internal/agent/enrolment.go says the same thing from the client's end).

// handleAPIMachines returns the declared machines, sorted by name.
//
// It joins one fact from OUTSIDE the config — whether hz holds an agent
// credential for the machine — because that is the question every operator
// looking at this list is actually asking, and the credential store is hz's
// own file sitting beside its config. Nothing else about the agent is here:
// last poll and observed version belong to the report-back and to instances
// respectively, and a machine column derived from them would have to say so
// (plan/example-projection.md §3, the two clocks).
// GET /api/v1/machines
func (s *Server) handleAPIMachines(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	cfg := s.cfg()
	out := make([]apitypes.MachineResp, 0, len(cfg.Machines))
	for _, m := range cfg.Machines {
		out = append(out, s.machineResp(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAPIMachineAdd declares a machine.
// POST /api/v1/machines/add
func (s *Server) handleAPIMachineAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.MachineAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	if err := next.AddMachine(config.Machine{Name: req.Name, Segments: req.Segments, Note: req.Note}); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	m, _ := next.FindMachine(req.Name)
	writeJSON(w, s.machineResp(m))
}

// handleAPIMachineRm removes a machine, or — without confirm — says what that
// would take.
//
// The credential is the dependant, and it is a real one: a credential naming a
// machine hz no longer declares still authenticates and nothing in the config
// explains it. Without cascade that refuses the removal by name; with cascade
// the credential is REVOKED in the same request, after the config write, so a
// failure between them leaves a declared machine with a working credential
// rather than a credential for a machine nobody declares.
// POST /api/v1/machines/rm
func (s *Server) handleAPIMachineRm(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.MachineRmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	_, enrolled := s.agentCredentials().Find(req.Name)
	removes, blocked, err := next.MachineRemoval(req.Name, enrolled, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := apitypes.RemovalResp{Removes: dependantsResp(removes), Blocked: dependantsResp(blocked)}
	if len(blocked) > 0 || !req.Confirm {
		writeJSON(w, out)
		return
	}
	if _, err := next.RemoveMachine(req.Name, enrolled, req.Cascade); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	if enrolled {
		if _, err := s.agentCredentials().Revoke(req.Name); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "the machine was removed but its agent credential could not be revoked: "+err.Error())
			return
		}
	}
	out.OK = true
	writeJSON(w, out)
}

// handleAPIAgentEnroll issues a machine's agent credential. hz is the issuer.
//
// ADMIN-GATED, because enrolling a box is an act of authority. isAdmin is used
// unchanged and deliberately: no Bearer branch was added for the shared admin
// token here either. A personal API token authenticates as a Bearer through
// currentUser; the shared token authenticates by exchanging itself for a
// session at /api/v1/auth/login, which is what the hz CLI already does and what
// internal/agent's Enroller does.
//
// REFUSED FOR A MACHINE HZ DOES NOT DECLARE, with 404. This is the seam item 13
// exists to close: before it, anything that could write hz's credential file
// could enrol itself; now the right to hold a credential comes from a Machine
// record an admin declared.
//
// THE SECRET CROSSES EXACTLY ONCE, in this response, and hz keeps only its
// SHA-256 (CredentialStore.Enroll). An already-enrolled box that asks again
// gets an acknowledgement and no secret: the agent sends the HASH of what it
// holds, so re-running enrolment never puts a working credential on the wire.
// POST /api/v1/agent/enroll
func (s *Server) handleAPIAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req agent.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	m, declared := s.cfg().FindMachine(req.Machine)
	if !declared {
		// 404 rather than 403: there is no such machine to have a credential
		// for. The message names the command that would make one, because an
		// operator standing at a new box with a refusal and no next step is
		// the state this whole record exists to remove.
		writeJSONError(w, http.StatusNotFound,
			"hz declares no machine named "+jsonSafeName(req.Machine)+
				" — declare it first (`hz machine add <name> --segment <segment>`). hz issues an agent credential only for a machine it has been told about.")
		return
	}

	store := s.agentCredentials()

	// Already holding the credential hz has? Say so and write nothing. This is
	// the re-run path: `hz-agent install` enrols every time, so enrolment has
	// to be idempotent, and rotating on every install would retire a working
	// credential for no reason.
	if !req.Rotate && req.CurrentHash != "" {
		if existing, ok := store.Find(req.Machine); ok && existing.Hash == req.CurrentHash {
			writeJSON(w, agent.EnrollResponse{
				Machine: m.Name, AlreadyEnrolled: true, Segments: m.Segments, Note: m.Note,
			})
			return
		}
	}

	secret, err := agent.NewSecret()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "minting a credential: "+err.Error())
		return
	}
	// Recorded BEFORE it is answered with. The other order would hand a box a
	// credential hz does not accept if the write failed — which is a 401 the
	// operator would debug at the box. This order's failure mode is hz
	// accepting a credential nobody holds, which the next enrolment replaces.
	if err := store.Enroll(m.Name, secret); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "recording the credential: "+err.Error())
		return
	}
	writeJSON(w, agent.EnrollResponse{
		Machine: m.Name, Secret: secret, Segments: m.Segments, Note: m.Note,
	})
}

// machineResp renders one machine for the wire, joining hz's own credential
// store so a listing can answer "is this box enrolled".
func (s *Server) machineResp(m config.Machine) apitypes.MachineResp {
	out := apitypes.MachineResp{
		Name:       m.Name,
		Segments:   m.Segments,
		Note:       m.Note,
		MultiHomed: m.MultiHomed(),
	}
	if cred, ok := s.agentCredentials().Find(m.Name); ok {
		out.Enrolled = true
		out.EnrolledAt = cred.CreatedAt
	}
	return out
}

// jsonSafeName quotes a caller-supplied name for an error message, so a machine
// name full of punctuation cannot make the sentence unreadable.
func jsonSafeName(name string) string {
	b, err := json.Marshal(name)
	if err != nil {
		return "(unprintable)"
	}
	return string(b)
}
