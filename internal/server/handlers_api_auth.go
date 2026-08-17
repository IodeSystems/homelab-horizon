package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

func (s *Server) handleAPIAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	primaryID := ""
	if p := s.cfg().PrimaryPeer(); p != nil {
		primaryID = p.ID
	}

	usersAvailable := s.users != nil

	if s.isAdmin(r) {
		method := "cookie"
		if s.isVPNAdmin(r) {
			method = "vpn"
		}
		resp := apitypes.AuthStatusResponse{
			Authenticated:  true,
			Method:         method,
			PeerID:         s.cfg().PeerID,
			ConfigPrimary:  s.cfg().ConfigPrimary,
			PrimaryID:      primaryID,
			UsersAvailable: usersAvailable,
		}
		// A named account outranks the other two for reporting: it is the
		// only one the UI can address as a person.
		if u := s.currentUser(r); u != nil {
			resp.Method = "user"
			resp.Username = u.Username
			resp.Role = u.Role
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	_ = json.NewEncoder(w).Encode(apitypes.AuthStatusResponse{
		Authenticated:  false,
		PeerID:         s.cfg().PeerID,
		ConfigPrimary:  s.cfg().ConfigPrimary,
		PrimaryID:      primaryID,
		UsersAvailable: usersAvailable,
		NeedsBootstrap: s.bootstrapAllowed(r.Context()),
	})
}

func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	var body apitypes.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	token := strings.TrimSpace(body.Token)

	// A local account, when credentials were supplied. Checked before the
	// token so that an operator with an account never depends on the shared
	// secret still being enabled.
	if body.Username != "" && body.Password != "" {
		s.loginWithPassword(w, r, body.Username, body.Password)
		return
	}

	// Refuse before comparing, so a disabled token cannot be probed for
	// correctness by timing or by error message.
	if s.cfg().AdminTokenDisabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "The admin token is disabled. Administer over the VPN as an admin peer, " +
				"or restart hz with -enable-admin-token from the console.",
		})
		return
	}

	if token == s.adminToken {
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    s.signCookie("admin"),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		return
	}

	if s.isValidInvite(token) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{
			OK:       true,
			Invite:   true,
			Redirect: "/invite/" + token,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid token"})
}

func (s *Server) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	// Both cookies: a browser may hold a token-minted session and an account
	// session at once, and logging out must not leave either behind.
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	s.endUserSession(w, r)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// loginWithPassword authenticates a local account.
//
// The failure message never distinguishes an unknown user from a wrong
// password: the login form must not double as a way to enumerate accounts. A
// disabled account is the one exception, and only because the password already
// matched — saying so leaks nothing to someone who cannot authenticate, and
// saves the person a support round trip.
func (s *Server) loginWithPassword(w http.ResponseWriter, r *http.Request, username, password string) {
	w.Header().Set("Content-Type", "application/json")

	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	user, err := s.users.VerifyPasswordWithPolicy(r.Context(), username, password, s.lockoutPolicy())

	var locked *db.ErrAccountLocked
	switch {
	case errors.As(err, &locked):
		// Naming the wait is the difference between a user retrying forever
		// and a user coming back later. It reveals nothing: whoever triggered
		// the lock already knows they were failing.
		mins := int(time.Until(locked.Until).Minutes()) + 1
		writeJSONError(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many failed attempts. Try again in %d minute(s).", mins))
		return
	case errors.Is(err, db.ErrUserDisabled):
		writeJSONError(w, http.StatusForbidden, "That account is disabled.")
		return
	case err != nil:
		slog.Warn("failed login", "username", db.NormalizeUsername(username), "ip", s.getClientIP(r))
		writeJSONError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}

	// An expired password is not a session either. Checked before the second
	// factor so that someone with both is asked for the code first and the
	// change second — the change endpoint needs an authenticated caller, and
	// a half-authenticated one is not that.
	if s.passwordExpired(r.Context(), user.ID) {
		pendingID, err := s.pendingLogins.add(user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Could not start sign-in")
			return
		}
		_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{
			PasswordExpired: true,
			PendingID:       pendingID,
		})
		return
	}

	// A correct password is not a session when the account has a second
	// factor. The pending id proves this step happened and confers nothing
	// else; the session is issued only by the factor handlers.
	hasFactor, err := s.users.HasSecondFactor(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not check second factors")
		return
	}
	if hasFactor {
		pendingID, err := s.pendingLogins.add(user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Could not start sign-in")
			return
		}
		_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{
			MFARequired: true,
			PendingID:   pendingID,
			Factors:     s.factorKinds(r, user.ID),
		})
		return
	}

	if err := s.startUserSession(w, r, user); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not start session")
		return
	}

	slog.Info("login", "user", user.Username, "ip", s.getClientIP(r))
	_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
}

// factorKinds lists which second factors an account can finish with.
//
// Only kinds that will actually work: a passkey the relying party cannot be
// built for is offered nowhere, or the UI would present a button that fails
// after the browser prompt rather than before it.
func (s *Server) factorKinds(r *http.Request, userID string) []string {
	var kinds []string
	if _, err := s.users.TOTPSecret(r.Context(), userID); err == nil {
		kinds = append(kinds, db.KindTOTP)
	}
	if available, _ := s.accountPasskeysAvailable(); available {
		if creds, err := s.users.CredentialsFor(r.Context(), userID, db.KindPasskey); err == nil && len(creds) > 0 {
			kinds = append(kinds, db.KindPasskey)
		}
	}
	return kinds
}
