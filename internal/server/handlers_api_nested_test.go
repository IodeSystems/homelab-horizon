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

// N3 over the write surface: declare a nested hz as a Machine with an hz
// marker, place a rung in it, read the placement back off GET
// /api/v1/environments, and see the nested row on GET /api/v1/instances.
func TestANestedHZOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "redline"}}})

	var m apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", apitypes.MachineAddReq{
		Name: "redline-prod-hz", Project: "redline", HZ: &apitypes.MachineHZResp{URL: "https://hz.prod.redline.example"},
	}, &m)
	if m.HZ == nil || m.HZ.URL != "https://hz.prod.redline.example" {
		t.Fatalf("machines/add dropped the hz marker: %+v", m)
	}
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", apitypes.MachineAddReq{Name: "build-1"}, nil)

	if msg := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", apitypes.MachineAddReq{
		Name: "bad-hz", HZ: &apitypes.MachineHZResp{URL: "hz.example"},
	}); !strings.Contains(msg, "http") {
		t.Fatalf("a bad hz URL must be refused saying what shape it needs: %s", msg)
	}

	// An upstream without the marker is refused, naming why.
	if msg := postDeclareErr(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add", apitypes.EnvironmentAddReq{
		Project: "redline", Name: "prod", Posture: "prod", Upstream: "build-1",
	}); !strings.Contains(msg, "no hz marker") {
		t.Fatalf("an upstream without an hz marker must be refused: %s", msg)
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

	// Clear through set: the pointer contract.
	clear := ""
	env = apitypes.EnvironmentResp{} // decoding into a used value would keep the omitted fields
	postDeclare(t, s, s.handleAPIEnvironmentSet, "/api/v1/environments/set", apitypes.EnvironmentSetReq{
		Project: "redline", Name: "prod", Upstream: &clear,
	}, &env)
	if env.Upstream != "" || env.Placement.State != apitypes.RungPlacementHere {
		t.Fatalf("clearing the upstream must place the rung here: %+v", env)
	}

	// machines/set: clear and re-set the marker.
	m = apitypes.MachineResp{}
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set", apitypes.MachineSetReq{Name: "redline-prod-hz", ClearHZ: true}, &m)
	if m.HZ != nil {
		t.Fatalf("clearHz left the marker: %+v", m)
	}
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set", apitypes.MachineSetReq{
		Name: "redline-prod-hz", HZ: &apitypes.MachineHZResp{URL: "https://hz2.prod.redline.example"},
	}, &m)
	if m.HZ == nil || m.HZ.URL != "https://hz2.prod.redline.example" {
		t.Fatalf("machines/set did not set the marker: %+v", m)
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
	if n.Name != "redline-prod-hz" || n.Address != "https://hz2.prod.redline.example" || n.Project != "redline" || !n.Declared || n.Self {
		t.Fatalf("the nested row must carry the machine, its hz URL and its owner: %+v", n)
	}
	if n.Version != "" || n.Sync != nil || n.PeerID != "" || n.PrimaryID != "" {
		t.Fatalf("a nested hz is not in this cluster and is never contacted, so it carries no version, sync or peer ids: %+v", n)
	}
	if !rows[0].Self {
		t.Fatal("self must still be the first row")
	}
}
