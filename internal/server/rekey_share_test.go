package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// A share link is an unauthenticated kiosk URL serving the client's private
// key. Rekey creates one only when asked, and always drops the old one (it
// carries the replaced key). The share=true call is the positive control: it
// shows the test can see a share being created.
func TestRekeyCreatesAShareLinkOnlyWhenAsked(t *testing.T) {
	// PATH holds only wg (key generation, unprivileged): rekey's reload then
	// fails at lookup instead of reaching systemd-run on the test host.
	wg, err := exec.LookPath("wg")
	if err != nil {
		t.Skip("wg not installed; rekey cannot generate a key")
	}
	bin := t.TempDir()
	if err := os.Symlink(wg, filepath.Join(bin, "wg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	s, _ := inviteServer(t)
	_, pub, err := wireguard.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.wg.AddPeer("phone", pub, "10.101.0.5/32"); err != nil {
		t.Fatal(err)
	}
	rekey := func(share bool) apitypes.RekeyPeerResponse {
		t.Helper()
		raw, _ := json.Marshal(apitypes.RekeyPeerReq{PublicKey: pub, Share: share})
		w := httptest.NewRecorder()
		s.handleAPIRekeyPeer(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/peers/rekey", string(raw)))
		var out apitypes.RekeyPeerResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("rekey share=%v: %d %s", share, w.Code, w.Body.String())
		}
		pub = s.wg.GetPeerByIP("10.101.0.5").PublicKey
		return out
	}
	shares := func() int {
		s.configSharesMu.Lock()
		defer s.configSharesMu.Unlock()
		return len(s.configShares)
	}

	got := rekey(true)
	if got.ShareURL == "" || shares() != 1 {
		t.Fatalf("control: share=true gave url %q, %d shares", got.ShareURL, shares())
	}
	got = rekey(false)
	if got.ShareURL != "" || got.ShareToken != "" {
		t.Errorf("share=false answered a share link: %q", got.ShareURL)
	}
	if n := shares(); n != 0 {
		t.Errorf("share=false: %d shares left (the old one carries the replaced key)", n)
	}
	if got.Config == "" {
		t.Error("share=false: no config returned")
	}
}
