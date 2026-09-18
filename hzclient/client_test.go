package hzclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// Tests run against an httptest.Server. This repo uses real things: a fake HTTP
// server exercises the headers, the status handling and the redirect policy,
// and a mocking framework would only assert that the code calls the methods the
// code calls.

// newTestClient wires a Client to a handler, with the version range hz really
// advertises.
func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hzapi.Advertise(w.Header())
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{BaseURL: srv.URL, Token: "tok-123"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNewRefusesOptionsThatCannotWork(t *testing.T) {
	for name, opts := range map[string]Options{
		"no base URL": {Token: "t"},
		"no token":    {BaseURL: "https://hz.internal"},
		"no scheme":   {BaseURL: "hz.internal:8080", Token: "t"},
		"odd scheme":  {BaseURL: "ftp://hz.internal", Token: "t"},
		"no host":     {BaseURL: "https://", Token: "t"},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("New with %s was accepted", name)
		}
	}
}

// Every request carries the token and the version this build compiled against.
// The version header is the whole point of hzapi landing first: a linked
// library is whatever a consumer built months ago, and without this a skew has
// no signal at all.
func TestEveryRequestDeclaresTokenAndVersion(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	})

	for _, call := range allCalls(c) {
		got = nil
		_ = call.run(context.Background())
		if got == nil {
			t.Fatalf("%s made no request", call.name)
		}
		if h := got.Get("Authorization"); h != "Bearer tok-123" {
			t.Errorf("%s sent Authorization %q", call.name, h)
		}
		if h := got.Get(hzapi.HeaderVersion); h != strconv.Itoa(hzapi.Version) {
			t.Errorf("%s sent %s %q, want %d", call.name, hzapi.HeaderVersion, h, hzapi.Version)
		}
	}
}

// The one-time code travels as a header. A query parameter would land in access
// logs and shell history, which is the one place a second factor must not be.
func TestOTPTravelsAsAHeaderAndOnlyWhenSet(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		hzapi.Advertise(w.Header())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	withOTP, err := New(Options{BaseURL: srv.URL, Token: "t", OTP: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withOTP.DeployStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("X-HZ-OTP") != "123456" {
		t.Errorf("X-HZ-OTP = %q", got.Header.Get("X-HZ-OTP"))
	}
	if strings.Contains(got.URL.RawQuery, "123456") {
		t.Errorf("the code reached the query string: %q", got.URL.RawQuery)
	}

	without, err := New(Options{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := without.DeployStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Header["X-Hz-Otp"]; ok {
		t.Error("an empty OTP was sent as an empty header")
	}
}

// Exit codes and text were the old interface. A caller must be able to branch.
func TestErrorsAreTypedNotText(t *testing.T) {
	cases := []struct {
		code int
		is   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusNotFound, ErrNotFound},
	}
	for _, tc := range cases {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", tc.code)
		})
		_, err := c.DeployStatus(context.Background())
		if !errors.Is(err, tc.is) {
			t.Errorf("status %d gave %v, want it to be %v", tc.code, err, tc.is)
		}
		var se *StatusError
		if !errors.As(err, &se) || se.Code != tc.code {
			t.Errorf("status %d did not carry a *StatusError with the code: %v", tc.code, err)
		}
	}

	// A 500 stays itself: guessing at what one means helps nobody.
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := c.DeployStatus(context.Background())
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
		t.Errorf("a 500 was classified as something else: %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 || !strings.Contains(se.Body, "boom") {
		t.Errorf("a 500 lost its body: %v", err)
	}
}

// A dead endpoint is a transport failure, distinguishable from anything hz
// said — because hz said nothing.
func TestTransportFailureIsItsOwnKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing is listening now
	c, err := New(Options{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeployStatus(context.Background())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("a closed server gave %v, want ErrTransport", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Errorf("the underlying *url.Error was lost: %v", err)
	}
}

// A version refusal must arrive as *hzapi.Mismatch, read from the range hz
// advertises on every response rather than scraped out of the prose.
func TestVersionMismatchIsReadableFromTheRefusal(t *testing.T) {
	// hz is newer: it no longer serves anything as old as this build.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(hzapi.HeaderVersion, strconv.Itoa(hzapi.Version+5))
		w.Header().Set(hzapi.HeaderMin, strconv.Itoa(hzapi.Version+3))
		http.Error(w, `{"error":"some sentence"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeployStatus(context.Background())
	var m *hzapi.Mismatch
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want a *hzapi.Mismatch", err)
	}
	if m.Client != hzapi.Version || m.ServerMin != hzapi.Version+3 || m.ServerCur != hzapi.Version+5 {
		t.Errorf("mismatch carried %+v, want this build against [%d,%d]", m, hzapi.Version+3, hzapi.Version+5)
	}
	if !m.TooOld {
		t.Error("a build below the server's floor is not marked TooOld")
	}
}

// The other direction: a client newer than the server. hz refuses it too, and
// the client must be able to say which side to move.
func TestVersionMismatchTheOtherWay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(hzapi.HeaderVersion, strconv.Itoa(hzapi.Version-1))
		w.Header().Set(hzapi.HeaderMin, "1")
		http.Error(w, "refused", http.StatusBadRequest)
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Token: "t"})
	_, err := c.DeployStatus(context.Background())
	var m *hzapi.Mismatch
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want a *hzapi.Mismatch", err)
	}
	if m.TooOld {
		t.Error("a build above the server's ceiling is marked TooOld")
	}
}

// A 400 that is NOT a version problem must stay a plain status error. Reporting
// every refusal as a mismatch would bury the real reason.
func TestAnOrdinaryBadRequestIsNotAMismatch(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "ip is required", http.StatusBadRequest)
	})
	_, err := c.DeployStatus(context.Background())
	var m *hzapi.Mismatch
	if errors.As(err, &m) {
		t.Fatalf("an ordinary 400 was reported as a version mismatch: %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || !strings.Contains(se.Body, "ip is required") {
		t.Fatalf("got %v, want the server's own message", err)
	}
}

// A server too old to advertise a range gets no synthesised mismatch.
func TestNoVersionHeadersMeansNoMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "old", http.StatusBadRequest)
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Token: "t"})
	_, err := c.DeployStatus(context.Background())
	var m *hzapi.Mismatch
	if errors.As(err, &m) {
		t.Fatalf("a server with no version headers produced %v", m)
	}
}

// --- The redirect policy --------------------------------------------------
//
// The script defended against curl refusing to resend Authorization across a
// scheme change. Go's rule is different, so the policy here is re-derived
// rather than translated. The next two tests VERIFY Go's actual behaviour
// against real servers, because the whole policy rests on it: if Go ever
// changes, these fail and say so rather than the policy quietly becoming
// wrong.

// authWatcher records whether an Authorization header reached it.
func authWatcher(saw *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*saw = r.Header.Get("Authorization")
		hzapi.Advertise(w.Header())
		_, _ = w.Write([]byte(`{}`))
	}
}

// Go strips sensitive headers on a HOST change, not a scheme change. Both
// halves are proven here: a bearer token is forwarded from http to https, AND
// from https back down to http, on the same host. The second is the leak this
// package's policy exists to close — Go will happily put the token on the wire
// in cleartext.
func TestGoForwardsAuthAcrossASchemeChange(t *testing.T) {
	// http -> https, the upgrade the script could only refuse to run against.
	t.Run("upgrade", func(t *testing.T) {
		var saw string
		tlsSrv := httptest.NewTLSServer(authWatcher(&saw))
		defer tlsSrv.Close()
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, tlsSrv.URL+"/api/deploy/status", http.StatusMovedPermanently)
		}))
		defer plain.Close()

		hc := tlsSrv.Client() // trusts the test cert; DEFAULT redirect policy
		req, _ := http.NewRequest(http.MethodGet, plain.URL+"/api/deploy/status", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("http->https: %v", err)
		}
		_ = resp.Body.Close()
		if saw != "Bearer secret" {
			t.Fatalf("Go did NOT forward Authorization http->https on the same host (saw %q). "+
				"checkRedirect is derived from the opposite behaviour; re-read it.", saw)
		}
	})

	// https -> http, the downgrade. Same host, so Go keeps the header and the
	// token goes out in cleartext. This is the leak the policy closes.
	t.Run("downgrade", func(t *testing.T) {
		var saw string
		plain := httptest.NewServer(authWatcher(&saw))
		defer plain.Close()
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/api/deploy/status", http.StatusMovedPermanently)
		}))
		defer tlsSrv.Close()

		hc := tlsSrv.Client()
		req, _ := http.NewRequest(http.MethodGet, tlsSrv.URL+"/api/deploy/status", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("https->http: %v", err)
		}
		_ = resp.Body.Close()
		if saw != "Bearer secret" {
			t.Skipf("Go no longer forwards Authorization https->http (saw %q); "+
				"the downgrade rule is now redundant rather than load-bearing", saw)
		}
	})
}

// Rule 3, the allow: http to https on the same host is the upgrade a control
// plane behind a redirect actually performs, and the one the script could only
// refuse to run against.
func TestRedirectAllowsHTTPToHTTPSOnTheSameHost(t *testing.T) {
	var saw string
	tlsSrv := httptest.NewTLSServer(authWatcher(&saw))
	defer tlsSrv.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer plain.Close()

	c, err := New(Options{BaseURL: plain.URL, Token: "tok-123", HTTP: tlsSrv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeployStatus(context.Background()); err != nil {
		t.Fatalf("the upgrade was refused: %v", err)
	}
	if saw != "Bearer tok-123" {
		t.Fatalf("the token did not survive the upgrade: %q", saw)
	}
}

// Rule 2, the refusal that matters: an https to http downgrade would put the
// token on the wire in cleartext, and Go does not stop it.
func TestRedirectRefusesAnHTTPSToHTTPDowngrade(t *testing.T) {
	var saw string
	plain := httptest.NewServer(authWatcher(&saw))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer tlsSrv.Close()

	c, err := New(Options{BaseURL: tlsSrv.URL, Token: "tok-123", HTTP: tlsSrv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeployStatus(context.Background())
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("the downgrade was followed or failed otherwise: %v", err)
	}
	if saw != "" {
		t.Fatalf("the token reached the cleartext endpoint anyway: %q", saw)
	}
	for _, want := range []string{"https", "http", "cleartext"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	// It is still a transport failure: nothing was answered.
	if !errors.Is(err, ErrTransport) {
		t.Errorf("a refused redirect is not reported as a transport failure: %v", err)
	}
}

// Rule 1: a host change is refused outright. Go permits foo.com to sub.foo.com;
// a token-bearing client must not, because a subdomain is a different party.
func TestRedirectRefusesAHostChange(t *testing.T) {
	var saw string
	other := httptest.NewServer(authWatcher(&saw))
	defer other.Close()
	// Same port, different hostname spelling: 127.0.0.1 and localhost are the
	// same machine and different hosts, which is exactly the case Go's rule
	// would wave through if they shared a parent domain.
	_, port, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")
	elsewhere := "http://localhost:" + port

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer origin.Close()

	c, err := New(Options{BaseURL: origin.URL, Token: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeployStatus(context.Background())
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("a host change was followed: %v", err)
	}
	if saw != "" {
		t.Fatalf("the token reached the other host: %q", saw)
	}
	if !strings.Contains(err.Error(), "localhost") {
		t.Errorf("the refusal does not name the host it refused: %v", err)
	}
}

// A redirect chain must not hang. Replacing CheckRedirect replaces Go's own
// cap, so this package needs its own.
func TestRedirectChainIsBounded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	c, err := New(Options{BaseURL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeployStatus(context.Background())
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("an endless redirect loop gave %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxRedirects)) {
		t.Errorf("the refusal does not say how many hops it allowed: %v", err)
	}
}

// A caller's http.Client is copied, not mutated: it may be shared with code
// that never asked for this policy. And the policy cannot be dropped by
// changing that client afterwards.
func TestTheCallersHTTPClientIsCopiedNotMutated(t *testing.T) {
	caller := &http.Client{}
	c, err := New(Options{BaseURL: "https://hz.internal", Token: "t", HTTP: caller})
	if err != nil {
		t.Fatal(err)
	}
	if caller.CheckRedirect != nil {
		t.Error("New installed its redirect policy on the caller's own client")
	}
	if c.http == caller {
		t.Error("New kept the caller's client rather than a copy")
	}
	if c.http.CheckRedirect == nil {
		t.Error("the copy carries no redirect policy")
	}
}

// --- helpers --------------------------------------------------------------

type namedCall struct {
	name string
	run  func(context.Context) error
}

// allCalls is every verb, so a header test covers the whole surface rather than
// whichever one was written first.
func allCalls(c *Client) []namedCall {
	return []namedCall{
		{"DeployStatus", func(ctx context.Context) error { _, err := c.DeployStatus(ctx); return err }},
		{"SetSlotState", func(ctx context.Context) error {
			_, err := c.SetSlotState(ctx, SlotNext, StateUp)
			return err
		}},
		{"DeploySwap", func(ctx context.Context) error { _, err := c.DeploySwap(ctx); return err }},
		{"MaintPageSet", func(ctx context.Context) error { _, err := c.MaintPageSet(ctx, "<h1>brb</h1>"); return err }},
		{"MaintPageClear", func(ctx context.Context) error { _, err := c.MaintPageClear(ctx); return err }},
		{"BanAdd", func(ctx context.Context) error { return c.BanAdd(ctx, "1.2.3.4", 0, "spam") }},
		{"BanRemove", func(ctx context.Context) error { return c.BanRemove(ctx, "1.2.3.4") }},
		{"BanList", func(ctx context.Context) error { _, err := c.BanList(ctx); return err }},
		{"SiteRollback", func(ctx context.Context) error { _, err := c.SiteRollback(ctx); return err }},
		{"SiteReleases", func(ctx context.Context) error { _, err := c.SiteReleases(ctx); return err }},
		{"RollingPhase", func(ctx context.Context) error { _, err := c.RollingPhase(ctx); return err }},
	}
}
