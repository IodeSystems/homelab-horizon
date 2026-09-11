package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
	"github.com/iodesystems/homelab-horizon/internal/server/hzbin"
)

// asAdmin builds a request carrying the admin session cookie.
func asAdmin(s *Server, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	return r
}

// postRemote runs one handler and returns the recorder.
func postRemote(t *testing.T, s *Server, h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, asAdmin(s, http.MethodPost, path, body))
	return w
}

func listRemotes(t *testing.T, s *Server) []apitypes.RemoteProbeResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIRemotes(w, asAdmin(s, http.MethodGet, "/api/v1/checks/remotes", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.RemoteProbeResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRemoteAddListDelete(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps-nyc","url":"https://198.51.100.7:8443/","token":"sekret","enabled":true,"poll":30}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add returned %d: %s", w.Code, w.Body.String())
	}

	got := listRemotes(t, s)
	if len(got) != 1 {
		t.Fatalf("expected 1 vantage, got %d", len(got))
	}
	if got[0].Name != "vps-nyc" || got[0].Poll != 30 || !got[0].Enabled {
		t.Fatalf("stored entry = %+v", got[0])
	}
	// The trailing slash is trimmed, so the client's URL join stays correct.
	if got[0].URL != "https://198.51.100.7:8443" {
		t.Fatalf("URL = %q, expected the trailing slash trimmed", got[0].URL)
	}
	if !got[0].HasToken {
		t.Fatal("hasToken should be true")
	}
	// Configured but never polled reads differently from configured and broken.
	if got[0].Polled {
		t.Fatal("a vantage that has never been polled must not report polled")
	}

	w = postRemote(t, s, s.handleAPIRemoteDelete, "/api/v1/checks/remotes/delete", `{"name":"vps-nyc"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", w.Code, w.Body.String())
	}
	if len(listRemotes(t, s)) != 0 {
		t.Fatal("vantage survived the delete")
	}
}

// The token is write-only. It must not appear anywhere in the list response,
// which lands in browser caches, screenshots and bug reports.
func TestRemoteTokenIsNeverReturned(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps","url":"https://198.51.100.7:8443","token":"super-secret-value","enabled":true}`)

	w := httptest.NewRecorder()
	s.handleAPIRemotes(w, asAdmin(s, http.MethodGet, "/api/v1/checks/remotes", ""))
	if strings.Contains(w.Body.String(), "super-secret-value") {
		t.Fatalf("the token leaked into the list response: %s", w.Body.String())
	}
}

func TestRemoteAddValidation(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	cases := []struct{ name, body string }{
		{"no name", `{"url":"https://h:8443","token":"t"}`},
		// Only pull needs an address; a pushing agent dials hz.
		{"pull with no url", `{"name":"a","token":"t","mode":"pull"}`},
		{"bad scheme for pull", `{"name":"a","url":"ftp://h","token":"t","mode":"pull"}`},
		{"no token", `{"name":"a","url":"https://h:8443"}`},
		{"url with no host", `{"name":"a","url":"https://","token":"t","mode":"pull"}`},
		// The name becomes a check-row prefix, so a colon would make the row
		// ambiguous against another vantage's.
		{"colon in name", `{"name":"a:b","url":"https://h:8443","token":"t"}`},
		{"space in name", `{"name":"a b","url":"https://h:8443","token":"t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestRemoteAddRejectsDuplicateName(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	body := `{"name":"vps","url":"https://198.51.100.7:8443","token":"t","enabled":true}`
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add", body)
	w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate add returned %d, want 400", w.Code)
	}
}

// Editing a URL must not require re-typing a credential the UI never showed.
func TestRemoteUpdateKeepsStoredToken(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps","url":"https://198.51.100.7:8443","token":"keep-me","enabled":true}`)

	w := postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","url":"https://203.0.113.9:8443","enabled":true,"poll":15}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", w.Code, w.Body.String())
	}

	stored := s.cfg().RemoteProbes
	if len(stored) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(stored))
	}
	if stored[0].Token != "keep-me" {
		t.Fatalf("token = %q, expected the stored one to survive an edit", stored[0].Token)
	}
	if stored[0].URL != "https://203.0.113.9:8443" || stored[0].Poll != 15 {
		t.Fatalf("edit did not apply: %+v", stored[0])
	}
}

func TestRemoteUpdateReplacesTokenWhenGiven(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps","url":"https://198.51.100.7:8443","token":"old","enabled":true}`)
	postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","url":"https://198.51.100.7:8443","token":"new","enabled":true}`)

	if got := s.cfg().RemoteProbes[0].Token; got != "new" {
		t.Fatalf("token = %q, expected the supplied one to replace the stored one", got)
	}
}

func TestRemoteRename(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"old","url":"https://198.51.100.7:8443","token":"t","enabled":true}`)

	w := postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"oldName":"old","name":"new","url":"https://198.51.100.7:8443","enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename returned %d: %s", w.Code, w.Body.String())
	}
	stored := s.cfg().RemoteProbes
	if len(stored) != 1 || stored[0].Name != "new" || stored[0].Token != "t" {
		t.Fatalf("rename produced %+v", stored)
	}
}

func TestRemoteRenameOntoAnExistingNameIsRejected(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"a","url":"https://198.51.100.7:8443","token":"t","enabled":true}`)
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"b","url":"https://203.0.113.9:8443","token":"t","enabled":true}`)

	w := postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"oldName":"a","name":"b","url":"https://198.51.100.7:8443","enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("collision returned %d, want 400", w.Code)
	}
}

func TestRemoteUpdateAndDeleteOnMissingEntry(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w := postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"ghost","url":"https://198.51.100.7:8443","enabled":true}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("update of a missing vantage returned %d, want 404", w.Code)
	}
	w = postRemote(t, s, s.handleAPIRemoteDelete, "/api/v1/checks/remotes/delete", `{"name":"ghost"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete of a missing vantage returned %d, want 404", w.Code)
	}
}

// The test endpoint exists so a wrong token fails in the dialog rather than
// silently in the poll loop ten minutes later.
func TestRemoteTestAgainstALiveAgent(t *testing.T) {
	agent := probe.NewAgent("vps-nyc", "v-test", "right-token", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	s := newTestServer(t, &config.Config{})

	w := postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps-nyc","url":"`+srv.URL+`","token":"right-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("test returned %d: %s", w.Code, w.Body.String())
	}
	var out apitypes.RemoteProbeTestResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.AgentVantage != "vps-nyc" || out.AgentVersion != "v-test" {
		t.Fatalf("test result = %+v", out)
	}

	// A wrong token is a reported failure, not an HTTP error: the dialog needs
	// to render the reason.
	w = postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps-nyc","url":"`+srv.URL+`","token":"wrong-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("failed test returned %d, want 200 with ok:false", w.Code)
	}
	out = apitypes.RemoteProbeTestResp{}
	_ = json.NewDecoder(w.Body).Decode(&out)
	if out.OK || !strings.Contains(out.Error, "401") {
		t.Fatalf("expected a 401 reported in the body, got %+v", out)
	}
}

// Testing must not configure the agent: an operator poking at a URL should
// not install hz's target set on a host they have not saved.
func TestRemoteTestDoesNotInstallTargets(t *testing.T) {
	agent := probe.NewAgent("vps", "v-test", "tok", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	s := newTestServer(t, &config.Config{
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.com"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.1:80"}},
		},
	})
	postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps","url":"`+srv.URL+`","token":"tok"}`)

	if n := len(agent.Targets().Targets); n != 0 {
		t.Fatalf("a trial poll installed %d targets on an unsaved agent", n)
	}
}

func TestRemoteEndpointsRequireAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	handlers := map[string]http.HandlerFunc{
		"list":   s.handleAPIRemotes,
		"add":    s.handleAPIRemoteAdd,
		"update": s.handleAPIRemoteUpdate,
		"delete": s.handleAPIRemoteDelete,
		"test":   s.handleAPIRemoteTest,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			// No session cookie.
			h(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", w.Code)
			}
		})
	}
}

// End to end: a configured vantage, a live agent, and the poll loop actually
// running. Everything else in this file stubs the loop out; this proves the
// list endpoint reports what the loop learned, and that the agent ends up
// holding hz's targets without anyone pushing them.
func TestRemoteLiveStateReachesTheAPI(t *testing.T) {
	agent := probe.NewAgent("vps-nyc", "v-test", "tok", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	s := newTestServer(t, &config.Config{
		SSLEnabled: true,
		PublicIP:   "203.0.113.10",
		Services: []config.Service{
			{Name: "api", Domains: []string{"api.example.invalid"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.1:80"}},
		},
		RemoteProbes: []config.RemoteProbe{
			{Name: "vps-nyc", Mode: config.ProbeModePull, URL: srv.URL, Token: "tok", Enabled: true, Poll: 1, Probe: 1},
		},
	})
	// Reload starts the poll loop for the configured vantage.
	s.monitor.Reload(s.cfg())
	defer s.monitor.Stop()

	deadline := time.Now().Add(5 * time.Second)
	var got apitypes.RemoteProbeResp
	for time.Now().Before(deadline) {
		list := listRemotes(t, s)
		if len(list) == 1 && list[0].Polled {
			got = list[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !got.Polled {
		t.Fatal("the poll loop never reported a poll within 5s")
	}
	if !got.Reachable || got.LastError != "" {
		t.Fatalf("vantage should be reachable: %+v", got)
	}
	if got.AgentVantage != "vps-nyc" || got.AgentVersion != "v-test" {
		t.Fatalf("agent identity not reported: %+v", got)
	}
	// The handshake ran: hz named a version the agent did not hold, the agent
	// asked, hz sent it — with nobody pushing a target list anywhere.
	if got.TargetCount != 1 {
		t.Fatalf("agent holds %d targets, expected the one served domain", got.TargetCount)
	}
	if held := agent.Targets(); len(held.Targets) != 1 || held.Targets[0].Host != "api.example.invalid" {
		t.Fatalf("agent installed the wrong target set: %+v", held)
	}
	// Only public facts crossed the wire.
	if held := agent.Targets(); len(held.Targets[0].ExpectIPs) != 1 ||
		held.Targets[0].ExpectIPs[0] != "203.0.113.10" {
		t.Fatalf("expected the public IP alone, got %+v", held.Targets[0].ExpectIPs)
	}
}

// The handler tests above call handlers directly, which proves nothing about
// whether anything reaches them. This walks the real mux.
func TestRemoteRoutesAreRegistered(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	mux := s.setupRoutes()

	paths := []string{
		"/api/v1/checks/remotes",
		"/api/v1/checks/remotes/add",
		"/api/v1/checks/remotes/update",
		"/api/v1/checks/remotes/delete",
		"/api/v1/checks/remotes/test",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// Unauthenticated: 401 from the handler proves the route resolved
			// to it. A 404 would mean the mux never had it.
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s returned %d, want 401 (404 means the route is missing)", path, w.Code)
			}

			// Authenticated: it reaches the handler and gets a real answer.
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, asAdmin(s, http.MethodPost, path, `{"name":"x","url":"https://h:1","token":"t"}`))
			if w.Code == http.StatusNotFound || w.Code == http.StatusUnauthorized {
				t.Fatalf("%s returned %d for an admin request", path, w.Code)
			}
		})
	}
}

// Saving a vantage must not cost the history of unrelated checks. This is the
// server-side half of the narrow reload: the handler has to call it.
func TestRemoteSaveKeepsUnrelatedHistory(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"a","url":"http://127.0.0.1:1","token":"t","enabled":true,"poll":3600}`)

	// Stand in for a local check that has been running a while.
	s.monitor.SeedForTest("svc:api", "ping", "10.0.0.1:80")
	if len(s.monitor.GetHistory("svc:api")) != 1 {
		t.Fatal("seed did not take")
	}

	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"b","url":"http://127.0.0.1:2","token":"t","enabled":true,"poll":3600}`)

	if got := len(s.monitor.GetHistory("svc:api")); got != 1 {
		t.Fatalf("adding a vantage cleared an unrelated check's history: %d entries", got)
	}
}

// The whole point of hz minting the token: by the time the agent is running,
// hz already holds the credential, so nothing is copied back by hand.
func TestRemoteTokenMinting(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	mint := func() string {
		w := postRemote(t, s, s.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
		if w.Code != http.StatusOK {
			t.Fatalf("mint returned %d: %s", w.Code, w.Body.String())
		}
		var out apitypes.RemoteProbeTokenResp
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Token
	}

	a, b := mint(), mint()
	if len(a) < 32 {
		t.Fatalf("token is only %d characters", len(a))
	}
	if a == b {
		t.Fatal("two mints returned the same token")
	}
	// Minting must not persist anything: a dialog opened and abandoned should
	// leave no credential behind.
	if len(s.cfg().RemoteProbes) != 0 {
		t.Fatal("minting a token created a vantage")
	}
}

// Testing an agent with no pin reports the certificate it presented, so the
// operator can adopt a self-signed one by looking at the fingerprint.
func TestRemoteTestReportsTheCertificateForPinning(t *testing.T) {
	agent := probe.NewAgent("vps-nyc", "v-test", "tok", "")
	srv := httptest.NewTLSServer(agent.Handler())
	defer srv.Close()

	s := newTestServer(t, &config.Config{})
	w := postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps-nyc","url":"`+srv.URL+`","token":"tok"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("test returned %d: %s", w.Code, w.Body.String())
	}

	var out apitypes.RemoteProbeTestResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("an observed self-signed agent should still answer: %+v", out)
	}
	if out.CertSHA256 == "" {
		t.Fatal("no fingerprint reported, so there is nothing to pin")
	}
	if out.CertTrusted {
		t.Fatal("a self-signed certificate must not be reported as trusted")
	}

	// Saving that fingerprint must then work as a pin.
	w = postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps-nyc","url":"`+srv.URL+`","token":"tok","pinSha256":"`+out.CertSHA256+`"}`)
	var pinned apitypes.RemoteProbeTestResp
	_ = json.NewDecoder(w.Body).Decode(&pinned)
	if !pinned.OK {
		t.Fatalf("the reported fingerprint does not work as a pin: %+v", pinned)
	}

	// And a wrong pin must fail, or the pin is decoration.
	w = postRemote(t, s, s.handleAPIRemoteTest, "/api/v1/checks/remotes/test",
		`{"name":"vps-nyc","url":"`+srv.URL+`","token":"tok","pinSha256":"`+strings.Repeat("ab", 32)+`"}`)
	var wrong apitypes.RemoteProbeTestResp
	_ = json.NewDecoder(w.Body).Decode(&wrong)
	if wrong.OK {
		t.Fatal("a wrong pin was accepted")
	}
}

// The installer is fetched by a bare host with no session, so it is not
// admin-gated — but it must carry no secret, and must have every placeholder
// filled in.
func TestProbeInstallScriptIsRenderedForTheCaller(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	mux := s.setupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/admin/hz-probe/install", nil)
	req.Host = "hz.example.com"
	req.RemoteAddr = "198.51.100.7:51234"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("installer returned %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "@@") {
		t.Fatalf("unsubstituted placeholder left in the script: %s", body[:200])
	}
	if !strings.Contains(body, "http://hz.example.com") {
		t.Fatal("the script does not point back at this instance")
	}
	// The caller's own address, so the script can print the URL to paste.
	if !strings.Contains(body, "198.51.100.7") {
		t.Fatal("the script does not carry the caller's address")
	}
	// It must not carry a credential: the token is supplied by whoever runs it.
	if strings.Contains(body, s.adminToken) {
		t.Fatal("the installer leaked the admin token")
	}
}

func TestProbeBinaryRoute(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	mux := s.setupRoutes()

	// Authorised, so this exercises key validation rather than the grant.
	authed := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+s.adminToken)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		// Three segments would let "hz" serve an "hz-probe-…" file.
		{"/admin/hz-probe/bin/probe-linux-amd64", http.StatusBadRequest},
		{"/admin/hz-probe/bin/nonsense", http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := authed(tc.path)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			// Unauthorised, a malformed key must not be distinguishable from
			// a well-formed one — the grant check comes first on purpose.
			anon := httptest.NewRecorder()
			mux.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if anon.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous got %d, want 401", anon.Code)
			}
		})
	}

	// A well-formed key resolves to a binary when one is embedded, and to a
	// 404 that says why when the build has none. Both are correct; which one
	// depends on the build tag, so assert against what this build holds.
	w := authed("/admin/hz-probe/bin/linux-amd64")
	if len(hzbin.Available(hzbin.ToolProbe)) == 0 {
		if w.Code != http.StatusNotFound {
			t.Fatalf("got %d with no embedded binaries, want 404", w.Code)
		}
		if !strings.Contains(w.Body.String(), "hzembed") {
			t.Fatalf("the 404 should say the build has no embedded clients: %s", w.Body.String())
		}
		return
	}
	if w.Code != http.StatusOK || w.Body.Len() < 1000 {
		t.Fatalf("embedded build served %d, %d bytes", w.Code, w.Body.Len())
	}
}

// A push vantage is valid with no URL at all — nothing dials it.
func TestRemoteAddPushNeedsNoURL(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"pushed","token":"t","enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	stored := s.cfg().RemoteProbes
	if len(stored) != 1 || !stored[0].IsPush() {
		t.Fatalf("expected a push entry, got %+v", stored)
	}

	// And the listing says which mode it is, so the UI can render the right
	// fields rather than an empty URL row.
	got := listRemotes(t, s)
	if got[0].Mode != "push" {
		t.Fatalf("mode = %q, want push", got[0].Mode)
	}
}
