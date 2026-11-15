package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz vpn invite` sends the flags as the create request and prints the link,
// alone, on stdout.
func TestVPNInvitePrintsTheLink(t *testing.T) {
	var got apitypes.CreateInviteReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/vpn/invites/create":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(apitypes.CreateInviteResponse{OK: true,
				URL: "https://kiosk.example.com/invite/abc", ID: "0123456789ab", Name: got.Name, Profile: "lan-access", ExpiresAt: "2026-10-08T00:00:00Z"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := dispatch(c, "vpn", []string{"invite", "--name", "carl-2", "--profile", "lan-access", "--user", "carl", "--expires", "48h"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(out) != "https://kiosk.example.com/invite/abc" {
		t.Fatalf("stdout = %q, want the link only", out)
	}
	want := apitypes.CreateInviteReq{Name: "carl-2", Profile: "lan-access", User: "carl", ExpiresSeconds: 48 * 3600}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
	if err := dispatch(c, "vpn", []string{"invite", "--expires", "-1h"}); err == nil {
		t.Fatal("a negative --expires was accepted")
	}
}
