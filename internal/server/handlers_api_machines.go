package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wgkey"
)

// The Machine record's surface, and hz becoming the ISSUER of agent
// credentials (plan/design/architecture.md, phase 4 item 13).
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
// (plan/design/example-projection.md §3, the two clocks).
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
//
// SELF IS RESOLVED HERE, and it has to be. `hz` runs wherever the operator is
// and the gateway is where hz is, so a client that filled in its own hostname
// would declare the operator's laptop. The name is LocalMachineName() —
// os.Hostname, which is also what cmd/hz-agent's machineName() defaults to, so
// a box declared this way enrols under the name it was declared under. That
// agreement is the whole risk in this feature and
// TestTheNameHZDeclaresIsTheNameTheAgentEnrolsWith is where it is pinned.
//
// Declare-then-enrol is unchanged: this is still an admin-gated write by an
// operator holding a credential, and no box gains the ability to write itself
// in. What it removes is the one question hz has no business asking — whether
// the machine it is running on exists.
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

	name := req.Name
	if req.Self {
		if strings.TrimSpace(req.Name) != "" {
			writeJSONError(w, http.StatusBadRequest,
				"self and a name were both given. The point of self is that hz fills the name in from its own identity;"+
					" a name beside it is either the same string typed twice or a disagreement hz would have to pick a winner of")
			return
		}
		name = LocalMachineName()
		if strings.TrimSpace(name) == "" || name == "unknown" {
			writeJSONError(w, http.StatusInternalServerError,
				"hz cannot read this host's name from the kernel, so it has nothing to declare itself as."+
					" Declare it by name instead: `hz machine add <name>`")
			return
		}
		// ALREADY DECLARED IS SUCCESS. `--self` asserts a state — this gateway
		// is in the model — so running it twice has to read as done. A named
		// add still gets AddMachine's refusal: there the operator asserted
		// something about the estate and being told it was already true is the
		// answer they asked for.
		if existing, declared := s.cfg().FindMachine(name); declared {
			out := s.machineResp(existing)
			out.AlreadyDeclared = true
			writeJSON(w, out)
			return
		}
	}

	next := *s.cfg()
	if err := next.AddMachine(config.Machine{Name: name, Project: req.Project, Segments: req.Segments, Note: req.Note, HZ: machineHZ(req.HZ)}); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	m, _ := next.FindMachine(name)
	writeJSON(w, s.machineResp(m))
}

// handleAPIMachineSet edits a declared machine's owner, note and segment
// membership in place.
//
// It exists so none of those costs remove and re-add: removing an enrolled
// machine revokes its agent credential, and re-attributing a box or moving it
// between segments is not a reason to make it re-enrol. It writes immediately
// like machines/add. Attribution renders nothing; a membership change moves
// the machine's projection (and so its agent payload), which nothing applies
// until that box's agent is armed — and a newly joined segment carries no key
// for this box until it re-enrols (`hz-agent enroll`), which the projection
// says as a gap.
// POST /api/v1/machines/set
func (s *Server) handleAPIMachineSet(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.MachineSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	m, err := next.SetMachine(req.Name, config.MachinePatch{
		Project: req.Project, Note: req.Note, Segments: req.Segments,
		HZ: machineHZ(req.HZ), ClearHZ: req.ClearHZ,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
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
//
// IT ALSO CARRIES THE BOX'S SEGMENT KEYS NOW, which is what closes the
// projection's "cannot emit a `[Peer]` block" gap. The keys are the PUBLIC
// halves, one per segment, minted on the box; recordSegmentKeys below is where
// they land and where the rotation-versus-impostor question is answered. They
// ride on this request rather than on one of their own because this request is
// already the authenticated act — see that function's comment.
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

	// THE KEYS FIRST, then the credential. A key that cannot be recorded (a
	// config hz cannot save) must not cost the box a minted credential nobody
	// received — the mint is the expensive, once-only half, so everything that
	// can fail cheaply fails before it.
	keyResults, keyedCfg, err := s.recordSegmentKeys(m.Name, req.SegmentKeys, req.RotateKeys)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if keyedCfg != nil {
		if err := s.updateConfig(func(cfg *config.Config) { *cfg = *keyedCfg }); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "recording this machine's segment keys: "+err.Error())
			return
		}
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
				SegmentKeys: keyResults,
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
		SegmentKeys: keyResults,
	})
}

// recordSegmentKeys writes the public keys a box reported onto its
// SegmentMember entries, and says per segment what it did.
//
// WHY THIS IS TRUSTED ON SIGHT, AND WHY THAT IS NOT TRUST-ON-FIRST-USE.
// Enrolment is already an authenticated act of authority: handleAPIAgentEnroll
// is isAdmin-gated, the credential is supplied by an operator standing at the
// box and is never stored there, and the whole point of item 13 is that a
// machine cannot write itself into hz. A key arriving on that request carries
// exactly the authority the credential hz issues in the same request carries.
// configmgr's registration ceremony (fingerprints, an approval queue) exists
// because an agent registers ITSELF there, unauthenticated, and somebody has to
// vouch for it; here somebody already has. Adding a second approval step would
// be a second trust model for one fact, which is the thing that makes a
// security model impossible to reason about.
//
// WHAT IS NOT TRUSTED: A CHANGE. A box re-enrolling with a DIFFERENT key for a
// segment it is already keyed on is either a rotation or an impostor, and
// nothing in the request distinguishes them. Accepting it silently is a peer
// takeover — whoever last reported a key receives that machine's traffic. So hz
// assumes IMPOSTOR: it keeps what it holds, reports a conflict, and logs it at
// WARN. A rotation is made to say so (`--rotate-keys`), which is a deliberate
// act by the same operator who could enrol the box in the first place.
//
// The returned config is nil when nothing was written, so the caller can skip
// the save entirely rather than rewriting the file to say what it already said.
func (s *Server) recordSegmentKeys(machine string, reported []agent.SegmentKey, rotate bool) ([]agent.SegmentKeyResult, *config.Config, error) {
	if len(reported) == 0 {
		return nil, nil, nil
	}

	next := *s.cfg()
	results := make([]agent.SegmentKeyResult, 0, len(reported))
	wrote := false

	for _, rep := range reported {
		segment := strings.TrimSpace(rep.Segment)
		key := strings.TrimSpace(rep.PublicKey)
		result := agent.SegmentKeyResult{Segment: segment}

		seg, declared := next.FindSegment(segment)
		switch {
		case segment == "":
			continue
		case !wgkey.Valid(key):
			result.Status = agent.SegmentKeyInvalid
			result.Detail = "that is not a WireGuard public key — hz stores only what `wg pubkey` prints"
		case !declared:
			result.Status = agent.SegmentKeyUnknown
			result.Detail = "hz declares no segment by that name, so there is no membership to key"
		default:
			mem, addressed := seg.Member(machine)
			switch {
			case !addressed:
				// LEGAL, and the state `hz machine add --segment` leaves. A key
				// needs a member entry to attach to, and hz will not invent an
				// address to make one.
				result.Status = agent.SegmentKeyUnaddressed
				result.Detail = "this machine is in " + segment + " with no address on it, so there is no member entry to hold a key. " +
					"Address it: hz segment set " + segment + " --member machine=" + machine + ",address=<ip>"
			case mem.PublicKey == key:
				result.Status = agent.SegmentKeyUnchanged
			case mem.PublicKey != "" && !rotate:
				result.Status = agent.SegmentKeyConflict
				result.Detail = "hz already holds a DIFFERENT key for " + machine + " on " + segment + " and kept it. " +
					"A box presenting a new key is either a rotation or another box claiming this peering, and hz cannot tell. " +
					"If it is a rotation, say so: hz-agent enroll --rotate-keys"
				slog.Warn("enrol: refused a changed segment key",
					"machine", machine, "segment", segment,
					"held_prefix", keyPrefix(mem.PublicKey), "presented_prefix", keyPrefix(key))
			default:
				if _, err := next.SetSegment(segment, config.SegmentPatch{
					Members: []config.SegmentMemberPatch{{Machine: machine, PublicKey: &key}},
				}, false); err != nil {
					// The validator refusing is a refusal about the estate, not
					// about this key — a duplicate key on the segment, say. It
					// is the operator's to resolve, so it fails the request
					// rather than being folded into a per-segment status.
					return nil, nil, fmt.Errorf("recording %s's key on %s: %w", machine, segment, err)
				}
				wrote = true
				result.Status = agent.SegmentKeyRecorded
				if mem.PublicKey != "" {
					result.Detail = "replaced the key hz held, because the enrolment asked for a rotation"
					slog.Warn("enrol: rotated a segment key",
						"machine", machine, "segment", segment,
						"was_prefix", keyPrefix(mem.PublicKey), "now_prefix", keyPrefix(key))
				}
			}
		}
		results = append(results, result)
	}

	if !wrote {
		return results, nil, nil
	}
	return results, &next, nil
}

// keyPrefix is how a key appears in a log: enough to tell two apart, not enough
// to be the key. It is a PUBLIC key, so this is legibility rather than secrecy —
// a whole one per line makes the log unreadable and invites pasting it around as
// though it meant something on its own.
func keyPrefix(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8] + "…"
}

// machineResp renders one machine for the wire, joining hz's own credential
// store so a listing can answer "is this box enrolled".
func (s *Server) machineResp(m config.Machine) apitypes.MachineResp {
	out := apitypes.MachineResp{
		Name:       m.Name,
		Project:    m.Project,
		Segments:   m.Segments,
		Note:       m.Note,
		MultiHomed: m.MultiHomed(),
	}
	if m.HZ != nil {
		out.HZ = &apitypes.MachineHZResp{URL: m.HZ.URL}
	}
	if cred, ok := s.agentCredentials().Find(m.Name); ok {
		out.Enrolled = true
		out.EnrolledAt = cred.CreatedAt
	}
	return out
}

// machineHZ converts the wire marker to the record's. nil stays nil.
func machineHZ(h *apitypes.MachineHZResp) *config.MachineHZ {
	if h == nil {
		return nil
	}
	return &config.MachineHZ{URL: h.URL}
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
