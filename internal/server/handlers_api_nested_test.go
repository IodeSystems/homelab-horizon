package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func listHZInstances(t *testing.T, s *Server) []apitypes.HZInstanceResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIHZInstances(w, asAdmin(s, http.MethodGet, apitypes.HZInstancesPath, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("hz-instances returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.HZInstanceResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func listMachinesAPI(t *testing.T, s *Server) []apitypes.MachineResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIMachines(w, asAdmin(s, http.MethodGet, "/api/v1/machines", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("machines returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.MachineResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// N3 + B3 over the write surface: declare a nested hz as an HZ INSTANCE, place
// a rung in it, read the placement back off GET /api/v1/environments, and see
// the nested row on GET /api/v1/instances and GET /api/v1/hz-instances — and
// never on GET /api/v1/machines.
func TestANestedHZOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "redline"}}})

	var h apitypes.HZInstanceResp
	postDeclare(t, s, s.handleAPIHZInstanceAdd, "/api/v1/hz-instances/add", apitypes.HZInstanceAddReq{
		Name: "redline-prod-hz", Project: "redline", URL: "https://hz.prod.redline.example",
	}, &h)
	if h.Name != "redline-prod-hz" || h.URL != "https://hz.prod.redline.example" || h.Project != "redline" || h.HasToken {
		t.Fatalf("hz-instances/add: %+v", h)
	}
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", apitypes.MachineAddReq{Name: "build-1"}, nil)

	if msg := postDeclareErr(t, s, s.handleAPIHZInstanceAdd, "/api/v1/hz-instances/add", apitypes.HZInstanceAddReq{
		Name: "bad-hz", URL: "hz.example",
	}); !strings.Contains(msg, "http") {
		t.Fatalf("a bad hz URL must be refused saying what shape it needs: %s", msg)
	}

	// A MACHINE as an upstream is refused, naming why.
	if msg := postDeclareErr(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add", apitypes.EnvironmentAddReq{
		Project: "redline", Name: "prod", Posture: "prod", Upstream: "build-1",
	}); !strings.Contains(msg, "names no declared hz instance") {
		t.Fatalf("a machine as an upstream must be refused: %s", msg)
	}

	var env apitypes.EnvironmentResp
	postDeclare(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add", apitypes.EnvironmentAddReq{
		Project: "redline", Name: "prod", Posture: "prod", Upstream: "redline-prod-hz",
	}, &env)
	if env.Upstream != "redline-prod-hz" || env.Placement.State != apitypes.RungPlacementRemote || env.Placement.URL != "https://hz.prod.redline.example" {
		t.Fatalf("environments/add must return the upstream and a remote placement: %+v", env)
	}
	postDeclare(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add", apitypes.EnvironmentAddReq{
		Project: "redline", Name: "staging", Posture: "staging",
	}, nil)

	w := httptest.NewRecorder()
	s.handleAPIEnvironments(w, asAdmin(s, http.MethodGet, "/api/v1/environments", ""))
	var envs []apitypes.EnvironmentResp
	if err := json.NewDecoder(w.Body).Decode(&envs); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, e := range envs {
		states[e.Name] = e.Placement.State
		if len(e.Placement.Unresolved) != 0 || e.Placement.Statement == "" {
			t.Fatalf("%s: placement must be a statement with no gap, got %+v", e.Name, e.Placement)
		}
	}
	if states["prod"] != apitypes.RungPlacementRemote || states["staging"] != apitypes.RungPlacementHere {
		t.Fatalf("placements read %v", states)
	}

	// hz-instances/set: a URL edit.
	u := "https://hz2.prod.redline.example"
	h = apitypes.HZInstanceResp{}
	postDeclare(t, s, s.handleAPIHZInstanceSet, "/api/v1/hz-instances/set", apitypes.HZInstanceSetReq{Name: "redline-prod-hz", URL: &u}, &h)
	if h.URL != u {
		t.Fatalf("hz-instances/set did not set the URL: %+v", h)
	}

	if got := listHZInstances(t, s); len(got) != 1 || got[0].Name != "redline-prod-hz" || got[0].URL != u {
		t.Fatalf("GET /api/v1/hz-instances = %+v", got)
	}
	// NEVER a machine.
	for _, m := range listMachinesAPI(t, s) {
		if m.Name == "redline-prod-hz" {
			t.Fatalf("an hz instance appeared in /api/v1/machines: %+v", m)
		}
	}

	rows := listInstances(t, s)
	var nested []apitypes.InstanceResp
	for _, r := range rows {
		if r.Role == apitypes.InstanceRoleNested {
			nested = append(nested, r)
		}
	}
	if len(nested) != 1 {
		t.Fatalf("want exactly one nested row, got %+v", rows)
	}
	n := nested[0]
	if n.Name != "redline-prod-hz" || n.Address != u || n.Project != "redline" || !n.Declared || n.Self {
		t.Fatalf("the nested row must carry the instance, its URL and its owner: %+v", n)
	}
	if n.Version != "" || n.Sync != nil || n.PeerID != "" || n.PrimaryID != "" {
		t.Fatalf("a nested hz is not in this cluster and is never contacted, so it carries no version, sync or peer ids: %+v", n)
	}
	if !rows[0].Self {
		t.Fatal("self must still be the first row")
	}
}

// hz-instances/rm: a dry run without confirm that writes nothing; refused
// while a rung's Upstream names it; cascade CLEARS the Upstream and keeps the
// rung.
func TestHZInstanceRmRefusesAndDryRuns(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Projects:     []config.Project{{Name: "redline"}},
		HZInstances:  []config.HZInstance{{Name: "rl-hz", Project: "redline", URL: "http://192.0.2.4:8080"}},
		Environments: []config.Environment{{Project: "redline", Name: "loadtest", Posture: "prod", Upstream: "rl-hz"}},
	})
	rm := func(req apitypes.HZInstanceRmReq) apitypes.RemovalResp {
		t.Helper()
		var out apitypes.RemovalResp
		postDeclare(t, s, s.handleAPIHZInstanceRm, "/api/v1/hz-instances/rm", req, &out)
		return out
	}

	out := rm(apitypes.HZInstanceRmReq{Name: "rl-hz", Confirm: true})
	if out.OK || len(out.Blocked) != 1 || out.Blocked[0].Kind != "environment" || out.Blocked[0].Name != "redline/loadtest" {
		t.Fatalf("an upstream rung must block the removal: %+v", out)
	}
	out = rm(apitypes.HZInstanceRmReq{Name: "rl-hz", Cascade: true})
	if out.OK || len(out.Blocked) != 0 || len(out.Removes) < 2 {
		t.Fatalf("the cascade dry run must list what it takes and write nothing: %+v", out)
	}
	if _, ok := s.cfg().FindHZInstance("rl-hz"); !ok {
		t.Fatal("a dry run removed the instance")
	}
	out = rm(apitypes.HZInstanceRmReq{Name: "rl-hz", Cascade: true, Confirm: true})
	if !out.OK {
		t.Fatalf("cascade + confirm: %+v", out)
	}
	if _, ok := s.cfg().FindHZInstance("rl-hz"); ok {
		t.Fatal("the instance is still declared")
	}
	e, err := s.cfg().LookupEnvironment("redline", "loadtest")
	if err != nil || e.Upstream != "" {
		t.Fatalf("cascade must keep the rung and clear its upstream: %+v %v", e, err)
	}
	if msg := postDeclareErr(t, s, s.handleAPIHZInstanceRm, "/api/v1/hz-instances/rm", apitypes.HZInstanceRmReq{Name: "rl-hz"}); !strings.Contains(msg, "no hz instance") {
		t.Fatalf("removing an undeclared instance: %s", msg)
	}
}
