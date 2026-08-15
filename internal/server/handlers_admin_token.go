package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// handleAPIAdminTokenDisable switches off the shared admin token.
//
// A shared bearer token gives no individual accountability: every action is
// attributable to "whoever holds it" and nothing finer, which is what PCI DSS
// 8.2.1 exists to prevent. Once VPN admin peers exist there is a better way
// in, and the token becomes a standing shared credential with no owner.
//
// Refused unless someone would still be able to administer the box. Recovery
// from a mistake here is a console restart with -enable-admin-token, which is
// fine when you are next to the machine and useless when you are not.
func (s *Server) handleAPIAdminTokenDisable(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Disabled bool `json:"disabled"`
		Force    bool `json:"force,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	// Re-enabling from the UI would defeat the point: the token is exactly
	// what an attacker holding it would use to turn itself back on. The only
	// way back is at the console.
	if !req.Disabled {
		writeJSONError(w, http.StatusBadRequest,
			"The admin token can only be re-enabled at the console: restart hz with -enable-admin-token")
		return
	}

	cfg := s.cfg()
	if len(cfg.VPNAdmins) == 0 && !req.Force {
		writeJSONError(w, http.StatusConflict,
			"No VPN admin peers exist, so disabling the token would leave no way to administer hz "+
				"except a console restart with -enable-admin-token. Promote a peer to admin first, "+
				"or resend with force:true if that is intended.")
		return
	}

	if err := s.updateConfig(func(c *config.Config) {
		c.AdminTokenDisabled = true
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	slog.Warn("admin token disabled; VPN admin peers are now the only way in",
		"vpn_admins", len(cfg.VPNAdmins), "by", s.adminActor(r))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"recovery": "restart hz with -enable-admin-token from the console to re-enable",
	})
}
