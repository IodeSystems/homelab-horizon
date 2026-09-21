package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Key custody: hz's half of the recovery recipient.
//
// hz's job here is small and it is bounded on purpose. It STORES two things —
// a list of public keys, and blobs addressed to them — and it verifies the one
// property it can verify without a private key: that a blob is a wrapped
// environment key addressed to the recipient it claims. That check is free (a
// wrapped-key envelope carries its recipient fingerprint in cleartext, and hz
// holds the recipient's public key) and without it a recipient list would be a
// label rather than a grant: a blob wrapped to a stale or wrong key would store
// cleanly and be discovered unopenable at the only moment it matters.
//
// What hz deliberately CANNOT do:
//
//   - open a wrap. It holds no private key, for a machine or a recipient.
//   - receive one. There is no field in apitypes.CMRecovery* that could carry a
//     private key, no handler below reads one, and none may be added.
//   - prove a wrap holds the key it names. That needs the private half, so it
//     happens on the operator's machine — `hz cm recovery verify`.
//
// Removal is not revocation. Dropping a recipient stops FUTURE wraps; the wraps
// already addressed to it stay readable by whoever holds that private key, and
// stay stored, because deleting them would destroy custody without removing
// access. Every message below says so where an operator can read it.

// handleAPICMRecovery is GET /api/v1/cm/recovery — recipients and wraps.
func (s *Server) handleAPICMRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "admin required")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeJSON(w, cmRecoveryResp(s.cfg()))
}

// cmRecoveryResp renders the stored state. The fingerprint is DERIVED here
// rather than stored for display: a recipient whose public key no longer parses
// shows a blank fingerprint, which is visibly wrong rather than plausible —
// same rule cmFingerprint follows for machines.
func cmRecoveryResp(cfg *config.Config) apitypes.CMRecoveryResp {
	out := apitypes.CMRecoveryResp{
		Recipients: make([]apitypes.CMRecoveryRecipient, 0, len(cfg.RecoveryRecipients)),
		Wraps:      make([]apitypes.CMRecoveryWrap, 0, len(cfg.RecoveryWraps)),
	}
	for _, rec := range cfg.RecoveryRecipients {
		row := apitypes.CMRecoveryRecipient{
			Name:      rec.Name,
			PublicKey: rec.PublicKey,
			AddedAt:   rec.AddedAt,
		}
		if pub, err := configmgr.ParseMachinePublicKey(rec.PublicKey); err == nil {
			row.Fingerprint = configmgr.FingerprintOf(pub).String()
		}
		out.Recipients = append(out.Recipients, row)
	}
	for _, wrap := range cfg.RecoveryWraps {
		out.Wraps = append(out.Wraps, apitypes.CMRecoveryWrap{
			Environment: wrap.Environment,
			App:         wrap.App,
			Role:        wrap.Role,
			KeyID:       wrap.KeyID,
			Recipient:   wrap.Recipient,
			Fingerprint: wrap.Fingerprint,
			Wrapped:     wrap.Wrapped,
			WrappedAt:   wrap.WrappedAt,
		})
	}
	return out
}

// handleAPICMRecoveryRecipients is POST /api/v1/cm/recovery/recipients and
// DELETE /api/v1/cm/recovery/recipients/{name}.
//
// One handler for both because ServeMux routes the prefix, and splitting them
// would mean two places that have to agree about what a name is.
func (s *Server) handleAPICMRecoveryRecipients(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "admin required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, apitypes.CMPathRecoveryRecipients)
	rest = strings.TrimPrefix(rest, "/")

	switch {
	case rest == "" && r.Method == http.MethodPost:
		s.cmRecoveryAddRecipient(w, r)
	case rest != "" && r.Method == http.MethodDelete:
		s.cmRecoveryRemoveRecipient(w, r, rest)
	case rest == "":
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required; read the list at "+apitypes.CMPathRecovery)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "DELETE required for a named recipient")
	}
}

func (s *Server) cmRecoveryAddRecipient(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CMRecoveryRecipientReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	name, err := config.CanonRecoveryName(req.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Parsed, not merely stored. ecdh.NewPublicKey refuses a point that is not
	// on the curve, which is the check that stops a hostile entry steering a
	// future wrap into an invalid-curve attack — and it is also what stops a
	// typo becoming a recipient nothing can ever wrap to.
	pub, err := configmgr.ParseMachinePublicKey(strings.TrimSpace(req.PublicKey))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "publicKey: "+err.Error())
		return
	}

	rec := config.RecoveryRecipient{
		Name:      name,
		PublicKey: configmgr.MarshalMachinePublicKey(pub),
		AddedAt:   config.NowRFC3339(),
	}
	replacing, existed := s.cfg().FindRecoveryRecipient(name)
	if existed {
		// A name kept with a new key is a ROTATION, and the wraps already
		// written to the old key do not stop opening. Logged, because it is the
		// one change here that silently redirects every future wrap.
		rec.AddedAt = replacing.AddedAt
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.AddRecoveryRecipient(rec) }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not save the recipient: "+err.Error())
		return
	}

	slog.Info("cm recovery recipient saved", "name", name,
		"fingerprint", configmgr.FingerprintOf(pub).String(),
		"replaced_existing_key", existed && replacing.PublicKey != rec.PublicKey,
		"by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, cmRecoveryResp(s.cfg()))
}

func (s *Server) cmRecoveryRemoveRecipient(w http.ResponseWriter, r *http.Request, ref string) {
	ref, err := url.PathUnescape(ref)
	if err != nil || strings.Contains(ref, "/") {
		writeJSONError(w, http.StatusNotFound, "expected "+apitypes.CMPathRecoveryRecipients+"/{name}")
		return
	}
	name, err := config.CanonRecoveryName(ref)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := s.cfg().FindRecoveryRecipient(name); !ok {
		writeJSONError(w, http.StatusNotFound, "no recovery recipient named "+name)
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { cfg.RemoveRecoveryRecipient(name) }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not save the removal: "+err.Error())
		return
	}
	slog.Info("cm recovery recipient removed", "name", name,
		"note", "future wraps only; wraps already addressed to this key remain readable by its holder",
		"by", s.adminActor(r), "ip", s.getClientIP(r))
	writeJSON(w, cmRecoveryResp(s.cfg()))
}

// handleAPICMRecoveryWraps is POST /api/v1/cm/recovery/wraps.
//
// The blob is verified before it is stored, for the same reason cmApprove
// verifies an approval blob: hz cannot open it, but it CAN check that the
// envelope is a wrapped environment key addressed to the recipient named — and
// without that check a wrap is a filename rather than custody, discovered wrong
// at the only moment it is ever needed.
//
// What it cannot check is the content: whether the bytes inside are the key
// KeyID names. That requires the private half, so it is `hz cm recovery
// verify`'s job, and no message here may imply this endpoint proved it.
func (s *Server) handleAPICMRecoveryWraps(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusForbidden, "admin required")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.CMRecoveryWrapReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	env := strings.TrimSpace(req.Environment)
	app := strings.TrimSpace(req.App)
	role := strings.TrimSpace(req.Role)
	if env == "" || app == "" || role == "" {
		writeJSONError(w, http.StatusBadRequest, "a wrap needs an environment, an app and a role")
		return
	}
	// The key id is a keystore filename on every client, so it is validated as
	// a canonical id here rather than stored as whatever the caller typed.
	if _, err := configmgr.ParseKeyID(strings.TrimSpace(req.KeyID)); err != nil {
		writeJSONError(w, http.StatusBadRequest, "keyId: "+err.Error())
		return
	}
	name, err := config.CanonRecoveryName(req.Recipient)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "recipient: "+err.Error())
		return
	}
	rec, ok := s.cfg().FindRecoveryRecipient(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound,
			"no recovery recipient named "+name+"; add it before wrapping to it")
		return
	}
	pub, err := configmgr.ParseMachinePublicKey(rec.PublicKey)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			"recovery recipient "+name+" has an unreadable public key: "+err.Error())
		return
	}

	blob, err := configmgr.DecodeEnvelope(strings.TrimSpace(req.Wrapped))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "wrapped: "+err.Error())
		return
	}
	header, err := configmgr.ParseEnvelopeHeader(blob)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "wrapped: "+err.Error())
		return
	}
	if header.Kind != configmgr.KindWrappedEnvKey {
		writeJSONError(w, http.StatusBadRequest,
			"wrapped: expected a wrapped environment key, got "+header.Kind.String())
		return
	}
	want := configmgr.FingerprintOf(pub)
	if header.Recipient != want {
		writeJSONError(w, http.StatusBadRequest,
			"wrapped is addressed to "+header.Recipient.String()+
				" but recipient "+name+"'s key is "+want.String()+
				"; that blob would store cleanly and open for nobody")
		return
	}

	stored := config.RecoveryWrap{
		Environment: env,
		App:         app,
		Role:        role,
		KeyID:       strings.TrimSpace(req.KeyID),
		Recipient:   name,
		Fingerprint: want.String(),
		Wrapped:     configmgr.EncodeEnvelope(blob),
		WrappedAt:   config.NowRFC3339(),
	}
	wrote := false
	if err := s.updateConfig(func(cfg *config.Config) {
		wrote = cfg.PutRecoveryWrap(stored, req.Replace)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not save the wrap: "+err.Error())
		return
	}

	if wrote {
		slog.Info("cm recovery wrap stored", "address", stored.Addr(), "key_id", stored.KeyID,
			"recipient", name, "fingerprint", want.String(),
			"by", s.adminActor(r), "ip", s.getClientIP(r))
	}
	held, _ := s.cfg().FindRecoveryWrap(env, app, role, stored.KeyID, name)
	writeJSON(w, apitypes.CMRecoveryWrapResp{
		Stored: wrote,
		Wrap: apitypes.CMRecoveryWrap{
			Environment: held.Environment,
			App:         held.App,
			Role:        held.Role,
			KeyID:       held.KeyID,
			Recipient:   held.Recipient,
			Fingerprint: held.Fingerprint,
			Wrapped:     held.Wrapped,
			WrappedAt:   held.WrappedAt,
		},
	})
}
