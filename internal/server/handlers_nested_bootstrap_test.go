package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// The one-command nested-hz bootstrap (handlers_nested_bootstrap.go): generate,
// the script, the enrolment and the binary.

const (
	bootKiosk    = "https://portal.example.com"
	bootPublic   = "portal.example.com"
	bootServerPK = "UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko="
	// The child's kiosk (N4g), and this parent's LAN as the probe reads it.
	bootKioskName = "kiosk.hz.loadtest.example.com"
	bootLAN       = "192.168.1.0/24"
)

// boxKey is the public key a box sends (the private half never exists in
// these tests: the parent must not need it).
var boxKey = attributionKeys[1]

const bootWG0 = `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24
ListenPort = 51820

[Peer]
# alice-phone
PublicKey = 8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=
AllowedIPs = 10.100.0.2/32
`

type bootFixture struct {
	s       *Server
	wgPath  string
	binDir  string
	reloads *int
}

func writeChildBin(t *testing.T, dir, platform, body, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, childBinPrefix+platform), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	sha := hex.EncodeToString(sum[:])
	m, _ := json.Marshal(childBinManifest{Version: version, SHA256: sha})
	if err := os.WriteFile(filepath.Join(dir, platform+".json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
	return sha
}

// newBootFixture is a parent with a public vhost, wg0 at 10.100.0.1, hz on
// :8080, project redline with a staging rung, and both child binaries.
func newBootFixture(t *testing.T) bootFixture {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:      ":8080",
		KioskURL:        bootKiosk,
		VPNRange:        "10.100.0.0/24",
		ServerPublicKey: bootServerPK,
		ServerEndpoint:  "vpn.example.com:51820",
		Projects:        []config.Project{{Name: "redline"}, {Name: "other"}},
		Environments:    []config.Environment{{Project: "redline", Name: "staging", Posture: "staging"}},
		WGPeers:         []config.WGPeer{{Name: "alice-phone", PublicKey: attributionKeys[0], AllowedIPs: "10.100.0.2/32"}},
		// The zone the kiosk name is in: this hz owns it, with a provider,
		// so it issues the kiosk's certificate and publishes its A record.
		Zones: []config.Zone{{Name: "example.com", ZoneID: "Z1", DNSProvider: &config.DNSProviderConfig{Type: "route53"}}},
	}
	s := newTestServer(t, cfg)
	s.parentLANProbe = func() []string { return []string{bootLAN} }
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte(bootWG0), 0o600); err != nil {
		t.Fatal(err)
	}
	s.wg = wireguard.NewConfig(path, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	n := 0
	s.wgReload = func() error { n++; return nil }
	dir := filepath.Join(t.TempDir(), "child-bin")
	writeChildBin(t, dir, "linux-amd64", "hz amd64 build", "v0.4.1-test")
	writeChildBin(t, dir, "linux-arm64", "hz arm64 build", "v0.4.1-test")
	s.childBins = newChildBinStore(dir)
	return bootFixture{s: s, wgPath: path, binDir: dir, reloads: &n}
}

func bootReq() apitypes.NestedBootstrapReq {
	return apitypes.NestedBootstrapReq{Project: "redline", Instance: "loadtest-hz", Rung: "loadtest", From: "staging", Kiosk: bootKioskName}
}

func (f bootFixture) generate(t *testing.T, req apitypes.NestedBootstrapReq) apitypes.NestedBootstrapResp {
	t.Helper()
	var out apitypes.NestedBootstrapResp
	postDeclare(t, f.s, f.s.handleAPINestedBootstrap, apitypes.NestedBootstrapPath, req, &out)
	return out
}

var commandRe = regexp.MustCompile(`^curl -fsSL https://portal\.example\.com/admin/hz-child/install/(hzc_[0-9a-f]{48}) \| sudo bash$`)

func grantOf(t *testing.T, cmd string) string {
	t.Helper()
	m := commandRe.FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("command has the wrong shape: %q", cmd)
	}
	return m[1]
}

func (f bootFixture) script(grant string) string {
	r := httptest.NewRequest(http.MethodGet, bootKiosk+"/admin/hz-child/install/"+grant, nil)
	w := httptest.NewRecorder()
	f.s.handleChildInstallScript(w, r)
	return w.Body.String()
}

func (f bootFixture) enrol(grant string, req apitypes.NestedEnrolReq) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, bootKiosk+"/admin/hz-child/enrol/"+grant, bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.s.handleChildEnrol(w, r)
	return w
}

func boxEnrol() apitypes.NestedEnrolReq {
	return apitypes.NestedEnrolReq{PublicKey: boxKey, Platform: "linux-arm64", Hostname: "ip-192-0-2-50"}
}

func decodeEnrol(t *testing.T, w *httptest.ResponseRecorder) apitypes.NestedEnrolResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("enrol: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.NestedEnrolResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Generate declares an HZ INSTANCE — never a machine — and the rung, in one
// save. A machine that happens to share the name is a different record and is
// left exactly as it was.
func TestBootstrapDeclaresInstanceRungAndGrantInOneStep(t *testing.T) {
	f := newBootFixture(t)
	_ = f.s.updateConfig(func(c *config.Config) {
		c.Machines = []config.Machine{{Name: "loadtest-hz", Note: "a box of the same name"}}
	})
	before := time.Now()
	out := f.generate(t, bootReq())
	grant := grantOf(t, out.Command)

	if exp := time.Unix(out.ExpiresAt, 0); exp.Before(before.Add(59*time.Minute)) || exp.After(before.Add(61*time.Minute)) {
		t.Fatalf("a bootstrap command lasts an hour, got expiry %s", exp)
	}
	if out.Rung != "redline/loadtest" || out.Instance != "loadtest-hz" || out.Machine != "loadtest-hz" || len(out.Reused) != 0 || out.Revoked || out.Warning != "" || len(out.Missing) != 0 {
		t.Fatalf("response: %+v", out)
	}
	cfg := f.s.cfg()
	h, ok := cfg.FindHZInstance("loadtest-hz")
	if !ok || h.Project != "redline" || !isPendingHZURL(h.URL) || h.TokenSHA256 != "" {
		t.Fatalf("instance: %+v %v", h, ok)
	}
	if len(cfg.Machines) != 1 || cfg.Machines[0].Name != "loadtest-hz" || cfg.Machines[0].Note != "a box of the same name" || cfg.Machines[0].Project != "" {
		t.Fatalf("generate touched the machines: %+v", cfg.Machines)
	}
	e, err := cfg.LookupEnvironment("redline", "loadtest")
	if err != nil || e.Posture != "prod" || e.From != "staging" || e.Upstream != "loadtest-hz" {
		t.Fatalf("rung: %+v %v", e, err)
	}
	// And it is on disk, not only in memory: one save carried both.
	disk, err := config.Load(f.s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk.FindHZInstance("loadtest-hz"); !ok {
		t.Fatal("the instance is not on disk")
	}
	if _, err := disk.LookupEnvironment("redline", "loadtest"); err != nil {
		t.Fatal("the rung is not on disk")
	}
	if exp, ok := f.s.childGrantStore().pending("loadtest-hz"); !ok || exp.Unix() != out.ExpiresAt {
		t.Fatal("no live grant for the instance")
	}
	if s := f.script(grant); !strings.Contains(s, `GRANT="`+grant+`"`) {
		t.Fatalf("the script for a live grant does not carry it:\n%s", s)
	}
	if *f.reloads != 0 {
		t.Fatal("generate touched wg0 — the box's peer exists only after it enrols")
	}
	if b, _ := os.ReadFile(f.wgPath); string(b) != bootWG0 {
		t.Fatalf("generate wrote wg0.conf:\n%s", b)
	}
}

// Each refusal has its own sentence, and leaves nothing declared and no grant.
func TestBootstrapRefusalsEachSaySomething(t *testing.T) {
	type tc struct {
		name  string
		setup func(f bootFixture)
		req   func(r *apitypes.NestedBootstrapReq)
		want  string
	}
	cases := []tc{
		{name: "no project", req: func(r *apitypes.NestedBootstrapReq) { r.Project = "" }, want: "project is required"},
		{name: "no instance", req: func(r *apitypes.NestedBootstrapReq) { r.Instance = "" }, want: "instance is required"},
		{name: "instance and machine disagree", req: func(r *apitypes.NestedBootstrapReq) { r.Machine = "other-hz" }, want: "disagree"},
		{name: "no rung", req: func(r *apitypes.NestedBootstrapReq) { r.Rung = "" }, want: "rung is required"},
		{name: "no from", req: func(r *apitypes.NestedBootstrapReq) { r.From = "" }, want: "from is required"},
		{name: "unknown project", req: func(r *apitypes.NestedBootstrapReq) { r.Project = "ghost" }, want: `no project "ghost"`},
		{name: "from not a rung", req: func(r *apitypes.NestedBootstrapReq) { r.From = "qa" }, want: `from-rung "qa" is not a rung`},
		{name: "bad instance name", req: func(r *apitypes.NestedBootstrapReq) { r.Instance = "a b" }, want: "must be letters, digits"},
		{name: "bad platform", req: func(r *apitypes.NestedBootstrapReq) { r.Platform = "linux-mips" }, want: "not one a nested hz is bootstrapped on"},
		{name: "no public vhost", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) { c.KioskURL = "" })
		}, want: "no public vhost"},
		{name: "no wireguard endpoint", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) { c.ServerEndpoint = "" })
		}, want: "endpoint is not set"},
		{name: "hz not on the WG address", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) { c.ListenAddr = "127.0.0.1:8080" })
		}, want: "could not reach this hz over the VPN"},
		{name: "no binaries", setup: func(f bootFixture) {
			_ = os.RemoveAll(f.binDir)
		}, want: "no server binary for a child is uploaded"},
		{name: "named platform missing", setup: func(f bootFixture) {
			_ = os.Remove(filepath.Join(f.binDir, childBinPrefix+"linux-arm64"))
		}, req: func(r *apitypes.NestedBootstrapReq) { r.Platform = "linux-arm64" }, want: "no linux-arm64 server binary is uploaded"},
		{name: "a partial upload", setup: func(f bootFixture) {
			_ = os.WriteFile(filepath.Join(f.binDir, childBinPrefix+"linux-arm64"), []byte("hz arm64 bui"), 0o755)
		}, req: func(r *apitypes.NestedBootstrapReq) { r.Platform = "linux-arm64" }, want: "does not match its manifest"},
		{name: "instance owned elsewhere", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) {
				c.Projects = append(append([]config.Project(nil), c.Projects...), config.Project{Name: "other"})
				c.HZInstances = []config.HZInstance{{Name: "loadtest-hz", Project: "other", URL: "http://192.0.2.7:8080"}}
			})
		}, want: "owned by other, not redline"},
		{name: "rung placed here", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) {
				c.Environments = append(append([]config.Environment(nil), c.Environments...), config.Environment{Project: "redline", Name: "loadtest", Posture: "prod", From: "staging"})
			})
		}, want: "is placed HERE"},
		{name: "rung in another hz", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) {
				c.HZInstances = []config.HZInstance{{Name: "other-hz", Project: "redline", URL: "http://192.0.2.8:8080"}}
				c.Environments = append(append([]config.Environment(nil), c.Environments...), config.Environment{Project: "redline", Name: "loadtest", Posture: "prod", From: "staging", Upstream: "other-hz"})
			})
		}, want: "placed in other-hz, not loadtest-hz"},
		{name: "client name taken by a human client", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) {
				c.WGPeers = append(append([]config.WGPeer(nil), c.WGPeers...), config.WGPeer{Name: "loadtest-hz-upstream", PublicKey: attributionKeys[2], AllowedIPs: "10.100.0.9/32"})
			})
		}, want: `already exists with profile "lan-access"`},
		// N4g: the human VPN.
		{name: "human VPN with no kiosk", req: func(r *apitypes.NestedBootstrapReq) { r.Kiosk = "" }, want: "domain is required"},
		{name: "a kiosk with the human VPN off", req: func(r *apitypes.NestedBootstrapReq) { off := false; r.HumanVPN = &off },
			want: "humanVpn is false"},
		{name: "a range on the parent's VPN", req: func(r *apitypes.NestedBootstrapReq) { r.HumanVPNRange = "10.100.0.0/24" },
			want: "overlaps 10.100.0.0/24"},
		{name: "a range on the parent's LAN", req: func(r *apitypes.NestedBootstrapReq) { r.HumanVPNRange = "192.168.1.128/25" },
			want: "overlaps " + bootLAN},
		{name: "a kiosk outside every zone", req: func(r *apitypes.NestedBootstrapReq) { r.Kiosk = "kiosk.example.invalid" },
			want: "outside every zone this hz owns"},
		{name: "a kiosk that is a service here", setup: func(f bootFixture) {
			_ = f.s.updateConfig(func(c *config.Config) {
				c.Services = append(append([]config.Service(nil), c.Services...), config.Service{Name: "portal", Domains: []string{bootKioskName}})
			})
		}, want: "also a domain of service portal"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newBootFixture(t)
			if c.setup != nil {
				c.setup(f)
			}
			before := f.s.cfg()
			req := bootReq()
			if c.req != nil {
				c.req(&req)
			}
			var e struct{ Error string }
			_ = json.Unmarshal([]byte(postDeclareErr(t, f.s, f.s.handleAPINestedBootstrap, apitypes.NestedBootstrapPath, req)), &e)
			msg := e.Error
			if !strings.Contains(msg, c.want) {
				t.Fatalf("refusal %q does not say %q", msg, c.want)
			}
			if other, dup := seen[msg]; dup {
				t.Fatalf("refusal shared with %q: %s", other, msg)
			}
			seen[msg] = c.name
			if f.s.cfg() != before {
				t.Fatal("a refused generate changed the config")
			}
			if _, ok := f.s.childGrantStore().pending(req.Instance); ok {
				t.Fatal("a refused generate minted a grant")
			}
		})
	}
}

func TestBootstrapUnnamedMissingPlatformWarnsAndTheScriptStopsFirst(t *testing.T) {
	f := newBootFixture(t)
	_ = os.Remove(filepath.Join(f.binDir, childBinPrefix+"linux-amd64"))
	out := f.generate(t, bootReq())
	if len(out.Missing) != 1 || out.Missing[0] != "linux-amd64" || !strings.Contains(out.Warning, "works only on linux-arm64") {
		t.Fatalf("a missing platform must be said before the command: %+v", out)
	}
	s := f.script(grantOf(t, out.Command))
	if !strings.Contains(s, `PLATFORMS="linux-arm64"`) {
		t.Fatalf("the script must know which platforms exist, to stop before enrolling: %s", s)
	}
	// The enrolment refuses it too, WITHOUT consuming the grant.
	req := boxEnrol()
	req.Platform = "linux-amd64"
	if w := f.enrol(grantOf(t, out.Command), req); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "nothing was changed") {
		t.Fatalf("enrol for a missing platform: %d %s", w.Code, w.Body.String())
	}
	decodeEnrol(t, f.enrol(grantOf(t, out.Command), boxEnrol()))
}

// The pre-B3 `machine` field still names the instance, one release: the same
// command, the same instance declared.
func TestBootstrapAcceptsTheMachineAlias(t *testing.T) {
	f := newBootFixture(t)
	req := bootReq()
	req.Instance, req.Machine = "", "loadtest-hz"
	out := f.generate(t, req)
	if out.Instance != "loadtest-hz" || !commandRe.MatchString(out.Command) {
		t.Fatalf("alias: %+v", out)
	}
	if _, ok := f.s.cfg().FindHZInstance("loadtest-hz"); !ok {
		t.Fatal("the alias declared no instance")
	}
	if _, ok := f.s.cfg().FindMachine("loadtest-hz"); ok {
		t.Fatal("the alias declared a machine")
	}
}

func TestRegenerateRevokesTheUnusedGrantAndReuses(t *testing.T) {
	f := newBootFixture(t)
	first := grantOf(t, f.generate(t, bootReq()).Command)
	again := f.generate(t, bootReq())
	second := grantOf(t, again.Command)
	if first == second || !again.Revoked || strings.Join(again.Reused, ",") != "instance,rung" {
		t.Fatalf("re-generate: %+v", again)
	}
	if s := f.script(first); s != childRefusalScript {
		t.Fatalf("the revoked command still serves a script:\n%s", s)
	}
	if w := f.enrol(first, boxEnrol()); w.Code != http.StatusGone {
		t.Fatalf("the revoked grant enrolled: %d %s", w.Code, w.Body.String())
	}
	decodeEnrol(t, f.enrol(second, boxEnrol()))
}

func TestEnrolIsSingleUseAndTheParentStoresNoPrivateKey(t *testing.T) {
	f := newBootFixture(t)
	grant := grantOf(t, f.generate(t, bootReq()).Command)
	w := f.enrol(grant, boxEnrol())
	out := decodeEnrol(t, w)

	if out.WG.Address != "10.100.0.3" || out.WG.PeerPublicKey != bootServerPK || out.WG.Endpoint != "vpn.example.com:51820" || out.WG.AllowedIPs != "10.100.0.1/32" {
		t.Fatalf("wg: %+v", out.WG)
	}
	if out.ParentURL != "http://10.100.0.1:8080" || out.Rung != "loadtest" || out.Project != "redline" || out.ServiceName != "redline-loadtest" {
		t.Fatalf("enrol: %+v", out)
	}
	if out.Binary.URL != "http://10.100.0.1:8080/api/v1/nested/bin/linux-arm64" || out.Binary.Version != "v0.4.1-test" || out.Binary.SHA256 != digestOf("hz arm64 build") {
		t.Fatalf("binary: %+v", out.Binary)
	}
	if out.Seed.Services[0].Token != "" {
		t.Fatal("the parent wrote a local service token into the seed — the box generates it")
	}

	cfg := f.s.cfg()
	h, _ := cfg.FindHZInstance("loadtest-hz")
	if h.VPNClient != "loadtest-hz-upstream" || h.TokenSHA256 != tokenDigest(out.InstanceToken) || h.URL != "http://10.100.0.3:8080" {
		t.Fatalf("instance after enrol: %+v", h)
	}
	if out.Instance != "loadtest-hz" || out.Machine != "loadtest-hz" {
		t.Fatalf("enrol answer names %q / %q", out.Instance, out.Machine)
	}
	if _, isMachine := cfg.FindMachine("loadtest-hz"); isMachine {
		t.Fatal("enrolment declared a machine")
	}
	if cfg.GetPeerProfile("loadtest-hz-upstream") != config.ProfileUpstream || cfg.VPNProjects["loadtest-hz-upstream"] != "redline" {
		t.Fatal("the box's client must be upstream, owned by the project")
	}
	if *f.reloads != 1 {
		t.Fatalf("wg0 reloads = %d", *f.reloads)
	}

	// THE PARENT HOLDS NO PRIVATE KEY (#4). The client it stores is exactly
	// the public key the box sent, and nothing it answered or wrote carries a
	// private half. Positive control: a parent-generated keypair stores a key
	// the box never sent, and puts a private key in the answer.
	var stored *config.WGPeer
	for i, p := range cfg.WGPeers {
		if p.Name == "loadtest-hz-upstream" {
			stored = &cfg.WGPeers[i]
		}
	}
	if stored == nil || stored.PublicKey != boxKey || stored.AllowedIPs != "10.100.0.3/32" {
		t.Fatalf("stored client: %+v — it must be the box's own public key", stored)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "private") {
		t.Fatalf("the enrolment answer names a private key: %s", w.Body.String())
	}
	wg0, _ := os.ReadFile(f.wgPath)
	block := wg0[len(bootWG0):]
	if !strings.Contains(string(block), "PublicKey = "+boxKey) || strings.Contains(string(block), "PrivateKey") {
		t.Fatalf("the box's wg0 peer block:\n%s", block)
	}
	disk, _ := os.ReadFile(f.s.configPath)
	if strings.Contains(strings.ToLower(string(disk)), "private") {
		t.Fatal("config.json names a private key")
	}

	// Single use.
	if w := f.enrol(grant, apitypes.NestedEnrolReq{PublicKey: attributionKeys[2], Platform: "linux-arm64"}); w.Code != http.StatusGone {
		t.Fatalf("a used grant enrolled again: %d %s", w.Code, w.Body.String())
	}
	if s := f.script(grant); s != childRefusalScript {
		t.Fatal("a used grant still serves the script")
	}

	// The instance token fetches the binary; nothing else does.
	get := func(tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://10.100.0.1:8080/api/v1/nested/bin/linux-arm64", nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		f.s.handleNestedBinary(w, r)
		return w
	}
	if w := get(out.InstanceToken); w.Code != http.StatusOK || w.Body.String() != "hz arm64 build" || w.Header().Get("X-HZ-SHA256") != out.Binary.SHA256 {
		t.Fatalf("binary with the instance token: %d %q", w.Code, w.Body.String())
	}
	for _, tok := range []string{"", grant, "hzi_" + strings.Repeat("0", 64)} {
		if w := get(tok); w.Code != http.StatusUnauthorized {
			t.Fatalf("binary with %q: %d", tok, w.Code)
		}
	}
}

func TestEnrolRefusesABadPublicKeyAndKeepsTheGrant(t *testing.T) {
	f := newBootFixture(t)
	grant := grantOf(t, f.generate(t, bootReq()).Command)
	for _, k := range []string{"", "not-a-key", "cGFzc3dvcmQ=", strings.Repeat("A", 44)} {
		if w := f.enrol(grant, apitypes.NestedEnrolReq{PublicKey: k, Platform: "linux-arm64"}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a WireGuard public key") {
			t.Fatalf("key %q: %d %s", k, w.Code, w.Body.String())
		}
	}
	// A key this hz already has as a client is refused, also before the grant goes.
	if w := f.enrol(grant, apitypes.NestedEnrolReq{PublicKey: attributionKeys[0], Platform: "linux-arm64"}); w.Code != http.StatusConflict {
		t.Fatalf("a key already in wg0: %d %s", w.Code, w.Body.String())
	}
	decodeEnrol(t, f.enrol(grant, boxEnrol()))
}

func TestAnExpiredGrantIsRefused(t *testing.T) {
	f := newBootFixture(t)
	grant := grantOf(t, f.generate(t, bootReq()).Command)
	g := f.s.childGrantStore()
	g.mu.Lock()
	g.now = func() time.Time { return time.Now().Add(childGrantTTL + time.Second) }
	g.mu.Unlock()
	if s := f.script(grant); s != childRefusalScript {
		t.Fatal("an expired grant serves the script")
	}
	if w := f.enrol(grant, boxEnrol()); w.Code != http.StatusGone {
		t.Fatalf("an expired grant enrolled: %d", w.Code)
	}
	if _, ok := g.pending("loadtest-hz"); ok {
		t.Fatal("an expired grant reads as pending")
	}
}

func TestTheInstallRoutesAreOnThePublicVhostOnly(t *testing.T) {
	f := newBootFixture(t)
	grant := grantOf(t, f.generate(t, bootReq()).Command)
	r := httptest.NewRequest(http.MethodGet, "https://hz.example.com/admin/hz-child/install/"+grant, nil)
	w := httptest.NewRecorder()
	f.s.handleChildInstallScript(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("install script on the admin vhost: %d", w.Code)
	}
	body, _ := json.Marshal(boxEnrol())
	r = httptest.NewRequest(http.MethodPost, "https://hz.example.com/admin/hz-child/enrol/"+grant, bytes.NewReader(body))
	w = httptest.NewRecorder()
	f.s.handleChildEnrol(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("enrol on the admin vhost: %d", w.Code)
	}
	// Positive control: the same grant works on the public vhost.
	decodeEnrol(t, f.enrol(grant, boxEnrol()))
}

// Re-provisioning a wiped box: the instance and rung are reused, the box's new
// key REPLACES the old one in wg0 (same address), and the old instance token
// stops authenticating.
func TestReprovisionRotatesTheKeyAndTheToken(t *testing.T) {
	f := newBootFixture(t)
	first := decodeEnrol(t, f.enrol(grantOf(t, f.generate(t, bootReq()).Command), boxEnrol()))

	again := f.generate(t, bootReq())
	if strings.Join(again.Reused, ",") != "instance,rung" || again.Revoked {
		t.Fatalf("re-generate after enrol: %+v", again)
	}
	if st := nestedRow(t, f.s, "loadtest-hz").State; st != apitypes.NestedStateCommandGenerated {
		t.Fatalf("state with a live command = %s", st)
	}
	second := decodeEnrol(t, f.enrol(grantOf(t, again.Command), apitypes.NestedEnrolReq{PublicKey: attributionKeys[2], Platform: "linux-amd64"}))
	if second.WG.Address != first.WG.Address || second.InstanceToken == first.InstanceToken {
		t.Fatalf("re-provision: address %s -> %s, token rotated %v", first.WG.Address, second.WG.Address, second.InstanceToken != first.InstanceToken)
	}
	wg0, _ := os.ReadFile(f.wgPath)
	if strings.Contains(string(wg0), boxKey) || !strings.Contains(string(wg0), attributionKeys[2]) || strings.Count(string(wg0), "[Peer]") != 2 {
		t.Fatalf("wg0 after re-provision:\n%s", wg0)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/nested/bin/linux-amd64", nil)
	r.Header.Set("Authorization", "Bearer "+first.InstanceToken)
	if _, ok := f.s.instanceCaller(r); ok {
		t.Fatal("the old box's instance token still authenticates")
	}
}

func nestedRow(t *testing.T, s *Server, name string) apitypes.InstanceNestedResp {
	t.Helper()
	for _, r := range listInstances(t, s) {
		if r.Name == name && r.Nested != nil {
			return *r.Nested
		}
	}
	t.Fatalf("no nested row %s", name)
	return apitypes.InstanceNestedResp{}
}

func TestNestedStates(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name                    string
		token                   bool
		grant, enrolled, pulled time.Time
		want                    string
	}{
		{"declared", false, time.Time{}, time.Time{}, time.Time{}, apitypes.NestedStateDeclared},
		{"command live", false, t0, time.Time{}, time.Time{}, apitypes.NestedStateCommandGenerated},
		{"command live over a connected box", true, t0, t0, t0.Add(time.Minute), apitypes.NestedStateCommandGenerated},
		{"enrolled, no pull", true, time.Time{}, t0, time.Time{}, apitypes.NestedStateEnrolled},
		{"re-enrolled, old pull", true, time.Time{}, t0, t0.Add(-time.Hour), apitypes.NestedStateEnrolled},
		{"connected", true, time.Time{}, t0, t0.Add(time.Minute), apitypes.NestedStateConnected},
		{"connected, enrolment forgotten", true, time.Time{}, time.Time{}, t0, apitypes.NestedStateConnected},
		{"token, never pulled", true, time.Time{}, time.Time{}, time.Time{}, apitypes.NestedStateNeverConnected},
	}
	for _, c := range cases {
		if got := nestedState(c.token, c.grant, c.enrolled, c.pulled); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// N4g: generate plans the child's human VPN — a range clear of this hz's VPN
// range, LAN and upstream addresses, and the kiosk name made one of the
// instance's TLS domains and put inside its DNS grant, in the same save, and
// valid by every rule of both. The script and the seed carry the same plan.
func TestBootstrapPlansTheChildsHumanVPN(t *testing.T) {
	f := newBootFixture(t)
	out := f.generate(t, bootReq())
	hv := out.HumanVPN
	if hv == nil {
		t.Fatal("no human VPN planned")
	}
	avoid := append(f.s.cfg().ChildVPNAvoid(), f.s.parentLANs()...)
	if len(avoid) < 2 {
		t.Fatalf("instrument: the avoid list is %v", avoid)
	}
	if err := config.CheckChildVPNRange(hv.Range, avoid); err != nil {
		t.Fatalf("OVERLAP — the proposed range overlaps what this hz routes: %v", err)
	}
	// The child's upstream address is in this hz's VPN range: the proposal
	// is clear of every address an upstream client is handed.
	if err := config.CheckChildVPNRange(hv.Range, []string{"10.100.0.3"}); err != nil {
		t.Fatalf("OVERLAP — the proposed range holds an upstream address: %v", err)
	}
	if hv.Range != "10.101.0.0/24" || !hv.Proposed || hv.ServerAddress != "10.101.0.1" || hv.Kiosk != bootKioskName ||
		hv.KioskURL != "https://"+bootKioskName || hv.Endpoint != bootKioskName+":51820" || !hv.DomainsChanged {
		t.Fatalf("the human VPN plan: %+v", hv)
	}

	h, _ := f.s.cfg().FindHZInstance("loadtest-hz")
	if !slices.Contains(h.Domains, bootKioskName) || !config.GrantCovers(h.DNSGrant, bootKioskName) {
		t.Fatalf("the kiosk is not the instance's: domains %v grant %v", h.Domains, h.DNSGrant)
	}
	if err := config.Save(filepath.Join(t.TempDir(), "config.json"), f.s.cfg()); err != nil {
		t.Fatalf("the generated config does not validate: %v", err)
	}

	s := f.script(grantOf(t, out.Command))
	for _, want := range []string{`HUMAN_VPN_RANGE="10.101.0.0/24"`, `HUMAN_ADDR="10.101.0.1"`, `HUMAN_IFACE="wg0"`, `HUMAN_PORT="51820"`,
		`KIOSK="` + bootKioskName + `"`, `OPERATOR=""`} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %s", want)
		}
	}

	// Regenerate: the same range (the box keeps its wg0), the kiosk already
	// the instance's, no duplicate.
	again := f.generate(t, bootReq())
	if again.HumanVPN == nil || again.HumanVPN.Range != hv.Range || again.HumanVPN.DomainsChanged {
		t.Fatalf("a regenerate: %+v", again.HumanVPN)
	}
	h2, _ := f.s.cfg().FindHZInstance("loadtest-hz")
	if !slices.Equal(h.Domains, h2.Domains) || !slices.Equal(h.DNSGrant, h2.DNSGrant) {
		t.Fatalf("a regenerate changed the instance: %v %v", h2.Domains, h2.DNSGrant)
	}

	// The enrolment's seed is the grant's plan.
	seed := decodeEnrol(t, f.enrol(grantOf(t, again.Command), boxEnrol())).Seed
	if seed.VPNRange != hv.Range || seed.KioskURL != hv.KioskURL || seed.ServerEndpoint != hv.Endpoint || len(seed.Services) != 2 ||
		seed.Services[1].Name != childKioskService {
		t.Fatalf("the enrolment seed: %+v", seed)
	}

	// The LAN is avoided too.
	g := newBootFixture(t)
	g.s.parentLANProbe = func() []string { return []string{"10.101.0.0/16"} }
	if r := g.generate(t, bootReq()).HumanVPN.Range; r != "10.102.0.0/24" {
		t.Fatalf("with the LAN on 10.101.0.0/16 the proposal is %s", r)
	}
}

// Without a human VPN the command is what it was: no kiosk asked, nothing
// added to the instance, no range in the script, none in the seed.
func TestBootstrapWithoutAHumanVPN(t *testing.T) {
	f := newBootFixture(t)
	off := false
	req := bootReq()
	req.HumanVPN, req.Kiosk = &off, ""
	out := f.generate(t, req)
	if out.HumanVPN != nil {
		t.Fatalf("a human VPN was planned: %+v", out.HumanVPN)
	}
	if h, _ := f.s.cfg().FindHZInstance("loadtest-hz"); len(h.Domains) != 0 || len(h.DNSGrant) != 0 {
		t.Fatalf("the instance gained names: %+v", h)
	}
	if s := f.script(grantOf(t, out.Command)); !strings.Contains(s, `HUMAN_VPN_RANGE=""`) {
		t.Fatal("the script carries a human VPN range")
	}
	seed := decodeEnrol(t, f.enrol(grantOf(t, out.Command), boxEnrol())).Seed
	if seed.VPNRange != "" || seed.KioskURL != "" || len(seed.Services) != 1 {
		t.Fatalf("the seed sets a human VPN: %+v", seed)
	}
}

func TestConsoleUserName(t *testing.T) {
	for in, want := range map[string]string{"carl": "carl", "carl.t": "carl.t", "a b": "", "-x": "", "": ""} {
		var u *db.User
		if in != "" {
			u = &db.User{Username: in}
		}
		if got := consoleUserName(u); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
