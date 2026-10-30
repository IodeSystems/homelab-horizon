package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `vpn add --vpn-admin` ran twice on the loadtest child (2026-10-07) and
// left carl-1 NOT an admin: the console called toggle-admin, which flips, so
// the second run undid the first. With `admin` set the call SETS the flag and
// is idempotent; without it the UI's chip still flips.
func TestToggleAdminWithAdminSetsIdempotently(t *testing.T) {
	s, _ := inviteServer(t)
	call := func(req apitypes.ToggleAdminReq) bool {
		t.Helper()
		raw, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		s.handleAPIToggleAdmin(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/peers/toggle-admin", string(raw)))
		var out apitypes.ToggleAdminResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("toggle-admin %+v: %d %s", req, w.Code, w.Body.String())
		}
		stored := false
		for _, a := range s.cfg().VPNAdmins {
			if a == req.Name {
				stored = true
			}
		}
		if stored != out.IsAdmin {
			t.Fatalf("answered isAdmin=%v, stored %v", out.IsAdmin, stored)
		}
		return out.IsAdmin
	}
	yes, no := true, false
	// Both calls run, in order, before either result is read: a short-circuit
	// would skip the second call, which is the one that tells a set from a flip.
	first, second := call(apitypes.ToggleAdminReq{Name: "carl-1", Admin: &yes}), call(apitypes.ToggleAdminReq{Name: "carl-1", Admin: &yes})
	if !first || !second {
		t.Fatal("set admin twice: not an admin after the second (a flip, not a set)")
	}
	if n := len(s.cfg().VPNAdmins); n != 1 {
		t.Fatalf("VPNAdmins has %d entries after two sets, want 1", n)
	}
	first, second = call(apitypes.ToggleAdminReq{Name: "carl-1", Admin: &no}), call(apitypes.ToggleAdminReq{Name: "carl-1", Admin: &no})
	if first || second {
		t.Fatal("unset twice: still an admin")
	}
	// No admin field: the UI's flip.
	if !call(apitypes.ToggleAdminReq{Name: "carl-1"}) || call(apitypes.ToggleAdminReq{Name: "carl-1"}) {
		t.Fatal("the flip no longer flips")
	}
}
