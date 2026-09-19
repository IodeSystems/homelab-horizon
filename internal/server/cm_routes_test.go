package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Four config-manager endpoints were unreachable from the CLI and nothing
// caught it: `hz cm resolve` sent "environment=" where the handler read "env=",
// `hz cm key current` did the same for both GET and PUT, and `hz cm promote`
// sent "configId"/"to" to /promote while the handler read "config"/"target" at
// /promote/gate. Every one 400'd or 404'd for every user.
//
// Nothing failed, because both sides were only ever tested against themselves:
// the CLI against an httptest stub that answered whatever it happened to send,
// the handler against a request its own test constructed. Neither test could
// observe the disagreement, which is the shape to watch for — two tests, both
// green, one contract, broken.
//
// This test uses the REAL mux and the REAL parameter names, so a rename on one
// side is a failure here rather than a 400 discovered on a box.
func TestCMAdminRoutesAreReachableAsBuilt(t *testing.T) {
	s, admin := cmServer(t)
	mux := s.setupRoutes()

	cases := []struct {
		name string
		path string
		q    url.Values
	}{
		{"resolve", apitypes.CMPathResolve, url.Values{
			apitypes.CMQueryEnv:     {"prod"},
			apitypes.CMQueryApp:     {"redline"},
			apitypes.CMQueryRole:    {"app"},
			apitypes.CMQueryVersion: {"1.2.0"},
		}},
		{"promotion gate", apitypes.CMPathPromoteGate, url.Values{
			apitypes.CMQueryConfigID: {"cfg_nope"},
			apitypes.CMQueryTarget:   {"prod"},
		}},
		{"current key", apitypes.CMPathCurrentKey, url.Values{
			apitypes.CMQueryEnv:  {"prod"},
			apitypes.CMQueryApp:  {"redline"},
			apitypes.CMQueryRole: {"app"},
		}},
		{"configs", apitypes.CMPathConfigs, url.Values{
			apitypes.CMQueryEnv:  {"prod"},
			apitypes.CMQueryApp:  {"redline"},
			apitypes.CMQueryRole: {"app"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path+"?"+tc.q.Encode(), nil)
			r.AddCookie(admin)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)

			body := w.Body.String()

			// An UNROUTED path is ServeMux's own plain-text 404. A handler's own
			// 404 is JSON and means the request was understood and the thing
			// asked for is genuinely absent — which is the expected answer here,
			// since this server holds no configs.
			if w.Code == http.StatusNotFound && !strings.Contains(body, `"error"`) {
				t.Fatalf("path %q is not registered: %s", tc.path, strings.TrimSpace(body))
			}

			// 400 naming what it "required" means the handler saw NONE of the
			// parameters the CLI sent — the names disagree. That is the bug.
			if w.Code == http.StatusBadRequest && strings.Contains(body, "required") {
				t.Fatalf("handler did not see the parameters the CLI sends (%s): %s",
					tc.q.Encode(), strings.TrimSpace(body))
			}
		})
	}
}

// `hz cm approve` reads the registration before it does anything else, and the
// bare {id} route did not exist — so approval 404'd on its first call for every
// operator, on the command `hz cm pending` tells them to run.
//
// The suffixed forms all existed and were tested. Nothing tested the shape the
// CLI actually asks for, which is the same gap that made four other endpoints
// unreachable: a URL agreed in two places and enforced in neither.
func TestCMRegistrationBareReadIsRouted(t *testing.T) {
	s, admin := cmServer(t)
	mux := s.setupRoutes()
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")

	for _, path := range []string{
		"/api/v1/cm/registrations/" + box.resp.ID,
		"/api/v1/cm/registrations/" + box.resp.ID + "/public-key",
	} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.AddCookie(admin)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", path, w.Code, strings.TrimSpace(w.Body.String()))
			}
		})
	}

	// And the bare read must carry the fingerprint, since that is the whole
	// reason the approve path fetches it.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cm/registrations/"+box.resp.ID, nil)
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var got apitypes.CMRegistrationResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Fingerprint == "" {
		t.Error("the bare registration read carries no fingerprint; approve has nothing to compare")
	}
	if got.ID != box.resp.ID {
		t.Errorf("read returned registration %q, want %q", got.ID, box.resp.ID)
	}
}
