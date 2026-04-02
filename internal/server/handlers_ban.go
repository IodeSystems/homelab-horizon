package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
)

var banMu sync.Mutex

// iptables helpers

func iptablesBan(ip string) error {
	cmd := exec.Command("iptables", "-I", "INPUT", "1", "-s", ip, "-j", "DROP")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables ban failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func iptablesUnban(ip string) error {
	cmd := exec.Command("iptables", "-D", "INPUT", "-s", ip, "-j", "DROP")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables unban failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func iptablesCheckBan(ip string) bool {
	cmd := exec.Command("iptables", "-C", "INPUT", "-s", ip, "-j", "DROP")
	return cmd.Run() == nil
}

// banIP adds an iptables DROP rule and persists the ban to config.
func (s *Server) banIP(ip string, timeout int, reason, service string) error {
	banMu.Lock()
	defer banMu.Unlock()

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	ip = parsed.String() // normalize

	// Self-lockout protection
	gatewayIP := s.config.GetWGGatewayIP()
	if ip == gatewayIP || ip == s.config.LocalInterface || ip == s.config.PublicIP {
		return fmt.Errorf("refusing to ban server IP %s (self-lockout protection)", ip)
	}

	// Already banned?
	for _, b := range s.config.IPBans {
		if b.IP == ip {
			return nil // already banned, no-op
		}
	}

	if err := iptablesBan(ip); err != nil {
		return err
	}

	now := time.Now().Unix()
	ban := config.IPBan{
		IP:        ip,
		Timeout:   timeout,
		CreatedAt: now,
		Reason:    reason,
		Service:   service,
	}
	if timeout > 0 {
		ban.ExpiresAt = now + int64(timeout)
	}

	s.config.IPBans = append(s.config.IPBans, ban)

	fmt.Printf("[ban] banned %s timeout=%d reason=%q service=%q\n", ip, timeout, reason, service)

	return config.Save(s.configPath, s.config)
}

// unbanIP removes the iptables rule and removes the ban from config.
func (s *Server) unbanIP(ip string) error {
	banMu.Lock()
	defer banMu.Unlock()

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	ip = parsed.String()

	// Remove iptables rule (ignore error if rule doesn't exist)
	_ = iptablesUnban(ip)

	// Remove from config
	filtered := s.config.IPBans[:0]
	for _, b := range s.config.IPBans {
		if b.IP != ip {
			filtered = append(filtered, b)
		}
	}
	s.config.IPBans = filtered

	fmt.Printf("[ban] unbanned %s\n", ip)

	return config.Save(s.configPath, s.config)
}

// reapplyBans restores iptables rules from persisted config on startup.
func (s *Server) reapplyBans() {
	banMu.Lock()
	defer banMu.Unlock()

	now := time.Now().Unix()
	active := s.config.IPBans[:0]
	for _, ban := range s.config.IPBans {
		if ban.ExpiresAt > 0 && ban.ExpiresAt <= now {
			// Expired — clean up iptables just in case
			_ = iptablesUnban(ban.IP)
			fmt.Printf("[ban] expired ban removed on startup: %s\n", ban.IP)
			continue
		}
		if !iptablesCheckBan(ban.IP) {
			if err := iptablesBan(ban.IP); err != nil {
				fmt.Printf("[ban] failed to reapply ban for %s: %v\n", ban.IP, err)
			} else {
				fmt.Printf("[ban] reapplied ban for %s\n", ban.IP)
			}
		}
		active = append(active, ban)
	}
	if len(active) != len(s.config.IPBans) {
		s.config.IPBans = active
		if err := config.Save(s.configPath, s.config); err != nil {
			fmt.Printf("[ban] failed to save config after reapply: %v\n", err)
		}
	}
}

// startBanExpiry runs a background goroutine that removes expired bans every 30s.
func (s *Server) startBanExpiry() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now().Unix()
		var expired []string
		for _, ban := range s.config.IPBans {
			if ban.ExpiresAt > 0 && ban.ExpiresAt <= now {
				expired = append(expired, ban.IP)
			}
		}
		for _, ip := range expired {
			if err := s.unbanIP(ip); err != nil {
				fmt.Printf("[ban] failed to expire ban for %s: %v\n", ip, err)
			} else {
				fmt.Printf("[ban] expired ban for %s\n", ip)
			}
		}
	}
}

// banListEntries converts config bans to API response entries.
func banListEntries(bans []config.IPBan) []apitypes.BanEntry {
	entries := make([]apitypes.BanEntry, len(bans))
	for i, b := range bans {
		entries[i] = apitypes.BanEntry{
			IP:        b.IP,
			Timeout:   b.Timeout,
			CreatedAt: b.CreatedAt,
			ExpiresAt: b.ExpiresAt,
			Reason:    b.Reason,
			Service:   b.Service,
		}
	}
	return entries
}

// Service API — deploy token auth, no CSRF

func (s *Server) handleBanAPI(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, "Authorization: Bearer <token> required", http.StatusUnauthorized)
		return
	}

	idx := s.findServiceByDeployToken(token)
	if idx < 0 {
		http.Error(w, "invalid deploy token", http.StatusUnauthorized)
		return
	}

	service := s.config.Services[idx].Name
	action := strings.TrimPrefix(r.URL.Path, "/api/ban/")

	switch action {
	case "ban":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req apitypes.BanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.IP == "" {
			writeJSONError(w, http.StatusBadRequest, "ip is required")
			return
		}
		if err := s.banIP(req.IP, req.Timeout, req.Reason, service); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})

	case "unban":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req apitypes.UnbanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.IP == "" {
			writeJSONError(w, http.StatusBadRequest, "ip is required")
			return
		}
		if err := s.unbanIP(req.IP); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})

	case "list":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apitypes.BanListResponse{Bans: banListEntries(s.config.IPBans)})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// Admin API — session auth

func (s *Server) handleAPIBanList(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.BanListResponse{Bans: banListEntries(s.config.IPBans)})
}

func (s *Server) handleAPIBanAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req apitypes.BanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IP == "" {
		writeJSONError(w, http.StatusBadRequest, "ip is required")
		return
	}
	if err := s.banIP(req.IP, req.Timeout, req.Reason, "admin"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}

func (s *Server) handleAPIBanRemove(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req apitypes.UnbanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IP == "" {
		writeJSONError(w, http.StatusBadRequest, "ip is required")
		return
	}
	if err := s.unbanIP(req.IP); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}
