package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func TestLinesAndPinSendTheContractRequests(t *testing.T) {
	var pins []apitypes.LinePinReq
	var paths []string
	sha := strings.Repeat("c", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/projects/lines":
			if r.URL.Query().Get("project") != "redline" {
				http.Error(w, "wrong project", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(apitypes.ProjectLinesResp{
				Project: "redline",
				Rungs: []apitypes.RungLinesResp{
					{Environment: "prod", Posture: "prod", From: "staging", Declared: "1.0.1-0.3", Supported: []apitypes.SupportedLineResp{
						{Line: "1.0.1", Why: []apitypes.LineWhyResp{{Kind: "current", Detail: "redline/prod declares 1.0.1-0.3"}},
							Restore: apitypes.LineRestoreResp{Status: "no-kept-backup", Sentence: "line 1.0.1 is supported on redline/prod but has no kept backup"}},
						{Line: "1.0.0", Why: []apitypes.LineWhyResp{{Kind: "prior", Detail: "1.0.0-1.1 was promoted from staging by promotion #2"}},
							KeptBackup: &apitypes.KeptBackupResp{Line: "1.0.0", BackupSHA256: sha, Location: "s3://kept/1.0.0", TakenByVersion: "1.0.0-0.1", AgeSeconds: 120},
							Restore:    apitypes.LineRestoreResp{Status: "passed", Sentence: "1.0.2 passed its restore test"}},
					}},
					{Environment: "staging", Posture: "staging", NoneRequired: "none required: redline/staging has no supported line yet"},
				},
				Retired: []apitypes.KeptBackupResp{{Line: "0.9.0", BackupSHA256: sha, Location: "s3://kept/0.9.0", AgeSeconds: 86400 * 3}},
			})
		case "/api/v1/projects/lines/pin", "/api/v1/projects/lines/unpin":
			paths = append(paths, r.URL.Path)
			var req apitypes.LinePinReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			pins = append(pins, req)
			_ = json.NewEncoder(w).Encode([]apitypes.PinnedLineResp{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := dispatch(c, "lines", []string{"redline"}); err != nil {
			t.Fatalf("lines: %v", err)
		}
	})
	for _, want := range []string{
		"redline/prod (prod, from staging) declares 1.0.1-0.3",
		"line 1.0.1 — current (redline/prod declares 1.0.1-0.3)",
		"kept backup  none",
		"restore      no-kept-backup: line 1.0.1 is supported on redline/prod but has no kept backup",
		"kept backup  cccccccccccc at s3://kept/1.0.0, taken by 1.0.0-0.1, 2m ago",
		"none required: redline/staging has no supported line yet",
		"0.9.0  cccccccccccc at s3://kept/0.9.0, 3d ago",
		"hz deletes nothing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hz lines output lacks %q:\n%s", want, out)
		}
	}

	if err := dispatch(c, "line", []string{"pin", "redline", "1.0.0"}); err == nil || !strings.Contains(err.Error(), "needs --reason") {
		t.Fatalf("pin without --reason: %v", err)
	}
	if len(pins) != 0 {
		t.Fatal("a reasonless pin reached the server")
	}
	_ = captureStdout(t, func() {
		if err := dispatch(c, "line", []string{"pin", "redline", "--reason", "legacy import", "1.0.0"}); err != nil {
			t.Fatalf("pin: %v", err)
		}
		if err := dispatch(c, "line", []string{"unpin", "redline", "1.0.0"}); err != nil {
			t.Fatalf("unpin: %v", err)
		}
	})
	if len(pins) != 2 || pins[0] != (apitypes.LinePinReq{Project: "redline", Line: "1.0.0", Reason: "legacy import"}) ||
		paths[1] != "/api/v1/projects/lines/unpin" || pins[1].Line != "1.0.0" {
		t.Fatalf("requests = %+v %v", pins, paths)
	}
}
