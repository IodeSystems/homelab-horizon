package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/backup"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// backupServer is a test Server with a database whose recent rows are still in
// the WAL, and one build log.
func backupServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{UsersDB: filepath.Join(t.TempDir(), "hz.db")}
	s := newTestServer(t, cfg)
	store, err := db.Open(cfg.UsersDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.users = store
	ctx := context.Background()
	store.SetMaxOpenConns(1)
	if _, err := store.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.CreateUser(ctx, fmt.Sprintf("op%d", i), "", db.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	s.buildDir = filepath.Join(t.TempDir(), "builds")
	if err := os.MkdirAll(filepath.Join(s.buildDir, "redline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.buildDir, "redline", "7.log"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.version = "1.2.3-test"
	return s
}

func exportAs(s *Server, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/admin/backup/export", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.backupAuthMiddleware(s.handleBackupExport)(w, r)
	return w
}

func TestBackupExportCarriesTheDatabase(t *testing.T) {
	s := backupServer(t)
	w := exportAs(s, "Bearer "+s.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	v, err := backup.Verify(context.Background(), w.Body.Bytes())
	if err != nil {
		t.Fatalf("the export does not verify: %v", err)
	}
	if v.Snapshot.RowCounts["users"] != 5 {
		t.Fatalf("users in the snapshot: %d, want 5 (the WAL rows)", v.Snapshot.RowCounts["users"])
	}
	latest, _ := db.LatestMigration()
	m := v.Manifest
	if m.HZVersion != "1.2.3-test" || m.SchemaVersion != latest || m.SnapshotAt.IsZero() {
		t.Fatalf("manifest: %+v", m)
	}
	for _, want := range []string{"hz.db", "config.json", "token", "builds/redline/7.log"} {
		if _, ok := m.Files[want]; !ok {
			t.Errorf("%s is not in the zip", want)
		}
	}
	var excludesArtifacts bool
	for _, e := range m.Excluded {
		excludesArtifacts = excludesArtifacts || e.Path == "artifacts/"
	}
	if !excludesArtifacts {
		t.Errorf("the manifest does not say artifacts/ is excluded: %+v", m.Excluded)
	}
}

func TestBackupExportRequiresAuth(t *testing.T) {
	s := backupServer(t)
	if w := exportAs(s, "Bearer "+s.adminToken); w.Code != http.StatusOK {
		t.Fatalf("positive control: the admin token got %d", w.Code)
	}
	for name, auth := range map[string]string{
		"none":         "",
		"wrong token":  "Bearer not-the-token",
		"empty bearer": "Bearer ",
		"not a bearer": s.adminToken,
	} {
		if w := exportAs(s, auth); w.Code != http.StatusUnauthorized || w.Body.Len() > 100 {
			t.Errorf("%s: %d, %d bytes", name, w.Code, w.Body.Len())
		}
	}
	// The switch that turns the shared token off turns it off here too.
	cfg := *s.cfg()
	cfg.AdminTokenDisabled = true
	s.config.Store(&cfg)
	if w := exportAs(s, "Bearer "+s.adminToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled admin token exported a backup: %d", w.Code)
	}
}

func TestBackupExportRefusesWithoutADatabase(t *testing.T) {
	s := backupServer(t)
	s.users = nil
	w := exportAs(s, "Bearer "+s.adminToken)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "hz.db") {
		t.Fatalf("got %d %s %.200q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w.Header().Get("Content-Type") == "application/zip" {
		t.Fatal("a refused export was served as a zip")
	}
}

// A database that is not at the running binary's latest migration is not
// served: the snapshot would not be what this hz runs on.
func TestBackupExportRefusesASchemaThatIsNotTheBinarys(t *testing.T) {
	s := backupServer(t)
	latest, _ := db.LatestMigration()
	behind, err := db.OpenAt(filepath.Join(t.TempDir(), "behind.db"), latest-1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = behind.Close() })
	s.users = behind
	w := exportAs(s, "Bearer "+s.adminToken)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "not this binary's latest migration") {
		t.Fatalf("got %d %s %.200q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w.Header().Get("Content-Type") == "application/zip" {
		t.Fatal("a refused export was served as a zip")
	}
}

// unzip, edit, re-zip a backup, keeping the manifest's hashes honest so only
// the edit under test is wrong.
func rezip(t *testing.T, data []byte, edit func(files map[string][]byte, m *backup.Manifest)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = b
	}
	var m backup.Manifest
	if err := json.Unmarshal(files[backup.ManifestName], &m); err != nil {
		t.Fatal(err)
	}
	edit(files, &m)
	m.Files = map[string]string{}
	for n, b := range files {
		if n != backup.ManifestName {
			m.Files[n] = backup.Sum(b)
		}
	}
	files[backup.ManifestName], _ = json.Marshal(m)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		fw, _ := zw.Create(n)
		_, _ = fw.Write(b)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func importZip(s *Server, data []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/backup/import", bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+s.adminToken)
	w := httptest.NewRecorder()
	s.backupAuthMiddleware(s.handleBackupImport)(w, r)
	return w
}

// The restore half: the database is staged, never written over the open one,
// and the next start puts it in place.
func TestBackupImportStagesTheDatabase(t *testing.T) {
	src := backupServer(t)
	w := exportAs(src, "Bearer "+src.adminToken)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	// Point the restored config at the destination's database; only the
	// config changes, and its hash is recomputed.
	dst := backupServer(t)
	dbPath := dst.cfg().UsersDB
	zipData := rezip(t, w.Body.Bytes(), func(files map[string][]byte, _ *backup.Manifest) {
		var c config.Config
		if err := json.Unmarshal(files["config.json"], &c); err != nil {
			t.Fatal(err)
		}
		c.UsersDB = dbPath
		files["config.json"], _ = json.Marshal(&c)
	})
	if err := os.RemoveAll(dst.buildDir); err != nil {
		t.Fatal(err)
	}

	res := importZip(dst, zipData)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"restart_required":true`) {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	if st, err := os.Stat(dbPath + db.PendingRestoreSuffix); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("staged database: %v %v", st, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst.buildDir, "redline", "7.log")); err != nil || string(b) != "ok\n" {
		t.Fatalf("build log not restored: %q %v", b, err)
	}

	// The next start: close, swap, open.
	_ = dst.users.Close()
	if applied, err := db.ApplyPendingRestore(dbPath); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var n int
	if err := store.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("restored users: %d %v", n, err)
	}
}

// A snapshot from a NEWER hz is refused before anything is written.
func TestBackupImportRefusesANewerSchema(t *testing.T) {
	src := backupServer(t)
	w := exportAs(src, "Bearer "+src.adminToken)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	latest, _ := db.LatestMigration()
	zipData := rezip(t, w.Body.Bytes(), func(files map[string][]byte, m *backup.Manifest) {
		p := filepath.Join(t.TempDir(), "x.db")
		if err := os.WriteFile(p, files[backup.DBName], 0o600); err != nil {
			t.Fatal(err)
		}
		conn, err := sql.Open("sqlite", p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`UPDATE schema_migrations SET version = ?`, latest+1); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		files[backup.DBName], _ = os.ReadFile(p)
		m.SchemaVersion = latest + 1
	})

	dst := backupServer(t)
	cfgBefore, _ := os.ReadFile(dst.configPath)
	res := importZip(dst, zipData)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "newer than this binary") {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(dst.cfg().UsersDB + db.PendingRestoreSuffix); err == nil {
		t.Fatal("a refused database was staged")
	}
	if cfgAfter, _ := os.ReadFile(dst.configPath); !bytes.Equal(cfgBefore, cfgAfter) {
		t.Fatal("a refused import wrote config.json")
	}
}

// A tampered zip is refused whole.
func TestBackupImportRefusesAHashMismatch(t *testing.T) {
	src := backupServer(t)
	w := exportAs(src, "Bearer "+src.adminToken)
	zr, _ := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if f.Name == "token" {
			b = []byte("swapped\n")
		}
		fw, _ := zw.Create(f.Name)
		_, _ = fw.Write(b)
	}
	_ = zw.Close()

	dst := backupServer(t)
	res := importZip(dst, buf.Bytes())
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "token: sha256") {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	if dst.adminToken == "swapped" {
		t.Fatal("the tampered token was applied")
	}
}
