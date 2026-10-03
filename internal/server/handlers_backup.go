package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/backup"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// handleBackupExport answers with a zip of this server's state. hz never sends
// it anywhere: a caller PULLS it (`hz backup pull`, CLAUDE.md #1).
//
// Zip layout:
//
//	manifest.json         — hz version, schema version, snapshot time, per-table
//	                        row counts, the sha256 of every other file, and what
//	                        is excluded (backup.Manifest)
//	config.json           — main config (public_ip cleared for re-detection)
//	token                 — admin bearer token
//	wireguard.conf        — WireGuard config (private key + peers)
//	invites.txt           — invite tokens (if any)
//	certs/live/<domain>/  — SSL certificates (fullchain.pem, privkey.pem, chain.pem)
//	hz.db                 — an online snapshot of the database (VACUUM INTO, so
//	                        committed WAL content is in it): users, sessions, API
//	                        tokens, the config manager's registrations, approvals,
//	                        wrapped keys and sealed values, deploy reports,
//	                        promotions, applies, builds, kept backups and restore
//	                        tests
//	builds/<project>/<n>.log — build logs
//
// NOT in the zip, on purpose: <data dir>/artifacts/ (release artifacts, large,
// and re-uploadable from the build that produced them; their sha256s are in
// hz.db) and <data dir>/child-bin/ (server binaries a nested hz installs,
// re-staged from a release). The manifest's "excluded" says the same.
//
// Before anything is served the snapshot is opened read-only and must pass
// integrity_check, be at exactly this binary's latest migration, and hold the
// row counts read from the live database. Any failure answers 500 with the
// reason and no zip.
//
//	GET /admin/backup/export
//	Authorization: Bearer <token>
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		slog.Error("LOUD: backup export refused", "reason", msg)
		http.Error(w, "backup refused: "+msg, http.StatusInternalServerError)
	}
	if s.users == nil {
		fail("the database is not open, and a backup without hz.db is not a backup of this server")
		return
	}

	var entries []zipEntry
	add := func(name string, data []byte) { entries = append(entries, zipEntry{name, data}) }

	// 1. Config (clear public_ip so the new server re-detects)
	cfgCopy := *s.cfg()
	cfgCopy.PublicIP = ""
	cfgJSON, err := json.MarshalIndent(&cfgCopy, "", "  ")
	if err != nil {
		fail("marshal config: %v", err)
		return
	}
	add("config.json", cfgJSON)

	// 2. Admin token
	add("token", []byte(s.adminToken+"\n"))

	// 3. WireGuard config
	if s.cfg().WGConfigPath != "" {
		if data, err := os.ReadFile(s.cfg().WGConfigPath); err == nil {
			add("wireguard.conf", data)
		}
	}

	// 4. Invites
	if s.cfg().InvitesFile != "" {
		if data, err := os.ReadFile(s.cfg().InvitesFile); err == nil && len(data) > 0 {
			add("invites.txt", data)
		}
	}

	// 5. SSL certificates (letsencrypt live dir)
	if s.cfg().SSLEnabled && s.cfg().SSLCertDir != "" {
		liveDir := filepath.Join(s.cfg().SSLCertDir, "live")
		if dirs, err := os.ReadDir(liveDir); err == nil {
			for _, entry := range dirs {
				if !entry.IsDir() {
					continue
				}
				domainDir := filepath.Join(liveDir, entry.Name())
				for _, certFile := range []string{"fullchain.pem", "privkey.pem", "chain.pem"} {
					certPath := filepath.Join(domainDir, certFile)
					if data, err := os.ReadFile(certPath); err == nil {
						add(filepath.ToSlash(filepath.Join("certs", "live", entry.Name(), certFile)), data)
					}
				}
			}
		}
	}

	// 6. The database: a snapshot, never the file.
	m, dbData, err := s.snapshotDB(r.Context())
	if err != nil {
		fail("%v", err)
		return
	}
	add(backup.DBName, dbData)

	// 7. Build logs.
	if s.buildDir != "" {
		err := filepath.WalkDir(s.buildDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == s.buildDir {
					return fs.SkipDir
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(s.buildDir, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			add(backup.BuildsPrefix+filepath.ToSlash(rel), data)
			return nil
		})
		if err != nil {
			fail("read build logs: %v", err)
			return
		}
	}

	m.Files = make(map[string]string, len(entries))
	for _, e := range entries {
		m.Files[e.name] = backup.Sum(e.data)
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fail("marshal manifest: %v", err)
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range append([]zipEntry{{backup.ManifestName, manifest}}, entries...) {
		if err := zipWriteFile(zw, e.name, e.data); err != nil {
			fail("write %s: %v", e.name, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		fail("finalize zip: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=homelab-horizon-backup.zip")
	_, _ = w.Write(buf.Bytes())
}

type zipEntry struct {
	name string
	data []byte
}

// snapshotDB takes and checks the database snapshot for an export. The
// returned manifest has everything but Files.
func (s *Server) snapshotDB(ctx context.Context) (*backup.Manifest, []byte, error) {
	latest, err := db.LatestMigration()
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "hz-backup-")
	if err != nil {
		return nil, nil, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	snap := filepath.Join(dir, backup.DBName)

	at := time.Now().UTC()
	live, err := s.users.Snapshot(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	info, err := db.InspectSnapshot(ctx, snap)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot does not open: %w", err)
	}
	if err := backup.CheckSnapshot(info, latest, live); err != nil {
		return nil, nil, fmt.Errorf("snapshot check: %w", err)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	return &backup.Manifest{
		Format:        backup.FormatVersion,
		HZVersion:     s.version,
		Host:          host,
		SchemaVersion: info.MigrationVersion,
		SnapshotAt:    at,
		RowCounts:     live,
		Excluded: []backup.Exclusion{
			{Path: "artifacts/", Reason: "release artifacts: large, and re-uploadable from the build that produced them; their sha256s are in hz.db"},
			{Path: "child-bin/", Reason: "server binaries a nested hz installs: re-staged from a release"},
		},
	}, data, nil
}

// handleBackupImport restores server state from a backup zip.
// After import, run a sync to regenerate dnsmasq/haproxy configs and package certs.
//
// A zip that carries hz.db (one with a manifest.json) is verified whole —
// every sha256, the snapshot's integrity, row counts and schema version —
// before anything is written, and refused if its schema is NEWER than this
// binary's. The database is not written over the live one, which is open: it
// is staged as <db path>.restore and swapped in at the next start, before the
// database is opened (db.ApplyPendingRestore). The answer says
// "restart_required". Build logs are written in place. A zip with no manifest
// is a backup from before hz.db was included and imports as it always did.
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
		_ = rc.Close()
		if err != nil {
			continue
		}
		files[f.Name] = data
	}

	// A backup that carries the database is checked whole before anything is
	// written: a half-applied restore is worse than a refused one.
	var verified *backup.Verified
	if _, hasDB := files[backup.DBName]; hasDB || files[backup.ManifestName] != nil {
		v, err := backup.VerifyFiles(r.Context(), files)
		if err != nil {
			http.Error(w, "backup refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		latest, err := db.LatestMigration()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := db.CheckRestorable(v.Snapshot, latest); err != nil {
			http.Error(w, "backup refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		verified = v
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

	// 1. Write config. A pre-B3 backup still carries `machines[].hz`: migrate
	// it here and settle its carrier machines against the credential store,
	// which only the server holds (Save would migrate it but cannot settle).
	if err := cfg.MigrateLegacyMachineHZ(); err != nil {
		http.Error(w, "invalid config.json: "+err.Error(), http.StatusBadRequest)
		return
	}
	settleLegacyHZCarriers(&cfg, s.configPath)
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
			if err := s.wg.Load(); err != nil {
				errors = append(errors, fmt.Sprintf("wireguard load: %v", err))
			}
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

	// 6. The database: staged, swapped in at the next start.
	restartRequired := false
	if verified != nil {
		dbPath := s.cfg().UsersDBPath()
		if err := backup.WriteFileAtomic(dbPath+db.PendingRestoreSuffix, files[backup.DBName]); err != nil {
			errors = append(errors, fmt.Sprintf("database: %v", err))
		} else {
			restartRequired = true
			slog.Warn("LOUD: database restore staged; it replaces hz.db at the next start",
				"staged", dbPath+db.PendingRestoreSuffix, "schema_version", verified.Snapshot.MigrationVersion,
				"taken_by", verified.Manifest.HZVersion, "snapshot_at", verified.Manifest.SnapshotAt)
		}

		// 7. Build logs.
		if s.buildDir != "" {
			for name, data := range files {
				rel, ok := strings.CutPrefix(name, backup.BuildsPrefix)
				if !ok {
					continue
				}
				dest := filepath.Join(s.buildDir, filepath.FromSlash(rel))
				if !strings.HasPrefix(dest, filepath.Clean(s.buildDir)+string(filepath.Separator)) {
					errors = append(errors, fmt.Sprintf("build log %s: outside the build directory", name))
					continue
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
					errors = append(errors, fmt.Sprintf("build log %s: %v", rel, err))
					continue
				}
				if err := os.WriteFile(dest, data, 0o600); err != nil {
					errors = append(errors, fmt.Sprintf("build log %s: %v", rel, err))
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if len(errors) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":           "partial",
			"errors":           errors,
			"restart_required": restartRequired,
		})
		return
	}

	resp := map[string]any{"status": "ok", "restart_required": restartRequired}
	if restartRequired {
		resp["database"] = "staged; restart hz to put it in place"
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func zipWriteFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// backupAuthMiddleware accepts Bearer token (API) or session cookie / VPN admin (UI).
func (s *Server) backupAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bearer token (API/CLI usage). The zip carries every secret hz has, so
		// the shared token counts only while it is switched on, never when it
		// is empty, and is compared in constant time.
		// Header only: a ?token= would put it in access logs.
		tok, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if isBearer && tok != "" && s.adminToken != "" && !s.cfg().AdminTokenDisabled &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(s.adminToken)) == 1 {
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
