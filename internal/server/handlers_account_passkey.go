package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// purposeAccountTest is its own ceremony purpose so a test assertion can never
// be replayed into a sign-in, and a sign-in ceremony can never be finished by
// the test endpoint. The store checks purpose alongside the user.
const purposeAccountTest = "account-test"

// POST /api/v1/account/passkey/test/begin
//
// Same ceremony as signing in, deliberately: a test that exercised some other
// path would prove the other path works. What differs is the end — no session
// is issued, because the caller already has one and minting a second on a
// button labelled "test" would be a surprise.
func (s *Server) handleAPIAccountPasskeyTestBegin(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account to test a passkey")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
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
		writeJSONError(w, http.StatusBadRequest, "No passkey is enrolled on this account")
		return
	}

	options, session, err := rp.BeginLogin(waUser)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := s.ceremonies.put(user.ID, purposeAccountTest, *session)
	raw, err := marshalOptions(options)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, apitypes.PasskeyBeginResponse{CeremonyID: id, Options: raw})
}

// POST /api/v1/account/passkey/test/finish
//
// Verifies the assertion and reports which passkey answered, then stops. The
// signature counter is still written back: this was a real assertion, so
// skipping it would leave the stored counter behind the authenticator's and
// make the next real sign-in look like a clone (AUTH-4).
func (s *Server) handleAPIAccountPasskeyTestFinish(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account to test a passkey")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.PasskeyFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	session, err := s.ceremonies.take(req.CeremonyID, user.ID, purposeAccountTest)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "That test expired. Try again.")
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
		// The library's reason is the whole diagnosis — origin mismatch, bad
		// challenge, signature failure and user-handle mismatch all arrive
		// here and mean very different things. Dropping it, as this did,
		// turned every failure into a guess.
		slog.Warn("passkey test failed",
			"user", user.Username, "ip", s.getClientIP(r), "err", err,
			"rp_id", rp.Config.RPID, "rp_origins", rp.Config.RPOrigins)
		writeJSON(w, map[string]any{
			"ok": false,
			// Deliberately no longer asserting a cause. The first version
			// blamed the hostname the passkey was enrolled on, which sent an
			// operator looking at a setting that was correct.
			"message": "That passkey was not accepted. The gateway log records why — " +
				"look for \"passkey test failed\".",
		})
		return
	}

	label, cloned := "passkey", credential.Authenticator.CloneWarning
	encoded := base64.StdEncoding.EncodeToString(credential.ID)
	for i := range stored {
		if stored[i].Secret() != encoded {
			continue
		}
		if stored[i].Label != "" {
			label = stored[i].Label
		}
		if err := s.users.TouchCredential(r.Context(), stored[i].ID,
			credential.Authenticator.SignCount, cloned); err != nil {
			slog.Warn("could not update passkey counter", "credential", stored[i].ID, "error", err)
		}
		if cloned {
			slog.Error("passkey signature counter went backwards; the authenticator may be cloned",
				"user", user.Username, "credential", stored[i].ID)
		}
		break
	}

	slog.Info("passkey test passed", "user", user.Username, "credential", label)
	message := fmt.Sprintf("%q answered correctly. It will work at sign-in.", label)
	if cloned {
		// Reported rather than buried: the test is exactly when someone is
		// looking, and a counter going backwards is the one signal WebAuthn
		// gives that a credential may have been copied.
		message = fmt.Sprintf("%q answered, but its signature counter went backwards, "+
			"which can mean the authenticator was copied. Remove it and enrol a new one.", label)
	}
	writeJSON(w, map[string]any{"ok": true, "cloneWarning": cloned, "label": label, "message": message})
}
