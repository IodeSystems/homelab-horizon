package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/backup"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// makeBackupZip builds a real backup: a snapshot of a database with two users,
// a manifest that matches it, and a config. edit runs before the hashes are
// taken, so it changes the backup's content honestly; tamper runs after, so
// it makes the zip disagree with its manifest.
func makeBackupZip(t *testing.T, edit func(files map[string][]byte, m *backup.Manifest), tamper func(files map[string][]byte)) []byte {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, u := range []string{"carl", "ops"} {
		if _, err := d.CreateUser(ctx, u, "", db.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	snap := filepath.Join(t.TempDir(), "snap.db")
	live, err := d.Snapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	dbData, _ := os.ReadFile(snap)
	latest, _ := db.LatestMigration()
	files := map[string][]byte{backup.DBName: dbData, "config.json": []byte("{}")}
	m := backup.Manifest{Format: backup.FormatVersion, HZVersion: "test", SchemaVersion: latest,
		SnapshotAt: time.Now().UTC(), RowCounts: live,
		Excluded: []backup.Exclusion{{Path: "artifacts/", Reason: "test"}}}
	if edit != nil {
		edit(files, &m)
	}
	m.Files = map[string]string{}
	for n, b := range files {
		m.Files[n] = backup.Sum(b)
	}
	if tamper != nil {
		tamper(files)
	}
	files[backup.ManifestName], _ = json.Marshal(m)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const testPAT = "hz_pat_backup-test"

// exportServer serves body at /admin/backup/export to the test token only.
func exportServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/backup/export" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testPAT {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestBackupPullWritesAVerifiedZip(t *testing.T) {
	srv := exportServer(t, makeBackupZip(t, nil, nil))
	dir := filepath.Join(t.TempDir(), "hz-office")
	var out bytes.Buffer
	if err := runBackupPull(newClient(srv.URL, testPAT), []string{"--dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	names := dirFiles(t, dir)
	if len(names) != 1 || !strings.HasPrefix(names[0], "hz-backup-127.0.0.1-") || !strings.HasSuffix(names[0], "Z.zip") {
		t.Fatalf("files: %v", names)
	}
	st, _ := os.Stat(filepath.Join(dir, names[0]))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if !strings.Contains(out.String(), "schema ") {
		t.Fatalf("output: %s", out.String())
	}
	if err := runBackupVerify([]string{filepath.Join(dir, names[0])}, &out); err != nil {
		t.Fatalf("the pulled file does not verify: %v", err)
	}
}

// Every way a pull can fail exits non-zero, says LOUD, and writes nothing.
func TestBackupPullFailsLoudAndWritesNothing(t *testing.T) {
	cases := map[string]struct {
		body  []byte
		token string
		want  string
	}{
		"a file's sha256 does not match": {makeBackupZip(t, nil, func(f map[string][]byte) { f["config.json"] = []byte(`{"x":1}`) }), testPAT, "config.json: sha256"},
		"row counts do not match": {makeBackupZip(t, func(_ map[string][]byte, m *backup.Manifest) {
			counts := map[string]int64{}
			for k, v := range m.RowCounts {
				counts[k] = v
			}
			counts["users"] = 9
			m.RowCounts = counts
		}, nil), testPAT, "users: manifest 9 rows, snapshot 2"},
		"not a zip":       {[]byte("<html>login</html>"), testPAT, "not a zip"},
		"refused auth":    {makeBackupZip(t, nil, nil), "hz_pat_wrong", "401"},
		"no hz.db at all": {makeBackupZip(t, func(f map[string][]byte, _ *backup.Manifest) { delete(f, backup.DBName) }, nil), testPAT, "no hz.db"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := exportServer(t, tc.body)
			dir := filepath.Join(t.TempDir(), "b")
			err := runBackupPull(newClient(srv.URL, tc.token), []string{"--dir", dir}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "LOUD") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want LOUD and %q", err, tc.want)
			}
			if names := dirFiles(t, dir); len(names) != 0 {
				t.Fatalf("a failed pull wrote %v", names)
			}
		})
	}
}

func TestPruneKeepsTheNewestN(t *testing.T) {
	dir := t.TempDir()
	mine := []string{
		"hz-backup-hz.example.com-20261001T000000Z.zip",
		"hz-backup-hz.example.com-20261002T000000Z.zip",
		"hz-backup-hz.example.com-20261003T000000Z.zip",
		"hz-backup-hz.example.com-20261004T000000Z.zip",
		"hz-backup-hz.example.com-20261005T000000Z.zip",
	}
	others := []string{
		"hz-backup-other.example.com-20200101T000000Z.zip", // another host
		"notes.txt",
		"hz-backup-hz.example.com-manual.zip", // not a timestamped name
	}
	for _, n := range append(append([]string{}, mine...), others...) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := pruneBackups(dir, "hz.example.com", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 3 {
		t.Fatalf("pruned %v", pruned)
	}
	got := strings.Join(dirFiles(t, dir), " ")
	for _, keep := range append([]string{mine[3], mine[4]}, others...) {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was deleted", keep)
		}
	}
	for _, gone := range mine[:3] {
		if strings.Contains(got, gone) {
			t.Errorf("%s was kept", gone)
		}
	}
	if p, err := pruneBackups(dir, "hz.example.com", 2); err != nil || len(p) != 0 {
		t.Fatalf("a second prune at the limit: %v %v", p, err)
	}
}

func TestBackupVerify(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out bytes.Buffer
	good := write("good.zip", makeBackupZip(t, nil, nil))
	if err := runBackupVerify([]string{good}, &out); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	if !strings.Contains(out.String(), "would restore into this binary") || !strings.Contains(out.String(), "artifacts/") {
		t.Fatalf("output: %s", out.String())
	}

	latest, _ := db.LatestMigration()
	newer := write("newer.zip", makeBackupZip(t, func(f map[string][]byte, m *backup.Manifest) {
		p := filepath.Join(t.TempDir(), "x.db")
		_ = os.WriteFile(p, f[backup.DBName], 0o600)
		conn, err := sql.Open("sqlite", p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`UPDATE schema_migrations SET version = ?`, latest+1); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		f[backup.DBName], _ = os.ReadFile(p)
		m.SchemaVersion = latest + 1
	}, nil))
	if err := runBackupVerify([]string{newer}, &out); err == nil || !strings.Contains(err.Error(), "would NOT restore") {
		t.Fatalf("a newer schema: %v", err)
	}

	tampered := write("tampered.zip", makeBackupZip(t, nil, func(f map[string][]byte) {
		b := append([]byte(nil), f[backup.DBName]...)
		b[len(b)/2] ^= 0xff
		f[backup.DBName] = b
	}))
	if err := runBackupVerify([]string{tampered}, &out); err == nil || !strings.Contains(err.Error(), "hz.db: sha256") {
		t.Fatalf("a tampered snapshot: %v", err)
	}
}

func TestBackupInstallTimerWritesUserUnits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	cfgPath := filepath.Join(home, "hz_config")
	if err := os.WriteFile(cfgPath, []byte(`{"host":"https://hz.example.com","token":"`+testPAT+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HZ_CONFIG", cfgPath)
	t.Setenv("HZ_HOST", "")
	t.Setenv("HZ_TOKEN", "")

	var out bytes.Buffer
	dir := filepath.Join(home, "backups", "hz-office")
	if err := runBackupInstallTimer(newClient("", ""), []string{"--dir", dir, "--keep", "7"}, &out); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(home, "config", "systemd", "user")
	svc, err := os.ReadFile(filepath.Join(unitDir, "hz-backup-hz.example.com.service"))
	if err != nil {
		t.Fatal(err)
	}
	tmr, err := os.ReadFile(filepath.Join(unitDir, "hz-backup-hz.example.com.timer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backup pull --dir " + dir + " --keep 7", "Type=oneshot", "Environment=HZ_CONFIG=" + cfgPath} {
		if !strings.Contains(string(svc), want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=daily", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(string(tmr), want) {
			t.Errorf("timer lacks %q:\n%s", want, tmr)
		}
	}
	if strings.Contains(string(svc)+string(tmr), testPAT) {
		t.Fatal("the token was written into a unit")
	}
	if !strings.Contains(out.String(), "not enabled") || !strings.Contains(out.String(), unitDir) {
		t.Fatalf("output: %s", out.String())
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/hz":     "/usr/local/bin/hz",
		"/home/a b/backups":     `"/home/a b/backups"`,
		"100%":                  `"100%%"`,
		`say "hi"`:              `"say \"hi\""`,
		"HZ_CONFIG=/etc/x.json": "HZ_CONFIG=/etc/x.json",
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
