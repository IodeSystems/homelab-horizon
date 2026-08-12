package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Passkey endpoints, all authenticated the same way the TOTP ones are: by
// WireGuard source IP via getPeerFromRequest. They are reachable from inside
// the MFA jail by design — they are how a jailed peer gets out.

// beginCeremony is the shared preamble: identify the peer, build the relying
// party, and load its existing credentials.
func (s *Server) beginCeremony(w http.ResponseWriter, r *http.Request) (string, *webauthn.WebAuthn, *peerUser, bool) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return "", nil, nil, false
	}
	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return "", nil, nil, false
	}
	cfg := s.cfg()
	wa, err := webAuthnRP(cfg)
	if err != nil {
		// A deployment problem, not a caller problem: the operator has to fix
		// kiosk_url before passkeys can work at all.
		writeJSONError(w, http.StatusServiceUnavailable, "passkeys unavailable: "+err.Error())
		return "", nil, nil, false
	}
	user, err := peerWebAuthnUser(cfg, peerName)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return "", nil, nil, false
	}
	return peerName, wa, user, true
}

// handleAPIPasskeyRegisterBegin starts enrollment for the calling peer.
func (s *Server) handleAPIPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	peerName, wa, user, ok := s.beginCeremony(w, r)
	if !ok {
		return
	}

	// Existing credentials become the exclusion list, so an authenticator the
	// peer already registered declines instead of silently making a second.
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts := append(registrationOptions(), webauthn.WithExclusions(exclude))

	creation, session, err := wa.BeginRegistration(user, opts...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "begin registration: "+err.Error())
		return
	}
	options, err := marshalOptions(creation)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.PasskeyBeginResponse{
		OK:         true,
		CeremonyID: s.ceremonies.put(peerName, "register", *session),
		Options:    options,
	})
}

// handleAPIPasskeyRegisterFinish verifies the attestation and stores the
// credential. Enrolling does not itself clear the jail — the peer still has to
// assert with it, same as TOTP requires a code after showing the QR.
func (s *Server) handleAPIPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	var req apitypes.PasskeyFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	session, err := s.ceremonies.take(req.CeremonyID, peerName, "register")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg := s.cfg()
	wa, err := webAuthnRP(cfg)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "passkeys unavailable: "+err.Error())
		return
	}
	user, err := peerWebAuthnUser(cfg, peerName)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid credential: "+err.Error())
		return
	}
	cred, err := wa.CreateCredential(user, session, parsed)
	if err != nil {
		slog.Warn("passkey registration failed", "peer", peerName, "err", err)
		writeJSONError(w, http.StatusBadRequest, "passkey registration failed")
		return
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "passkey"
	}
	if err := s.updateConfig(func(c *config.Config) {
		c.AddPasskey(peerName, credentialToPasskey(cred, label))
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	slog.Info("passkey registered", "peer", peerName, "label", label)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIPasskeyAssertBegin starts an unlock ceremony.
func (s *Server) handleAPIPasskeyAssertBegin(w http.ResponseWriter, r *http.Request) {
	peerName, wa, user, ok := s.beginCeremony(w, r)
	if !ok {
		return
	}
	if len(user.creds) == 0 {
		writeJSONError(w, http.StatusBadRequest, "No passkey enrolled for this peer")
		return
	}

	assertion, session, err := wa.BeginLogin(user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "begin assertion: "+err.Error())
		return
	}
	options, err := marshalOptions(assertion)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.PasskeyBeginResponse{
		OK:         true,
		CeremonyID: s.ceremonies.put(peerName, "assert", *session),
		Options:    options,
	})
}

// handleAPIPasskeyAssertFinish validates the assertion and opens an MFA
// session — the passkey equivalent of handleAPIMFAVerify, and it lifts the
// jail through the same path.
func (s *Server) handleAPIPasskeyAssertFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	var req apitypes.PasskeyFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	session, err := s.ceremonies.take(req.CeremonyID, peerName, "assert")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg := s.cfg()
	wa, err := webAuthnRP(cfg)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "passkeys unavailable: "+err.Error())
		return
	}
	user, err := peerWebAuthnUser(cfg, peerName)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid credential: "+err.Error())
		return
	}
	cred, err := wa.ValidateLogin(user, session, parsed)
	if err != nil {
		slog.Warn("passkey assertion failed", "peer", peerName, "err", err)
		writeJSONError(w, http.StatusUnauthorized, "passkey authentication failed")
		return
	}

	// Same duration allowlist as the TOTP path — the factor changes, the
	// session policy does not.
	expiry, err := s.mfaSessionExpiry(cfg, req.Duration)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	credID := credentialToPasskey(cred, "").CredentialID
	if err := s.updateConfig(func(c *config.Config) {
		c.UpdatePasskeySignCount(peerName, credID, cred.Authenticator.SignCount, cred.Authenticator.CloneWarning)
		c.SetMFASession(peerName, expiry)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	if cred.Authenticator.CloneWarning {
		slog.Warn("passkey sign counter did not advance — possible cloned authenticator",
			"peer", peerName)
	}
	s.rebuildWGChains()

	resp := apitypes.MFAVerifyResponse{OK: true}
	if expiry != 0 {
		resp.Expiry = mfaExpiryString(expiry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleAPIPasskeyDelete removes one credential. Peer-scoped: a peer may
// manage its own keys, which it needs when replacing a lost device and still
// holding a second one.
func (s *Server) handleAPIPasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	var req struct {
		CredentialID string `json:"credentialId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if strings.TrimSpace(req.CredentialID) == "" {
		writeJSONError(w, http.StatusBadRequest, "credentialId required")
		return
	}

	removed := false
	if err := s.updateConfig(func(c *config.Config) {
		removed = c.DeletePasskey(peerName, req.CredentialID)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	if !removed {
		writeJSONError(w, http.StatusNotFound, "no such passkey for this peer")
		return
	}
	slog.Info("passkey deleted", "peer", peerName)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
