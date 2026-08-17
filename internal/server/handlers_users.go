package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// User account management. Everything here requires admin, except the
// bootstrap path, which is open exactly while no account exists.

func (s *Server) handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		s.listUsers(w, r)
	case http.MethodPost:
		s.createUser(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	users, err := s.users.ListUsers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]apitypes.User, 0, len(users))
	for _, u := range users {
		out = append(out, toAPIUser(u))
	}
	_ = json.NewEncoder(w).Encode(apitypes.UsersResponse{
		Users: out,
		// The UI needs to know whether turning the shared token off would
		// strand everyone, and it must not have to derive that from the list.
		CanDisableAdminToken: s.hasUsableAccount(r.Context()),
	})
}

// createUser adds an account. Open unauthenticated only while none exists —
// the bootstrap — and admin-only from then on.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	bootstrap := s.bootstrapAllowed(r.Context())
	if !bootstrap && !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var body apitypes.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	role := body.Role
	if bootstrap {
		// The first account administers the box; a read-only first user would
		// be a lockout with extra steps.
		role = db.RoleAdmin
	}

	user, err := s.users.CreateUser(r.Context(), body.Username, body.Email, role)
	switch {
	case errors.Is(err, db.ErrUsernameTaken):
		writeJSONError(w, http.StatusConflict, "That username is taken")
		return
	case err != nil:
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A password is optional: an account can exist before its holder has one,
	// which is what an invite is.
	if body.Password != "" {
		if err := s.users.SetPassword(r.Context(), user.ID, body.Password); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	slog.Info("user created", "username", user.Username, "role", user.Role,
		"bootstrap", bootstrap, "by", s.adminActor(r))

	// Log the bootstrap user straight in. They just proved they can reach an
	// unconfigured gateway; making them re-enter the password they set two
	// seconds ago protects nothing.
	if bootstrap && body.Password != "" {
		if err := s.startUserSession(w, r, user); err != nil {
			slog.Warn("could not start session for bootstrap user", "error", err)
		}
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(toAPIUser(*user))
}

// handleAPIUserPassword sets a password. Admins may set anyone's; a user
// setting their own must prove the current one.
func (s *Server) handleAPIUserPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	var body apitypes.SetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	self := s.currentUser(r)
	target := body.UserID

	switch {
	case self != nil && (target == "" || target == self.ID):
		// Changing your own password requires the old one, so a borrowed
		// session cannot be turned into permanent ownership of the account.
		target = self.ID
		if _, err := s.users.VerifyPassword(r.Context(), self.Username, body.CurrentPassword); err != nil {
			writeJSONError(w, http.StatusForbidden, "Current password is incorrect")
			return
		}
	case s.isAdmin(r):
		if target == "" {
			writeJSONError(w, http.StatusBadRequest, "userId is required")
			return
		}
	default:
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// History applies to a self-service change. An admin resetting somebody
	// else's is exempt: they are usually resetting it precisely because the
	// account is stuck, and a reuse check that blocks the fix would turn a
	// lockout into a worse one.
	history := 0
	if self != nil && target == self.ID {
		history = s.cfg().Policy.EffectivePasswordHistory()
	}
	if err := s.users.SetPasswordWithHistory(r.Context(), target, body.Password, history); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A password change ends every other session for that account. If the
	// reason for the change was that someone else had it, leaving their
	// session alive would defeat the change.
	if err := s.users.RevokeUserSessions(r.Context(), target); err != nil {
		slog.Warn("could not revoke sessions after password change", "user", target, "error", err)
	}
	if self != nil && target == self.ID {
		if err := s.startUserSession(w, r, self); err != nil {
			slog.Warn("could not re-establish session after password change", "error", err)
		}
	}

	slog.Info("password set", "user", target, "by", s.adminActor(r))
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIUserDisable disables or re-enables an account.
func (s *Server) handleAPIUserDisable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	var body apitypes.DisableUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(body.UserID) == "" {
		writeJSONError(w, http.StatusBadRequest, "userId is required")
		return
	}

	// Disabling the last usable account while the shared token is already off
	// is a lockout with no network path back. Refuse it; the console is not a
	// recovery plan anyone should be steered into by a UI toggle.
	if body.Disabled && s.cfg().AdminTokenDisabled {
		users, err := s.users.ListUsers(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		remaining := 0
		for _, u := range users {
			if u.ID != body.UserID && u.Enabled() && u.Role == db.RoleAdmin {
				remaining++
			}
		}
		if remaining == 0 {
			writeJSONError(w, http.StatusConflict,
				"That is the last enabled admin and the shared admin token is disabled. "+
					"Re-enable the token or add another admin first.")
			return
		}
	}

	if err := s.users.SetUserDisabled(r.Context(), body.UserID, body.Disabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("user disabled state changed", "user", body.UserID,
		"disabled", body.Disabled, "by", s.adminActor(r))
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func toAPIUser(u db.User) apitypes.User {
	out := apitypes.User{
		ID:       u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     u.Role,
		Disabled: !u.Enabled(),
	}
	// RFC3339, not a preformatted string: SQLite stamps CURRENT_TIMESTAMP in
	// UTC, so formatting here renders every time in the wrong zone unless the
	// server happens to run on UTC. The browser knows the viewer's zone; the
	// server does not.
	if !u.CreatedAt.IsZero() {
		out.CreatedAt = u.CreatedAt.UTC().Format(time.RFC3339)
	}
	if u.LastLoginAt != nil {
		out.LastLogin = u.LastLoginAt.UTC().Format(time.RFC3339)
	}
	return out
}

// handleAPILoginChangePassword completes a login that stopped because the
// password expired.
//
// Separate from the ordinary change endpoint because the caller has no session
// yet: they proved the old password at the password step and hold a pending
// id, which is exactly as much authority as changing that password needs.
func (s *Server) handleAPILoginChangePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	var body struct {
		PendingID       string `json:"pendingId"`
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	userID, ok := s.pendingLogins.take(body.PendingID)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "That sign-in expired. Start again.")
		return
	}
	user, err := s.users.UserByID(r.Context(), userID)
	if err != nil || !user.Enabled() {
		writeJSONError(w, http.StatusForbidden, "That account is disabled.")
		return
	}

	// The old password again: a pending id proves the password step happened,
	// but this endpoint replaces that password, so it is worth proving twice
	// rather than letting a captured id rewrite the credential.
	if _, err := s.users.VerifyPassword(r.Context(), user.Username, body.CurrentPassword); err != nil {
		writeJSONError(w, http.StatusForbidden, "Current password is incorrect")
		return
	}

	if err := s.users.SetPasswordWithHistory(r.Context(), user.ID, body.Password,
		s.cfg().Policy.EffectivePasswordHistory()); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.users.RevokeUserSessions(r.Context(), user.ID); err != nil {
		slog.Warn("could not revoke sessions after a forced change", "user", user.ID, "error", err)
	}
	if err := s.startUserSession(w, r, user); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not start session")
		return
	}

	slog.Info("password rotated at login", "user", user.Username, "ip", s.getClientIP(r))
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIPolicy reads and writes the account policy.
//
// Its own endpoint rather than another corner of /settings: these are the
// switches an operator changes when an assessor asks, they are decided
// together, and each one can log people out — worth being able to see the set
// as a set.
func (s *Server) handleAPIPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		p := s.cfg().Policy
		_ = json.NewEncoder(w).Encode(apitypes.AccountPolicyResponse{
			IdleMinutes:        p.IdleMinutes,
			MaxFailedAttempts:  p.EffectiveMaxFailedAttempts(),
			LockoutMinutes:     p.EffectiveLockoutMinutes(),
			PasswordMaxAgeDays: p.PasswordMaxAgeDays,
			PasswordHistory:    p.EffectivePasswordHistory(),
			MinPasswordLength:  db.MinPasswordLength,
		})

	case http.MethodPut:
		var body apitypes.AccountPolicyResponse
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		// Bounds rather than free numbers: a 1-minute idle timeout logs
		// everyone out mid-task and a 10-year password age is rotation in
		// name only. Refusing beats storing a value that reads as configured
		// and behaves as broken.
		switch {
		case body.IdleMinutes < 0 || body.IdleMinutes > 1440:
			writeJSONError(w, http.StatusBadRequest, "Idle timeout must be between 0 (off) and 1440 minutes")
			return
		case body.MaxFailedAttempts < -1 || body.MaxFailedAttempts > 100:
			writeJSONError(w, http.StatusBadRequest, "Failed attempts must be between -1 (off) and 100")
			return
		case body.LockoutMinutes < 0 || body.LockoutMinutes > 1440:
			writeJSONError(w, http.StatusBadRequest, "Lockout must be between 0 and 1440 minutes")
			return
		case body.PasswordMaxAgeDays < 0 || body.PasswordMaxAgeDays > 3650:
			writeJSONError(w, http.StatusBadRequest, "Password age must be between 0 (off) and 3650 days")
			return
		case body.PasswordHistory < -1 || body.PasswordHistory > 24:
			writeJSONError(w, http.StatusBadRequest, "Password history must be between -1 (off) and 24")
			return
		}

		if err := s.updateConfig(func(c *config.Config) {
			c.Policy = config.AccountPolicy{
				IdleMinutes:        body.IdleMinutes,
				MaxFailedAttempts:  body.MaxFailedAttempts,
				LockoutMinutes:     body.LockoutMinutes,
				PasswordMaxAgeDays: body.PasswordMaxAgeDays,
				PasswordHistory:    body.PasswordHistory,
			}
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
			return
		}

		slog.Info("account policy updated",
			"idle_minutes", body.IdleMinutes, "max_failed", body.MaxFailedAttempts,
			"lockout_minutes", body.LockoutMinutes, "password_age_days", body.PasswordMaxAgeDays,
			"password_history", body.PasswordHistory, "by", s.adminActor(r))
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
