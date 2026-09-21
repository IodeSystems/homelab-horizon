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
		{"machines", apitypes.CMPathMachines, nil},
		// Key custody. Registered from the same constants the CLI reads, for
		// the reason this whole file exists — and reachability matters more
		// here than anywhere: an unrouted recovery endpoint means `hz cm key
		// new` cannot wrap, which would be discovered as a missing wrap during
		// a recovery rather than as a 404 now.
		{"recovery", apitypes.CMPathRecovery, nil},
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

// The machine routes are the pair the re-enrol refusal points at, and the pair
// most able to repeat the bug above: DELETE on a subtree the mux would answer
// for even when no handler reads it, plus a query parameter (confirm) that two
// sides have to spell the same way.
//
// Driven through the real mux with the real constants, so a DELETE that lands
// on the collection handler, a subtree pattern that was never registered, or a
// renamed confirm parameter all fail HERE rather than on a box an operator is
// trying to rebuild.
func TestCMMachineRoutesAreReachableAsBuilt(t *testing.T) {
	s, admin := cmServer(t)
	mux := s.setupRoutes()
	cmRegister(t, s, "box-1", "prod", "redline", "app")

	call := func(t *testing.T, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(admin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusNotFound && !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s %s is not registered: %s", method, path, strings.TrimSpace(w.Body.String()))
		}
		return w
	}

	t.Run("list", func(t *testing.T) {
		w := call(t, http.MethodGet, apitypes.CMPathMachines)
		var rows []apitypes.CMMachineResp
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		if len(rows) != 1 || rows[0].Name != "box-1" {
			t.Fatalf("want one machine box-1, got %+v", rows)
		}
		// The preview an operator acts on has to carry the registration, or
		// `hz cm remove` cannot say what it is about to destroy.
		if len(rows[0].Registrations) != 1 {
			t.Errorf("machine listing carries no registrations: %+v", rows[0])
		}
	})

	t.Run("read by name", func(t *testing.T) {
		w := call(t, http.MethodGet, apitypes.CMPathMachines+"/box-1")
		if w.Code != http.StatusOK {
			t.Fatalf("GET by name = %d: %s", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})

	// The DELETE must reach the per-machine handler, not the collection one.
	// Without its own subtree pattern the mux would hand this to the listing
	// handler, which answers 405 — indistinguishable at a glance from a removal
	// that is simply not allowed.
	t.Run("delete reaches the machine handler", func(t *testing.T) {
		w := call(t, http.MethodDelete, apitypes.CMPathMachines+"/box-1")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("DELETE with no confirm = %d, want 400: %s", w.Code, strings.TrimSpace(w.Body.String()))
		}
		// And the refusal must name the parameter by the constant both sides
		// read, so a rename cannot leave the CLI sending a word hz ignores.
		if !strings.Contains(w.Body.String(), apitypes.CMQueryConfirm+"=") {
			t.Errorf("the refusal does not name %q: %s", apitypes.CMQueryConfirm, w.Body.String())
		}
	})

	t.Run("delete with confirm", func(t *testing.T) {
		q := url.Values{apitypes.CMQueryConfirm: {"box-1"}}
		w := call(t, http.MethodDelete, apitypes.CMPathMachines+"/box-1?"+q.Encode())
		if w.Code != http.StatusOK {
			t.Fatalf("DELETE = %d: %s", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})
}
