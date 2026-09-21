package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// agentTestServer is an hz with the two file-writing subsystems wired up and
// pointed at a temp directory, so the endpoint renders real bytes without
// touching /etc.
func agentTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		HAProxyEnabled:    true,
		HAProxyConfigPath: filepath.Join(dir, "haproxy.cfg"),
		HAProxyHTTPPort:   80,
		HAProxyHTTPSPort:  443,
		DNSMasqEnabled:    true,
		DNSMasqConfigPath: filepath.Join(dir, "dnsmasq.conf"),
		DNSMasqHostsPath:  filepath.Join(dir, "records.conf"),
		WGInterface:       "wg0",
		UpstreamDNS:       []string{"192.0.2.53"},
	}
	s := newTestServer(t, cfg)
	s.haproxy = haproxy.New(cfg.HAProxyConfigPath, "/run/haproxy/admin.sock")
	s.dns = dnsmasq.New(cfg.DNSMasqConfigPath, cfg.DNSMasqHostsPath, []string{"wg0"}, cfg.UpstreamDNS)
	return s, dir
}

// enrolledAgent gives this hz an agent credential for the machine it renders
// for, and hands back the secret the agent would hold.
//
// THE POINT OF THIS HELPER. These tests used to authenticate with a session
// cookie — a credential hz-agent has never sent and cannot send. The handler
// was green for a caller that does not exist while the real one got a 401
// (plan/privilege-audit.md §1.1). Every test below now presents what the agent
// presents, through agent.Authorize, which is the same function the client
// calls. A cookie cannot get in here any more without somebody deliberately
// writing one.
func enrolledAgent(t *testing.T, s *Server) string {
	t.Helper()
	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll(s.buildAgentDesired().Machine, secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func agentGET(t *testing.T, s *Server, etag string) *httptest.ResponseRecorder {
	t.Helper()
	return agentGETWith(t, s, enrolledAgent(t, s), etag)
}

func agentGETWith(t *testing.T, s *Server, secret, etag string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	agent.Authorize(r, secret)
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	s.handleAgentDesired(w, r)
	return w
}

// The desired state must be what hz itself would write, byte for byte.
// Anything else and the agent's clean diff would be a lie about the flip.
func TestAgentDesiredMatchesWhatHZWouldWrite(t *testing.T) {
	s, _ := agentTestServer(t)
	w := agentGET(t, s, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var d agent.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.HAProxy == nil || len(d.HAProxy.Files) == 0 {
		t.Fatal("no haproxy section")
	}
	if got, want := d.HAProxy.Files[0].Contents, s.haproxy.GenerateConfig(80, 443, nil); got != want {
		t.Fatal("the agent would write different haproxy bytes than hz does")
	}
	if d.DNSMasq == nil || len(d.DNSMasq.Files) != 2 {
		t.Fatalf("want dnsmasq.conf and the records file, got %+v", d.DNSMasq)
	}
	if got, want := d.DNSMasq.Files[0].Contents, s.dns.GenerateConfig(); got != want {
		t.Fatal("the agent would write different dnsmasq bytes than hz does")
	}
}

// A disabled subsystem is ABSENT, not empty. An agent that read an empty
// section as "hz wants nothing here" would tear down the gateway's DNS.
func TestDisabledSubsystemIsAbsentNotEmpty(t *testing.T) {
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.DNSMasqEnabled = false
	s.config.Store(&cfg)

	var d agent.Desired
	if err := json.Unmarshal(agentGET(t, s, "").Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.DNSMasq != nil {
		t.Fatalf("a disabled dnsmasq produced a section: %+v", d.DNSMasq)
	}
}

// The poll is conditional, so a fleet that is in sync costs a 304.
func TestAgentDesiredAnswers304OnAMatchingETag(t *testing.T) {
	s, _ := agentTestServer(t)
	first := agentGET(t, s, "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the full response")
	}

	second := agentGET(t, s, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("a 304 carried a body: %q", second.Body.String())
	}
}

// A config change must move the ETag, or an agent would never be told.
func TestETagMovesWhenTheConfigDoes(t *testing.T) {
	s, _ := agentTestServer(t)
	before := agentGET(t, s, "").Header().Get("ETag")

	cfg := *s.cfg()
	cfg.HAProxyHTTPPort = 8080
	s.config.Store(&cfg)

	if after := agentGET(t, s, "").Header().Get("ETag"); after == before {
		t.Fatal("changing the HTTP port did not move the generation")
	}
}

// This endpoint describes a machine's whole network shape. It is not public.
func TestAgentDesiredNeedsAdmin(t *testing.T) {
	s, _ := agentTestServer(t)
	w := httptest.NewRecorder()
	s.handleAgentDesired(w, httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for an anonymous caller, got %d", w.Code)
	}
}

// THE PAIR TEST. The real client, over a real socket, against the real routing
// table — not a hand-built request and not a hand-built server.
//
// This is the durable half of the fix. Every other test here drives the
// handler directly, so any of them can be written against a credential the
// agent does not send; that is exactly how a 401 shipped with 37 green tests
// (plan/privilege-audit.md §1.1). This one cannot be: the request is built by
// agent.HTTPSource, which is the code the daemon runs, and the route comes
// from setupRoutes, which is the table hz serves. Break the credential at
// either end and this fails.
func TestTheRealAgentClientAuthenticatesToTheRealHZ(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	src := &agent.HTTPSource{BaseURL: hz.URL, Token: secret}

	d, etag, changed, err := src.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("the agent could not fetch its desired state: %v", err)
	}
	if !changed || d == nil {
		t.Fatalf("first poll: changed=%v desired=%v", changed, d)
	}
	if d.HAProxy == nil || len(d.HAProxy.Files) == 0 {
		t.Fatal("the agent got a payload with no haproxy section")
	}

	// And the conditional poll the whole transport is built around.
	if _, _, changed, err = src.Fetch(context.Background(), etag); err != nil || changed {
		t.Fatalf("second poll: changed=%v err=%v", changed, err)
	}
}

// The negative control for the test above: the same real client with no
// credential must be refused, so a green pair test means the credential was
// checked rather than that the route is open.
func TestTheRealAgentClientIsRefusedWithoutACredential(t *testing.T) {
	s, _ := agentTestServer(t)
	enrolledAgent(t, s) // hz HAS a credential; this client is not holding it.

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	for name, src := range map[string]*agent.HTTPSource{
		"no credential":    {BaseURL: hz.URL},
		"wrong credential": {BaseURL: hz.URL, Token: "not-the-one"},
		// The admin token is the credential the agent USED to be given. It
		// must not be a way in, or this fix would have widened hz's surface
		// rather than narrowed the agent's.
		"the admin token": {BaseURL: hz.URL, Token: s.adminToken},
	} {
		_, _, changed, err := src.Fetch(context.Background(), "")
		if err == nil || changed {
			t.Fatalf("%s: hz served the desired state (changed=%v err=%v)", name, changed, err)
		}
		if !strings.Contains(err.Error(), "401") {
			t.Fatalf("%s: want a 401, got %v", name, err)
		}
	}
}

// The credential the agent holds must not be an admin credential. This is the
// whole reason isAdmin did not grow a Bearer branch: had it, the check below
// would pass for every admin surface hz serves.
func TestTheAgentCredentialIsNotAnAdminCredential(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	agent.Authorize(r, secret)
	if s.isAdmin(r) {
		t.Fatal("an agent credential passed isAdmin; the agent now holds the estate")
	}

	// And it mints no session either.
	if _, ok := s.verifyCookie(secret); ok {
		t.Fatal("the agent credential verified as a session cookie")
	}
}

// The other direction, stated on the handler rather than through the client:
// hz's own admin token, presented the way the agent presents its credential,
// is not an agent credential.
func TestTheAdminTokenIsNotAnAgentCredential(t *testing.T) {
	s, _ := agentTestServer(t)
	if _, ok := s.agentCaller(requestWithBearer(s.adminToken)); ok {
		t.Fatal("the admin token authenticated as an agent")
	}
	// Even if somebody enrols it, which is the mistake this guards.
	if err := s.agentCredentials().Enroll("gateway", s.adminToken); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.agentCaller(requestWithBearer(s.adminToken)); ok {
		t.Fatal("enrolling the admin token made it an agent credential")
	}
}

func requestWithBearer(secret string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	agent.Authorize(r, secret)
	return r
}

// hz renders for the box it runs on. An agent enrolled under a different
// machine is told so rather than handed this machine's network config — the
// seam item 13's Machine record fills in.
func TestACredentialForAnotherMachineGetsNoPayload(t *testing.T) {
	s, _ := agentTestServer(t)
	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("some-other-box", secret); err != nil {
		t.Fatal(err)
	}

	w := agentGETWith(t, s, secret, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for another machine's agent, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "haproxy") {
		t.Fatal("hz leaked this machine's config to another machine's agent")
	}
}

// Nothing hz says about a failed or successful authentication may contain the
// credential. An error body is the one place a secret gets pasted into a chat.
func TestNoCredentialReachesTheResponse(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)

	for _, w := range []*httptest.ResponseRecorder{
		agentGETWith(t, s, secret, ""),
		agentGETWith(t, s, "a-wrong-credential", ""),
		agentGETWith(t, s, "", ""),
	} {
		body := w.Body.String()
		for _, needle := range []string{secret, "a-wrong-credential", s.adminToken} {
			if needle != "" && strings.Contains(body, needle) {
				t.Fatalf("a credential reached the response body (status %d)", w.Code)
			}
		}
	}
}

// WHAT REPLACED TestNoKeyMaterialCrossesThisEndpoint, AND WHY.
//
// That test pinned "no key material crosses this endpoint at all", which was
// the right constraint while the route was gated on an ADMIN credential: the
// WireGuard section was withheld precisely because the shared admin token
// would otherwise have become a key-fetch. Item 12 step 1 gave the agent a
// credential of its own and step 2 took the admin branch off, so the section
// is served — and wg0.conf carries the interface private key by construction.
// The old assertion is therefore false by design, not by accident, and
// deleting it would leave nothing standing where it stood.
//
// What is still true, and what the four tests below pin in its place:
//
//	the payload FORCES Secret on WireGuard files, it does not trust a producer
//	the pattern redaction in diff.go catches a key even if Secret were wrong
//	no key material reaches a log line, an error, or a diff report
//	only a credential for THIS machine is answered at all
//
// The third and fourth were what the old test was really protecting; the
// first two are the layers that let a private key cross safely instead of not
// crossing.

// Obviously fake: 32 bytes of counting pattern, base64'd the way wg writes a
// key. It is the right SHAPE so the parser and the redaction regex see what
// they would see on a real box, and it is not a key anybody has.
const (
	fakeWGPrivateKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	fakeWGPeerKey    = "Hx4dHBsaGRgXFhUUExIREA8ODQwLCgkIBwYFBAMCAQA="
)

// withWireGuard gives the test server a wg0.conf to serve.
//
// In its OWN temp directory, not agentTestServer's: that one is globbed by
// TestAgentDesiredWritesNothing, which asserts the endpoint creates no files
// there. A fixture file dropped in it would turn that assertion into noise.
func withWireGuard(t *testing.T, s *Server) (path, contents string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "wg0.conf")
	contents = "[Interface]\n" +
		"PrivateKey = " + fakeWGPrivateKey + "\n" +
		"Address = 10.99.0.1/24\n" +
		"ListenPort = 51820\n\n" +
		"[Peer]\n" +
		"PublicKey = " + fakeWGPeerKey + "\n" +
		"AllowedIPs = 10.99.0.2/32\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := *s.cfg()
	cfg.WGInterface = "wg0"
	cfg.WGConfigPath = path
	s.config.Store(&cfg)
	return path, contents
}

func servedDesired(t *testing.T, s *Server) (agent.Desired, string) {
	t.Helper()
	body := agentGET(t, s, "").Body.String()
	var d agent.Desired
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d, body
}

// The section is served, it is the file hz maintains, and it is marked secret.
func TestWireGuardSectionCrossesAsTheFileHZMaintains(t *testing.T) {
	s, _ := agentTestServer(t)
	path, contents := withWireGuard(t, s)

	d, _ := servedDesired(t, s)
	if d.WireGuard == nil {
		t.Fatal("no WireGuard section; the agent cannot own an interface it is never told about")
	}
	if d.WireGuard.Interface != "wg0" || d.WireGuard.ConfigPath != path {
		t.Fatalf("section names the wrong interface or path: %+v", d.WireGuard)
	}
	if len(d.WireGuard.Files) != 1 {
		t.Fatalf("want exactly wg0.conf, got %d files", len(d.WireGuard.Files))
	}
	f := d.WireGuard.Files[0]
	if f.Contents != contents {
		t.Fatal("the agent would write different WireGuard bytes than hz has")
	}
	if f.Mode != 0o600 {
		t.Fatalf("wg0.conf must land 0600, payload says %04o", f.Mode)
	}
	if !f.Secret {
		t.Fatal("the WireGuard file crossed without the Secret flag")
	}

	// A missing or unreadable wg0.conf is NO SECTION, never an empty one —
	// the agent reads an empty section as "hz wants this file empty".
	cfg := *s.cfg()
	cfg.WGConfigPath = filepath.Join(t.TempDir(), "absent.conf")
	s.config.Store(&cfg)
	if d, _ := servedDesired(t, s); d.WireGuard != nil {
		t.Fatalf("an unreadable wg0.conf produced a section: %+v", d.WireGuard)
	}
}

// LAYER ONE, and the thing that makes it a property rather than a habit:
// Secret is FORCED by Desired.files(), not read off the wire.
//
// The check tampers with the decoded payload — sets Secret back to false, the
// way a producer that forgot or a middlebox that rewrote it would — and then
// runs the real reconcile and the real report over it. If the flag were
// trusted, the diff would widen into line-level output and print the key.
func TestTheSecretFlagIsForcedNotTrusted(t *testing.T) {
	s, _ := agentTestServer(t)
	path, _ := withWireGuard(t, s)

	d, _ := servedDesired(t, s)
	d.WireGuard.Files[0].Secret = false // the producer "forgot"

	// Observed differs from desired, so the differ has something to describe.
	obs := agent.Observed{Files: map[string]agent.FileState{
		path: {Exists: true, Contents: "[Interface]\nPrivateKey = " + fakeWGPeerKey + "\nAddress = 10.99.0.1/24\n"},
	}}
	report := agent.Report(agent.Compute(&d, obs))

	if strings.Contains(report, fakeWGPrivateKey) || strings.Contains(report, fakeWGPeerKey) {
		t.Fatalf("a key reached the report with Secret cleared; the flag is being trusted:\n%s", report)
	}
	if !strings.Contains(report, "key material") {
		t.Fatalf("the WireGuard change was not described as secret, so the flag was not forced:\n%s", report)
	}
}

// LAYER TWO, proven INDEPENDENT of layer one.
//
// The forcing in Desired.files() only knows about WireGuard files. So the
// bytes hz actually served are put into a section that gets no forcing at all
// — an HAProxy file, Secret false — and the report must still not carry the
// key. That is diff.go's pattern redaction working with layer one switched
// off, which is what "two layers" has to mean to be worth having.
func TestRedactionCatchesTheKeyWithTheSecretFlagOutOfTheWay(t *testing.T) {
	s, _ := agentTestServer(t)
	withWireGuard(t, s)

	d, _ := servedDesired(t, s)
	served := d.WireGuard.Files[0].Contents
	if !strings.Contains(served, fakeWGPrivateKey) {
		t.Fatal("fixture is not carrying the key, so this test would pass for the wrong reason")
	}

	unforced := &agent.Desired{
		Machine: d.Machine,
		HAProxy: &agent.HAProxySection{
			ConfigPath: "/etc/haproxy/haproxy.cfg",
			Files:      []agent.File{{Path: "/etc/haproxy/haproxy.cfg", Contents: served}},
		},
	}
	obs := agent.Observed{Files: map[string]agent.FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, Contents: "global\n"},
	}}
	report := agent.Report(agent.Compute(unforced, obs))

	if strings.Contains(report, fakeWGPrivateKey) || strings.Contains(report, fakeWGPeerKey) {
		t.Fatalf("the key survived into a report with no Secret flag anywhere:\n%s", report)
	}
	if !strings.Contains(report, "[redacted]") {
		t.Fatalf("nothing was redacted, so the line never reached the redactor:\n%s", report)
	}
}

// The endpoint's own answers. A key may cross to the machine it belongs to; it
// may not appear in anything hz says to anybody else — a refusal body, an
// error, or the payload served to another machine's agent.
func TestNoKeyMaterialReachesARefusalOrAnotherMachine(t *testing.T) {
	s, _ := agentTestServer(t)
	withWireGuard(t, s)

	otherSecret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("some-other-box", otherSecret); err != nil {
		t.Fatal(err)
	}

	admin := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	admin.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	adminW := httptest.NewRecorder()
	s.handleAgentDesired(adminW, admin)

	anon := httptest.NewRecorder()
	s.handleAgentDesired(anon, httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil))

	for name, w := range map[string]*httptest.ResponseRecorder{
		"an hz admin session":     adminW,
		"an anonymous caller":     anon,
		"a wrong credential":      agentGETWith(t, s, "not-the-one", ""),
		"another machine's agent": agentGETWith(t, s, otherSecret, ""),
	} {
		if w.Code == http.StatusOK {
			t.Fatalf("%s was served the payload (status %d)", name, w.Code)
		}
		body := w.Body.String()
		for _, needle := range []string{fakeWGPrivateKey, fakeWGPeerKey, "PrivateKey"} {
			if strings.Contains(body, needle) {
				t.Fatalf("%s: key material in a %d body: %s", name, w.Code, body)
			}
		}
	}
}

// The admin path is GONE, stated on its own so a regression names itself.
// An hz admin session used to read this endpoint; while the payload was
// haproxy.cfg that was defensible, and it stopped being the moment a private
// key started crossing (plan/privilege-audit.md §3, constraint 4).
func TestAnAdminSessionIsRefused(t *testing.T) {
	s, _ := agentTestServer(t)

	r := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	// Prove the credential is a real one, or this test passes on a typo.
	if !s.isAdmin(r) {
		t.Fatal("the fixture is not an admin session, so refusing it proves nothing")
	}

	w := httptest.NewRecorder()
	s.handleAgentDesired(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an hz admin read the agent's desired state (status %d)", w.Code)
	}
}

// THE 503 PAGE AND THE CONFIG THAT NAMES IT TRAVEL TOGETHER.
//
// `errorfile 503 …` is always emitted and HAProxy refuses to start on a
// missing errorfile, so these two cannot be in different sections: a section
// is the reload granularity, and two sections would mean two reloads with a
// window in between where the config names a file that is not there. They are
// in one section, so Apply writes both and reloads once.
func TestThe503PageIsInTheSameSectionAsTheConfigThatNamesIt(t *testing.T) {
	s, dir := agentTestServer(t)
	d, _ := servedDesired(t, s)

	if d.HAProxy == nil {
		t.Fatal("no haproxy section")
	}
	// The path WriteConfig writes today, spelled the way apply.go spells it.
	want := filepath.Join(dir, haproxy.Error503Path)
	var page *agent.File
	for i, f := range d.HAProxy.Files {
		if f.Path == want {
			page = &d.HAProxy.Files[i]
		}
	}
	if page == nil {
		t.Fatalf("errors/503.http is not in the haproxy section: %+v", d.HAProxy.Files)
	}
	if page.Contents != haproxy.RenderError503() {
		t.Fatal("the agent would write a different 503 page than hz does")
	}
	if page.Secret {
		t.Fatal("the 503 page is a constant with no secret in it; marking it secret hides a diff for nothing")
	}
	if page.Mode != 0o644 {
		t.Fatalf("the 503 page must land 0644, payload says %04o", page.Mode)
	}
	// And the config that names it is in the same section, so one pass writes
	// both and one reload follows.
	if d.HAProxy.Files[0].Path != s.cfg().HAProxyConfigPath {
		t.Fatalf("the config is not in this section: %+v", d.HAProxy.Files)
	}
	if !strings.Contains(d.HAProxy.Files[0].Contents, "errorfile 503") {
		t.Fatal("the config does not name an errorfile, so this test proves nothing")
	}
}

// The maintenance pages, and the claim that lets a cleared one be REMOVED.
//
// This is the end-to-end of the whole item: hz's payload, the agent's real
// observer against a real directory, and a plan that says the page an admin
// cleared would go — while the vanilla error pages the distribution put in
// that same directory stay.
func TestMaintenancePagesCrossAndTheStaleOnesAreRemoved(t *testing.T) {
	s, dir := agentTestServer(t)
	cfg := *s.cfg()
	cfg.Services = []config.Service{{
		Name:    "alpha",
		Domains: []string{"alpha.example.test"},
		Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:9001", MaintenancePage: "<h1>back soon</h1>"},
	}}
	s.config.Store(&cfg)

	errorsDir := cfg.HAProxyErrorsDir()
	if errorsDir != filepath.Join(dir, "errors") {
		t.Fatalf("errors dir is %s, not beside haproxy.cfg", errorsDir)
	}
	if err := os.MkdirAll(errorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// What a real gateway has in there: the distribution's pages, and one
	// maintenance page left over from a service that no longer wants one.
	for name, body := range map[string]string{
		"400.http":      "HTTP/1.0 400\r\n\r\nvanilla\n",
		"beta_503.http": "HTTP/1.0 503\r\n\r\nstale\n",
	} {
		if err := os.WriteFile(filepath.Join(errorsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	d, _ := servedDesired(t, s)
	var listed []string
	for _, f := range d.HAProxy.Files {
		listed = append(listed, f.Path)
	}
	alpha := filepath.Join(errorsDir, "alpha_503.http")
	if !slices.Contains(listed, alpha) {
		t.Fatalf("the live maintenance page is not in the payload: %v", listed)
	}
	if len(d.HAProxy.Dirs) != 1 || d.HAProxy.Dirs[0].Path != errorsDir {
		t.Fatalf("the errors directory is not claimed: %+v", d.HAProxy.Dirs)
	}

	plan := agent.Compute(&d, agent.NewSystemObserver().Observe(&d))
	var removed []string
	for _, c := range plan.Changes {
		if c.Kind == agent.KindRemove {
			removed = append(removed, c.Target)
		}
	}
	want := []string{filepath.Join(errorsDir, "beta_503.http")}
	if !slices.Equal(removed, want) {
		t.Fatalf("the plan would remove %v, want exactly %v", removed, want)
	}
}

// A certificate bundle crosses; the issuance material does not.
func TestCertSectionCarriesTheServedBundleAndNothingElse(t *testing.T) {
	s, _ := agentTestServer(t)
	certDir, bundlePath := withCerts(t, s)

	d, body := servedDesired(t, s)
	if d.Certs == nil {
		t.Fatal("no cert section; the agent cannot own an edge it is never told about")
	}
	if d.Certs.Dir != certDir {
		t.Fatalf("section names %s, not the cert store", d.Certs.Dir)
	}
	if len(d.Certs.Files) != 1 || d.Certs.Files[0].Path != bundlePath {
		t.Fatalf("want exactly the served bundle, got %+v", d.Certs.Files)
	}
	if !d.Certs.Files[0].Secret {
		t.Fatal("a bundle carrying a private key crossed without the Secret flag")
	}
	if d.Certs.Files[0].Mode != 0o600 {
		t.Fatalf("a bundle must land 0600, payload says %04o", d.Certs.Files[0].Mode)
	}

	// NOT the issuance record, NOT the account key, NOT a provider credential.
	// The whole payload is searched, not just the cert section: a leak through
	// some other section would be the same leak.
	for _, forbidden := range []string{
		s.cfg().SSLCertDir + "/live",
		s.cfg().SSLCertDir + "/accounts",
		fakeAccountKeyBody,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the payload carries %q — the agent gets issued material, not the power to issue", forbidden)
		}
	}

	// Off means NO SECTION, not an empty one.
	off := *s.cfg()
	off.SSLEnabled = false
	s.config.Store(&off)
	if d, _ := servedDesired(t, s); d.Certs != nil {
		t.Fatalf("SSL is off and a cert section was served anyway: %+v", d.Certs)
	}
}

// Layer one, for certificates: the flag is FORCED, not read off the wire.
// The mirror of TestTheSecretFlagIsForcedNotTrusted, on the other forced
// section — same tampering, same reconcile, same requirement.
func TestTheCertSecretFlagIsForcedNotTrusted(t *testing.T) {
	s, _ := agentTestServer(t)
	_, bundlePath := withCerts(t, s)

	d, _ := servedDesired(t, s)
	d.Certs.Files[0].Secret = false // the producer "forgot"

	obs := agent.Observed{Files: map[string]agent.FileState{
		bundlePath: {Exists: true, Contents: "-----BEGIN CERTIFICATE-----\nb2xk\n-----END CERTIFICATE-----\n"},
	}}
	report := agent.Report(agent.Compute(&d, obs))

	if strings.Contains(report, fakeCertKeyBody) {
		t.Fatalf("a key body reached the report with Secret cleared; the flag is being trusted:\n%s", report)
	}
	if !strings.Contains(report, "key material") {
		t.Fatalf("the cert change was not described as secret, so the flag was not forced:\n%s", report)
	}
}

// Layer two, for certificates, proven INDEPENDENT of layer one: the bytes hz
// actually served, in a section that gets no forcing at all.
func TestCertRedactionHoldsWithTheSecretFlagOutOfTheWay(t *testing.T) {
	s, _ := agentTestServer(t)
	withCerts(t, s)

	d, _ := servedDesired(t, s)
	served := d.Certs.Files[0].Contents
	if !strings.Contains(served, fakeCertKeyBody) {
		t.Fatal("fixture is not carrying the key body, so this test would pass for the wrong reason")
	}

	unforced := &agent.Desired{
		Machine: d.Machine,
		HAProxy: &agent.HAProxySection{
			ConfigPath: "/etc/haproxy/haproxy.cfg",
			Files:      []agent.File{{Path: "/etc/haproxy/haproxy.cfg", Contents: served}},
		},
	}
	obs := agent.Observed{Files: map[string]agent.FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, Contents: "global\n"},
	}}
	report := agent.Report(agent.Compute(unforced, obs))

	if strings.Contains(report, fakeCertKeyBody) || strings.Contains(report, "BEGIN PRIVATE KEY") {
		t.Fatalf("the key survived into a report with no Secret flag anywhere:\n%s", report)
	}
	if !strings.Contains(report, "[redacted") {
		t.Fatalf("nothing was redacted, so the line never reached the redactor:\n%s", report)
	}
}

// And the endpoint's own answers: a bundle may cross to the machine it belongs
// to, never into a refusal or another machine's payload.
func TestNoCertMaterialReachesARefusalOrAnotherMachine(t *testing.T) {
	s, _ := agentTestServer(t)
	withCerts(t, s)

	otherSecret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("some-other-box", otherSecret); err != nil {
		t.Fatal(err)
	}

	anon := httptest.NewRecorder()
	s.handleAgentDesired(anon, httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil))

	for name, w := range map[string]*httptest.ResponseRecorder{
		"an anonymous caller":     anon,
		"a wrong credential":      agentGETWith(t, s, "not-the-one", ""),
		"another machine's agent": agentGETWith(t, s, otherSecret, ""),
	} {
		if w.Code == http.StatusOK {
			t.Fatalf("%s was served the payload (status %d)", name, w.Code)
		}
		for _, needle := range []string{fakeCertKeyBody, "BEGIN PRIVATE KEY"} {
			if strings.Contains(w.Body.String(), needle) {
				t.Fatalf("%s: cert material in a %d body", name, w.Code)
			}
		}
	}
}

// withCerts gives the test server a certificate store with one served bundle
// in it, plus an issuance tree beside it that must NOT cross.
//
// Shape-correct and not a key: the bodies are base64 alphabets, which is what
// a PEM body looks like to every parser and every redactor in the path.
func withCerts(t *testing.T, s *Server) (certDir, bundlePath string) {
	t.Helper()
	root := t.TempDir()
	certDir = filepath.Join(root, "certs")
	leRoot := filepath.Join(root, "letsencrypt")
	for _, d := range []string{certDir, filepath.Join(leRoot, "live", "edge.example.test"), filepath.Join(leRoot, "accounts")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bundlePath = filepath.Join(certDir, "edge.example.test.pem")
	if err := os.WriteFile(bundlePath, []byte(fakeBundlePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	// The issuance record and the account key, on disk, in the directory the
	// config names. Nothing may lift them into the payload.
	if err := os.WriteFile(filepath.Join(leRoot, "live", "edge.example.test", "privkey.pem"), []byte(fakeBundlePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leRoot, "accounts", "account.key"), []byte(fakeAccountKeyBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := *s.cfg()
	cfg.SSLEnabled = true
	cfg.SSLHAProxyCertDir = certDir
	cfg.SSLCertDir = leRoot
	s.config.Store(&cfg)
	return certDir, bundlePath
}

const (
	fakeCertKeyBody    = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2"
	fakeAccountKeyBody = "YWNjb3VudC1rZXktdGhhdC1tdXN0LW5ldmVyLWNyb3NzLXRvLWFuLWFnZW50Cg"
	fakeBundlePEM      = "-----BEGIN CERTIFICATE-----\n" + fakeCertKeyBody + "\n-----END CERTIFICATE-----\n" +
		"-----BEGIN PRIVATE KEY-----\n" + fakeCertKeyBody + "\n-----END PRIVATE KEY-----\n"
)

// The endpoint is a READ. Serving it must not apply anything — that is what
// makes installing the agent on the live gateway a no-op.
func TestAgentDesiredWritesNothing(t *testing.T) {
	s, dir := agentTestServer(t)
	agentGET(t, s, "")

	// The directory the config names for haproxy.cfg, dnsmasq.conf and the
	// records file. Serving the desired state renders all three and must
	// create none of them.
	entries, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	// newTestServer writes config.json into its own temp dir, not this one.
	if len(entries) != 0 {
		t.Fatalf("serving the desired state created %v", entries)
	}
}
