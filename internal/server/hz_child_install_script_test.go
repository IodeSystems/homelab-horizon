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
		Expires: time.Unix(1_800_000_000, 0), Human: &childHumanVPN{Range: "10.101.0.0/24", Kiosk: bootKioskName, Operator: "carl"}}
	return renderChildInstallScript(bootKiosk, gr, []string{"linux-amd64", "linux-arm64"}, "10.100.0.1")
}

// The human-VPN preflight, run in bash with `ip` faked (as the parent-address
// preflight's test does): a box where the range is already an address or a
// route on another interface is refused BEFORE enrolling, and so is a box
// whose wg0.conf is another install's; the child's own wg0 on the range (a
// re-run) and the upstream tunnel's /32 pass.
func TestChildScriptRefusesWhenTheHumanVPNRangeIsInUse(t *testing.T) {
	s := renderedTestScript()
	fnStart := strings.Index(s, "human_vpn_clash() {")
	fnEnd := strings.Index(s[fnStart:], "\n}\n")
	start := strings.Index(s, `if [ -n "$HUMAN_VPN_RANGE" ]; then`+"\n  command -v ip")
	end := strings.Index(s[start:], "\nfi\n")
	if fnStart < 0 || fnEnd < 0 || start < 0 || end < 0 {
		t.Fatal("instrument: the human-VPN preflight is not where this test looks")
	}
	if start > strings.Index(s, `step "enrolling with the parent hz"`) {
		t.Fatal("the human-VPN preflight must run before enrolling")
	}
	block := s[fnStart:fnStart+fnEnd+3] + s[start:start+end+4]
	const (
		eth0     = "2: ens5    inet 10.0.1.5/24 brd 10.0.1.255 scope global ens5"
		upstream = "9: hz-upstream    inet 10.100.0.7/32 scope global hz-upstream"
		vpcRoute = "10.0.0.0/16 dev ens5 proto kernel scope link src 10.0.1.5"
		upRoute  = "10.100.0.1 dev hz-upstream scope link"
	)
	for name, tc := range map[string]struct{ addr, route, wg0, want string }{
		"a clean box":                {eth0 + "\n" + upstream, vpcRoute + "\n" + upRoute, "", "OK"},
		"an address in range":        {eth0 + "\n8: docker9    inet 10.101.0.7/24 scope global docker9", vpcRoute, "", "DIE"},
		"a route over the range":     {eth0, vpcRoute + "\n10.96.0.0/12 via 10.0.1.1 dev ens5", "", "DIE"},
		"a /32 route inside it":      {eth0, "10.101.0.200 dev tun0 scope link", "", "DIE"},
		"the child's own wg0":        {eth0 + "\n7: wg0    inet 10.101.0.1/24 scope global wg0", "10.101.0.0/24 dev wg0 proto kernel scope link src 10.101.0.1", "Address = 10.101.0.1/24", "OK"},
		"another install's wg0.conf": {eth0, vpcRoute, "Address = 10.100.0.1/24", "DIE"},
		"a wg0.conf with no address": {eth0, vpcRoute, "ListenPort = 51820", "DIE"},
	} {
		dir := t.TempDir()
		if tc.wg0 != "" {
			if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte("[Interface]\nPrivateKey = cGFzc3dvcmQ=\n"+tc.wg0+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		prog := `HUMAN_VPN_RANGE=10.101.0.0/24; HUMAN_ADDR=10.101.0.1; HUMAN_IFACE=wg0; WG_DIR="$FAKE_WG"; ` +
			`ip() { case "$*" in "-4 -o addr show") printf '%s\n' "$FAKE_ADDR" ;; "-4 route show") printf '%s\n' "$FAKE_ROUTE" ;; esac; }; ` +
			`die() { echo DIE; exit 0; }; ` + block + "echo OK"
		cmd := exec.Command("bash", "-c", prog)
		cmd.Env = append(os.Environ(), "FAKE_ADDR="+tc.addr, "FAKE_ROUTE="+tc.route, "FAKE_WG="+dir)
		out, err := cmd.Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != tc.want {
			t.Errorf("%s: got %q (%v), want %s", name, got, err, tc.want)
		}
	}
}

// The human VPN step: after the unit is installed (hz-agent is placed by the
// install) and before hz starts (hz reads the server key's public half from
// wg0.conf at start); an existing wg0.conf is kept, a missing one is minted on
// the box by hz-agent. The summary prints the first-client console commands,
// attributed to the account that generated the command.
func TestChildScriptSetsUpTheHumanVPNAndNamesTheFirstClient(t *testing.T) {
	s := renderedTestScript()
	install := strings.Index(s, `"$BIN" install --config "$HZ_CFG"`)
	create := strings.Index(s, `"$AGENT" wg-create-config --hz-config "$HZ_CFG"`)
	up := strings.Index(s, `systemctl restart "wg-quick@$HUMAN_IFACE"`)
	start := strings.Index(s, "systemctl restart homelab-horizon")
	if install < 0 || create < 0 || up < 0 || start < 0 || !(install < create && create < up && up < start) {
		t.Fatalf("order: install %d, wg-create-config %d, wg-quick up %d, hz start %d", install, create, up, start)
	}
	if !strings.Contains(s, `if [ -e "$WG_DIR/$HUMAN_IFACE.conf" ]; then`+"\n    say") {
		t.Fatal("an existing wg0.conf is not kept")
	}
	if !strings.Contains(s, `need="$need haproxy"`) {
		t.Fatal("the kiosk needs haproxy, and the script does not install it")
	}
	for _, want := range []string{
		`sudo homelab-horizon vpn add $WHO-1 --profile vpn-only --vpn-admin --user $WHO`,
		`sudo homelab-horizon user create $WHO --admin --token-file $HZ_DIR/$WHO.token`,
		`WHO="${OPERATOR:-<you>}"`, `OPERATOR="carl"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the summary lacks %s", want)
		}
	}
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
	seed := childSeed("redline-loadtest", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080")
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
	if len(childCfg.Services) != 2 || childCfg.Services[0].Name != out.ServiceName || childCfg.Services[0].Token != svcToken ||
		childCfg.Services[0].Project != "redline" || childCfg.Services[0].Environment != "loadtest" {
		t.Fatalf("child service: %+v", childCfg.Services)
	}
	// N4g: the child's own human VPN and kiosk, as the parent planned them.
	assertSeededHumanVPN(t, childCfg)
	// ...and the kiosk's A record: the child asks for it (N4h) and the
	// parent's grant takes it — the generate step put the name there.
	childCfg.PublicIPOverride = "203.0.113.50"
	asks, unasked := childCfg.ParentDNSAsks()
	if unasked != "" || len(asks) != 1 || asks[0].Name != bootKioskName || asks[0].Values[0] != "203.0.113.50" {
		t.Fatalf("the child's DNS ask: %+v %q, want the kiosk A record at its public IP", asks, unasked)
	}
	_, verdicts, _, err := f.s.cfg().ApplyInstanceDNSRequest("loadtest-hz", asks, time.Now().Unix())
	if err != nil || len(verdicts) != 1 || !verdicts[0].Accepted {
		t.Fatalf("the parent's verdict on the kiosk record: %+v %v", verdicts, err)
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

// assertSeededHumanVPN holds a seeded child to the human VPN the fixture's
// parent plans (10.101.0.0/24 after its 10.100.0.0/24, kiosk bootKioskName):
// hz's wg0 on that range, the endpoint and kiosk at the kiosk name, the MFA
// jail with nobody exempt and its portal routed by HAProxy to hz's own UI,
// split-horizon DNS for the kiosk at the child's wg0 address, hz ALSO
// listening there — and hz-upstream still not hz's.
func assertSeededHumanVPN(t *testing.T, c *config.Config) {
	t.Helper()
	const addr = "10.101.0.1"
	if c.WGInterface != "wg0" || c.WGConfigPath != "/etc/wireguard/wg0.conf" || c.VPNRange != "10.101.0.0/24" || c.DNS != addr {
		t.Fatalf("seeded wg0: iface %q path %q range %q dns %q", c.WGInterface, c.WGConfigPath, c.VPNRange, c.DNS)
	}
	if c.ServerEndpoint != bootKioskName+":51820" || c.KioskURL != "https://"+bootKioskName || c.ServerPublicKey != "" {
		t.Fatalf("seeded endpoint %q, kiosk_url %q, server_public_key %q (must be read from the box's wg0.conf)", c.ServerEndpoint, c.KioskURL, c.ServerPublicKey)
	}
	if c.VPNListen == nil || !c.VPNListenEnabled() || !c.VPNMFAEnabled || c.MFAScope() != config.MFAScopeAll || !c.HAProxyEnabled {
		t.Fatalf("seeded vpn_listen %v, mfa %v scope %s, haproxy %v", c.VPNListen, c.VPNMFAEnabled, c.MFAScope(), c.HAProxyEnabled)
	}
	if err := c.UpstreamTunnelCollision(); err != nil {
		t.Fatalf("the seed hands hz its upstream tunnel: %v", err)
	}
	var kiosk *config.Service
	for i := range c.Services {
		if c.Services[i].Name == childKioskService {
			kiosk = &c.Services[i]
		}
	}
	if kiosk == nil || len(kiosk.Domains) != 1 || kiosk.Domains[0] != bootKioskName || kiosk.Proxy == nil || !kiosk.Proxy.Self ||
		kiosk.InternalDNS == nil || kiosk.InternalDNS.IP != addr || kiosk.ExternalDNS == nil || len(kiosk.ExternalDNS.GetIPs()) != 0 {
		t.Fatalf("seeded kiosk service: %+v", kiosk)
	}
	if got := portalRedirectURL(c, c.DeriveHAProxyBackends()); got != "https://"+bootKioskName+mfaPortalPath {
		t.Fatalf("the MFA jail's portal: %q — a jailed peer would get a 403, not the kiosk", got)
	}
	answered := false
	for _, r := range c.DeriveDNSRecords() {
		if r.Name == bootKioskName && r.IP == addr {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("dnsmasq does not answer %s with %s: a VPN client would reach the kiosk outside the tunnel", bootKioskName, addr)
	}
	if got, why := vpnListenTarget(c.EffectiveListenAddr(), addr, c.VPNListenEnabled()); got != addr+":"+childListenPort {
		t.Fatalf("hz's VPN listener: %q (%s), want %s:%s", got, why, addr, childListenPort)
	}
}

// The seed's merge sets the human VPN over an existing child's config — the
// defaults hz saved there (the PARENT's range, the example kiosk) included —
// and keeps everything else, an operator's MFA scope among it.
func TestChildScriptMergeSetsTheHumanVPN(t *testing.T) {
	seed, err := withHumanVPN(childSeed("loadtest-hz", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080"),
		&childHumanVPN{Range: "10.101.0.0/24", Kiosk: bootKioskName, Operator: "carl"})
	if err != nil {
		t.Fatal(err)
	}
	current := `{"listen_addr": "127.0.0.1:8080", "vpn_range": "10.100.0.0/24", "dns": "10.100.0.1", "kiosk_url": "https://kiosk.vpn.example.com",
	  "server_endpoint": "vpn.example.com:51820", "server_public_key": "UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko=", "haproxy_enabled": false,
	  "vpn_admins": ["carl-1"], "ntfy_url": "https://ntfy.example.com/x",
	  "services": [{"name": "grafana", "domains": ["grafana.example.com"]},
	               {"name": "hz-kiosk", "domains": ["old.example.com"], "pci_scope": "out"}]}`
	cfg, err := config.LoadFromJSON(runJQMerge(t, seed, current, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	assertSeededHumanVPN(t, cfg)
	if len(cfg.VPNAdmins) != 1 || cfg.NtfyURL != "https://ntfy.example.com/x" || len(cfg.Services) != 3 {
		t.Fatalf("the merge lost something: admins %v, ntfy %q, %d services", cfg.VPNAdmins, cfg.NtfyURL, len(cfg.Services))
	}

	kept, err := config.LoadFromJSON(runJQMerge(t, seed, `{"vpn_mfa_scope": "admins-exempt"}`, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if kept.MFAScope() != config.MFAScopeAdminsExempt {
		t.Fatalf("an operator's MFA scope was replaced: %s", kept.MFAScope())
	}

	// Without a human VPN the merge sets none of it.
	plain, err := config.LoadFromJSON(runJQMerge(t, childSeed("loadtest-hz", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080"),
		`{"vpn_range": "10.100.0.0/24", "kiosk_url": "https://kiosk.vpn.example.com"}`, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.VPNRange != "10.100.0.0/24" || plain.KioskURL != "https://kiosk.vpn.example.com" || plain.HAProxyEnabled || len(plain.Services) != 1 {
		t.Fatalf("a command without a human VPN changed the VPN settings: range %s kiosk %s haproxy %v services %d",
			plain.VPNRange, plain.KioskURL, plain.HAProxyEnabled, len(plain.Services))
	}
}

// A seeded child binds loopback, not every interface: an empty or default
// ":8080" listen address becomes 127.0.0.1:8080; an explicit one is kept.
func TestChildScriptSeedsALoopbackListener(t *testing.T) {
	seed := childSeed("redline-loadtest", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080")
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
