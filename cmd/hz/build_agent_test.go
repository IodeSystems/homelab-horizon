package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz build-agent` and its deprecated alias `hz builder` send the same request
// to the same endpoint; the alias says it is deprecated, on stderr.
func TestBuildAgentCLIAndItsAlias(t *testing.T) {
	var got []apitypes.BuildAgentReq
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case r.URL.Path == "/api/v1/build-agents" && r.Method == http.MethodPost:
			var req apitypes.BuildAgentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			got = append(got, req)
			_ = json.NewEncoder(w).Encode(apitypes.BuildAgentTokenResp{Name: req.Name, Project: req.Project, Host: req.Host, Token: "hzb_x"})
		case r.URL.Path == "/api/v1/build-agents" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]apitypes.BuildAgentResp{
				{Name: "desktop", Project: "redline", Host: "desktop", HasToken: true},
				{Name: "desktop-storefront", Project: "storefront", Host: "desktop"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/build-agents/") && r.Method == http.MethodDelete:
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/build-agents/")
			deleted = append(deleted, name)
			_ = json.NewEncoder(w).Encode(apitypes.BuildAgentRemovedResp{Removed: true, Name: name})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "test-token")

	out := captureStdout(t, func() {
		if err := dispatch(c, "build-agent", []string{"add", "desktop", "--project", "redline", "--host", "desktop"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(out) != "hzb_x" {
		t.Fatalf("stdout carries the token only: %q", out)
	}
	stderr := captureStderr(t, func() {
		if err := dispatch(c, "builder", []string{"add", "ci-1", "--project", "storefront"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "deprecated") || !strings.Contains(stderr, "hz build-agent") {
		t.Fatalf("the alias does not say it is deprecated: %q", stderr)
	}
	want := []apitypes.BuildAgentReq{{Name: "desktop", Project: "redline", Host: "desktop"}, {Name: "ci-1", Project: "storefront"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requests = %+v", got)
	}

	if err := dispatch(c, "build-agent", []string{"add", "x", "--project", "a", "--project", "b"}); err == nil || !strings.Contains(err.Error(), "ONE project") {
		t.Fatalf("two projects: %v", err)
	}

	out = captureStdout(t, func() {
		if err := dispatch(c, "build-agent", []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "desktop-storefront") || !strings.Contains(out, "NO TOKEN") {
		t.Fatalf("ls = %s", out)
	}

	_ = captureStdout(t, func() {
		if err := dispatch(c, "build-agent", []string{"rm", "desktop"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(deleted) != 1 || deleted[0] != "desktop" {
		t.Fatalf("deleted = %v", deleted)
	}
}
