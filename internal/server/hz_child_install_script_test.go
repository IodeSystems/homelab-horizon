package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/nested"
)

func renderedTestScript() string {
	gr := childGrant{Token: "hzc_" + strings.Repeat("ab", 24), Machine: "loadtest-hz", Project: "redline", Rung: "loadtest",
		Expires: time.Unix(1_800_000_000, 0)}
	return renderChildInstallScript(bootKiosk, gr, []string{"linux-amd64", "linux-arm64"})
}

func TestChildScriptRendersForAGrant(t *testing.T) {
	s := renderedTestScript()
	for _, want := range []string{
		`BASE="https://portal.example.com"`, `GRANT="hzc_abab`, `MACHINE="loadtest-hz"`, `PROJECT="redline"`,
		`RUNG="loadtest"`, `PLATFORMS="linux-amd64 linux-arm64"`, `IFACE="hz-upstream"`, `TOKEN_FILE="/etc/homelab-horizon/upstream.token"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %s", want)
		}
	}
	if strings.Contains(s, "@@") {
		t.Fatalf("an unfilled placeholder:\n%s", s)
	}
	for _, sc := range []string{s, childRefusalScript} {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(sc)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
}

// shellcheck when it is installed; the test says so when it is not.
func TestChildScriptShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed — the script was not linted")
	}
	for name, body := range map[string]string{"install": renderedTestScript(), "refusal": childRefusalScript} {
		cmd := exec.Command(sc, "-s", "bash", "-")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("shellcheck %s: %v\n%s", name, err, out)
		}
	}
}

// secretLeaks is the rule the script is held to: no tracing, no command
// substitution of a file's contents (that would put a secret in a variable or
// on a terminal), the instance token only ever redirected into a file, and a
// secret file only cat'ed inside a { ... } > file group.
func secretLeaks(script string) []string {
	var bad []string
	secretCat := regexp.MustCompile(`cat "\$(TMP/(wg\.key|instance\.token|svc\.token)|SVC_TOKEN_FILE|TOKEN_FILE)"`)
	inGroup := false
	for i, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		at := func(why string) { bad = append(bad, why+" at line "+itoa(i+1)+": "+t) }
		if regexp.MustCompile(`\bset\s+(-[a-wyz]*x|-o\s+xtrace)|bash\s+-x|^#!.*-x`).MatchString(t) {
			at("tracing")
		}
		if strings.Contains(t, "$(cat") || strings.Contains(t, "$(<") || strings.Contains(t, "`cat") {
			at("a file's contents in a command substitution")
		}
		if strings.Contains(t, ".instance_token") && !strings.Contains(t, `> "$TMP/`) {
			at("the instance token not redirected into a file")
		}
		if t == "{" || strings.HasPrefix(t, "{ ") {
			inGroup = true
		}
		if secretCat.MatchString(t) && !inGroup {
			at("a secret file printed outside a redirected group")
		}
		if strings.HasPrefix(t, "}") {
			if inGroup && !strings.Contains(t, "> ") {
				at("a group holding a secret that is not redirected into a file")
			}
			inGroup = false
		}
		if strings.HasPrefix(t, "{ ") && strings.Contains(t, "}") {
			if !strings.Contains(t[strings.LastIndex(t, "}"):], "> ") {
				at("a one-line group not redirected into a file")
			}
			inGroup = false
		}
	}
	return bad
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + jsonNumber(n)) }

func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestChildScriptLeaksNoSecret(t *testing.T) {
	script := renderedTestScript()
	if bad := secretLeaks(script); len(bad) > 0 {
		t.Fatalf("the script exposes a secret:\n%s", strings.Join(bad, "\n"))
	}
	// Instrument: the rule catches each kind of leak it claims to.
	for _, leak := range []string{
		"set -x",
		"set -euxo pipefail",
		`echo "token: $(cat "$TMP/instance.token")"`,
		`TOKEN=$(jq -r '.instance_token' "$TMP/enrol.json")`,
		`cat "$TMP/svc.token"`,
		`{ printf 'x'; cat "$TMP/wg.key"; }`,
	} {
		if len(secretLeaks(script+"\n"+leak+"\n")) == 0 {
			t.Errorf("instrument: the leak check misses %q", leak)
		}
	}
}

// The arch mapping, run in bash with uname faked.
func TestChildScriptArchMapping(t *testing.T) {
	s := renderedTestScript()
	start := strings.Index(s, `case "$(uname -m)" in`)
	end := strings.Index(s[start:], "esac") + start + len("esac")
	block := s[start:end]
	for uname, want := range map[string]string{
		"x86_64": "linux-amd64", "amd64": "linux-amd64", "aarch64": "linux-arm64", "arm64": "linux-arm64", "armv7l": "DIE", "riscv64": "DIE",
	} {
		prog := `uname() { echo "$FAKE"; }; die() { echo DIE; exit 0; }; ` + block + `; echo "$PLATFORM"`
		cmd := exec.Command("bash", "-c", prog)
		cmd.Env = append(os.Environ(), "FAKE="+uname)
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("uname -m %s -> %q (%v), want %s", uname, out, err, want)
		}
	}
}

// scriptJQMerge is the jq program the script writes config.json with.
func scriptJQMerge(t *testing.T) string {
	t.Helper()
	s := renderedTestScript()
	open := `jq --slurpfile seed "$TMP/seed.json" --rawfile tok "$TMP/svc.token" '`
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatal("instrument: the script's merge program is not where this test looks for it")
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, `' "$TMP/current.json"`)
	if j < 0 {
		t.Fatal("instrument: the merge program's end is not where this test looks")
	}
	return rest[:j]
}

func runJQMerge(t *testing.T, seed apitypes.NestedChildSeed, current, svcToken string) []byte {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not installed — the script's merge program was not run")
	}
	dir := t.TempDir()
	raw, _ := json.Marshal(seed)
	for name, body := range map[string]string{"seed.json": string(raw), "svc.token": svcToken + "\n", "current.json": current} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(jq, "--slurpfile", "seed", filepath.Join(dir, "seed.json"), "--rawfile", "tok", filepath.Join(dir, "svc.token"),
		scriptJQMerge(t), filepath.Join(dir, "current.json"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jq merge: %v", err)
	}
	return out
}

// An existing config is MERGED: other services, projects and settings are
// kept, an existing service's domains are kept, and the upstream is replaced.
func TestChildScriptMergeKeepsAnExistingConfig(t *testing.T) {
	seed := childSeed("redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080")
	current := `{"listen_addr": ":9090", "projects": [{"name": "redline", "parent": ""}, {"name": "ops"}],
	  "services": [{"name": "grafana", "domains": ["grafana.example.com"]},
	               {"name": "redline-loadtest", "domains": ["loadtest.example.com"], "token": "old"}],
	  "upstream": {"url": "http://192.0.2.1:8080", "token_file": "/x", "rungs": [{"project": "a", "environment": "b"}]}}`
	out := runJQMerge(t, seed, current, "fresh-token")
	cfg, err := config.LoadFromJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9090" || len(cfg.Projects) != 2 || len(cfg.Services) != 2 {
		t.Fatalf("merge lost something: listen %s, %d projects, %d services", cfg.ListenAddr, len(cfg.Projects), len(cfg.Services))
	}
	var svc config.Service
	for _, s := range cfg.Services {
		if s.Name == "redline-loadtest" {
			svc = s
		}
	}
	if svc.Token != "fresh-token" || svc.Project != "redline" || svc.Environment != "loadtest" || len(svc.Domains) != 1 {
		t.Fatalf("merged service: %+v", svc)
	}
	if cfg.Upstream == nil || cfg.Upstream.URL != "http://10.100.0.1:8080" || len(cfg.Upstream.Rungs) != 1 || !cfg.ServesUpstream("redline", "loadtest") {
		t.Fatalf("upstream not replaced: %+v", cfg.Upstream)
	}
}

// End to end, with a real parent behind HTTP and the child's real config
// loader: the script's requests in order (script, enrol, binary over the
// "VPN"), the seed it writes, config.Load + Save accepting it, and the child
// built from it reaching the parent — after which the parent shows the
// instance connected.
func TestEnrolSeedLoadsAsTheChildsConfig(t *testing.T) {
	f := newBootFixture(t)
	store, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.s.users = store
	parent := httptest.NewServer(f.s.handler())
	t.Cleanup(parent.Close)

	do := func(method, path, host, auth string, body io.Reader) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, parent.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			r.Host = host
		}
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	grant := grantOf(t, f.generate(t, bootReq()).Command)
	if st := nestedRow(t, f.s, "loadtest-hz").State; st != apitypes.NestedStateCommandGenerated {
		t.Fatalf("state after generate: %s", st)
	}
	script, _ := io.ReadAll(do("GET", "/admin/hz-child/install/"+grant, bootPublic, "", nil).Body)
	if !strings.Contains(string(script), `GRANT="`+grant+`"`) {
		t.Fatalf("install script over HTTP:\n%s", script)
	}
	raw, _ := json.Marshal(boxEnrol())
	er := do("POST", "/admin/hz-child/enrol/"+grant, bootPublic, "", strings.NewReader(string(raw)))
	var out apitypes.NestedEnrolResp
	if er.StatusCode != http.StatusOK || json.NewDecoder(er.Body).Decode(&out) != nil {
		t.Fatalf("enrol over HTTP: %d", er.StatusCode)
	}
	if st := nestedRow(t, f.s, "loadtest-hz").State; st != apitypes.NestedStateEnrolled {
		t.Fatalf("state after enrol: %s", st)
	}

	// The binary, from the URL the parent gave, re-pointed at the test
	// server (the real one is the parent's WireGuard address).
	bu, _ := url.Parse(out.Binary.URL)
	br := do("GET", bu.Path, "", out.InstanceToken, nil)
	bin, _ := io.ReadAll(br.Body)
	sum := sha256.Sum256(bin)
	if br.StatusCode != http.StatusOK || hex.EncodeToString(sum[:]) != out.Binary.SHA256 {
		t.Fatalf("binary over HTTP: %d, sha matches %v", br.StatusCode, hex.EncodeToString(sum[:]) == out.Binary.SHA256)
	}

	// The seed the script writes: the jq merge over an empty config when jq
	// is here (the script's own program), else the same edit in Go.
	const svcToken = "box-generated-service-token"
	var seeded []byte
	if _, err := exec.LookPath("jq"); err == nil {
		seeded = runJQMerge(t, out.Seed, "{}", svcToken)
	} else {
		t.Log("jq not installed: the seed is edited in Go, not by the script's program")
		s := out.Seed
		s.Services[0].Token = svcToken
		seeded, _ = json.Marshal(s)
	}
	childDir := t.TempDir()
	cfgPath := filepath.Join(childDir, "config.json")
	if err := os.WriteFile(cfgPath, seeded, 0o600); err != nil {
		t.Fatal(err)
	}
	childCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// Save runs every validator a write does — the seed must pass them all.
	if err := config.Save(filepath.Join(childDir, "saved.json"), childCfg); err != nil {
		t.Fatalf("the seed does not validate as a config: %v", err)
	}
	u := childCfg.Upstream
	if u == nil || u.URL != out.ParentURL || u.TokenFile != childTokenFile || !childCfg.ServesUpstream("redline", "loadtest") {
		t.Fatalf("child upstream: %+v", u)
	}
	env, err := childCfg.LookupEnvironment("redline", "loadtest")
	if err != nil || env.Posture != "prod" || env.Upstream != "" {
		t.Fatalf("child rung: %+v %v — the rung is LOCAL on the child", env, err)
	}
	if len(childCfg.Services) != 1 || childCfg.Services[0].Name != out.ServiceName || childCfg.Services[0].Token != svcToken ||
		childCfg.Services[0].Project != "redline" || childCfg.Services[0].Environment != "loadtest" {
		t.Fatalf("child service: %+v", childCfg.Services)
	}
	if childCfg.ListenAddr != ":"+childListenPort {
		t.Fatalf("the seed changed the child's listen address: %s", childCfg.ListenAddr)
	}

	// The child, as the script starts it — with the token file the script
	// writes, and its upstream re-pointed at the test server.
	tokFile := filepath.Join(childDir, "upstream.token")
	if err := os.WriteFile(tokFile, []byte(out.InstanceToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	childCfg.Upstream.URL = parent.URL
	childCfg.Upstream.TokenFile = tokFile
	child := newTestServer(t, childCfg)
	child.child = nested.New(*childCfg.Upstream, childDir)
	child.child.PollOnce(context.Background())

	q := url.Values{"project": {"redline"}, "environment": {"loadtest"}}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deploys/desired?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+svcToken)
	w := httptest.NewRecorder()
	child.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "nothing applied") {
		t.Fatalf("the child's answer with its local service token: %d %s — the script reads 404 nothing-applied as connected", w.Code, w.Body.String())
	}
	n := nestedRow(t, f.s, "loadtest-hz")
	if n.State != apitypes.NestedStateConnected || n.LastPullRung != "redline/loadtest" {
		t.Fatalf("the parent after the child's first pull: %+v", n)
	}
}
