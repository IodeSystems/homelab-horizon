package server

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
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
//
// There is no swap. Under `balance first` next is listed first (render.go),
// so a label swap would put the slot still running the old code first; the
// rolling sequence leaves both slots on the new code instead.
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
	backendName := haproxy.BackendName(svc.Name)
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
		http.Error(w, "swap is removed: next is listed first, so swapping labels would serve the old code; deploy with the rolling sequence (hz-client rolling start|continue|finalize)", http.StatusGone)

	case "maint-page":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) < 2 {
			http.Error(w, "missing subcommand (set/clear)", http.StatusBadRequest)
			return
		}
		s.handleDeployMaintPage(w, r, idx, parts[1])

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

	// Both slots are reported as ADDRESSES: this is the status a deployer
	// reads to know where to push, and HAProxy's own state is keyed by the
	// address too, so a "@nas:8080" here would match nothing.
	cfg := s.cfg()
	backend := cfg.ServiceBackend(svc)
	resolved := cfg.ResolvedDeploy(svc)

	status := apitypes.DeployStatus{
		Service:     svc.Name,
		Domain:      svc.PrimaryDomain(),
		Domains:     svc.Domains,
		ActiveSlot:  deploy.ActiveSlot,
		Balance:     balance,
		HealthCheck: healthCheck,
		Current: apitypes.DeploySlotStatus{
			Slot:    currentSlot,
			Backend: resolved.CurrentServer(backend),
			State:   "unknown",
		},
		Next: apitypes.DeploySlotStatus{
			Slot:    nextSlot,
			Backend: resolved.InactiveServer(backend),
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
	_ = json.NewEncoder(w).Encode(status)
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
	_ = json.NewEncoder(w).Encode(apitypes.DeployStateChangeResponse{
		Status: "ok",
		Server: slot,
		State:  action,
	})
}

func (s *Server) handleDeployMaintPage(w http.ResponseWriter, r *http.Request, svcIdx int, sub string) {
	var html string
	switch sub {
	case "set":
		var req struct {
			HTML string `json:"html"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.HTML == "" {
			http.Error(w, "body must be JSON {\"html\": \"...\"}", http.StatusBadRequest)
			return
		}
		html = req.HTML
	case "clear":
		// html stays ""
	default:
		http.Error(w, "unknown subcommand: "+sub+", expected set/clear", http.StatusBadRequest)
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.Services[svcIdx].Proxy.MaintenancePage = html
	}); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.syncHAProxyBackends()
	ssl := haproxySSLFor(s.cfg())
	if err := s.haproxy.WriteConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, ssl); err != nil {
		http.Error(w, "failed to write haproxy config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.haproxy.Reload(); err != nil {
		http.Error(w, "failed to reload haproxy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if html != "" {
		sum := md5.Sum([]byte(html))
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "maintenance_page_md5": fmt.Sprintf("%x", sum)})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
