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
	gr := childGrant{Token: "hzc_" + strings.Repeat("ab", 24), Instance: "loadtest-hz", Project: "redline", Rung: "loadtest",
		Expires: time.Unix(1_800_000_000, 0)}
	return renderChildInstallScript(bootKiosk, gr, []string{"linux-amd64", "linux-arm64"}, "10.100.0.1")
}

func TestChildScriptRendersForAGrant(t *testing.T) {
	s := renderedTestScript()
	for _, want := range []string{
		`BASE="https://portal.example.com"`, `GRANT="hzc_abab`, `INSTANCE="loadtest-hz"`, `PROJECT="redline"`,
		`RUNG="loadtest"`, `PLATFORMS="linux-amd64 linux-arm64"`, `IFACE="hz-upstream"`, `TOKEN_FILE="/etc/homelab-horizon/upstream.token"`,
		`ADMIN_USER="hz-local-admin"`, `ADMIN_TOKEN_FILE="/etc/homelab-horizon/admin.token"`,
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
	secretCat := regexp.MustCompile(`cat "\$(TMP/(wg\.key|instance\.token|svc\.token)|SVC_TOKEN_FILE|ADMIN_TOKEN_FILE|TOKEN_FILE)"`)
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
		`cat "$ADMIN_TOKEN_FILE"`,
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
	// Loopback, at the port the parent records in HZInstance.URL.
	if childCfg.ListenAddr != "127.0.0.1:"+childListenPort {
		t.Fatalf("the seed's listen address: %s, want 127.0.0.1:%s", childCfg.ListenAddr, childListenPort)
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

	// Step 8 on the seeded box: the script's own block runs, and the console
	// command it issues is performed by the function `user create` calls,
	// against the child's identity store. Afterwards the child's first-account
	// door is CLOSED and the token authenticates on its sync surface.
	ctx := context.Background()
	childStore, err := db.Open(filepath.Join(childDir, "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childStore.Close() })
	child.users = childStore
	if !child.bootstrapAllowed(ctx) {
		t.Fatal("instrument: the fresh child's first-account door is already closed")
	}
	step8 := newAdminStep(t)
	step8.cfg = cfgPath
	for _, c := range creates(step8.run(t)) {
		f := strings.Fields(c) // user create <name> --admin --token-file <file> --config <cfg>
		if len(f) != 8 || f[3] != "--admin" || f[4] != "--token-file" || f[6] != "--config" || f[7] != cfgPath {
			t.Fatalf("the script's user create: %q", c)
		}
		if _, err := CreateConsoleAdmin(ctx, childStore, f[2], f[5]); err != nil {
			t.Fatalf("user create %s: %v", f[2], err)
		}
	}
	if child.bootstrapAllowed(ctx) {
		t.Fatal("after the bootstrap the child's first-account door is still OPEN: anyone reaching its API can create the first admin")
	}
	adminTok, err := os.ReadFile(step8.tokenFile)
	if err != nil {
		t.Fatalf("the admin token file: %v", err)
	}
	sr := httptest.NewRequest(http.MethodGet, "/api/v1/services/sync/status", nil)
	sr.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(adminTok)))
	sw := httptest.NewRecorder()
	child.handler().ServeHTTP(sw, sr)
	if sw.Code != http.StatusOK {
		t.Fatalf("the local admin's token on the child's sync status: %d %s", sw.Code, sw.Body.String())
	}

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

// A seeded child binds loopback, not every interface: an empty or default
// ":8080" listen address becomes 127.0.0.1:8080; an explicit one is kept.
func TestChildScriptSeedsALoopbackListener(t *testing.T) {
	seed := childSeed("redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080")
	for current, want := range map[string]string{
		`{}`:                               "127.0.0.1:8080",
		`{"listen_addr": ":8080"}`:         "127.0.0.1:8080",
		`{"listen_addr": "10.0.0.5:9000"}`: "10.0.0.5:9000",
	} {
		cfg, err := config.LoadFromJSON(runJQMerge(t, seed, current, "tok"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ListenAddr != want {
			t.Errorf("current %s: listen %q, want %q", current, cfg.ListenAddr, want)
		}
	}
}

// The preflight, run in bash with `ip` faked: a box where the parent's VPN
// address is already on a local interface is refused BEFORE enrolling (the
// first real run hit exactly this, a leftover wg0 on the parent's range).
func TestChildScriptRefusesWhenTheParentAddressIsLocal(t *testing.T) {
	s := renderedTestScript()
	start := strings.Index(s, `if [ -n "$PARENT_ADDR" ]`)
	end := strings.Index(s[start:], "\nfi\n")
	if start < 0 || end < 0 {
		t.Fatal("instrument: the preflight is not where this test looks")
	}
	block := s[start : start+end+4]
	if strings.Index(s, block) > strings.Index(s, `step "enrolling with the parent hz"`) {
		t.Fatal("the preflight must run before enrolling")
	}
	for name, tc := range map[string]struct{ ipOut, want string }{
		"clash":      {"8: wg0    inet 10.100.0.1/24 scope global wg0", "DIE"},
		"no clash":   {"2: eth0    inet 192.0.2.10/24 scope global eth0", "OK"},
		"own tunnel": {"9: hz-upstream    inet 10.100.0.1/32 scope global hz-upstream", "OK"},
	} {
		prog := `PARENT_ADDR=10.100.0.1; IFACE=hz-upstream; ip() { echo "$FAKE"; }; die() { echo DIE; exit 0; }; ` + block + "echo OK"
		cmd := exec.Command("bash", "-c", prog)
		cmd.Env = append(os.Environ(), "FAKE="+tc.ipOut)
		out, err := cmd.Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != tc.want {
			t.Errorf("%s: got %q (%v), want %s", name, got, err, tc.want)
		}
	}
}

// adminStepBlock is the script's step 8, as it runs on the box.
func adminStepBlock(t *testing.T) string {
	t.Helper()
	s := renderedTestScript()
	start := strings.Index(s, `step "creating the local admin account" 8`)
	end := strings.Index(s, "# --------------------------------------------------------------- 9. service")
	if start < 0 || end < start {
		t.Fatal("instrument: step 8 is not where this test looks")
	}
	if strings.Index(s, `install -m 0755 "$TMP/hz.bin" "$BIN.new"`) > start {
		t.Fatal("step 8 runs before the binary is installed")
	}
	return s[start:end]
}

// adminStep runs step 8 in bash with BIN faked: the fake records every
// invocation, and `user list` prints the accounts file. It returns the
// invocations this run made, one per line.
type adminStep struct {
	dir, accounts, log, tokenFile, cfg string
}

func newAdminStep(t *testing.T) *adminStep {
	t.Helper()
	dir := t.TempDir()
	a := &adminStep{dir: dir, accounts: filepath.Join(dir, "accounts"), log: filepath.Join(dir, "bin.log"),
		tokenFile: filepath.Join(dir, "admin.token"), cfg: filepath.Join(dir, "config.json")}
	fake := "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$FAKE_LOG\"\n" +
		"if [ \"$1 $2\" = \"user list\" ]; then cat \"$FAKE_ACCOUNTS\" 2>/dev/null || true; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "hz"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return a
}

func (a *adminStep) run(t *testing.T) []string {
	t.Helper()
	_ = os.Remove(a.log)
	prog := `set -euo pipefail; say() { printf '%s\n' "$*"; }; die() { echo "DIE: $*"; exit 1; }; step() { :; }; ` +
		`TMP=$(mktemp -d); BIN="$FAKE_BIN"; HZ_CFG="$FAKE_CFG"; ADMIN_USER=hz-local-admin; ADMIN_TOKEN_FILE="$FAKE_TOKEN_FILE"; ` +
		adminStepBlock(t)
	cmd := exec.Command("bash", "-c", prog)
	cmd.Env = append(os.Environ(), "FAKE_BIN="+filepath.Join(a.dir, "hz"), "FAKE_LOG="+a.log, "FAKE_ACCOUNTS="+a.accounts,
		"FAKE_CFG="+a.cfg, "FAKE_TOKEN_FILE="+a.tokenFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("step 8: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(a.log)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// creates reports the `user create` invocations among calls.
func creates(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "user create ") {
			out = append(out, c)
		}
	}
	return out
}

// Step 8 creates hz-local-admin, with its token in the admin token file, only
// when the box has no account — a re-run keeps the one it made.
func TestChildScriptCreatesTheLocalAdminOnce(t *testing.T) {
	a := newAdminStep(t)
	first := creates(a.run(t))
	want := "user create hz-local-admin --admin --token-file " + a.tokenFile + " --config " + a.cfg
	if len(first) != 1 || first[0] != want {
		t.Fatalf("first run on a box with no account: %q, want [%q]", first, want)
	}
	if err := os.WriteFile(a.accounts, []byte("hz-local-admin\tadmin\tenabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := a.run(t)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "user list") {
		t.Fatalf("instrument: the re-run did not ask for the accounts: %q", calls)
	}
	if again := creates(calls); len(again) != 0 {
		t.Fatalf("a re-run with an existing account created another: %q", again)
	}

	// The step precedes the service start, so a fresh box never serves with
	// the first-account door open.
	s := renderedTestScript()
	if strings.Index(s, `step "creating the local admin account" 8`) > strings.Index(s, `systemctl restart homelab-horizon`) {
		t.Fatal("the admin is created after hz starts")
	}
}
