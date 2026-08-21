package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Passkey registration and assertion for user accounts.

const (
	purposeAccountRegister = "account-register"
	purposeAccountLogin    = "account-login"
)

// handleAPIAccountPasskeyRegisterBegin starts enrolment for the signed-in
// account.
func (s *Server) handleAPIAccountPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account first")
		return
	}

	rp, err := accountWebAuthnRP(s.cfg())
	if err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	waUser, _, err := s.accountWebAuthnUser(r.Context(), user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	options, session, err := rp.BeginRegistration(waUser, registrationOptions()...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	id := s.ceremonies.put(user.ID, purposeAccountRegister, *session)
	raw, err := marshalOptions(options)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(apitypes.PasskeyBeginResponse{CeremonyID: id, Options: raw})
}

// handleAPIAccountPasskeyRegisterFinish stores a newly created passkey.
func (s *Server) handleAPIAccountPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account first")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Could not read request")
		return
	}
	var req apitypes.PasskeyFinishRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	session, err := s.ceremonies.take(req.CeremonyID, user.ID, purposeAccountRegister)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	rp, err := accountWebAuthnRP(s.cfg())
	if err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	waUser, _, err := s.accountWebAuthnUser(r.Context(), user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Could not parse the authenticator response: "+err.Error())
		return
	}
	credential, err := rp.CreateCredential(waUser, session, parsed)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Registration failed: "+err.Error())
		return
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "passkey"
	}
	if _, err := s.users.AddPasskey(r.Context(), user.ID, credentialToBlob(credential),
		credential.Authenticator.SignCount, label); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("second factor enrolled", "user", user.Username, "kind", "passkey", "label", label)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPILoginPasskeyBegin issues an assertion challenge for a login that
// stopped for a second factor.
func (s *Server) handleAPILoginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	var req apitypes.LoginFactorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	// peek, not take: the id is consumed when the assertion is verified. A
	// begin that burned it would strand anyone who dismissed the browser
	// prompt and tried again.
	userID, ok := s.pendingLogins.peek(req.PendingID)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "That sign-in expired. Start again.")
		return
	}
	user, err := s.users.UserByID(r.Context(), userID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "That sign-in expired. Start again.")
		return
	}

	rp, err := accountWebAuthnRP(s.cfg())
	if err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	waUser, _, err := s.accountWebAuthnUser(r.Context(), user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(waUser.creds) == 0 {
		writeJSONError(w, http.StatusBadRequest, "No passkey is enrolled for that account")
		return
	}

	options, session, err := rp.BeginLogin(waUser)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	id := s.ceremonies.put(user.ID, purposeAccountLogin, *session)
	raw, err := marshalOptions(options)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(apitypes.PasskeyBeginResponse{CeremonyID: id, Options: raw})
}

// handleAPILoginPasskeyFinish verifies an assertion and issues the session.
func (s *Server) handleAPILoginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Could not read request")
		return
	}
	var req apitypes.PasskeyFinishRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	userID, ok := s.pendingLogins.take(req.PendingID)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "That sign-in expired. Start again.")
		return
	}
	session, err := s.ceremonies.take(req.CeremonyID, userID, purposeAccountLogin)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, err := s.users.UserByID(r.Context(), userID)
	if err != nil || !user.Enabled() {
		writeJSONError(w, http.StatusForbidden, "That account is disabled.")
		return
	}

	rp, err := accountWebAuthnRP(s.cfg())
	if err != nil {
		writeJSONError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	waUser, stored, err := s.accountWebAuthnUser(r.Context(), user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Could not parse the authenticator response: "+err.Error())
		return
	}
	credential, err := rp.ValidateLogin(waUser, session, parsed)
	if err != nil {
		slog.Warn("failed second factor", "user", user.Username, "ip", s.getClientIP(r), "kind", "passkey")
		writeJSONError(w, http.StatusUnauthorized, "That passkey was not accepted")
		return
	}

	// Persist the counter and any clone warning against the row this
	// assertion actually used (AUTH-4). Without the write-back the counter
	// never advances and the clone check can never fire.
	encoded := base64.StdEncoding.EncodeToString(credential.ID)
	for i := range stored {
		if stored[i].Secret() != encoded {
			continue
		}
		if err := s.users.TouchCredential(r.Context(), stored[i].ID,
			credential.Authenticator.SignCount, credential.Authenticator.CloneWarning); err != nil {
			slog.Warn("could not update passkey counter", "credential", stored[i].ID, "error", err)
		}
		if credential.Authenticator.CloneWarning {
			slog.Error("passkey signature counter went backwards; the authenticator may be cloned",
				"user", user.Username, "credential", stored[i].ID)
		}
		break
	}

	s.completeFactorLogin(w, r, user, "passkey")
}
