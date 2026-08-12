package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// getPeerFromRequest identifies the VPN peer making the request by their VPN IP.
func (s *Server) getPeerFromRequest(r *http.Request) (string, error) {
	clientIP := s.getClientIP(r)
	if clientIP == "" {
		return "", fmt.Errorf("cannot determine client IP")
	}
	if !s.isInVPNRange(clientIP) {
		return "", fmt.Errorf("not a VPN client")
	}
	peer := s.wg.GetPeerByIP(clientIP)
	if peer == nil {
		return "", fmt.Errorf("unknown VPN peer")
	}
	return peer.Name, nil
}

// handleAPIMFAStatus returns the MFA enrollment and session state for the requesting peer.
func (s *Server) handleAPIMFAStatus(w http.ResponseWriter, r *http.Request) {
	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	cfg := s.cfg()
	resp := apitypes.MFAStatusResponse{
		Durations: cfg.VPNMFADurations,
	}
	if len(resp.Durations) == 0 {
		resp.Durations = []string{"2h", "4h", "8h", "forever"}
	}

	if cfg.VPNMFASecrets != nil {
		_, resp.Enrolled = cfg.VPNMFASecrets[peerName]
	}

	if cfg.VPNMFASessions != nil {
		if expiry, ok := cfg.VPNMFASessions[peerName]; ok {
			resp.SessionActive = expiry == 0 || expiry > time.Now().Unix()
			if expiry != 0 {
				resp.SessionExpiry = time.Unix(expiry, 0).Format(time.RFC3339)
			}
		}
	}

	resp.PasskeysAvailable, resp.PasskeysUnavailableReason = PasskeysAvailable(cfg)
	for _, k := range cfg.PasskeysFor(peerName) {
		resp.Passkeys = append(resp.Passkeys, apitypes.PasskeyInfo{
			Label:        k.Label,
			CredentialID: k.CredentialID,
			AddedAt:      k.AddedAt,
			CloneWarning: k.CloneWarning,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleAPIMFAEnroll generates a new TOTP secret for the requesting peer.
// The secret is not saved until the peer confirms with a valid code via handleAPIMFAVerify.
func (s *Server) handleAPIMFAEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	cfg := s.cfg()
	if cfg.VPNMFASecrets != nil {
		if _, enrolled := cfg.VPNMFASecrets[peerName]; enrolled {
			writeJSONError(w, http.StatusConflict, "Already enrolled. Admin must reset to re-enroll.")
			return
		}
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Horizon VPN",
		AccountName: peerName,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to generate TOTP key: "+err.Error())
		return
	}

	// Store pending secret — it becomes active when the user confirms with a valid code.
	// We store it immediately so the user can scan the QR and verify in the next step.
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASecret(peerName, key.Secret())
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.MFAEnrollResponse{
		OK:              true,
		ProvisioningURI: key.URL(),
		Secret:          key.Secret(),
	})
}

// handleAPIMFAVerify validates a TOTP code and creates an MFA session.
func (s *Server) handleAPIMFAVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	peerName, err := s.getPeerFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	var req apitypes.MFAVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeJSONError(w, http.StatusBadRequest, "Code required")
		return
	}

	cfg := s.cfg()
	secret := ""
	if cfg.VPNMFASecrets != nil {
		secret = cfg.VPNMFASecrets[peerName]
	}
	if secret == "" {
		writeJSONError(w, http.StatusBadRequest, "Not enrolled. Enroll first.")
		return
	}

	if !totp.Validate(code, secret) {
		writeJSONError(w, http.StatusUnauthorized, "Invalid code")
		return
	}

	expiry, err := s.mfaSessionExpiry(cfg, req.Duration)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASession(peerName, expiry)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	s.rebuildWGChains()

	resp := apitypes.MFAVerifyResponse{OK: true}
	if expiry != 0 {
		resp.Expiry = mfaExpiryString(expiry)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// defaultMFADurations is the allowlist when the operator hasn't set one.
var defaultMFADurations = []string{"2h", "4h", "8h", "forever"}

// mfaSessionExpiry validates a requested session duration against the
// operator's allowlist and returns the unix expiry (0 = forever).
//
// Shared by the TOTP and passkey paths deliberately: which factor was used
// says nothing about how long a session should last, and two copies of this
// would drift the moment someone edited one allowlist check.
func (s *Server) mfaSessionExpiry(cfg *config.Config, requested string) (int64, error) {
	duration := strings.TrimSpace(requested)

	allowed := cfg.VPNMFADurations
	if len(allowed) == 0 {
		allowed = defaultMFADurations
	}
	valid := false
	for _, a := range allowed {
		if a == duration || (duration == "" && a == "forever") {
			valid = true
			break
		}
	}
	if !valid {
		return 0, fmt.Errorf("duration not allowed")
	}

	if duration == "" || duration == "forever" {
		return 0, nil
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %w", err)
	}
	return time.Now().Add(d).Unix(), nil
}

// mfaExpiryString renders a session expiry for the API.
func mfaExpiryString(expiry int64) string {
	return time.Unix(expiry, 0).Format(time.RFC3339)
}

// handleAPIMFAReset clears a peer's TOTP secret (admin only, forces re-enrollment).
func (s *Server) handleAPIMFAReset(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Peer name required")
		return
	}

	// Clears every factor, not just TOTP. An operator resetting a peer is
	// almost always responding to a lost or compromised device, and leaving a
	// registered passkey behind would let that device keep clearing the jail —
	// the opposite of what "reset" means.
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.ClearMFASecret(name)
		cfg.ClearPasskeys(name)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	slog.Info("MFA reset — TOTP secret and passkeys cleared", "peer", name)
	s.rebuildWGChains()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIMFAGrantSession grants an MFA session to a peer (admin only).
func (s *Server) handleAPIMFAGrantSession(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name     string `json:"name"`
		Duration string `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Peer name required")
		return
	}

	var expiry int64
	duration := strings.TrimSpace(req.Duration)
	if duration == "" || duration == "forever" {
		expiry = 0
	} else {
		d, err := time.ParseDuration(duration)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid duration: "+err.Error())
			return
		}
		expiry = time.Now().Add(d).Unix()
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASession(name, expiry)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	s.rebuildWGChains()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIMFARevokeSession revokes a peer's MFA session (admin only).
func (s *Server) handleAPIMFARevokeSession(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Peer name required")
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.ClearMFASession(name)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	s.rebuildWGChains()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAPIMFASettings returns/updates global MFA settings (admin only).
func (s *Server) handleAPIMFASettings(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if r.Method == http.MethodGet {
		cfg := s.cfg()
		durations := cfg.VPNMFADurations
		if durations == nil {
			durations = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.MFASettingsResponse{
			Enabled:   cfg.VPNMFAEnabled,
			Durations: durations,
		})
		return
	}

	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST required")
		return
	}

	var req struct {
		Enabled   bool     `json:"enabled"`
		Durations []string `json:"durations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.VPNMFAEnabled = req.Enabled
		if len(req.Durations) > 0 {
			cfg.VPNMFADurations = req.Durations
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	s.rebuildWGChains()
	// Toggling MFA changes the rules baked into haproxy.cfg itself, not just
	// the jailed-source list, so the config has to be regenerated.
	s.applyMFAJailConfig()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// startMFASessionPruner starts a goroutine that periodically prunes expired MFA sessions.
func (s *Server) startMFASessionPruner(done <-chan struct{}) {
	ticker := time.NewTicker(60 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !s.cfg().VPNMFAEnabled {
					continue
				}
				// Check if any sessions expired
				if s.cfg().PruneExpiredMFASessions() {
					// Persist the pruned config
					if err := s.updateConfig(func(cfg *config.Config) {
						cfg.PruneExpiredMFASessions()
					}); err != nil {
						slog.Warn("updateConfig prune MFA sessions", "err", err)
					}
					s.rebuildWGChains()
				}
			}
		}
	}()
}
