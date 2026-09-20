package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// envStub serves the two routes `hz env` joins: the declared rungs and the services that
// name them. Kept separate from cmStub because it is a different surface, and it answers
// on the same paths the real handlers register rather than on whatever the CLI asks for.
func envStub(t *testing.T, envs []apitypes.EnvironmentResp, svcs []apitypes.ServiceResp) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case "/api/v1/environments":
			_ = json.NewEncoder(w).Encode(envs)
		case "/api/v1/services":
			_ = json.NewEncoder(w).Encode(svcs)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unrouted: ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

func fixture() ([]apitypes.EnvironmentResp, []apitypes.ServiceResp) {
	envs := []apitypes.EnvironmentResp{
		{Project: "redline", Name: "staging", Posture: "staging", From: "dev", Version: "1.2.3"},
		{Project: "redline", Name: "dev", Posture: "dev"},
		{Project: "redline", Name: "prod", Posture: "staging", From: "staging", Version: "1.2.1"},
		{Project: "veliode", Name: "beta", Posture: "staging"},
	}
	svcs := []apitypes.ServiceResp{
		{Name: "redline-app", Project: "redline", Environment: "staging"},
		{Name: "redline-ops", Project: "redline", Environment: "staging"},
		{Name: "redline-cron", Project: "redline"}, // in the project, no rung
		{Name: "grafana"}, // neither — legal, and the common case today
	}
	return envs, svcs
}

// TestEnvListGroupsAndSortsLikeProjectLs pins the two ordering conventions `hz project
// ls` established: unassigned sorts last between projects, and — the same reasoning one
// level down — a bucket with no declaration sorts last within a project.
func TestEnvListGroupsAndSortsLikeProjectLs(t *testing.T) {
	envs, svcs := fixture()
	groups, err := groupEnvironments(envStub(t, envs, svcs))
	if err != nil {
		t.Fatalf("group: %v", err)
	}

	var projects []string
	for _, g := range groups {
		projects = append(projects, g.project)
	}
	want := []string{"redline", "veliode", "(unassigned)"}
	if strings.Join(projects, ",") != strings.Join(want, ",") {
		t.Fatalf("projects %v, want %v", projects, want)
	}

	var rows []string
	for _, r := range groups[0].rows {
		rows = append(rows, r.name)
	}
	wantRows := []string{"dev", "prod", "staging", "(none)"}
	if strings.Join(rows, ",") != strings.Join(wantRows, ",") {
		t.Fatalf("redline rows %v, want %v", rows, wantRows)
	}
}

// A service with no project has nothing to resolve its environment string against —
// ValidateEnvironments deliberately does not check it — so it must not be reported as a
// rung. Otherwise `hz env ls` invents environments the config never declared.
func TestProjectlessServiceIsNotAnEnvironment(t *testing.T) {
	c := envStub(t, nil, []apitypes.ServiceResp{{Name: "grafana", Environment: "prod"}})
	groups, err := groupEnvironments(c)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if len(groups) != 1 || groups[0].project != "(unassigned)" {
		t.Fatalf("want one (unassigned) group, got %+v", groups)
	}
	if len(groups[0].rows) != 1 || groups[0].rows[0].name != "(none)" {
		t.Fatalf("want a single (none) row, got %+v", groups[0].rows)
	}
}

// The declared attributes are the reason this command exists: a name alone does not tell
// you what is real in there. An environment with no services still has to appear —
// declaring prod before a box exists is the normal order, per the architecture doc.
func TestEnvShowReportsPostureFromVersionAndServices(t *testing.T) {
	envs, svcs := fixture()
	c := envStub(t, envs, svcs)

	out := captureStdout(t, func() {
		if err := environmentShow(c, []string{"redline/staging"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	for _, want := range []string{"redline/staging", "staging", "dev", "1.2.3", "redline-app", "redline-ops"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "redline-cron") {
		t.Fatalf("a service with no rung must not appear in an environment:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := environmentShow(c, []string{"veliode/beta"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(out, "none yet") {
		t.Fatalf("a declared environment with no services must still report itself:\n%s", out)
	}
}

// Names are unique per project, not globally, so a bare name is ambiguous and the CLI
// must say so rather than guess a project.
func TestEnvShowRequiresBothHalves(t *testing.T) {
	envs, svcs := fixture()
	c := envStub(t, envs, svcs)
	if err := environmentShow(c, []string{"staging"}); err == nil {
		t.Fatal("env show accepted a bare environment name")
	}
	err := captureStdoutErr(t, func() error { return environmentShow(c, []string{"redline/ghost"}) })
	if err == nil {
		t.Fatal("env show accepted an environment that does not exist")
	}
}
