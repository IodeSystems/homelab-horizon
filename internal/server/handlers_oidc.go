package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The OIDC login flow: two endpoints, one browser redirect each way.

// oidcStateCookie carries the state parameter back so the callback can check
// the browser that returns is the one that left.
//
// State is held in a cookie AND server-side. The server copy is what makes it
// single-use; the cookie is what binds the callback to this browser. Checking
// only the server copy would let anyone who observed a state parameter finish
// somebody else's login in their own browser.
const oidcStateCookie = "hz_oidc_state"

// handleAPIOIDCStart redirects the browser to the provider.
func (s *Server) handleAPIOIDCStart(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg()
	ready, reason := cfg.OIDCReady()
	if !ready {
		if reason == "" {
			reason = "single sign-on is not enabled"
		}
		writeJSONError(w, http.StatusPreconditionFailed, reason)
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errNoIdentityStore.Error())
		return
	}

	provider, err := s.oidcProviders.get(r.Context(), cfg.OIDC.Issuer, cfg.OIDC.ClientID)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	state, nonce, verifier, err := s.oidcFlows.start()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not start sign-in")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		// Lax, not Strict: the provider redirects the browser back to hz
		// cross-site, and a Strict cookie is not sent on that navigation — the
		// callback would then never see the state it is supposed to check.
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
		MaxAge:   int(oidcFlowTTL.Seconds()),
	})

	url := s.oauthConfig(provider).AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge(verifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, url, http.StatusFound)
}

// handleAPIOIDCCallback completes the flow and issues a session.
func (s *Server) handleAPIOIDCCallback(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg()
	if ready, _ := cfg.OIDCReady(); !ready || s.users == nil {
		s.oidcFail(w, r, "Single sign-on is not available.")
		return
	}

	// A provider that refuses says so here rather than by silence.
	if e := r.URL.Query().Get("error"); e != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("oidc provider returned an error", "error", e, "description", desc)
		s.oidcFail(w, r, "The identity provider refused the sign-in: "+e)
		return
	}

	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(oidcStateCookie)
	clearCookie(w, oidcStateCookie)

	// Both halves, and constant-time: the cookie proves it is the same
	// browser, the server copy proves the flow is live and unused.
	if err != nil || cookie.Value == "" || !db.TokensEqual(cookie.Value, state) {
		s.oidcFail(w, r, "That sign-in did not come back to the browser it started in.")
		return
	}
	flow, ok := s.oidcFlows.take(state)
	if !ok {
		s.oidcFail(w, r, "That sign-in expired or was already used. Try again.")
		return
	}

	provider, err := s.oidcProviders.get(r.Context(), cfg.OIDC.Issuer, cfg.OIDC.ClientID)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}

	token, err := s.oauthConfig(provider).Exchange(r.Context(),
		r.URL.Query().Get("code"),
		oauth2.SetAuthURLParam("code_verifier", flow.verifier),
	)
	if err != nil {
		slog.Warn("oidc code exchange failed", "error", err)
		s.oidcFail(w, r, "Could not complete the sign-in with the identity provider.")
		return
	}

	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		s.oidcFail(w, r, "The identity provider returned no ID token.")
		return
	}

	// Verification is the whole point: signature against the provider's JWKS,
	// issuer, audience and expiry. An unverified ID token is a JSON blob the
	// browser handed us.
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}).Verify(r.Context(), rawID)
	if err != nil {
		slog.Warn("oidc id token failed verification", "error", err)
		s.oidcFail(w, r, "The identity provider's token did not verify.")
		return
	}
	// The nonce ties this token to the request that started the flow, which is
	// what stops one obtained elsewhere being injected here.
	if idToken.Nonce != flow.nonce {
		s.oidcFail(w, r, "That sign-in did not match the request that started it.")
		return
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		s.oidcFail(w, r, "Could not read the identity provider's claims.")
		return
	}
	var raw map[string]any
	_ = idToken.Claims(&raw)
	groups := groupsFrom(raw, cfg.OIDC.GroupsClaim)

	if len(cfg.OIDC.AllowedGroups) > 0 && !anyGroupMatches(groups, cfg.OIDC.AllowedGroups) {
		slog.Warn("oidc sign-in refused: not in an allowed group",
			"subject", claims.Subject, "groups", groups)
		s.oidcFail(w, r, "That account is not permitted to administer this gateway.")
		return
	}

	// No admin groups configured means the provider itself is the gate, which
	// is only sane on an IdP dedicated to this gateway — but it is a
	// deliberate configuration, so honour it rather than quietly demoting.
	isAdmin := len(cfg.OIDC.AdminGroups) == 0 || anyGroupMatches(groups, cfg.OIDC.AdminGroups)

	// Read-only accounts are refused rather than admitted, because hz has no
	// read-only mode yet: every admin surface checks for the admin role, so a
	// viewer would get a valid session that authenticates nothing and an app
	// that errors on every panel. Failing closed with a reason beats handing
	// out a login that does not work. When viewer enforcement lands, this
	// becomes a role assignment instead.
	if !isAdmin {
		slog.Warn("oidc sign-in refused: not in an admin group",
			"subject", claims.Subject, "groups", groups, "admin_groups", cfg.OIDC.AdminGroups)
		s.oidcFail(w, r, "That account is not in an admin group, and hz has no read-only access yet.")
		return
	}
	role := db.RoleAdmin

	user, err := s.resolveOIDCUser(r, claims, role)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	if err := s.startUserSession(w, r, user); err != nil {
		s.oidcFail(w, r, "Could not start a session.")
		return
	}

	slog.Info("login", "user", user.Username, "ip", s.getClientIP(r), "factor", "oidc",
		"subject", claims.Subject, "role", role)

	// Back to the app, not to JSON: this endpoint is reached by a browser
	// redirect, and the person on the other end expects a page.
	http.Redirect(w, r, appBase(cfg), http.StatusFound)
}

// ssoError is a message meant for the person signing in.
//
// Go error strings are lowercase and unpunctuated by convention because they
// are usually wrapped into a larger sentence. These are the whole sentence,
// rendered in a browser, so they get their own type instead of pretending to
// be idiomatic Go errors or being rewritten into prose nobody can act on.
type ssoError string

func (e ssoError) Error() string { return string(e) }

// resolveOIDCUser maps verified claims onto a local account.
//
// Identity is the subject, never the username or email: those are the parts a
// provider lets people change, and matching on them would let a renamed
// account take over somebody else's. The subject is stored as a credential row
// so the link survives a rename on either side.
func (s *Server) resolveOIDCUser(r *http.Request, claims oidcClaims, role string) (*db.User, error) {
	ctx := r.Context()
	cfg := s.cfg()

	if user, _, err := s.users.UserByOIDCSubject(ctx, claims.Subject); err == nil {
		if !user.Enabled() {
			return nil, ssoError("That account is disabled.")
		}
		return user, nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}

	// No link yet. Attach to an existing account with the same username if one
	// exists — the operator who created it meant that person — otherwise
	// provision, if allowed.
	username := claims.usernameFor()
	user, err := s.users.UserByUsername(ctx, username)
	switch {
	case err == nil:
		if !user.Enabled() {
			return nil, ssoError("That account is disabled.")
		}
	case errors.Is(err, db.ErrNotFound):
		if !cfg.OIDC.AutoProvision {
			slog.Warn("oidc sign-in refused: no local account and auto-provisioning is off",
				"username", username, "subject", claims.Subject)
			return nil, ssoError("No account here matches that sign-in. Ask an admin to create one.")
		}
		user, err = s.users.CreateUser(ctx, username, claims.Email, role)
		if err != nil {
			return nil, err
		}
		slog.Info("user provisioned from oidc", "username", user.Username, "role", role)
	default:
		return nil, err
	}

	if _, err := s.users.AddCredential(ctx, user.ID, db.KindOIDC, claims.Subject, "", cfg.OIDCDisplayName()); err != nil {
		return nil, err
	}
	return user, nil
}

// oidcFail sends the browser back to the login page carrying the reason.
//
// A redirect rather than a JSON body: the caller is a browser following the
// provider's redirect, and an error page it cannot navigate away from is a
// dead end. The message is escaped into a query parameter the app renders.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	slog.Warn("oidc sign-in failed", "reason", msg, "ip", s.getClientIP(r))
	target := appBase(s.cfg()) + "?sso_error=" + url.QueryEscape(msg)
	http.Redirect(w, r, target, http.StatusFound)
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// appBase is where the SPA lives.
func appBase(cfg *config.Config) string {
	base := strings.TrimRight(strings.TrimSpace(cfg.AdminURL), "/")
	return base + "/app/"
}

// handleAPIOIDCStatus reports whether the login page should offer SSO.
func (s *Server) handleAPIOIDCStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cfg := s.cfg()
	ready, reason := cfg.OIDCReady()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"enabled": ready,
		"name":    cfg.OIDCDisplayName(),
		"reason":  reason,
	})
}
