package server

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
)


// findServiceByToken returns the service index matching either the service token
// or the legacy deploy token.
func (s *Server) findServiceByToken(token string) int {
	for i, svc := range s.cfg().Services {
		if svc.Token == token {
			return i
		}
		if svc.Proxy != nil && svc.Proxy.Deploy != nil && svc.Proxy.Deploy.Token == token {
			return i
		}
	}
	return -1
}

// extractBearerToken gets the deploy token from the Authorization: Bearer header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// handleDeployAPI handles all /api/deploy/... requests.
// Token must be in Authorization: Bearer header.
// Routes:
//
//	GET  /api/deploy/status
//	POST /api/deploy/current/up|drain|down
//	POST /api/deploy/next/up|drain|down
//	POST /api/deploy/swap
func (s *Server) handleDeployAPI(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, "Authorization: Bearer <token> required", http.StatusUnauthorized)
		return
	}

	idx := s.findServiceByToken(token)
	if idx < 0 {
		http.Error(w, "invalid deploy token", http.StatusUnauthorized)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/deploy/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	svc := &s.cfg().Services[idx]
	deploy := svc.Proxy.Deploy
	backendName := haproxy.SanitizeName(svc.Name) + "_backend"
	action := parts[0]

	switch action {
	case "status":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleDeployStatus(w, svc, deploy, backendName)

	case "current", "next":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) < 2 {
			http.Error(w, "missing action (up/drain/down)", http.StatusBadRequest)
			return
		}
		s.handleDeployStateChange(w, backendName, action, parts[1])

	case "swap":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleDeploySwap(w, idx)

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (s *Server) handleDeployStatus(w http.ResponseWriter, svc *config.Service, deploy *config.DeployConfig, backendName string) {
	balance := deploy.Balance
	if balance == "" {
		balance = "first"
	}

	healthCheck := ""
	if svc.Proxy.HealthCheck != nil {
		healthCheck = svc.Proxy.HealthCheck.Path
	}

	// Determine which underlying slot letter is current vs next
	currentSlot := "a"
	nextSlot := "b"
	if deploy.ActiveSlot == "b" {
		currentSlot = "b"
		nextSlot = "a"
	}

	status := apitypes.DeployStatus{
		Service:     svc.Name,
		Domain:      svc.PrimaryDomain(),
		Domains:     svc.Domains,
		ActiveSlot:  deploy.ActiveSlot,
		Balance:     balance,
		HealthCheck: healthCheck,
		Current: apitypes.DeploySlotStatus{
			Slot:    currentSlot,
			Backend: deploy.CurrentServer(svc.Proxy.Backend),
			State:   "unknown",
		},
		Next: apitypes.DeploySlotStatus{
			Slot:    nextSlot,
			Backend: deploy.InactiveServer(svc.Proxy.Backend),
			State:   "unknown",
		},
	}
	if svc.Proxy.MaintenancePage != "" {
		sum := md5.Sum([]byte(svc.Proxy.MaintenancePage))
		status.MaintenancePageMD5 = fmt.Sprintf("%x", sum)
	}

	// Query haproxy socket for live state
	if states, err := s.haproxy.GetServerState(backendName); err == nil {
		if st, ok := states["current"]; ok {
			status.Current.State = st
		}
		if st, ok := states["next"]; ok {
			status.Next.State = st
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleDeployStateChange(w http.ResponseWriter, backendName, slot, action string) {
	var haState string
	switch action {
	case "up":
		haState = "ready"
	case "drain":
		haState = "drain"
	case "down":
		haState = "maint"
	default:
		http.Error(w, "unknown action: "+action+", expected up/drain/down", http.StatusBadRequest)
		return
	}

	if err := s.haproxy.SetServerState(backendName, slot, haState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DeployStateChangeResponse{
		Status: "ok",
		Server: slot,
		State:  action,
	})
}

func (s *Server) handleDeploySwap(w http.ResponseWriter, svcIdx int) {
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.Services[svcIdx].Proxy.Deploy.Swap()
	}); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Regenerate and reload haproxy so current/next ports reflect the new active slot
	s.syncHAProxyBackends()
	ssl := &haproxy.SSLConfig{
		Enabled: s.cfg().SSLEnabled,
		CertDir: s.cfg().SSLHAProxyCertDir,
	}
	if err := s.haproxy.WriteConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, ssl); err != nil {
		http.Error(w, "failed to write haproxy config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.haproxy.Reload(); err != nil {
		http.Error(w, "failed to reload haproxy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	svc := s.cfg().Services[svcIdx]
	deploy := svc.Proxy.Deploy

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.DeploySwapResponse{
		Status:     "ok",
		ActiveSlot: deploy.ActiveSlot,
		Current:    deploy.CurrentServer(svc.Proxy.Backend),
		Next:       deploy.InactiveServer(svc.Proxy.Backend),
	})
}

