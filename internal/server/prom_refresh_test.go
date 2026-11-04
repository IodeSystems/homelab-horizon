package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The refresh script runs on the Prometheus box every 2 min. These tests run
// the REAL rendered script under bash against a temp /etc/prometheus, with the
// real curl and promtool, and stubs only for systemctl and logger. PATH holds
// exactly the tools the script may use, so "promtool missing" is a real absence.

const refreshPromYML = `global:
  scrape_interval: 15s
scrape_config_files:
  - HZYML
scrape_configs:
  - job_name: local
    static_configs:
      - targets: ["localhost:9090"]
`

const refreshHZOld = `scrape_configs:
  - job_name: node
    static_configs:
      - targets: ["10.0.0.1:9100"]
`

const refreshHZNew = `scrape_configs:
  - job_name: node
    static_configs:
      - targets: ["10.0.0.1:9100", "10.0.0.2:9100"]
`

type refreshBox struct {
	dir, promYML, hzYML, script, reloads string
	path                                string
}

func newRefreshBox(t *testing.T, body *string, status *int, withPromtool bool) *refreshBox {
	t.Helper()
	for _, tool := range []string{"bash", "curl", "promtool"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed; the refresh script cannot be exercised", tool)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(*status)
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	b := &refreshBox{dir: filepath.Join(root, "prometheus")}
	if err := os.Mkdir(b.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b.promYML = filepath.Join(b.dir, "prometheus.yml")
	b.hzYML = filepath.Join(b.dir, "hz.yml")
	b.reloads = filepath.Join(root, "systemctl.log")
	writeFile(t, b.promYML, strings.ReplaceAll(refreshPromYML, "HZYML", b.hzYML), 0o644)
	writeFile(t, b.hzYML, refreshHZOld, 0o644)

	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{"bash", "dirname", "mktemp", "rm", "curl", "grep", "cmp", "awk", "chmod", "mv", "cat"}
	if withPromtool {
		tools = append(tools, "promtool")
	}
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not installed", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(bin, "systemctl"), "#!"+mustLook(t, "bash")+"\necho \"$*\" >> "+b.reloads+"\n", 0o755)
	writeFile(t, filepath.Join(bin, "logger"), "#!"+mustLook(t, "bash")+"\nexit 0\n", 0o755)
	b.path = bin

	b.script = filepath.Join(root, "hz-scrape-refresh.sh")
	writeFile(t, b.script, renderPromRefreshScript(srv.URL+"/integration/prometheus/scrape.yaml", "tok", b.promYML, b.hzYML)+"\n", 0o755)
	return b
}

func mustLook(t *testing.T, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not installed", tool)
	}
	return p
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// run executes the refresh script; it returns the exit error and the output.
func (b *refreshBox) run(t *testing.T) (error, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(b.path, "bash"), b.script)
	cmd.Env = []string{"PATH=" + b.path}
	out, err := cmd.CombinedOutput()
	return err, string(out)
}

func (b *refreshBox) hz(t *testing.T) string {
	t.Helper()
	got, err := os.ReadFile(b.hzYML)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func (b *refreshBox) reloaded(t *testing.T) bool {
	t.Helper()
	got, err := os.ReadFile(b.reloads)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(got), "reload prometheus")
}

// noCandidatesLeft: the script's temp files live in the Prometheus dir, so a
// leak would accumulate there every 2 minutes.
func (b *refreshBox) noCandidatesLeft(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hz-scrape-") {
			t.Errorf("temp file left in %s: %s", b.dir, e.Name())
		}
	}
}

func TestPromRefreshInstallsAValidChangeAndReloads(t *testing.T) {
	body, status := refreshHZNew, http.StatusOK
	b := newRefreshBox(t, &body, &status, true)
	if err, out := b.run(t); err != nil {
		t.Fatalf("refresh failed: %v\n%s", err, out)
	}
	if b.hz(t) != refreshHZNew {
		t.Errorf("hz.yml not updated:\n%s", b.hz(t))
	}
	if !b.reloaded(t) {
		t.Error("valid change installed but prometheus not reloaded")
	}
	b.noCandidatesLeft(t)
}

func TestPromRefreshUnchangedDoesNotReload(t *testing.T) {
	body, status := refreshHZOld, http.StatusOK
	b := newRefreshBox(t, &body, &status, true)
	if err, out := b.run(t); err != nil {
		t.Fatalf("refresh failed: %v\n%s", err, out)
	}
	if b.reloaded(t) {
		t.Error("unchanged config reloaded prometheus")
	}
	b.noCandidatesLeft(t)
}

// Each case passes the cheap grep for scrape_configs: and is refused by a later
// gate. The live hz.yml must stay byte-identical and Prometheus must not reload.
func TestPromRefreshRefusesAndLeavesHZYMLUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		status   int
		promtool bool
		wantLog  string
	}{
		{"invalid yaml", "scrape_configs:\n  - job_name: [unclosed\n", http.StatusOK, true, "promtool rejected"},
		// Valid on its own; invalid only together with prometheus.yml. A
		// check of hz.yml alone would pass this.
		{"duplicate of a local job", "scrape_configs:\n  - job_name: local\n    static_configs:\n      - targets: [\"x:1\"]\n", http.StatusOK, true, "promtool rejected"},
		{"truncated transfer", "scrape_configs:\n  - job_name: node\n    static_configs:\n      - targets: [\"10.0.0.1:91", http.StatusOK, true, "promtool rejected"},
		{"hz error", "scrape_configs: oops", http.StatusInternalServerError, true, "fetch failed"},
		{"promtool missing", refreshHZNew, http.StatusOK, false, "promtool not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, status := tc.body, tc.status
			b := newRefreshBox(t, &body, &status, tc.promtool)
			err, out := b.run(t)
			if err == nil {
				t.Fatalf("refresh exited 0; want failure\n%s", out)
			}
			if !strings.Contains(out, tc.wantLog) {
				t.Errorf("output lacks %q:\n%s", tc.wantLog, out)
			}
			if b.hz(t) != refreshHZOld {
				t.Errorf("hz.yml changed on a refused refresh:\n%s", b.hz(t))
			}
			if b.reloaded(t) {
				t.Error("prometheus reloaded on a refused refresh")
			}
			b.noCandidatesLeft(t)
		})
	}
}

func TestPromRefreshRefusesWhenIncludeNotWired(t *testing.T) {
	body, status := refreshHZNew, http.StatusOK
	b := newRefreshBox(t, &body, &status, true)
	writeFile(t, b.promYML, "global:\n  scrape_interval: 15s\n", 0o644)
	err, out := b.run(t)
	if err == nil || !strings.Contains(out, "does not include") {
		t.Fatalf("want refusal for an unwired include; err=%v\n%s", err, out)
	}
	if b.hz(t) != refreshHZOld || b.reloaded(t) {
		t.Error("unwired include: hz.yml changed or prometheus reloaded")
	}
}

// setup.sh is the one installer. It must carry the validated refresh script
// verbatim, replace the legacy cron refresher, and parse as bash.
func TestPromSetupScriptInstallsTheValidatedRefresher(t *testing.T) {
	setup := renderPromSetupScript("https://hz.example/integration/prometheus/scrape.yaml", "tok")
	refresh := renderPromRefreshScript("https://hz.example/integration/prometheus/scrape.yaml", "tok", promYMLPath, promHZYMLPath)

	if !strings.Contains(setup, "<<'HZ_REFRESH_EOF'\n"+refresh+"\nHZ_REFRESH_EOF\n") {
		t.Error("setup.sh does not embed the refresh script verbatim in a quoted heredoc")
	}
	if strings.Contains(refresh, "\nHZ_REFRESH_EOF\n") {
		t.Error("refresh script contains its own heredoc delimiter")
	}
	if !strings.Contains(setup, `rm -f "$LEGACY_CRON"`) || !strings.Contains(setup, `LEGACY_CRON="/etc/cron.d/hz-scrape-refresh"`) {
		t.Error("setup.sh does not remove the legacy cron refresher")
	}
	if strings.Contains(setup, "*/2 * * * *") {
		t.Error("setup.sh installs a cron entry")
	}
	if strings.Contains(setup, "%!") {
		t.Errorf("setup.sh has a fmt error: %s", setup)
	}
	if !strings.Contains(setup, "systemctl enable --now hz-scrape-refresh.timer") {
		t.Error("setup.sh does not enable the timer")
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	f := filepath.Join(t.TempDir(), "setup.sh")
	writeFile(t, f, setup, 0o644)
	if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
		t.Errorf("setup.sh does not parse: %v\n%s", err, out)
	}
}
