package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz nested bootstrap` sends the contract's request and prints the command
// alone on stdout.
func TestNestedBootstrapSendsTheContract(t *testing.T) {
	var got []apitypes.NestedBootstrapReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case apitypes.NestedBootstrapPath:
			var req apitypes.NestedBootstrapReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			got = append(got, req)
			_ = json.NewEncoder(w).Encode(apitypes.NestedBootstrapResp{
				Command:  "curl -fsSL https://portal.example.com/admin/hz-child/install/hzc_x | sudo bash",
				Instance: req.Instance, Machine: req.Instance, Rung: req.Project + "/" + req.Rung, ExpiresAt: 1_800_000_000,
				Warning: "This command works only on linux-arm64 boxes",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := dispatch(c, "nested", []string{"bootstrap", "redline", "loadtest-hz", "--rung", "loadtest", "--from", "staging", "--platform", "linux-arm64"}); err != nil {
			t.Fatal(err)
		}
	})
	want := apitypes.NestedBootstrapReq{Project: "redline", Instance: "loadtest-hz", Rung: "loadtest", From: "staging", Platform: "linux-arm64"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("request = %+v", got)
	}
	// --instance and the deprecated --machine name the same thing, sent as
	// `instance`.
	for _, flagName := range []string{"--instance", "--machine"} {
		got = nil
		captureStdout(t, func() {
			if err := dispatch(c, "nested", []string{"bootstrap", "redline", flagName, "loadtest-hz", "--rung", "loadtest", "--from", "staging", "--platform", "linux-arm64"}); err != nil {
				t.Fatal(err)
			}
		})
		if len(got) != 1 || got[0] != want {
			t.Fatalf("%s: request = %+v", flagName, got)
		}
	}
	got = got[:1]
	if strings.TrimSpace(out) != "curl -fsSL https://portal.example.com/admin/hz-child/install/hzc_x | sudo bash" {
		t.Fatalf("stdout must be the command alone: %q", out)
	}
	for _, args := range [][]string{
		{"bootstrap", "redline", "loadtest-hz", "--rung", "loadtest"},
		{"bootstrap", "redline", "loadtest-hz", "--from", "staging"},
		{"bootstrap", "redline", "--rung", "loadtest", "--from", "staging"},
		{"bootstrap", "redline", "a-hz", "--instance", "b-hz", "--rung", "loadtest", "--from", "staging"},
		{"declare"},
		{},
	} {
		if err := runNested(c, args); err == nil || !strings.Contains(err.Error(), "usage: hz nested bootstrap") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if len(got) != 1 {
		t.Fatal("an incomplete command was sent")
	}

	// N4g: the kiosk and range are sent as given; the human VPN is the
	// parent's default (absent), and --human-vpn=false says so explicitly.
	got = nil
	captureStdout(t, func() {
		if err := dispatch(c, "nested", []string{"bootstrap", "redline", "loadtest-hz", "--rung", "loadtest", "--from", "staging",
			"--kiosk", "kiosk.hz.loadtest.example.com", "--human-vpn-range", "10.150.0.0/24"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(got) != 1 || got[0].Kiosk != "kiosk.hz.loadtest.example.com" || got[0].HumanVPNRange != "10.150.0.0/24" || got[0].HumanVPN != nil {
		t.Fatalf("request = %+v", got)
	}
	got = nil
	captureStdout(t, func() {
		if err := dispatch(c, "nested", []string{"bootstrap", "redline", "loadtest-hz", "--rung", "loadtest", "--from", "staging", "--human-vpn=false"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(got) != 1 || got[0].HumanVPN == nil || *got[0].HumanVPN || got[0].Kiosk != "" {
		t.Fatalf("--human-vpn=false: request = %+v", got)
	}
}
