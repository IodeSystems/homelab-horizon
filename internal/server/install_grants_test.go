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
)

func TestInstallGrantLifecycle(t *testing.T) {
	g := newInstallGrants()

	if g.valid("never-issued") {
		t.Fatal("an unissued token must not be a grant")
	}
	if g.valid("") {
		t.Fatal("an empty token must never be a grant")
	}

	g.issue("tok-a")
	if !g.valid("tok-a") {
		t.Fatal("a freshly issued token should be redeemable")
	}
	// Redeemable more than once: a retried curl, or a second architecture,
	// should not need a new command.
	if !g.valid("tok-a") {
		t.Fatal("a grant should survive being used")
	}

	// Expiry.
	g.mu.Lock()
	g.issued["tok-a"] = time.Now().Add(-time.Second)
	g.mu.Unlock()
	if g.valid("tok-a") {
		t.Fatal("an expired grant must stop working")
	}
}

func TestInstallGrantsAreBounded(t *testing.T) {
	g := newInstallGrants()
	for i := 0; i < maxInstallGrants+50; i++ {
		g.issue(strings.Repeat("x", i%40) + string(rune('a'+i%26)) + string(rune(i)))
	}
	if n := g.count(); n > maxInstallGrants {
		t.Fatalf("%d live grants exceeds the %d cap", n, maxInstallGrants)
	}
	// The cap must not break minting — the newest is what somebody is about
	// to use.
	g.issue("newest")
	if !g.valid("newest") {
		t.Fatal("hitting the cap stopped new grants working")
	}
}

func kioskCfg() *config.Config {
	return &config.Config{KioskURL: "https://vpn.example.com"}
}

// The installer belongs on the vhost whose threat model already assumes
// anonymous public access, not on the admin hostname.
func TestInstallRoutesAreKioskOnly(t *testing.T) {
	s := newTestServer(t, kioskCfg())
	mux := s.setupRoutes()

	paths := []string{"/admin/hz-probe/install", "/admin/hz/install"}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			// On the admin hostname it simply does not exist.
			req := httptest.NewRequest(http.MethodGet, p, nil)
			req.Host = "hz.example.com"
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("admin vhost returned %d, want 404", w.Code)
			}

			// On the kiosk hostname it serves, with no credential.
			req = httptest.NewRequest(http.MethodGet, p, nil)
			req.Host = "vpn.example.com"
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("kiosk vhost returned %d, want 200", w.Code)
			}
			if strings.Contains(w.Body.String(), "@@") {
				t.Fatal("unsubstituted placeholder in the script")
			}
		})
	}
}

// A port on the Host header must not defeat the match.
func TestKioskVhostMatchIgnoresPort(t *testing.T) {
	s := newTestServer(t, kioskCfg())
	for _, host := range []string{"vpn.example.com", "vpn.example.com:8443", "VPN.EXAMPLE.COM", "vpn.example.com."} {
		if !s.onPublicVhost(host) {
			t.Fatalf("%q should match the kiosk host", host)
		}
	}
	for _, host := range []string{"hz.example.com", "evil.com", "vpn.example.com.evil.com"} {
		if s.onPublicVhost(host) {
			t.Fatalf("%q must not match the kiosk host", host)
		}
	}
}

// With no portal configured there is no second vhost to move to, so the
// feature has to keep working rather than silently 404.
func TestNoKioskConfiguredAllowsAnyHost(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	if !s.onPublicVhost("anything.example.com") {
		t.Fatal("with kiosk_url unset the installer must still be reachable")
	}
}

// The point of the change: the binary is no longer an anonymous download.
func TestBinaryNeedsAGrant(t *testing.T) {
	s := newTestServer(t, kioskCfg())
	mux := s.setupRoutes()

	get := func(path, bearer, query string) int {
		req := httptest.NewRequest(http.MethodGet, path+query, nil)
		req.Host = "vpn.example.com"
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	for _, p := range []string{"/admin/hz-probe/bin/linux-amd64", "/admin/hz/bin/linux-amd64"} {
		t.Run(p, func(t *testing.T) {
			if got := get(p, "", ""); got != http.StatusUnauthorized {
				t.Fatalf("anonymous download returned %d, want 401", got)
			}
			if got := get(p, "not-a-grant", ""); got != http.StatusUnauthorized {
				t.Fatalf("a bogus grant returned %d, want 401", got)
			}
			// The shared admin token counts, which is what keeps the hz CLI
			// install command working.
			if got := get(p, s.adminToken, ""); got == http.StatusUnauthorized {
				t.Fatal("the admin token should authorise the download")
			}
		})
	}

	// A minted vantage token is a grant, presented either way.
	w := postRemote(t, s, s.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
	var minted apitypes.RemoteProbeTokenResp
	if err := json.NewDecoder(w.Body).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	if got := get("/admin/hz-probe/bin/linux-amd64", minted.Token, ""); got == http.StatusUnauthorized {
		t.Fatal("a token hz just minted should authorise the download")
	}
	if got := get("/admin/hz-probe/bin/linux-amd64", "", "?grant="+minted.Token); got == http.StatusUnauthorized {
		t.Fatal("the grant should also be accepted as a query parameter")
	}
}

// Minting is what issues the grant; without that the download could not work
// from a bare VPS at all.
func TestMintingIssuesAGrant(t *testing.T) {
	s := newTestServer(t, kioskCfg())
	before := s.installGrants.count()

	w := postRemote(t, s, s.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
	var minted apitypes.RemoteProbeTokenResp
	_ = json.NewDecoder(w.Body).Decode(&minted)

	if s.installGrants.count() != before+1 {
		t.Fatal("minting a token did not issue an install grant")
	}
	if !s.installGrants.valid(minted.Token) {
		t.Fatal("the minted token is not redeemable")
	}
}

// Turning off the shared admin token must also stop it authorising downloads,
// or the switch is decorative.
func TestDisabledAdminTokenIsNotAGrant(t *testing.T) {
	cfg := kioskCfg()
	cfg.AdminTokenDisabled = true
	s := newTestServer(t, cfg)
	mux := s.setupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/admin/hz/bin/linux-amd64", nil)
	req.Host = "vpn.example.com"
	req.Header.Set("Authorization", "Bearer "+s.adminToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 — the disabled shared token must not authorise", w.Code)
	}
}

// The script stays anonymous on purpose: it is small, holds no secret, and
// the thing worth protecting is what it downloads next.
func TestInstallScriptCarriesTheGrantHeader(t *testing.T) {
	s := newTestServer(t, kioskCfg())
	mux := s.setupRoutes()

	for _, p := range []string{"/admin/hz-probe/install", "/admin/hz/install"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "vpn.example.com"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "Authorization: Bearer") {
			t.Fatalf("%s does not send a grant when fetching the binary", p)
		}
	}
}

// The command an admin copies must point at the vhost that serves the
// installer, not the admin origin they are looking at — otherwise every
// copied command is a 404.
func TestInstallBaseIsTheKioskOrigin(t *testing.T) {
	s := newTestServer(t, kioskCfg())

	w := postRemote(t, s, s.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
	var minted apitypes.RemoteProbeTokenResp
	if err := json.NewDecoder(w.Body).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	if minted.InstallBase != "https://vpn.example.com" {
		t.Fatalf("installBase = %q, want the kiosk origin", minted.InstallBase)
	}

	// With no kiosk configured the routes are served anywhere, so the
	// request origin is the right answer.
	plain := newTestServer(t, &config.Config{})
	w = postRemote(t, plain, plain.handleAPIRemoteToken, "/api/v1/checks/remotes/token", "{}")
	minted = apitypes.RemoteProbeTokenResp{}
	_ = json.NewDecoder(w.Body).Decode(&minted)
	if minted.InstallBase == "" {
		t.Fatal("installBase must never be empty — the UI builds a command from it")
	}

	// And whatever it returns must actually be the host the routes answer on.
	if !plain.onPublicVhost(strings.TrimPrefix(strings.TrimPrefix(minted.InstallBase, "https://"), "http://")) {
		t.Fatalf("installBase %q is not a host the install routes serve", minted.InstallBase)
	}
}
