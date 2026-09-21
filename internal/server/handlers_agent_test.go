package server

import (
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

func agentGET(t *testing.T, s *Server, etag string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
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
