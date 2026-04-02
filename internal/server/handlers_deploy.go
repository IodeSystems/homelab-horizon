package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
)

// DeployStatus is the JSON response for GET /api/deploy/{token}/status
type DeployStatus struct {
	Service     string           `json:"service"`
	Domain      string           `json:"domain"`
	Domains     []string         `json:"domains,omitempty"`
	ActiveSlot  string           `json:"active_slot"`
	Balance     string           `json:"balance"`
	HealthCheck string           `json:"health_check"`
	Current     DeploySlotStatus `json:"current"`
	Next        DeploySlotStatus `json:"next"`
}

type DeploySlotStatus struct {
	Slot    string `json:"slot"`    // "a" or "b"
	Backend string `json:"backend"` // host:port
	State   string `json:"state"`   // "up", "drain", "maint", "down", "unknown"
}

// findServiceByDeployToken returns the service index matching the deploy token.
func (s *Server) findServiceByDeployToken(token string) int {
	for i, svc := range s.config.Services {
		if svc.Proxy != nil && svc.Proxy.Deploy != nil && svc.Proxy.Deploy.Token == token {
			return i
		}
	}
	return -1
}

// handleDeployAPI handles all /api/deploy/{token}/... requests.
// Routes:
//   GET  /api/deploy/{token}/status
//   POST /api/deploy/{token}/current/up
//   POST /api/deploy/{token}/current/drain
//   POST /api/deploy/{token}/current/down
//   POST /api/deploy/{token}/next/up
//   POST /api/deploy/{token}/next/drain
//   POST /api/deploy/{token}/next/down
//   POST /api/deploy/{token}/swap
func (s *Server) handleDeployAPI(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/deploy/{token}/...
	path := strings.TrimPrefix(r.URL.Path, "/api/deploy/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	token := parts[0]
	idx := s.findServiceByDeployToken(token)
	if idx < 0 {
		http.Error(w, "invalid deploy token", http.StatusUnauthorized)
		return
	}

	svc := &s.config.Services[idx]
	deploy := svc.Proxy.Deploy
	backendName := haproxy.SanitizeName(svc.Name) + "_backend"
	action := parts[1]

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
		if len(parts) < 3 {
			http.Error(w, "missing action (up/drain/down)", http.StatusBadRequest)
			return
		}
		s.handleDeployStateChange(w, backendName, action, parts[2])

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

	status := DeployStatus{
		Service:     svc.Name,
		Domain:      svc.PrimaryDomain(),
		Domains:     svc.Domains,
		ActiveSlot:  deploy.ActiveSlot,
		Balance:     balance,
		HealthCheck: healthCheck,
		Current: DeploySlotStatus{
			Slot:    currentSlot,
			Backend: deploy.CurrentServer(svc.Proxy.Backend),
			State:   "unknown",
		},
		Next: DeploySlotStatus{
			Slot:    nextSlot,
			Backend: deploy.InactiveServer(svc.Proxy.Backend),
			State:   "unknown",
		},
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
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"server": slot,
		"state":  action,
	})
}

func (s *Server) handleDeploySwap(w http.ResponseWriter, svcIdx int) {
	deploy := s.config.Services[svcIdx].Proxy.Deploy
	deploy.Swap()

	// Save config
	if err := config.Save(s.configPath, s.config); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Regenerate and reload haproxy so current/next ports reflect the new active slot
	s.syncHAProxyBackends()
	ssl := &haproxy.SSLConfig{
		Enabled: s.config.SSLEnabled,
		CertDir: s.config.SSLHAProxyCertDir,
	}
	if err := s.haproxy.WriteConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, ssl); err != nil {
		http.Error(w, "failed to write haproxy config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.haproxy.Reload(); err != nil {
		http.Error(w, "failed to reload haproxy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	backend := s.config.Services[svcIdx].Proxy.Backend

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"active_slot": deploy.ActiveSlot,
		"current":     deploy.CurrentServer(backend),
		"next":        deploy.InactiveServer(backend),
	})
}

