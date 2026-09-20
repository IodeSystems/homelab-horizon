package server

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Config manager HTTP surface. Two audiences, two auth models:
//
//   - The machine protocol (/api/v1/cm/register, /api/v1/cm/config) is
//     peer-authenticated. Peer identity is topological — getPeerFromRequest
//     resolves a caller by source IP — so it is a coarse gate, not the
//     foundation; what actually grants anything is the wrapped key an approval
//     delivers, which hz relays and cannot open.
//   - Everything else is s.isAdmin(r).
//
// register is the ONE endpoint an unapproved caller may reach. Fetching config
// requires an approved registration.
//
// STATUS CODES ARE PART OF THE MACHINE CONTRACT. An agent boots its cached
// last-known-good on anything that is not a positive denial, and refuses only
// on a denial, so those two answers must never be collapsed
// (plan/config-manager.md, "Three states, not two"):
//
//	404  unknown — no such machine, no such registration, or no config matches
//	     the running version. NOT a denial. Boot the cache.
//	409  pending — approval is outstanding. NOT a denial. Boot the cache.
//	410  gone — the winning config holds a tombstoned value, so no complete
//	     config exists to serve. NOT a denial. Boot the cache.
//	400  the request contradicts the registration (environment mismatch) or is
//	     malformed. NOT a denial.
//	403  DENIED — the registration carries state 'denied'. This is the only
//	     answer an agent may refuse to boot on.
//
// No admin response carries key material. Registration has no field for a
// wrapped key by construction, and db.RegistrationWrappedKey — the single path
// by which key bytes leave internal/db — is called from exactly one place here:
// the relay that answers an approved box's own poll.

// cmPeerGate resolves the calling VPN peer for a machine-protocol request.
// The peer name is returned for the audit line only; nothing authorizes off it.
func (s *Server) cmPeerGate(w http.ResponseWriter, r *http.Request) (string, bool) {
	peer, err := s.getPeerFromRequest(r)
	if err != nil {
		// A LOCAL caller is admitted, because the deployed topology is hz and
		// the app on ONE box talking over loopback — and without this the
		// config manager cannot serve that shape at all.
		//
		// Deliberately scoped HERE and not in isInVPNRange, which has five
		// callers including the VPN MFA jail: widening that to fix a
		// config-manager problem would loosen an unrelated security surface.
		// This gate is the single chokepoint for the whole machine protocol.
		//
		// Why it is safe: registration only ever creates a PENDING row. The
		// capability is granted by a human approving and wrapping a key to the
		// machine's own keypair, never by registering — so a local caller gains
		// nothing but a place in a queue. A root local caller could read hz's
		// sqlite off the same disk regardless; a non-root one gets an entry an
		// operator must still bless.
		//
		// The peer name it replaces is advisory: its only use is the log line
		// below noting a mismatch with the claimed machine name. Real identity
		// is the keypair, and the real control is the typed fingerprint compare.
		if ip := s.getClientIP(r); isLoopback(ip) {
			slog.Info("cm request admitted from loopback",
				"ip", ip, "path", r.URL.Path, "peer", cmLocalPeer)
			peer = cmLocalPeer
		} else {
			writeJSONError(w, http.StatusForbidden, err.Error())
			return "", false
		}
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return "", false
	}
	return peer, true
}

// cmLocalPeer is the name a loopback caller registers under. It is not an
// identity — it is a placeholder where a WireGuard peer name would be, chosen
// so a reader of the log or the queue can see at once that no peer vouched for
// this and the operator is the only check.
const cmLocalPeer = "local"

// isLoopback covers 127.0.0.0/8, not just 127.0.0.1.
//
// /8 matters and /24 would have been a near-miss that works everywhere until it
// does not: Ubuntu maps the machine's own HOSTNAME to 127.0.1.1 in /etc/hosts,
// so an app configured with its own name rather than the literal "localhost" —
// HZ_URL=http://$(hostname):8080 — sources from 127.0.1.1 and would be refused
// by a narrower check, on that distro only.
func isLoopback(ip string) bool {
	addr := net.ParseIP(ip)
	return addr != nil && addr.IsLoopback()
}

// cmAdminGate is the admin half of the same guard.
func (s *Server) cmAdminGate(w http.ResponseWriter, r *http.Request) bool {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "admin required")
		return false
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return false
	}
	return true
}

// cmActor resolves the user id that a config-manager write is attributed to.
//
// It is an account or nothing, and that is the schema's rule rather than a
// preference: cm_configs.created_by and cm_registrations.approved_by are
// foreign keys into users, so a caller holding only the shared admin token has
// no id to record. Refusing is the honest outcome — blessing a config and
// approving a box are exactly the acts whose "who" must survive being asked
// about later, and "whoever holds the token" does not answer that.
func (s *Server) cmActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusForbidden,
			"sign in with an account: a config-manager change is recorded against a user, "+
				"and the shared admin token names nobody")
		return "", false
	}
	return user.ID, true
}

// cmFingerprint renders a stored public key's fingerprint, or "" if the bytes
// do not parse. A queue row with an unreadable key is still worth showing —
// with the fingerprint blank, which is visibly wrong rather than plausible.
func cmFingerprint(publicKey []byte) string {
	pub, err := ecdh.P256().NewPublicKey(publicKey)
	if err != nil {
		return ""
	}
	return configmgr.FingerprintOf(pub).String()
}

func cmTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func cmTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return cmTime(*t)
}

// --- Machine protocol ------------------------------------------------------

// handleAPICMRegister is POST /api/v1/cm/register.
func (s *Server) handleAPICMRegister(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.cmPeerGate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req configmgr.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.Machine = strings.TrimSpace(req.Machine)
	req.Environment = strings.TrimSpace(req.Environment)
	req.App = strings.TrimSpace(req.App)
	req.Role = strings.TrimSpace(req.Role)
	req.Version = strings.TrimSpace(req.Version)
	req.PublicKey = strings.TrimSpace(req.PublicKey)

	if req.Machine == "" || req.Environment == "" || req.App == "" || req.Role == "" || req.Version == "" {
		writeJSONError(w, http.StatusBadRequest,
			"a registration needs machine, environment, app, role and version")
		return
	}
	pub, err := configmgr.ParseMachinePublicKey(req.PublicKey)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	machine, err := s.users.MachineByName(r.Context(), req.Machine)
	switch {
	case errors.Is(err, db.ErrNotFound):
		machine, err = s.users.RegisterMachine(r.Context(), req.Machine, req.Environment, pub.Bytes())
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not enrol machine: "+err.Error())
			return
		}
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "could not look up machine: "+err.Error())
		return
	default:
		// A name already enrolled with a DIFFERENT key is refused rather than
		// re-keyed. Silently accepting a new key here would mean anything that
		// can reach this endpoint takes over an existing machine's identity and
		// has the next approval wrapped to a key it holds — so there is no
		// re-key path in internal/db and there must not be one.
		//
		// A genuine reinstall needs an operator to REMOVE the machine, which
		// frees the name: handleAPICMMachine's DELETE, or `hz cm remove
		// <machine>`. The refusal names it, because an instruction to perform
		// an action the reader cannot find is where this used to end — the
		// route and the command did not exist, so a rebuilt box could never
		// come back under its own name.
		//
		// The enrolled key's fingerprint is deliberately NOT in this answer.
		// The caller of a conflicting register is by definition not the machine
		// that holds that key, and the fingerprint is the value an operator
		// compares against a box's console — so it goes to admins, who have
		// `hz cm machines`, and not to whoever reached this endpoint.
		if !bytes.Equal(pub.Bytes(), machine.PublicKey) {
			writeJSONError(w, http.StatusConflict,
				"machine "+req.Machine+" is enrolled with a different public key; "+
					"an operator must remove it before it can re-enrol: "+
					"`hz cm remove "+req.Machine+"`")
			return
		}
	}

	// Peer identity is not authority, but a mismatch between the box's
	// WireGuard peer and the name it claims is the only signal hz has against a
	// machine-name squat, so it goes in the log where the queue can be checked
	// against it.
	if peer != req.Machine {
		slog.Info("cm registration name differs from vpn peer",
			"peer", peer, "machine", req.Machine, "app", req.App, "role", req.Role)
	}

	if err := s.users.RecordMachineSeen(r.Context(), machine.ID); err != nil {
		slog.Warn("cm record machine seen", "machine", machine.ID, "error", err)
	}

	reg, err := s.users.UpsertRegistration(r.Context(), machine.ID,
		req.Environment, req.App, req.Role, req.Version)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not register: "+err.Error())
		return
	}

	writeJSON(w, s.cmRegisterResponse(r, machine, reg))
}

// handleAPICMRegisterPoll is GET /api/v1/cm/register/{id}.
//
// Any VPN peer may poll any registration id it can name. That is deliberate
// rather than overlooked: the only thing worth anything here is the wrapped
// key, which opens with a private key that never left the box it was wrapped
// to, and the agent authenticates the address it asked for rather than one read
// back out of this answer. Binding the poll to the caller would need a
// peer-to-machine mapping hz does not have — peer identity is by source IP, so
// it would add a check that authenticates nothing.
func (s *Server) handleAPICMRegisterPoll(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cmPeerGate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/cm/register/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusBadRequest, "registration id required")
		return
	}

	reg, err := s.users.RegistrationByID(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		// Unknown, not denied: an hz restored from backup has never heard of a
		// registration it once approved, and a box in that position must boot
		// its cache rather than refuse.
		writeJSONError(w, http.StatusNotFound, "no such registration")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read registration: "+err.Error())
		return
	}

	machine, err := s.users.MachineByID(r.Context(), reg.MachineID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read machine: "+err.Error())
		return
	}

	writeJSON(w, s.cmRegisterResponse(r, machine, reg))
}

// cmRegisterResponse builds the polled answer, attaching the wrapped key only
// once the registration is approved.
func (s *Server) cmRegisterResponse(r *http.Request, machine *db.Machine, reg *db.Registration) configmgr.RegisterResponse {
	resp := configmgr.RegisterResponse{
		ID:          reg.ID,
		State:       string(reg.State),
		Fingerprint: cmFingerprint(machine.PublicKey),
		MachineID:   machine.ID,
	}
	if reg.State != db.RegistrationApproved {
		return resp
	}
	wrapped, err := s.users.RegistrationWrappedKey(r.Context(), reg.ID)
	if err != nil {
		// An approved registration with no grant is a bug, not a refusal. Say
		// so in the log and answer without the key: the agent then sees
		// "approved, no key", which is what it is.
		slog.Error("cm approved registration holds no wrapped key", "registration", reg.ID, "error", err)
		return resp
	}
	resp.WrappedEnvKey = configmgr.EncodeEnvelope(wrapped)
	return resp
}

// handleAPICMConfig is POST /api/v1/cm/config.
func (s *Server) handleAPICMConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cmPeerGate(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req configmgr.ConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.Machine = strings.TrimSpace(req.Machine)
	req.Environment = strings.TrimSpace(req.Environment)
	req.App = strings.TrimSpace(req.App)
	req.Role = strings.TrimSpace(req.Role)
	req.Version = strings.TrimSpace(req.Version)
	req.Build = strings.TrimSpace(req.Build)

	if req.Machine == "" || req.Environment == "" || req.App == "" || req.Role == "" || req.Version == "" {
		writeJSONError(w, http.StatusBadRequest,
			"a config request needs machine, environment, app, role and version")
		return
	}

	machine, err := s.users.MachineByName(r.Context(), req.Machine)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such machine: "+req.Machine)
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not look up machine: "+err.Error())
		return
	}

	reg, err := s.users.RegistrationAt(r.Context(), machine.ID, req.Environment, req.App, req.Role)
	if errors.Is(err, db.ErrNotFound) {
		// The registration fixes an environment; the request carries one too.
		// Resolving on the request would let a mistyped --env succeed, and
		// resolving on the registration would hand the box another
		// environment's config. Neither is right, so the mismatch is refused
		// BY NAME — otherwise the box sits failing to decrypt with nothing
		// anywhere explaining why.
		if other := s.cmOtherEnvironment(r, machine.ID, req.App, req.Role); other != "" {
			writeJSONError(w, http.StatusBadRequest,
				"environment mismatch: "+req.Machine+" is registered in "+other+
					" for "+req.App+"/"+req.Role+", but asked for "+req.Environment+
					". Fix --env on the box, or approve a registration in "+req.Environment+".")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no registration for "+
			req.Environment+"/"+req.App+"/"+req.Role+" on "+req.Machine)
		return
	} else if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// What the box says it is RUNNING — the observed half of the
	// desired/observed split (plan/architecture.md "Versions"). It is recorded
	// HERE, before the admission switch, for two reasons. The report is a fact
	// about the box independent of whether hz will serve it anything, so a
	// denied or pending registration that is still booting 1.2.1 is worth
	// seeing rather than invisible. And this is the request a running box
	// repeats, which is what makes observed_at a freshness signal without a
	// heartbeat endpoint existing.
	//
	// A failure here NEVER fails the request. Bookkeeping must not stand
	// between a box and its config; the worst case is a stale timestamp in a
	// listing, against an outage on every box on the fleet.
	if err := s.users.RecordObservedVersion(r.Context(), reg.ID, req.Version, req.Build); err != nil {
		slog.Warn("cm record observed version", "registration", reg.ID,
			"version", req.Version, "error", err)
	}

	switch reg.State {
	case db.RegistrationDenied:
		// The one positive denial in the protocol, and the only answer a box
		// may refuse to boot on.
		msg := "registration denied"
		if reg.DeniedReason != "" {
			msg += ": " + reg.DeniedReason
		}
		writeJSONError(w, http.StatusForbidden, msg)
		return
	case db.RegistrationApproved:
	default:
		writeJSONError(w, http.StatusConflict, "registration is pending approval")
		return
	}

	// Resolution runs on the REGISTRATION's address, which the check above has
	// already established the request agrees with.
	res, err := s.users.ResolveConfig(r.Context(), reg.Environment, reg.App, reg.Role, req.Version)
	if errors.Is(err, db.ErrNoConfigMatches) {
		// A named failure, never a hang, and never mistaken for a pending
		// approval (plan/config-manager.md, resolution rule 3).
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	} else if errors.Is(err, db.ErrInvalidVersion) || errors.Is(err, db.ErrInvalidAddress) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not resolve config: "+err.Error())
		return
	}

	entries := make([]configmgr.ConfigEntry, 0, len(res.Config.Values))
	for _, v := range res.Config.Values {
		if v.Tombstoned() {
			// A config with half its keys is not a config. Serving the rest
			// would drop a declared key, and an omitted key falls back to the
			// app's compiled default — the founding bug this exists to stop.
			writeJSONError(w, http.StatusGone,
				"config "+res.Config.ID+" has a destroyed value for "+v.Key+
					"; bless a replacement config at "+reg.Environment+"/"+reg.App+"/"+reg.Role)
			return
		}
		entries = append(entries, configmgr.ConfigEntry{
			Key:     v.Key,
			Binding: string(v.Binding),
			Sealed:  configmgr.EncodeEnvelope(v.Ciphertext),
		})
	}

	writeJSON(w, configmgr.ConfigResponse{
		ConfigID: res.Config.ID,
		Sequence: res.Config.Seq,
		MinVer:   res.Config.MinVer,
		MaxVer:   res.Config.MaxVer,
		Entries:  entries,
	})
}

// cmOtherEnvironment reports an environment this machine IS registered in for
// (app, role), when it is not registered in the one asked for. Empty means the
// machine has never registered at that app/role at all, which is a different
// answer and gets a different status.
func (s *Server) cmOtherEnvironment(r *http.Request, machineID, app, role string) string {
	regs, err := s.users.ListRegistrationsForMachine(r.Context(), machineID)
	if err != nil {
		return ""
	}
	app = strings.ToLower(strings.TrimSpace(app))
	role = strings.ToLower(strings.TrimSpace(role))
	for _, reg := range regs {
		if reg.App == app && reg.Role == role {
			return reg.Environment
		}
	}
	return ""
}

// --- Admin: the approval queue ---------------------------------------------

// handleAPICMRegistrations is GET /api/v1/cm/registrations?state=pending.
func (s *Server) handleAPICMRegistrations(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get(apitypes.CMQueryState))
	if state == "" {
		state = configmgr.StatePending
	}
	switch state {
	case configmgr.StatePending, configmgr.StateApproved, configmgr.StateDenied:
	default:
		writeJSONError(w, http.StatusBadRequest,
			"state must be pending, approved or denied")
		return
	}

	regs, err := s.users.ListRegistrationsByState(r.Context(), db.RegistrationState(state))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list registrations: "+err.Error())
		return
	}

	machines, err := s.users.ListMachines(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list machines: "+err.Error())
		return
	}
	byID := make(map[string]db.Machine, len(machines))
	for _, m := range machines {
		byID[m.ID] = m
	}

	out := make([]apitypes.CMRegistrationResp, 0, len(regs))
	for _, reg := range regs {
		m := byID[reg.MachineID]
		out = append(out, cmRegistrationResp(&m, &reg))
	}
	writeJSON(w, out)
}

// cmRegistrationResp projects one queue row.
//
// There is no wrapped key here and there is no way to put one here: the
// projection is built from db.Registration, which has no field for key
// material, and this function never calls RegistrationWrappedKey. A queue that
// cannot select the blob cannot leak it into a log, a screenshot or a bug
// report.
func cmRegistrationResp(m *db.Machine, reg *db.Registration) apitypes.CMRegistrationResp {
	out := apitypes.CMRegistrationResp{
		ID:          reg.ID,
		MachineID:   reg.MachineID,
		Environment: reg.Environment,
		App:         reg.App,
		Role:        reg.Role,
		Version:     reg.Version,
		State:       string(reg.State),

		ObservedVersion: reg.ObservedVersion,
		ObservedBuild:   reg.ObservedBuild,
		ObservedAt:      cmTimePtr(reg.ObservedAt),

		WrapKeyID:    reg.WrapKeyID,
		ApprovedBy:   reg.ApprovedBy,
		ApprovedAt:   cmTimePtr(reg.ApprovedAt),
		DeniedReason: reg.DeniedReason,
		CreatedAt:    cmTime(reg.CreatedAt),
		LastSeenAt:   cmTimePtr(reg.LastSeenAt),
	}
	if m != nil && m.ID != "" {
		out.MachineName = m.Name
		out.Fingerprint = cmFingerprint(m.PublicKey)
		// Surfaced only when it disagrees with the registration: an enrolment
		// environment that matches says nothing, one that differs is a squat
		// signal the approver should see.
		if m.EnrolledEnvironment != reg.Environment {
			out.EnrolledEnvironment = m.EnrolledEnvironment
		}
	}
	return out
}

// handleAPICMRegistrationAction is POST /api/v1/cm/registrations/{id}/approve
// and /deny.
func (s *Server) handleAPICMRegistrationAction(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/cm/registrations/")
	id, action, found := strings.Cut(rest, "/")
	if id == "" {
		writeJSONError(w, http.StatusNotFound,
			"expected /api/v1/cm/registrations/{id}, or {id}/public-key, /approve or /deny")
		return
	}
	// A bare {id} reads one registration. It was missing, so `hz cm approve`
	// 404'd on its very first call — the command is the first line of
	// `hz cm pending`'s own instructions, so approval was dead for every
	// operator. Same class as the promote path: a URL agreed in two places and
	// enforced in neither.
	if !found {
		action = "get"
	}
	if strings.Contains(action, "/") {
		writeJSONError(w, http.StatusNotFound,
			"expected /api/v1/cm/registrations/{id}, or {id}/public-key, /approve or /deny")
		return
	}
	// public-key and the bare read are GETs; approve and deny write.
	if action == "public-key" || action == "get" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
	} else if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	reg, err := s.users.RegistrationByID(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such registration")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read registration: "+err.Error())
		return
	}
	machine, err := s.users.MachineByID(r.Context(), reg.MachineID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read machine: "+err.Error())
		return
	}

	switch action {
	case "get":
		writeJSON(w, cmRegistrationResp(machine, reg))
	case "public-key":
		s.cmPublicKey(w, machine)
	case "approve":
		s.cmApprove(w, r, machine, reg)
	case "deny":
		s.cmDeny(w, r, machine, reg)
	default:
		writeJSONError(w, http.StatusNotFound, "expected nothing, public-key, approve or deny")
	}
}

// cmPublicKey hands out the machine's public key so an approver can wrap to it.
//
// It is a separate read rather than a field on the queue listing on purpose.
// The approver must wrap to the KEY, and must compute the fingerprint it shows
// an operator FROM THAT KEY — a fingerprint hz reports alongside a key hz chose
// proves nothing, because a compromised hz would simply report the fingerprint
// of the key it substituted. Keeping the two apart makes the queue a display
// surface and this the one place key bytes enter the ceremony, so a client that
// verifies the fingerprint it derived here is verifying the thing it is about
// to encrypt to.
//
// The key is public and safe to serve; the defence is not secrecy, it is that
// the operator compares a fingerprint derived from these exact bytes against
// what the box itself printed.
func (s *Server) cmPublicKey(w http.ResponseWriter, machine *db.Machine) {
	pub, err := ecdh.P256().NewPublicKey(machine.PublicKey)
	if err != nil {
		// Stored bytes that are not a point on the curve mean the row is
		// corrupt, and wrapping to it would produce a blob nothing can open.
		writeJSONError(w, http.StatusInternalServerError,
			"stored public key for this machine is not a valid P-256 point")
		return
	}
	writeJSON(w, struct {
		PublicKey string `json:"publicKey"`
	}{PublicKey: configmgr.MarshalMachinePublicKey(pub)})
}

// cmApprove grants one address's wrapped environment key to one machine.
//
// The blob is verified before it is stored. hz cannot open it and never could,
// but a wrapped-key envelope carries its recipient fingerprint in cleartext and
// hz holds the machine's public key, so the compare is free — and without it
// approval is an unvalidated flag rather than a cryptographic grant: a blob
// wrapped to a stale or wrong key stores cleanly, and the box bricks at its
// next boot where only someone standing at that box can see why.
func (s *Server) cmApprove(w http.ResponseWriter, r *http.Request, machine *db.Machine, reg *db.Registration) {
	var req apitypes.CMApproveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.WrappedEnvKey = strings.TrimSpace(req.WrappedEnvKey)
	req.WrapKeyID = strings.TrimSpace(req.WrapKeyID)

	if req.WrappedEnvKey == "" {
		writeJSONError(w, http.StatusBadRequest, "approving needs a wrapped environment key")
		return
	}
	// The key id becomes a glob in the client keystore, so it is validated as a
	// canonical id here rather than stored as whatever the caller typed.
	if _, err := configmgr.ParseKeyID(req.WrapKeyID); err != nil {
		writeJSONError(w, http.StatusBadRequest, "wrapKeyId: "+err.Error())
		return
	}

	blob, err := configmgr.DecodeEnvelope(req.WrappedEnvKey)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "wrappedEnvKey: "+err.Error())
		return
	}
	header, err := configmgr.ParseEnvelopeHeader(blob)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "wrappedEnvKey: "+err.Error())
		return
	}
	if header.Kind != configmgr.KindWrappedEnvKey {
		writeJSONError(w, http.StatusBadRequest,
			"wrappedEnvKey: expected a wrapped environment key, got "+header.Kind.String())
		return
	}
	pub, err := ecdh.P256().NewPublicKey(machine.PublicKey)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"machine "+machine.Name+" has an unreadable public key: "+err.Error())
		return
	}
	want := configmgr.FingerprintOf(pub)
	if header.Recipient != want {
		writeJSONError(w, http.StatusBadRequest,
			"wrappedEnvKey is addressed to "+header.Recipient.String()+
				" but "+machine.Name+"'s key is "+want.String()+
				"; that blob would store cleanly and brick the box at its next boot")
		return
	}

	actor, ok := s.cmActor(w, r)
	if !ok {
		return
	}
	updated, err := s.users.ApproveRegistration(r.Context(), reg.ID, blob, req.WrapKeyID, actor)
	if errors.Is(err, db.ErrRegistrationDenied) {
		writeJSONError(w, http.StatusConflict,
			"registration is denied; remove it so the box re-registers and is reviewed again")
		return
	} else if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such registration")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not approve: "+err.Error())
		return
	}

	slog.Info("cm registration approved", "registration", updated.ID, "machine", machine.Name,
		"address", updated.Environment+"/"+updated.App+"/"+updated.Role,
		"wrap_key_id", updated.WrapKeyID, "by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, cmRegistrationResp(machine, updated))
}

// cmDeny refuses a registration. Clearing hz's copy of the wrapped key is not a
// revocation — a box that was ever approved holds the key unwrapped on its own
// disk — so nothing here or in the UI may describe it as one.
func (s *Server) cmDeny(w http.ResponseWriter, r *http.Request, machine *db.Machine, reg *db.Registration) {
	var req apitypes.CMDenyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		writeJSONError(w, http.StatusBadRequest,
			"denying needs a reason; a denial with no stated cause is indistinguishable from a mistake six months later")
		return
	}

	updated, err := s.users.DenyRegistration(r.Context(), reg.ID, req.Reason)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such registration")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not deny: "+err.Error())
		return
	}

	slog.Info("cm registration denied", "registration", updated.ID, "machine", machine.Name,
		"address", updated.Environment+"/"+updated.App+"/"+updated.Role,
		"reason", req.Reason, "by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, cmRegistrationResp(machine, updated))
}

// --- Admin: enrolled machines ----------------------------------------------

// handleAPICMMachines is GET /api/v1/cm/machines.
//
// The listing exists because removal needs somewhere to start. Until now the
// only view of an enrolled box was the approval queue, which is indexed by
// registration and shows nothing about a machine that has no pending row — so
// an operator told "an operator must remove it" could not even find the thing
// they were told to remove.
func (s *Server) handleAPICMMachines(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	machines, err := s.users.ListMachines(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list machines: "+err.Error())
		return
	}

	out := make([]apitypes.CMMachineResp, 0, len(machines))
	for i := range machines {
		resp, err := s.cmMachineResp(r, &machines[i])
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, resp)
	}
	writeJSON(w, out)
}

// handleAPICMMachine is GET and DELETE /api/v1/cm/machines/{name-or-id}.
//
// GET is the removal PREVIEW and DELETE is the removal; they answer the same
// path on purpose, so the thing an operator read and the thing they destroyed
// cannot be two different boxes resolved by two different rules.
//
// The reference is a name OR an id, resolved name-first, because the name is
// what an operator has: the re-enrol refusal says "machine <name> is enrolled
// with a different public key", and making them translate that into an id
// before they can act on it is the gap this route exists to close. A machine
// whose name cannot survive a path segment is reachable by id, which the
// listing carries for every row.
func (s *Server) handleAPICMMachine(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, apitypes.CMPathMachines+"/")
	ref, err := url.PathUnescape(ref)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "machine name or id is not valid path escaping")
		return
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "/") {
		writeJSONError(w, http.StatusNotFound,
			"expected /api/v1/cm/machines/{name-or-id}")
		return
	}

	machine, err := s.cmResolveMachine(r, ref)
	if errors.Is(err, db.ErrNotFound) {
		// A JSON 404, which is the handler saying "no such box" — distinct from
		// the mux's plain-text 404 for a path that is not routed at all. An
		// operator removing a machine twice lands here, and "it is already
		// gone" is the answer, not an error to debug.
		writeJSONError(w, http.StatusNotFound, "no machine named or identified by "+ref)
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not look up machine: "+err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		resp, err := s.cmMachineResp(r, machine)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, resp)
	case http.MethodDelete:
		s.cmRemoveMachine(w, r, machine)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or DELETE required")
	}
}

// cmResolveMachine resolves a name first, then an id.
//
// Name-first rather than id-first because a name is what an operator types and
// an id is what a script holds, and only one of the two can be mistyped into
// something that exists. A machine deliberately named after another's id
// therefore wins for its own name, which is the caller's evident intent.
func (s *Server) cmResolveMachine(r *http.Request, ref string) (*db.Machine, error) {
	machine, err := s.users.MachineByName(r.Context(), ref)
	if err == nil {
		return machine, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	return s.users.MachineByID(r.Context(), ref)
}

// cmMachineResp projects one machine together with everything a removal would
// take with it. No key material, by the same construction as the queue: the
// registration projection has no field for a wrapped key, and secrets are
// listed by name only.
func (s *Server) cmMachineResp(r *http.Request, m *db.Machine) (apitypes.CMMachineResp, error) {
	out := apitypes.CMMachineResp{
		ID:                  m.ID,
		Name:                m.Name,
		EnrolledEnvironment: m.EnrolledEnvironment,
		Fingerprint:         cmFingerprint(m.PublicKey),
		CreatedAt:           cmTime(m.CreatedAt),
		LastSeenAt:          cmTimePtr(m.LastSeenAt),
	}

	regs, err := s.users.ListRegistrationsForMachine(r.Context(), m.ID)
	if err != nil {
		return out, errors.New("could not list registrations for " + m.Name + ": " + err.Error())
	}
	for i := range regs {
		out.Registrations = append(out.Registrations, cmRegistrationResp(m, &regs[i]))
	}

	secrets, err := s.users.ListMachineSecretKeys(r.Context(), m.ID)
	if err != nil {
		return out, errors.New("could not list secrets for " + m.Name + ": " + err.Error())
	}
	for _, sec := range secrets {
		out.SecretKeys = append(out.SecretKeys, sec.Key)
	}
	return out, nil
}

// cmRemoveMachine deletes an enrolled box so its name can be enrolled again.
//
// This is the missing half of the re-enrol refusal in handleAPICMRegister. That
// refusal is correct — silently re-keying a name would let anything that can
// reach /register take over a machine's identity — but it told an operator to
// perform an action nothing implemented, so a box that lost its state directory
// could never come back under its own name. A rebuilt VM is the NORMAL case for
// a config manager, not an edge one.
//
// THREE THINGS THIS DELIBERATELY DOES NOT DO:
//
//  1. It does not refuse a machine with live grants. Refusing would break the
//     exact case it exists for: a rebuilt box's old registrations are approved,
//     and they are what has to go. The guard is the confirm token below, not a
//     precondition that would only ever fire on the intended use.
//  2. It does not revoke anything, and no message here may imply it does. The
//     box already holds the unwrapped environment key on its own disk. Removal
//     stops hz serving that box; rotating the key is the only revocation. See
//     db.DeleteMachine.
//  3. It does not require an account the way approve and bless do. Those need
//     one because cm_configs.created_by and cm_registrations.approved_by are
//     foreign keys with nowhere else to record a name — a removal has no row
//     left to attribute against, so its record is the audit log line below,
//     which the shared admin token can populate just as well.
func (s *Server) cmRemoveMachine(w http.ResponseWriter, r *http.Request, machine *db.Machine) {
	// The operator has to name the box back. A DELETE that arrives without it
	// is refused rather than obeyed, so a mis-aimed client or a half-remembered
	// curl cannot destroy a fleet member in one call — and the CLI's typed-name
	// prompt becomes load-bearing rather than decorative.
	confirm := strings.TrimSpace(r.URL.Query().Get(apitypes.CMQueryConfirm))
	if confirm == "" {
		writeJSONError(w, http.StatusBadRequest,
			"removing "+machine.Name+" needs "+apitypes.CMQueryConfirm+"="+machine.Name+
				"; read GET "+apitypes.CMPathMachines+"/"+machine.Name+" first to see what it would destroy")
		return
	}
	if confirm != machine.Name {
		writeJSONError(w, http.StatusBadRequest,
			"refusing: "+apitypes.CMQueryConfirm+"="+confirm+" does not name the machine at this path ("+
				machine.Name+"). Nothing was removed")
		return
	}

	// Counted BEFORE the delete, because after it the rows are gone and there
	// is nothing left to count. The numbers are what the operator checks their
	// preview against.
	regs, err := s.users.ListRegistrationsForMachine(r.Context(), machine.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list registrations: "+err.Error())
		return
	}
	secrets, err := s.users.ListMachineSecretKeys(r.Context(), machine.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list secrets: "+err.Error())
		return
	}
	resp := apitypes.CMMachineRemovedResp{
		ID:                   machine.ID,
		Name:                 machine.Name,
		RegistrationsRemoved: len(regs),
		SecretsRemoved:       len(secrets),
	}
	addresses := make([]string, 0, len(regs))
	for _, reg := range regs {
		addresses = append(addresses, reg.Environment+"/"+reg.App+"/"+reg.Role)
		if reg.State == db.RegistrationApproved {
			resp.GrantsRemoved++
		}
	}
	secretKeys := make([]string, 0, len(secrets))
	for _, sec := range secrets {
		secretKeys = append(secretKeys, sec.Key)
	}

	if err := s.users.DeleteMachine(r.Context(), machine.ID); errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "machine "+machine.Name+" is already gone")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not remove machine: "+err.Error())
		return
	}

	// The addresses and secret key NAMES go in the log, because this is the one
	// destructive act here whose scope cannot be reconstructed afterwards: the
	// rows that would have answered "what did that take with it" are the rows
	// it deleted. Names only — a secret's value has never been readable by hz.
	slog.Warn("cm machine removed", "machine", machine.Name, "id", machine.ID,
		"fingerprint", cmFingerprint(machine.PublicKey),
		"registrations", strings.Join(addresses, ","), "grants", resp.GrantsRemoved,
		"secret_keys", strings.Join(secretKeys, ","),
		"by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, resp)
}

// --- Admin: configs ---------------------------------------------------------

// handleAPICMConfigs is POST /api/v1/cm/configs (bless) and
// GET /api/v1/cm/configs?env=&app=&role= (headers only).
func (s *Server) handleAPICMConfigs(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.cmListConfigs(w, r)
	case http.MethodPost:
		s.cmCreateConfig(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST required")
	}
}

func (s *Server) cmListConfigs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env := strings.TrimSpace(q.Get(apitypes.CMQueryEnv))
	app := strings.TrimSpace(q.Get(apitypes.CMQueryApp))
	role := strings.TrimSpace(q.Get(apitypes.CMQueryRole))
	if env == "" || app == "" || role == "" {
		writeJSONError(w, http.StatusBadRequest, "env, app and role are required")
		return
	}

	configs, err := s.users.ListConfigsForAddress(r.Context(), env, app, role)
	if errors.Is(err, db.ErrInvalidAddress) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list configs: "+err.Error())
		return
	}

	// Headers only. ListConfigsForAddress populates no values, so this listing
	// has nothing to disclose even though every value is unopenable ciphertext.
	out := make([]apitypes.CMConfigResp, 0, len(configs))
	for _, c := range configs {
		out = append(out, cmConfigResp(c))
	}
	writeJSON(w, out)
}

func (s *Server) cmCreateConfig(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CMCreateConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.Environment = strings.TrimSpace(req.Environment)
	req.App = strings.TrimSpace(req.App)
	req.Role = strings.TrimSpace(req.Role)
	req.MinVer = strings.TrimSpace(req.MinVer)
	req.MaxVer = strings.TrimSpace(req.MaxVer)

	if req.Environment == "" || req.App == "" || req.Role == "" {
		writeJSONError(w, http.StatusBadRequest, "env, app and role are required")
		return
	}
	if len(req.Values) == 0 {
		writeJSONError(w, http.StatusBadRequest, "a config needs at least one value")
		return
	}

	values := make([]db.ConfigValue, 0, len(req.Values))
	for _, v := range req.Values {
		v.Key = strings.TrimSpace(v.Key)
		v.Binding = strings.TrimSpace(v.Binding)
		v.KeyID = strings.TrimSpace(v.KeyID)
		v.Sealed = strings.TrimSpace(v.Sealed)
		v.SourceConfigID = strings.TrimSpace(v.SourceConfigID)

		if v.Key == "" {
			writeJSONError(w, http.StatusBadRequest, "a value needs a key")
			return
		}
		switch v.Binding {
		case configmgr.BindingInvariant, configmgr.BindingEnv:
		default:
			writeJSONError(w, http.StatusBadRequest,
				"key "+v.Key+": binding must be invariant or env")
			return
		}
		sealed, err := configmgr.DecodeEnvelope(v.Sealed)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "key "+v.Key+": "+err.Error())
			return
		}
		header, err := configmgr.ParseEnvelopeHeader(sealed)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "key "+v.Key+": "+err.Error())
			return
		}
		// Same free check as approval, pointed at a config value: a value that
		// says it was sealed under one key and was sealed under another opens
		// nowhere, and the box discovers that instead of the operator.
		if header.Kind != configmgr.KindEnvSealed {
			writeJSONError(w, http.StatusBadRequest,
				"key "+v.Key+": expected an environment-sealed value, got "+header.Kind.String())
			return
		}
		if id, err := configmgr.ParseKeyID(v.KeyID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "key "+v.Key+": keyId: "+err.Error())
			return
		} else if id != header.KeyID {
			writeJSONError(w, http.StatusBadRequest,
				"key "+v.Key+": declared keyId "+v.KeyID+" but the envelope names "+header.KeyID.String())
			return
		}

		values = append(values, db.ConfigValue{
			Key:            v.Key,
			Binding:        db.Binding(v.Binding),
			Ciphertext:     sealed,
			KeyID:          header.KeyID.String(),
			SourceConfigID: v.SourceConfigID,
		})
	}

	actor, ok := s.cmActor(w, r)
	if !ok {
		return
	}
	cfg, err := s.users.CreateConfig(r.Context(), req.Environment, req.App, req.Role,
		req.MinVer, req.MaxVer, actor, values)
	switch {
	case errors.Is(err, db.ErrInvalidAddress),
		errors.Is(err, db.ErrInvalidVersion),
		errors.Is(err, db.ErrInvalidVersionRange),
		errors.Is(err, db.ErrNoOpenRange),
		errors.Is(err, db.ErrValueOmitted):
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusBadRequest, "could not bless config: "+err.Error())
		return
	}

	slog.Info("cm config blessed", "config", cfg.ID,
		"address", cfg.Environment+"/"+cfg.App+"/"+cfg.Role,
		"range", cfg.MinVer+"-"+cfg.MaxVer, "seq", cfg.Seq,
		"keys", len(cfg.Values), "by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, cmConfigRespWithValues(*cfg))
}

// handleAPICMConfigByID is GET /api/v1/cm/configs/{id}.
func (s *Server) handleAPICMConfigByID(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/cm/configs/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusBadRequest, "config id required")
		return
	}

	cfg, err := s.users.GetConfig(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such config")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read config: "+err.Error())
		return
	}
	writeJSON(w, cmConfigRespWithValues(*cfg))
}

func cmConfigResp(c db.Config) apitypes.CMConfigResp {
	return apitypes.CMConfigResp{
		ID:          c.ID,
		Environment: c.Environment,
		App:         c.App,
		Role:        c.Role,
		MinVer:      c.MinVer,
		MaxVer:      c.MaxVer,
		Sequence:    c.Seq,
		CreatedAt:   cmTime(c.CreatedAt),
		CreatedBy:   c.CreatedBy,
	}
}

// cmConfigRespWithValues adds the sealed values.
//
// Sealed travels because a CLI holding the key decrypts locally; hz holds no
// key and the UI has none, so what the browser receives is bytes it cannot
// read. A tombstoned value keeps its row, its key name and its lineage and
// loses its bytes — the omitempty is what makes that visible rather than
// looking like an empty value.
func cmConfigRespWithValues(c db.Config) apitypes.CMConfigResp {
	out := cmConfigResp(c)
	out.Values = make([]apitypes.CMConfigValueResp, 0, len(c.Values))
	for _, v := range c.Values {
		val := apitypes.CMConfigValueResp{
			Key:            v.Key,
			Binding:        string(v.Binding),
			KeyID:          v.KeyID,
			Origin:         string(v.Origin),
			SourceConfigID: v.SourceConfigID,
			TombstonedAt:   cmTimePtr(v.TombstonedAt),
			TombstonedBy:   v.TombstonedBy,
		}
		if !v.Tombstoned() {
			val.Sealed = configmgr.EncodeEnvelope(v.Ciphertext)
		}
		out.Values = append(out.Values, val)
	}
	return out
}

// handleAPICMResolve is GET /api/v1/cm/resolve?env=&app=&role=&version=.
func (s *Server) handleAPICMResolve(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	q := r.URL.Query()
	env := strings.TrimSpace(q.Get(apitypes.CMQueryEnv))
	app := strings.TrimSpace(q.Get(apitypes.CMQueryApp))
	role := strings.TrimSpace(q.Get(apitypes.CMQueryRole))
	version := strings.TrimSpace(q.Get(apitypes.CMQueryVersion))
	if env == "" || app == "" || role == "" || version == "" {
		writeJSONError(w, http.StatusBadRequest, "env, app, role and version are required")
		return
	}

	res, err := s.users.ResolveConfig(r.Context(), env, app, role, version)
	if errors.Is(err, db.ErrNoConfigMatches) {
		// Zero matches is an answer, not an error: CMResolveResp has a field
		// for it precisely so the inspection surface can say "nothing covers
		// this version" instead of returning a bare 404 the caller has to
		// interpret.
		writeJSON(w, apitypes.CMResolveResp{Error: err.Error()})
		return
	} else if errors.Is(err, db.ErrInvalidAddress) || errors.Is(err, db.ErrInvalidVersion) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not resolve: "+err.Error())
		return
	}

	winner := cmConfigRespWithValues(res.Config)
	out := apitypes.CMResolveResp{Winner: &winner}
	// Shadowed candidates are headers: rule 2 asks that the overlap be
	// inspectable, not that every loser's ciphertext ride along.
	for _, c := range res.Shadowed {
		out.Shadowed = append(out.Shadowed, cmConfigResp(c))
	}
	writeJSON(w, out)
}

// handleAPICMPromotionGate is GET /api/v1/cm/promote/gate?config=&target=.
//
// It reads no values, and that is the property that makes it work at all: it
// asks whether a key is BOUND in the target, never what that key holds. hz can
// read nothing, so a gate that needed plaintext would be a gate that could
// never run.
func (s *Server) handleAPICMPromotionGate(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	q := r.URL.Query()
	configID := strings.TrimSpace(q.Get(apitypes.CMQueryConfigID))
	target := strings.TrimSpace(q.Get(apitypes.CMQueryTarget))
	if configID == "" || target == "" {
		writeJSONError(w, http.StatusBadRequest, "config and target are required")
		return
	}

	src, err := s.users.GetConfig(r.Context(), configID)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no such config")
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read config: "+err.Error())
		return
	}

	bound, err := s.cmBoundKeys(r, target, src.App, src.Role)
	if errors.Is(err, db.ErrInvalidAddress) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read the target address: "+err.Error())
		return
	}

	resp := apitypes.CMPromotionGateResp{SourceConfigID: src.ID, TargetEnv: target}
	for _, v := range src.Values {
		switch v.Binding {
		case db.BindingInvariant:
			// Carries by client-side re-seal; hz is not in that path.
			resp.Promotes = append(resp.Promotes, v.Key)
		default:
			if !bound[v.Key] {
				resp.Blocked = append(resp.Blocked, v.Key)
			}
		}
	}
	resp.OK = len(resp.Blocked) == 0
	writeJSON(w, resp)
}

// cmBoundKeys reports which key NAMES have a live value at an address, across
// every config ever blessed there — a value bound by an older config is still
// bound. A tombstoned value is not bound: its bytes are gone, so promoting
// against it would pass a gate and then fail on the box.
func (s *Server) cmBoundKeys(r *http.Request, environment, app, role string) (map[string]bool, error) {
	configs, err := s.users.ListConfigsForAddress(r.Context(), environment, app, role)
	if err != nil {
		return nil, err
	}
	bound := make(map[string]bool)
	for _, c := range configs {
		full, err := s.users.GetConfig(r.Context(), c.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range full.Values {
			if !v.Tombstoned() {
				bound[v.Key] = true
			}
		}
	}
	return bound, nil
}

// handleAPICMCurrentKey is GET/PUT /api/v1/cm/current-key?env=&app=&role=.
//
// hz stores an ID here, never a key. The id is derived from key material the
// client holds and is safe to publish, the way a fingerprint is.
//
// The pointer is ADVISORY and clients must not obey it. They refuse to SEAL
// when their local key disagrees, and only warn when OPENING. Obeying it would
// let a compromised hz pin every client to a key it had already stolen; merely
// warning in both directions would let anyone who can drop a file into a
// keystore become the sealing key. hz's job is only to carry one shared answer
// so a rotation reaches every client instead of each laptop drifting alone.
//
// A GET with no pointer set answers 404 rather than an empty pointer: "nobody
// has announced one" and "the current key is X" are different facts, and a
// client told the wrong one seals under whatever its filesystem offers.
func (s *Server) handleAPICMCurrentKey(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	env := strings.TrimSpace(r.URL.Query().Get(apitypes.CMQueryEnv))
	app := strings.TrimSpace(r.URL.Query().Get(apitypes.CMQueryApp))
	role := strings.TrimSpace(r.URL.Query().Get(apitypes.CMQueryRole))
	if env == "" || app == "" || role == "" {
		writeJSONError(w, http.StatusBadRequest, "env, app and role are required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cur, err := s.users.CurrentKeyFor(r.Context(), env, app, role)
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "no current key announced for this address")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, cmCurrentKeyResp(cur))

	case http.MethodPut:
		// Announcing requires a real account, not the shared admin token: this
		// is the same FK-backed attribution approve and bless need, and "who
		// told the fleet to rotate" is exactly the kind of fact that must not
		// resolve to "somebody with the token".
		actor, ok := s.cmActor(w, r)
		if !ok {
			return
		}
		var req apitypes.CMCurrentKeyReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		cur, err := s.users.SetCurrentKey(r.Context(), env, app, role, req.KeyID, actor)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Info("config manager current key announced",
			"env", cur.Environment, "app", cur.App, "role", cur.Role,
			"keyId", cur.KeyID, "by", s.adminActor(r))
		writeJSON(w, cmCurrentKeyResp(cur))

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or PUT required")
	}
}

func cmCurrentKeyResp(c *db.CurrentKey) apitypes.CMCurrentKeyResp {
	out := apitypes.CMCurrentKeyResp{
		Environment: c.Environment,
		App:         c.App,
		Role:        c.Role,
		KeyID:       c.KeyID,
		SetBy:       c.SetBy,
	}
	if !c.SetAt.IsZero() {
		out.SetAt = c.SetAt.UTC().Format(time.RFC3339)
	}
	return out
}
