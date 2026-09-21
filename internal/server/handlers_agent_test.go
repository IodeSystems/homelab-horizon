package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// hz does not serve the WireGuard section yet, and the reason is that wg0.conf
// carries the machine's private key while this endpoint is still gated on an
// ADMIN credential rather than a per-machine agent one (item 16). If somebody
// wires it up, this test is where they have to think about that first.
func TestNoKeyMaterialCrossesThisEndpoint(t *testing.T) {
	s, _ := agentTestServer(t)
	body := agentGET(t, s, "").Body.String()

	var d agent.Desired
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.WireGuard != nil {
		t.Fatal("hz served a WireGuard section; that file holds a private key and this route is admin-gated, not agent-gated")
	}
	// Named without the word "PrivateKey" in it, and deliberately: t.TempDir()
	// puts the TEST NAME in every path in this payload, so the first version of
	// this check failed on its own name. An assertion that can match itself is
	// not measuring the thing it claims to.
	for _, needle := range []string{"PrivateKey", "PresharedKey"} {
		if i := strings.Index(body, needle); i >= 0 {
			t.Fatalf("the payload mentions %s at offset %d", needle, i)
		}
	}
}

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
