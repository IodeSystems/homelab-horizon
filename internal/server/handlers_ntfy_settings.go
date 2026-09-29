package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz's own ntfy target: where check failures and DNS drift are posted, and
// the optional access token that authenticates those posts.
//
// The token is write-only, by the same rule as the OIDC client secret and a
// vantage's token: it goes in, it never comes back. GET says only whether one
// is stored (hasNtfyToken); an empty token on PUT keeps the stored one,
// because a form that never shows it cannot mean "erase it" by being blank;
// clearToken is how it is removed. It is also kept out of the pending diff
// (settingsExcluded), which would otherwise serve it before and after.
//
// GET/PUT /api/v1/settings/ntfy
func (s *Server) handleAPINtfySettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.cfg()
		_ = json.NewEncoder(w).Encode(apitypes.NtfySettingsResp{
			URL:          cfg.NtfyURL,
			HasNtfyToken: cfg.NtfyToken != "",
		})

	case http.MethodPut:
		var body apitypes.NtfySettingsReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		body.URL = strings.TrimSpace(body.URL)
		body.Token = strings.TrimSpace(body.Token)
		if body.URL != "" {
			u, err := url.Parse(body.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				writeJSONError(w, http.StatusBadRequest, "The ntfy URL must be an http(s) URL with a host")
				return
			}
		}
		if body.Token != "" && body.ClearToken {
			writeJSONError(w, http.StatusBadRequest, "Send a new token or clear the stored one, not both")
			return
		}

		if err := s.updateConfig(func(c *config.Config) {
			c.NtfyURL = body.URL
			switch {
			case body.ClearToken:
				c.NtfyToken = ""
			case body.Token != "":
				c.NtfyToken = body.Token
			}
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
			return
		}

		// Whether a token is set and whether this request changed it — never
		// the token.
		slog.Info("ntfy settings updated",
			"enabled", body.URL != "", "token_set", s.cfg().NtfyToken != "",
			"token_changed", body.Token != "" || body.ClearToken, "by", s.adminActor(r))
		_ = json.NewEncoder(w).Encode(apitypes.NtfySettingsResp{
			URL:          s.cfg().NtfyURL,
			HasNtfyToken: s.cfg().NtfyToken != "",
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
