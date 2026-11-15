package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz env promote` sends the contract's request — flags anywhere among the
// three positionals — and prints the server's refusal verbatim.
func TestEnvPromoteSendsTheContractRequest(t *testing.T) {
	var got []apitypes.PromoteReq
	refuse := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/environments/promote":
			var req apitypes.PromoteReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			got = append(got, req)
			if refuse != "" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": refuse})
				return
			}
			_ = json.NewEncoder(w).Encode(apitypes.PromoteResp{
				Promoted: true, Version: req.Version, ArtifactSHA256: strings.Repeat("a", 64), Downgrade: req.AllowDowngrade,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := runEnvironment(c, []string{"promote", "redline", "--version", "1.4.0", "staging", "prod"}); err != nil {
			t.Fatalf("promote: %v", err)
		}
	})
	if len(got) != 1 || got[0] != (apitypes.PromoteReq{Project: "redline", From: "staging", To: "prod", Version: "1.4.0"}) {
		t.Fatalf("request = %+v", got)
	}
	if !strings.Contains(out, "Promoted redline/staging -> prod: 1.4.0") || !strings.Contains(out, strings.Repeat("a", 64)) {
		t.Fatalf("output:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runEnvironment(c, []string{"promote", "redline", "staging", "prod", "--version=1.3.0", "--allow-downgrade"}); err != nil {
			t.Fatalf("downgrade: %v", err)
		}
	})
	if !got[1].AllowDowngrade || !strings.Contains(out, "recorded as a downgrade") {
		t.Fatalf("downgrade request %+v, output:\n%s", got[1], out)
	}

	refuse = "staging has not reported running 1.5.0; it last reported 1.4.0 at 2026-09-30T10:00:00Z on ubuntu@host"
	err := runEnvironment(c, []string{"promote", "redline", "staging", "prod", "--version", "1.5.0"})
	if err == nil || !strings.Contains(err.Error(), refuse) {
		t.Fatalf("refusal not carried verbatim: %v", err)
	}

	for _, bad := range [][]string{
		{"promote", "redline", "staging", "prod"},
		{"promote", "redline", "prod", "--version", "1.4.0"},
	} {
		if err := runEnvironment(c, bad); err == nil || !strings.Contains(err.Error(), "usage: hz env promote") {
			t.Errorf("%v: err = %v, want the usage", bad, err)
		}
	}
	if len(got) != 3 {
		t.Fatalf("a usage error reached the server: %d requests", len(got))
	}
}
