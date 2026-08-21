package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Personal API tokens, for the signed-in account.
//
// Deliberately not an admin-managing-others surface: a token nobody but its
// owner ever held is the only kind whose use identifies a person. An admin who
// needs someone's automation stopped disables the account or revokes the token,
// both of which are visible actions — minting a credential on another user's
// behalf would put their name on something they never created.

type apiTokenView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	LastUsedIP  string     `json:"lastUsedIp,omitempty"`
	MFARequired bool       `json:"mfaRequired"`
}

func tokenViews(tokens []db.APIToken) []apiTokenView {
	out := make([]apiTokenView, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, apiTokenView{
			ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt,
			ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, LastUsedIP: t.LastUsedIP,
			MFARequired: t.MFARequired,
		})
	}
	return out
}

// GET/POST /api/v1/account/tokens
func (s *Server) handleAPIAccountTokens(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		// Not isAdmin: this is about *your* tokens, so it needs an account
		// rather than any administrative credential. The shared token has no
		// user to own them, and a VPN admin peer is a machine, not a person.
		writeJSONError(w, http.StatusUnauthorized,
			"Sign in with an account to manage personal tokens")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tokens, err := s.users.ListAPITokens(r.Context(), user.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not list tokens: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"tokens": tokenViews(tokens)})

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
			Days int    `json:"days,omitempty"`
			// Off unless asked for: a token exists to work unattended.
			MFARequired bool `json:"mfaRequired,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeJSONError(w, http.StatusBadRequest,
				"Give the token a name, so you can tell later what it was for")
			return
		}
		if req.Days < 0 {
			writeJSONError(w, http.StatusBadRequest, "days cannot be negative")
			return
		}

		token, meta, err := s.users.CreateAPIToken(r.Context(), user.ID,
			req.Name, time.Duration(req.Days)*24*time.Hour, req.MFARequired)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not create token: "+err.Error())
			return
		}
		slog.Info("api token created",
			"user", user.Username, "token", meta.Name, "token_id", meta.ID,
			"expires_days", req.Days, "ip", s.getClientIP(r))

		// The only time the raw token exists outside the database.
		writeJSON(w, map[string]any{
			"token": token,
			"meta":  tokenViews([]db.APIToken{*meta})[0],
			"note":  "Copy this now — it is not stored and cannot be shown again.",
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// DELETE /api/v1/account/tokens/{id}
func (s *Server) handleAPIAccountTokenDelete(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized,
			"Sign in with an account to manage personal tokens")
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "DELETE required")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/account/tokens/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusBadRequest, "token id required")
		return
	}

	// Scoped to the caller in the query, so guessing an identifier cannot
	// revoke somebody else's credential.
	if err := s.users.RevokeAPIToken(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "no such token")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "could not revoke: "+err.Error())
		return
	}
	slog.Info("api token revoked", "user", user.Username, "token_id", id, "ip", s.getClientIP(r))
	writeJSON(w, map[string]any{"ok": true})
}
