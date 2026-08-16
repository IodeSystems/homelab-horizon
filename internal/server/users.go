package server

import (
	"context"
	"errors"
	"net/http"
	"time"

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
// Zero by default in this phase. Turning an idle timeout on is a change to how
// every admin's day feels, so it belongs with the rest of the policy switches
// rather than arriving unannounced with the feature that made it possible.
func (s *Server) idleTimeout() time.Duration {
	mins := s.cfg().SessionIdleMinutes
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
