package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The MFA portal's "Open hz" button: https://<kiosk host>/app/ from
// kiosk_url, or "" (the portal then uses its own origin).
func TestKioskUIURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://kiosk.hz.loadtest.example.com":    "https://kiosk.hz.loadtest.example.com/app/",
		"https://kiosk.example.com/":               "https://kiosk.example.com/app/",
		"https://kiosk.example.com:8443/some/path": "https://kiosk.example.com:8443/app/",
		"":          "",
		"not a url": "",
	} {
		if got := kioskUIURL(&config.Config{KioskURL: in}); got != want {
			t.Errorf("kioskUIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// And the status a VPN peer's portal reads carries it.
func TestMFAStatusCarriesTheUIURL(t *testing.T) {
	s, _ := inviteServer(t)
	if err := s.wg.AddPeer("carl-1", inviteDevKeyA, "10.101.0.2/32"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/mfa/status", nil)
	r.RemoteAddr = "10.101.0.2:40000"
	w := httptest.NewRecorder()
	s.handleAPIMFAStatus(w, r)
	var st apitypes.MFAStatusResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if st.UIURL != "https://"+inviteKiosk+"/app/" {
		t.Fatalf("uiUrl = %q", st.UIURL)
	}
}
