package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"homelab-horizon/internal/config"
)

// handleBackupExport produces a zip containing all state needed to clone this server.
// After importing and running a sync, the new server should behave identically.
//
// Zip layout:
//
//	config.json           — main config (public_ip cleared for re-detection)
//	token                 — admin bearer token
//	wireguard.conf        — WireGuard config (private key + peers)
//	invites.txt           — invite tokens (if any)
//	certs/live/<domain>/  — SSL certificates (fullchain.pem, privkey.pem, chain.pem)
//
//	GET /admin/backup/export
//	Authorization: Bearer <token>
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// 1. Config (clear public_ip so the new server re-detects)
	cfgCopy := *s.cfg()
	cfgCopy.PublicIP = ""
	cfgJSON, err := json.MarshalIndent(&cfgCopy, "", "  ")
	if err != nil {
		http.Error(w, "failed to marshal config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	zipWriteFile(zw, "config.json", cfgJSON)

	// 2. Admin token
	zipWriteFile(zw, "token", []byte(s.adminToken+"\n"))

	// 3. WireGuard config
	if s.cfg().WGConfigPath != "" {
		if data, err := os.ReadFile(s.cfg().WGConfigPath); err == nil {
			zipWriteFile(zw, "wireguard.conf", data)
		}
	}

	// 4. Invites
	if s.cfg().InvitesFile != "" {
		if data, err := os.ReadFile(s.cfg().InvitesFile); err == nil && len(data) > 0 {
			zipWriteFile(zw, "invites.txt", data)
		}
	}

	// 5. SSL certificates (letsencrypt live dir)
	if s.cfg().SSLEnabled && s.cfg().SSLCertDir != "" {
		liveDir := filepath.Join(s.cfg().SSLCertDir, "live")
		if entries, err := os.ReadDir(liveDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				domainDir := filepath.Join(liveDir, entry.Name())
				for _, certFile := range []string{"fullchain.pem", "privkey.pem", "chain.pem"} {
					certPath := filepath.Join(domainDir, certFile)
					if data, err := os.ReadFile(certPath); err == nil {
						zipWriteFile(zw, filepath.Join("certs", "live", entry.Name(), certFile), data)
					}
				}
			}
		}
	}

	if err := zw.Close(); err != nil {
		http.Error(w, "failed to finalize zip: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=homelab-horizon-backup.zip")
	w.Write(buf.Bytes())
}

// handleBackupImport restores server state from a backup zip.
// After import, run a sync to regenerate dnsmasq/haproxy configs and package certs.
//
//	POST /admin/backup/import
//	Authorization: Bearer <token>
//	Body: zip file
func (s *Server) handleBackupImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		http.Error(w, "invalid zip: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Index zip entries by name
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		files[f.Name] = data
	}

	// Require config.json
	cfgData, ok := files["config.json"]
	if !ok {
		http.Error(w, "zip missing config.json", http.StatusBadRequest)
		return
	}

	var cfg config.Config
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		http.Error(w, "invalid config.json: "+err.Error(), http.StatusBadRequest)
		return
	}

	var errors []string

	// 1. Write config
	cfg.PublicIP = "" // force re-detection
	if err := config.Save(s.configPath, &cfg); err != nil {
		errors = append(errors, fmt.Sprintf("config: %v", err))
	} else {
		s.config.Store(&cfg)
	}

	// 2. Write admin token
	if tokenData, ok := files["token"]; ok {
		token := strings.TrimSpace(string(tokenData))
		if token != "" {
			tokenFile := s.configPath + ".token"
			if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0600); err != nil {
				errors = append(errors, fmt.Sprintf("token: %v", err))
			} else {
				s.adminToken = token
			}
		}
	}

	// 3. Write WireGuard config
	if wgData, ok := files["wireguard.conf"]; ok && cfg.WGConfigPath != "" {
		if err := os.WriteFile(cfg.WGConfigPath, wgData, 0600); err != nil {
			errors = append(errors, fmt.Sprintf("wireguard: %v", err))
		} else {
			s.wg.Load()
		}
	}

	// 4. Write invites
	if invData, ok := files["invites.txt"]; ok && cfg.InvitesFile != "" {
		if err := os.WriteFile(cfg.InvitesFile, invData, 0600); err != nil {
			errors = append(errors, fmt.Sprintf("invites: %v", err))
		}
	}

	// 5. Restore SSL certificates
	if cfg.SSLEnabled && cfg.SSLCertDir != "" {
		for name, data := range files {
			if !strings.HasPrefix(name, "certs/live/") {
				continue
			}
			// certs/live/domain/file.pem -> {SSLCertDir}/live/domain/file.pem
			relPath := strings.TrimPrefix(name, "certs/")
			destPath := filepath.Join(cfg.SSLCertDir, relPath)
			if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
				errors = append(errors, fmt.Sprintf("ssl mkdir %s: %v", relPath, err))
				continue
			}
			if err := os.WriteFile(destPath, data, 0600); err != nil {
				errors = append(errors, fmt.Sprintf("ssl %s: %v", relPath, err))
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if len(errors) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "partial",
			"errors": errors,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

func zipWriteFile(zw *zip.Writer, name string, data []byte) {
	w, err := zw.Create(name)
	if err != nil {
		return
	}
	w.Write(data)
}

// backupAuthMiddleware accepts Bearer token (API) or session cookie / VPN admin (UI).
func (s *Server) backupAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bearer token (API/CLI usage)
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) && strings.TrimPrefix(auth, prefix) == s.adminToken {
			next(w, r)
			return
		}
		// Session cookie / VPN admin (UI usage)
		if s.isAdmin(r) {
			next(w, r)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}
