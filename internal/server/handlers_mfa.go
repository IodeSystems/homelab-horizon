package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASecret(peerName, key.Secret())
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.MFAEnrollResponse{
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

	// Parse duration
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

	// Validate duration is in allowed list
	allowed := cfg.VPNMFADurations
	if len(allowed) == 0 {
		allowed = []string{"2h", "4h", "8h", "forever"}
	}
	valid := false
	for _, a := range allowed {
		if a == duration || (duration == "" && a == "forever") {
			valid = true
			break
		}
	}
	if !valid {
		writeJSONError(w, http.StatusBadRequest, "Duration not allowed")
		return
	}

	s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASession(peerName, expiry)
	})
	s.rebuildWGForwardChain()

	resp := apitypes.MFAVerifyResponse{OK: true}
	if expiry != 0 {
		resp.Expiry = time.Unix(expiry, 0).Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

	s.updateConfig(func(cfg *config.Config) {
		cfg.ClearMFASecret(name)
	})
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
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

	s.updateConfig(func(cfg *config.Config) {
		cfg.SetMFASession(name, expiry)
	})
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
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

	s.updateConfig(func(cfg *config.Config) {
		cfg.ClearMFASession(name)
	})
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
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
		json.NewEncoder(w).Encode(apitypes.MFASettingsResponse{
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

	s.updateConfig(func(cfg *config.Config) {
		cfg.VPNMFAEnabled = req.Enabled
		if len(req.Durations) > 0 {
			cfg.VPNMFADurations = req.Durations
		}
	})
	s.rebuildWGForwardChain()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
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
					s.updateConfig(func(cfg *config.Config) {
						cfg.PruneExpiredMFASessions()
					})
					s.rebuildWGForwardChain()
				}
			}
		}
	}()
}
