package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Second factors for user accounts.
//
// Separate from handlers_mfa.go, which enrols VPN *peers* for the captive
// portal. Same primitives, different subject and different consequence: this
// one gates the admin UI, that one gates the network.

// handleAPIAccountTOTPEnroll issues a TOTP secret for the calling account.
//
// The secret is held in memory until a code proves it was stored correctly.
// The peer flow persists on issue, which means a scan that silently failed
// leaves an account "enrolled" against a secret nobody has — an easy way to
// lock someone out with no error anywhere.
func (s *Server) handleAPIAccountTOTPEnroll(w http.ResponseWriter, r *http.Request) {
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

	if _, err := s.users.TOTPSecret(r.Context(), user.ID); err == nil {
		writeJSONError(w, http.StatusConflict,
			"This account already has an authenticator app. Remove it before enrolling another.")
		return
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      s.totpIssuer(),
		AccountName: user.Username,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not generate a secret: "+err.Error())
		return
	}

	s.pendingTOTP.put(user.ID, key.Secret())

	_ = json.NewEncoder(w).Encode(apitypes.AccountTOTPEnrollResponse{
		ProvisioningURI: key.URL(),
		Secret:          key.Secret(),
	})
}

// handleAPIAccountTOTPConfirm activates a pending TOTP secret.
func (s *Server) handleAPIAccountTOTPConfirm(w http.ResponseWriter, r *http.Request) {
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

	var req apitypes.AccountTOTPConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	secret, ok := s.pendingTOTP.get(user.ID)
	if !ok {
		writeJSONError(w, http.StatusBadRequest,
			"That enrolment expired. Start again to get a fresh QR code.")
		return
	}
	if !totp.Validate(strings.TrimSpace(req.Code), secret) {
		writeJSONError(w, http.StatusUnauthorized,
			"That code is not right. Check your device's clock if it keeps failing.")
		return
	}

	if _, err := s.users.AddCredential(r.Context(), user.ID, db.KindTOTP, secret, "", "authenticator app"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pendingTOTP.clear(user.ID)

	slog.Info("second factor enrolled", "user", user.Username, "kind", "totp")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIAccountFactors lists and removes the calling account's factors.
func (s *Server) handleAPIAccountFactors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account first")
		return
	}

	switch r.Method {
	case http.MethodGet:
		out := apitypes.AccountFactorsResponse{}
		for _, kind := range []string{db.KindTOTP, db.KindPasskey} {
			creds, err := s.users.CredentialsFor(r.Context(), user.ID, kind)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, c := range creds {
				f := apitypes.AccountFactor{
					ID:           c.ID,
					Kind:         c.Kind,
					Label:        c.Label,
					CloneWarning: c.CloneWarning,
					CreatedAt:    c.CreatedAt.UTC().Format(time.RFC3339),
				}
				if c.LastUsedAt != nil {
					f.LastUsed = c.LastUsedAt.UTC().Format(time.RFC3339)
				}
				out.Factors = append(out.Factors, f)
			}
		}
		available, reason := s.accountPasskeysAvailable()
		out.PasskeysAvailable = available
		out.PasskeysUnavailableReason = reason
		_ = json.NewEncoder(w).Encode(out)

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeJSONError(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := s.users.DeleteCredential(r.Context(), user.ID, id); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "No such factor on this account")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.Info("second factor removed", "user", user.Username, "credential", id)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleAPILoginTOTP completes a login that stopped for a code.
func (s *Server) handleAPILoginTOTP(w http.ResponseWriter, r *http.Request) {
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

	// Consumed whether or not the code is right, so a captured pending id
	// cannot be used to grind codes.
	userID, ok := s.pendingLogins.take(req.PendingID)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "That sign-in expired. Start again.")
		return
	}

	secret, err := s.users.TOTPSecret(r.Context(), userID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "No authenticator app is enrolled for that account")
		return
	}
	if !totp.Validate(strings.TrimSpace(req.Code), secret) {
		slog.Warn("failed second factor", "user_id", userID, "ip", s.getClientIP(r), "kind", "totp")
		writeJSONError(w, http.StatusUnauthorized, "Invalid code")
		return
	}

	user, err := s.users.UserByID(r.Context(), userID)
	if err != nil || !user.Enabled() {
		writeJSONError(w, http.StatusForbidden, "That account is disabled.")
		return
	}
	if err := s.startUserSession(w, r, user); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not start session")
		return
	}

	slog.Info("login", "user", user.Username, "ip", s.getClientIP(r), "factor", "totp")
	_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
}

// totpIssuer is the label an authenticator app shows. The admin hostname when
// there is one: an operator with several gateways otherwise gets a list of
// identical "Horizon" entries and no way to tell them apart.
func (s *Server) totpIssuer() string {
	if host := adminHostname(s.cfg()); host != "" {
		return "hz " + host
	}
	return "Homelab Horizon"
}

// pendingTOTPStore holds unconfirmed TOTP secrets, keyed by user.
type pendingTOTPStore struct {
	mu sync.Mutex
	m  map[string]pendingTOTP
}

type pendingTOTP struct {
	secret  string
	expires time.Time
}

func newPendingTOTPStore() *pendingTOTPStore {
	return &pendingTOTPStore{m: make(map[string]pendingTOTP)}
}

func (p *pendingTOTPStore) put(userID, secret string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
		}
	}
	// One pending secret per user: re-enrolling replaces it, so a stale QR
	// from an abandoned attempt cannot be confirmed later.
	p.m[userID] = pendingTOTP{secret: secret, expires: now.Add(10 * time.Minute)}
}

func (p *pendingTOTPStore) get(userID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.m[userID]
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	return entry.secret, true
}

func (p *pendingTOTPStore) clear(userID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, userID)
}
