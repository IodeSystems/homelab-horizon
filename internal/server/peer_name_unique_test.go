package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A peer name is unique (loadtest child, 2026-10-07: `vpn add carl-1` ran
// twice and made two peers named carl-1, 10.101.0.2 and .3). Every add path
// refuses a taken name with a sentence naming the existing peer, before
// wg0.conf is touched: the API (which the console's `vpn add` calls), an
// invite (made with the name, or redeemed with it), and a rename.
func TestAPeerNameIsUniqueOnEveryAddPath(t *testing.T) {
	s, wgPath := inviteServer(t)

	add := func(name string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(apitypes.PeerAddReq{Name: name, Profile: config.ProfileVPNOnly})
		w := httptest.NewRecorder()
		s.handleAPIAddPeer(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/peers/add", string(raw)))
		return w
	}
	if w := add("carl-1"); w.Code != http.StatusOK {
		t.Fatalf("first add: %d %s", w.Code, w.Body.String())
	}
	before, _ := os.ReadFile(wgPath)

	refused := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `named \"carl-1\" already exists (10.101.0.2/32`) {
			t.Errorf("%s: %d %s, want 409 naming carl-1 at 10.101.0.2", what, w.Code, w.Body.String())
		}
	}
	refused("API add, same name", add("carl-1"))
	refused("API add, other case", add("Carl-1"))

	// An invite that names it is refused at create.
	raw, _ := json.Marshal(apitypes.CreateInviteReq{Name: "carl-1"})
	w := httptest.NewRecorder()
	s.handleAPICreateInvite(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/invites/create", string(raw)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("invite create naming carl-1: %d %s", w.Code, w.Body.String())
	}
	// An unnamed invite redeemed with it is refused — and not burned.
	tok := tokenOf(t, mintInvite(t, s, apitypes.CreateInviteReq{}).URL)
	refused("invite redeem", redeem(s, tok, inviteDevKeyA, "carl-1"))
	if _, ok := s.findInvite(tok); !ok {
		t.Error("the refused redeem burned the invite")
	}
	if after, _ := os.ReadFile(wgPath); string(after) != string(before) {
		t.Error("a refused add or redeem changed wg0.conf")
	}

	// A rename onto it is refused.
	if w := add("carl-2"); w.Code != http.StatusOK {
		t.Fatalf("add carl-2: %d", w.Code)
	}
	var pk string
	for _, p := range s.wg.GetPeers() {
		if p.Name == "carl-2" {
			pk = p.PublicKey
		}
	}
	before, _ = os.ReadFile(wgPath)
	raw, _ = json.Marshal(apitypes.PeerEditReq{PublicKey: pk, Name: "carl-1", Profile: config.ProfileVPNOnly})
	w = httptest.NewRecorder()
	s.handleAPIEditPeer(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/peers/edit", string(raw)))
	refused("rename onto carl-1", w)
	if after, _ := os.ReadFile(wgPath); string(after) != string(before) {
		t.Error("a refused rename touched wg0.conf")
	}

	n := 0
	for _, p := range s.wg.GetPeers() {
		if strings.EqualFold(p.Name, "carl-1") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d peers named carl-1, want 1", n)
	}
}
