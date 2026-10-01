package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz env apply|hold|unhold` and `hz machine hz-token` send the contract's
// requests and print the answers.
func TestEnvApplyHoldAndHZTokenSendTheContract(t *testing.T) {
	var applies []apitypes.ApplyReq
	var holds []string
	var mints []apitypes.InstanceTokenReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/environments/apply":
			var req apitypes.ApplyReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			applies = append(applies, req)
			_ = json.NewEncoder(w).Encode(apitypes.ApplyResp{Applied: true, ID: 7, Version: req.Version, ArtifactSHA256: strings.Repeat("a", 64), PromotionID: 3})
		case "/api/v1/environments/hold", "/api/v1/environments/unhold":
			var req apitypes.HoldReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			holds = append(holds, r.URL.Path+" "+req.Reason)
			out := apitypes.HoldStateResp{Project: req.Project, Environment: req.Environment}
			if strings.HasSuffix(r.URL.Path, "/hold") {
				out.Hold = &apitypes.HoldResp{By: "user:carl", Reason: req.Reason, At: "2026-10-01T00:00:00Z"}
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/api/v1/machines/hz-token":
			var req apitypes.InstanceTokenReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			mints = append(mints, req)
			_ = json.NewEncoder(w).Encode(apitypes.InstanceTokenResp{Machine: req.Machine, Token: "hzi_abc"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := runEnvironment(c, []string{"apply", "redline", "--version", "1.4.0", "prod"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(applies) != 1 || applies[0] != (apitypes.ApplyReq{Project: "redline", Environment: "prod", Version: "1.4.0"}) {
		t.Fatalf("apply request = %+v", applies)
	}
	if !strings.Contains(out, "Applied redline/prod: 1.4.0 (apply #7 of promotion #3)") {
		t.Fatalf("apply output:\n%s", out)
	}
	if err := runEnvironment(c, []string{"apply", "redline", "prod"}); err == nil {
		t.Fatal("apply without --version was sent")
	}
	if err := runEnvironment(c, []string{"hold", "redline", "prod"}); err == nil {
		t.Fatal("hold without --reason was sent")
	}
	out = captureStdout(t, func() {
		if err := runEnvironment(c, []string{"hold", "redline", "prod", "--reason", "cutover"}); err != nil {
			t.Fatal(err)
		}
		if err := runEnvironment(c, []string{"unhold", "redline", "prod"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(holds) != 2 || holds[0] != "/api/v1/environments/hold cutover" || holds[1] != "/api/v1/environments/unhold " {
		t.Fatalf("hold requests = %q", holds)
	}
	if !strings.Contains(out, "redline/prod is HELD by user:carl") || !strings.Contains(out, "redline/prod is not held") {
		t.Fatalf("hold output:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"hz-token", "redline-prod-hz"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(mints) != 1 || mints[0].Machine != "redline-prod-hz" || strings.TrimSpace(out) != "hzi_abc" {
		t.Fatalf("mint %+v, stdout %q (the token alone, so it can be piped to a file)", mints, out)
	}
}
