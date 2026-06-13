package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/sitedeploy"
)

const (
	// siteReleasesKept is how many releases to retain per static site (for rollback).
	siteReleasesKept = 5
	// siteMaxUploadBytes caps the compressed upload body. The uncompressed
	// total is bounded separately by sitedeploy.DefaultLimits.
	siteMaxUploadBytes = 256 << 20
)

// handleSiteAPI handles /api/site/... for static-folder services, authenticated
// by the per-service deploy token (Authorization: Bearer <token>).
//
//	POST /api/site/upload[?validate=1]   body: tar.gz of the site directory
//	POST /api/site/rollback              revert to the previous release
//	GET  /api/site/releases              list retained releases
func (s *Server) handleSiteAPI(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, "Authorization: Bearer <token> required", http.StatusUnauthorized)
		return
	}
	idx := s.findServiceByToken(token)
	if idx < 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	svc := &s.cfg().Services[idx]
	if svc.Proxy == nil || svc.Proxy.StaticRoot == "" {
		http.Error(w, "not a static-folder service", http.StatusBadRequest)
		return
	}

	mgr := s.siteManager(svc)
	action := strings.TrimPrefix(r.URL.Path, "/api/site/")
	switch action {
	case "upload":
		s.handleSiteUpload(w, r, mgr)
	case "rollback":
		s.handleSiteRollback(w, r, mgr)
	case "releases":
		s.handleSiteReleases(w, r, mgr)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (s *Server) handleSiteUpload(w http.ResponseWriter, r *http.Request, mgr *sitedeploy.Manager) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	dryRun := r.URL.Query().Get("validate") == "1" || r.URL.Query().Get("validate") == "true"

	// Bound the compressed body; the uncompressed total is bounded in Deploy.
	body := http.MaxBytesReader(w, r.Body, siteMaxUploadBytes)
	id := time.Now().UTC().Format("20060102T150405.000000Z")

	res, err := mgr.Deploy(body, id, dryRun, sitedeploy.DefaultLimits)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, res)
}

func (s *Server) handleSiteRollback(w http.ResponseWriter, r *http.Request, mgr *sitedeploy.Manager) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	rel, err := mgr.Rollback()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"rolledBackTo": rel})
}

func (s *Server) handleSiteReleases(w http.ResponseWriter, r *http.Request, mgr *sitedeploy.Manager) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	rels, err := mgr.Releases()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rels)
}

// siteManager builds a release manager for svc's static root. When hz runs as
// root, extracted files are chowned to the unprivileged file-server user so the
// child process can read them.
func (s *Server) siteManager(svc *config.Service) *sitedeploy.Manager {
	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		if cred, err := unprivilegedCredential(); err == nil {
			uid, gid = int(cred.Uid), int(cred.Gid)
		}
	}
	return sitedeploy.New(svc.Proxy.StaticRoot, siteReleasesKept, uid, gid)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
