package server

// Phase 1 of multi-instance HA — see plan/plan.md.
//
// /api/peer/* endpoints are reachable only over the WireGuard site-to-site
// tunnel (the auth boundary between fleet members). They are unauthenticated
// from the application's perspective: WG itself gates them.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"homelab-horizon/internal/config"
)

// PeerPingResponse is returned by GET /api/peer/ping.
type PeerPingResponse struct {
	PeerID        string `json:"peer_id"`
	ConfigPrimary bool   `json:"config_primary"`
}

// handlePeerPing returns this instance's identity. Cheap, used for liveness
// at decision time (cert ownership, config sync) instead of a heartbeat.
func (s *Server) handlePeerPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PeerPingResponse{
		PeerID:        s.cfg().PeerID,
		ConfigPrimary: s.cfg().ConfigPrimary,
	})
}

// handlePeerConfig returns the full config JSON. Only the config primary
// serves a meaningful response; non-primaries can serve too (it's read-only)
// but pulls should target the primary.
func (s *Server) handlePeerConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg()

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PeerCertResponse is returned by GET /api/peer/cert/<domain>.
type PeerCertResponse struct {
	Domain string `json:"domain"`
	Cert   string `json:"cert"` // fullchain.pem contents
	Key    string `json:"key"`  // privkey.pem contents
}

// handlePeerCert returns the certificate + private key for a domain so
// non-owner peers can pull certs they don't renew themselves. The domain
// is extracted from the URL path suffix after "/api/peer/cert/".
//
// Restricted to configured peers via peerOnlyMiddleware (item 2).
func (s *Server) handlePeerCert(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimPrefix(r.URL.Path, "/api/peer/cert/")
	if domain == "" {
		http.Error(w, "domain required", http.StatusBadRequest)
		return
	}

	cfg := s.cfg()
	// Strip wildcard prefix for the filesystem path (certs stored under
	// the base domain, e.g. /etc/letsencrypt/live/example.com/).
	baseDomain := strings.TrimPrefix(domain, "*.")
	certDir := filepath.Join(cfg.SSLCertDir, "live", baseDomain)

	certData, err := os.ReadFile(filepath.Join(certDir, "fullchain.pem"))
	if err != nil {
		http.Error(w, "cert not found", http.StatusNotFound)
		return
	}
	keyData, err := os.ReadFile(filepath.Join(certDir, "privkey.pem"))
	if err != nil {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PeerCertResponse{
		Domain: domain,
		Cert:   string(certData),
		Key:    string(keyData),
	})
}

// peerOnlyMiddleware allows only requests from configured peer addresses.
// When the fleet has configured peers, only those specific wg_addr hosts
// are allowed — not the entire VPN CIDR. This is critical for Phase 2
// endpoints like /api/peer/cert/:domain that expose private key material.
//
// Falls back to VPN CIDR check when no peers are configured (standalone
// mode or primary with no peers listed) so the endpoint still works in
// development/testing.
func (s *Server) peerOnlyMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if !s.isAllowedPeer(host) {
			http.Error(w, "peer api: not a configured peer", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// isAllowedPeer reports whether ip is a configured fleet peer. When
// peers are configured, only their wg_addr hosts (stripped of port) are
// accepted. When no peers are configured, falls back to VPN CIDR.
func (s *Server) isAllowedPeer(ip string) bool {
	peers := s.cfg().Peers
	if len(peers) == 0 {
		return s.isInVPNRange(ip)
	}
	for _, p := range peers {
		peerHost := p.WGAddr
		if h, _, err := net.SplitHostPort(peerHost); err == nil {
			peerHost = h
		}
		if peerHost == ip {
			return true
		}
	}
	return false
}

// nonPrimaryGuardMiddleware returns 403 with the primary peer ID when this
// instance is non-primary and the request is a config-mutating call.
//
// Default policy is "block on non-primary" for any non-GET request. Routes
// that should be allowed (per-instance runtime ops, peer-to-peer plumbing,
// auth) opt out by being registered via handlePeerInstance /
// handlePeerInstanceSubtree, which records them in s.peerInstancePaths /
// s.peerInstancePrefixes. There is no separate exempt list to maintain.
func (s *Server) nonPrimaryGuardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Primary always serves.
		if s.cfg().ConfigPrimary || s.cfg().PeerID == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Read-only methods always pass through.
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if s.isPeerInstanceRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		primaryID := ""
		if p := s.cfg().PrimaryPeer(); p != nil {
			primaryID = p.ID
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"error":      "this instance is read-only — edit on the config primary",
			"primary_id": primaryID,
		})
	})
}

// handlePeerInstance registers an exact-path route as a per-instance op
// (per-instance runtime, not a shared-config mutation). The path is
// recorded in s.peerInstancePaths so nonPrimaryGuardMiddleware will let
// non-GET requests through on a non-primary instance.
//
// Use this for any route that must keep working on the read-only spare:
// auth, peer-to-peer plumbing, subsystem reloads, sync triggers, etc.
// Default for routes registered via plain mux.HandleFunc is "blocked on
// non-primary".
func (s *Server) handlePeerInstance(mux *http.ServeMux, path string, h http.HandlerFunc) {
	if s.peerInstancePaths == nil {
		s.peerInstancePaths = make(map[string]bool)
	}
	s.peerInstancePaths[path] = true
	mux.HandleFunc(path, h)
}

// handlePeerInstanceSubtree is the subtree-pattern variant of
// handlePeerInstance. Use it when the underlying mux registration is a
// trailing-slash subtree (e.g. "/api/deploy/") so every path under the
// prefix is exempt from the non-primary guard.
func (s *Server) handlePeerInstanceSubtree(mux *http.ServeMux, prefix string, h http.HandlerFunc) {
	s.peerInstancePrefixes = append(s.peerInstancePrefixes, prefix)
	mux.HandleFunc(prefix, h)
}

// isPeerInstanceRoute reports whether the given URL path was registered as
// a per-instance route via handlePeerInstance or handlePeerInstanceSubtree.
func (s *Server) isPeerInstanceRoute(path string) bool {
	if s.peerInstancePaths[path] {
		return true
	}
	for _, prefix := range s.peerInstancePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// applyNewConfig swaps the live config and reloads derived subsystems.
// Used by the pull loop on non-primary instances.
//
// Fields that affect bind address, WG interface name, or token storage are
// not hot-swappable; if those change, log a warning and the operator must
// restart the instance.
func (s *Server) applyNewConfig(newCfg *config.Config) error {
	old := s.cfg()
	// Preserve runtime-only fields that should never come from the primary.
	newCfg.AdminToken = old.AdminToken
	s.config.Store(newCfg)

	if err := config.Save(s.configPath, newCfg); err != nil {
		return fmt.Errorf("save: %w", err)
	}

	// Re-derive subsystem state (dnsmasq mappings, haproxy backends, LE).
	s.syncServices()

	// Monitor watches ServiceChecks + auto-generated checks from Services.
	// Reload picks up both. Cheap when nothing actually changed but we only
	// reach here on a real config delta, so accept the unconditional restart.
	s.monitor.Reload(newCfg)

	if old.ListenAddr != newCfg.ListenAddr ||
		old.WGInterface != newCfg.WGInterface ||
		old.WGConfigPath != newCfg.WGConfigPath {
		fmt.Println("[peer-sync] WARNING: low-level config changed (listen/wg) — restart required to take effect")
	}
	return nil
}

