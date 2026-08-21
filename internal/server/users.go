package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// User sessions, layered under the two authentication paths hz already had.
//
// Order matters and is deliberate: a user session is checked first because it
// is the only one that names a person, then the shared admin token, then VPN
// admin. Nothing here removes a way in — Phase 2 adds accounts, it does not
// take the token away. That happens only when an operator disables it, and
// only once a real account exists to disable it from.

// userSessionCookie is the cookie name for a database-backed session. It is
// deliberately not "session": the old cookie carries an HMAC of the literal
// string "admin" keyed by the admin token, and reusing the name would mean a
// stale cookie from before the upgrade competing with a real one.
const userSessionCookie = "hz_user"

// idleTimeout is the configured inactivity limit, or zero for none.
//
// Off unless an operator turns it on. An idle timeout changes how every
// admin's day feels, so it is not something an upgrade should decide for
// them — the PCI control reports it unmet until they do.
func (s *Server) idleTimeout() time.Duration {
	mins := s.cfg().Policy.IdleMinutes
	if mins <= 0 {
		return 0
	}
	return time.Duration(mins) * time.Minute
}

// currentUser resolves the requesting user from their session cookie, or nil.
func (s *Server) currentUser(r *http.Request) *db.User {
	if s.users == nil {
		return nil
	}

	// A personal API token is checked first, because a request carrying one is
	// a script saying who it is on purpose. This is the path that lets the
	// shared admin token be switched off: automation still needs a credential,
	// and this one names a person, so 8.2.1 holds for scripted actions too.
	if tok := requestBearer(r); strings.HasPrefix(tok, db.APITokenPrefix) {
		user, _, err := s.users.LookupAPIToken(r.Context(), tok, s.getClientIP(r))
		if err != nil {
			// Deliberately not falling through to the cookie: a request that
			// presented a token meant to use it, and quietly authenticating it
			// as somebody else would put the wrong name in the audit log.
			return nil
		}
		return user
	}

	cookie, err := r.Cookie(userSessionCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}

	user, _, err := s.users.LookupSession(r.Context(), cookie.Value, s.idleTimeout())
	if err != nil {
		return nil
	}
	return user
}

// startUserSession mints a session and sets the cookie.
func (s *Server) startUserSession(w http.ResponseWriter, r *http.Request, user *db.User) error {
	token, _, err := s.users.CreateSession(r.Context(), user.ID,
		db.DefaultSessionTTL, s.getClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     userSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure only when the request arrived over TLS: hz is reachable on
		// plain HTTP over the LAN, and an unconditionally Secure cookie would
		// be dropped there, locking out exactly the path used for recovery.
		Secure: r.TLS != nil,
		MaxAge: int(db.DefaultSessionTTL.Seconds()),
	})
	return nil
}

// endUserSession revokes the caller's session and clears the cookie.
func (s *Server) endUserSession(w http.ResponseWriter, r *http.Request) {
	if s.users != nil {
		if cookie, err := r.Cookie(userSessionCookie); err == nil && cookie.Value != "" {
			if _, sess, err := s.users.LookupSession(r.Context(), cookie.Value, 0); err == nil {
				_ = s.users.RevokeSession(r.Context(), sess.ID)
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: userSessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

// hasUsableAccount reports whether at least one account can log in.
//
// The guard on disabling the shared admin token: an invite nobody has accepted
// is not a way in, so this counts only accounts that actually hold a
// credential.
func (s *Server) hasUsableAccount(ctx context.Context) bool {
	if s.users == nil {
		return false
	}
	n, err := s.users.CountEnabledUsers(ctx)
	return err == nil && n > 0
}

// bootstrapAllowed reports whether the first account may still be created.
//
// Open only while no account exists. After that, account creation is an
// authenticated admin action like any other — otherwise the endpoint would
// stay a permanent unauthenticated door into a gateway.
func (s *Server) bootstrapAllowed(ctx context.Context) bool {
	if s.users == nil {
		return false
	}
	users, err := s.users.ListUsers(ctx)
	return err == nil && len(users) == 0
}

// errNoIdentityStore is returned by the user endpoints when the database did
// not open. Naming it beats a generic 500: the cause is on disk, not in the
// request.
var errNoIdentityStore = errors.New("identity store unavailable; check the hz log for the database error")

// lockoutPolicy is the store-facing view of the configured policy.
func (s *Server) lockoutPolicy() db.LockoutPolicy {
	p := s.cfg().Policy
	return db.LockoutPolicy{
		MaxAttempts: p.EffectiveMaxFailedAttempts(),
		Duration:    time.Duration(p.EffectiveLockoutMinutes()) * time.Minute,
	}
}

// passwordExpired reports whether the account must change its password before
// it can do anything else.
//
// Expiry applies only to accounts whose password is their only factor. PCI DSS
// 8.3.9 says exactly that, and it is the right rule regardless: rotation is a
// hedge against an undetected compromise of a single secret, and an account
// with a second factor is not relying on a single secret.
// mustChangePassword reports an administrator-forced reset.
//
// Kept apart from passwordExpired because the two are enforced at different
// points. Expiry exempts accounts with a second factor, so it can be checked
// before one is presented. A forced change applies regardless of factors, so it
// must be checked AFTER the second factor — otherwise someone holding only the
// password an admin just set could reach the change-password step without ever
// presenting the factor, and walk away with the account.
func (s *Server) mustChangePassword(ctx context.Context, userID string) bool {
	if s.users == nil {
		return false
	}
	must, err := s.users.PasswordMustChange(ctx, userID)
	if err != nil {
		return false
	}
	return must
}

func (s *Server) passwordExpired(ctx context.Context, userID string) bool {
	days := s.cfg().Policy.PasswordMaxAgeDays
	if days <= 0 || s.users == nil {
		return false
	}
	if has, err := s.users.HasSecondFactor(ctx, userID); err != nil || has {
		return false
	}
	age, err := s.users.PasswordAge(ctx, userID)
	if err != nil {
		return false
	}
	return age > time.Duration(days)*24*time.Hour
}

// completeFactorLogin issues the session, unless an administrator has forced a
// password change — in which case it hands back a pending id for the change
// step instead.
//
// The second factor has already been presented by the time this runs, which is
// the ordering that makes a forced reset safe: the temporary password alone
// never reaches the change endpoint.
func (s *Server) completeFactorLogin(w http.ResponseWriter, r *http.Request, user *db.User, factor string) {
	if s.mustChangePassword(r.Context(), user.ID) {
		pendingID, err := s.pendingLogins.add(user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Could not start sign-in")
			return
		}
		slog.Info("password change required before sign-in completes",
			"user", user.Username, "ip", s.getClientIP(r), "factor", factor)
		_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{
			PasswordExpired: true,
			PendingID:       pendingID,
		})
		return
	}

	if err := s.startUserSession(w, r, user); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not start session")
		return
	}
	slog.Info("login", "user", user.Username, "ip", s.getClientIP(r), "factor", factor)
	_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
}
