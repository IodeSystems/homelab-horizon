package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Configuring single sign-on from the UI.
//
// Until this existed, the only way to turn on SSO was to hand-edit
// config.json on the server and restart — for the one feature whose failures
// are hardest to read, because a wrong issuer and a wrong redirect URI both
// surface as an opaque error at the provider rather than in hz.
//
// Two rules shape the shape of this:
//
//  1. The client secret is write-only. It goes in, it never comes back; the
//     response says only whether one is stored. A settings page that hands
//     the secret back to every admin session is a secret with a wider blast
//     radius than it needs.
//  2. The redirect URI is reported, not accepted. hz derives it from
//     admin_url, so letting someone type it here would let hz and the
//     provider disagree about a value hz controls.

func (s *Server) handleAPIOIDCSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.cfg()
		resp := apitypes.OIDCSettingsResp{
			RedirectURI: cfg.OIDCRedirectURI(),
		}
		if _, why := cfg.OIDCReady(); why != "" {
			resp.NotReady = why
		}
		if o := cfg.OIDC; o != nil {
			resp.Enabled = o.Enabled
			resp.Issuer = o.Issuer
			resp.ClientID = o.ClientID
			resp.SecretStored = strings.TrimSpace(o.ClientSecret) != ""
			resp.Name = o.Name
			resp.Scopes = o.Scopes
			resp.GroupsClaim = o.GroupsClaim
			resp.AllowedGroups = o.AllowedGroups
			resp.AdminGroups = o.AdminGroups
			resp.AllowedEmailDomains = o.AllowedEmailDomains
			resp.RequiredClaims = o.RequiredClaims
			resp.AutoProvision = o.AutoProvision
		}
		_ = json.NewEncoder(w).Encode(resp)

	case http.MethodPut:
		var body apitypes.OIDCSettingsReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		body.Issuer = strings.TrimSpace(body.Issuer)
		body.ClientID = strings.TrimSpace(body.ClientID)

		// Refuse a configuration that cannot work, rather than storing it and
		// leaving the failure for whoever next tries to sign in.
		if body.Enabled {
			switch {
			case body.Issuer == "":
				writeJSONError(w, http.StatusBadRequest, "An issuer URL is required")
				return
			case !strings.HasPrefix(body.Issuer, "https://") && !strings.HasPrefix(body.Issuer, "http://"):
				writeJSONError(w, http.StatusBadRequest, "The issuer must be a URL")
				return
			case body.ClientID == "":
				writeJSONError(w, http.StatusBadRequest, "A client ID is required")
				return
			}
			if !strings.HasPrefix(s.cfg().OIDCRedirectURI(), "https://") {
				writeJSONError(w, http.StatusBadRequest,
					"admin_url must be an https URL before SSO can be enabled: the redirect URI is derived from it")
				return
			}
		}

		if err := s.updateConfig(func(c *config.Config) {
			existing := ""
			if c.OIDC != nil {
				existing = c.OIDC.ClientSecret
			}
			secret := existing
			// An empty secret means "leave it alone", which is what a form
			// that never shows the current one has to mean. ClearSecret is
			// how someone actually removes it.
			if strings.TrimSpace(body.ClientSecret) != "" {
				secret = body.ClientSecret
			}
			if body.ClearSecret {
				secret = ""
			}
			c.OIDC = &config.OIDCConfig{
				Enabled:             body.Enabled,
				Issuer:              body.Issuer,
				ClientID:            body.ClientID,
				ClientSecret:        secret,
				Name:                strings.TrimSpace(body.Name),
				Scopes:              trimAll(body.Scopes),
				GroupsClaim:         strings.TrimSpace(body.GroupsClaim),
				AllowedGroups:       trimAll(body.AllowedGroups),
				AdminGroups:         trimAll(body.AdminGroups),
				AllowedEmailDomains: trimAll(body.AllowedEmailDomains),
				RequiredClaims:      body.RequiredClaims,
				AutoProvision:       body.AutoProvision,
			}
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
			return
		}

		slog.Info("oidc settings updated", "enabled", body.Enabled, "issuer", body.Issuer,
			"auto_provision", body.AutoProvision, "email_domains", body.AllowedEmailDomains,
			"by", s.adminActor(r))
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleAPIOIDCDiscover fetches a provider's discovery document and reports
// what it found.
//
// This is the difference between "SSO does not work" and a specific reason. A
// wrong issuer, a provider that is down, and a certificate hz does not trust
// all fail identically at sign-in time, and none of them say so there.
func (s *Server) handleAPIOIDCDiscover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	issuer := strings.TrimSpace(body.Issuer)
	if issuer == "" {
		writeJSONError(w, http.StatusBadRequest, "An issuer URL is required")
		return
	}

	// Bounded: an issuer that never answers must not hold an admin's browser
	// open, and this endpoint takes a URL from the operator.
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		_ = json.NewEncoder(w).Encode(apitypes.OIDCDiscoverResp{Error: err.Error()})
		return
	}
	var claims struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		UserInfo      string `json:"userinfo_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	_ = provider.Claims(&claims)
	_ = json.NewEncoder(w).Encode(apitypes.OIDCDiscoverResp{
		OK:                   true,
		Issuer:               provider.Endpoint().AuthURL,
		AuthorizationEndoint: claims.Authorization,
		TokenEndpoint:        claims.Token,
		UserInfoEndpoint:     claims.UserInfo,
		JWKSURI:              claims.JWKS,
	})
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}
