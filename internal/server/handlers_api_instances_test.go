package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func listInstances(t *testing.T, s *Server) []apitypes.InstanceResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIInstances(w, asAdmin(s, http.MethodGet, apitypes.InstancesPath, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("instances returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.InstanceResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A box with no peers is one row, standalone, named by the same call
// machines/add?self uses — and until a Machine record of that name exists it
// is NOT declared, which is a different answer from "declared, global".
func TestInstancesStandalone(t *testing.T) {
	s := newTestServer(t, &config.Config{LocalInterface: "192.168.1.1"})
	s.version = "v9.9.9"

	rows := listInstances(t, s)
	if len(rows) != 1 {
		t.Fatalf("standalone must be exactly one row, got %+v", rows)
	}
	self := rows[0]
	if !self.Self || self.Name != LocalMachineName() {
		t.Fatalf("the one row must be self named %q, got %+v", LocalMachineName(), self)
	}
	if self.Role != apitypes.InstanceRoleStandalone || self.PrimaryID != "" {
		t.Fatalf("no peers is standalone with no primary, got %+v", self)
	}
	if self.Address != "192.168.1.1" || self.Version != "v9.9.9" {
		t.Fatalf("self carries local_interface and the running version, got %+v", self)
	}
	if self.Declared || self.Project != "" {
		t.Fatalf("no Machine record yet: undeclared, got %+v", self)
	}
	if self.Sync != nil {
		t.Fatalf("a standalone box has no pull loop to report, got %+v", self.Sync)
	}
}

// The project join, end to end over the real write: declaring self through
// machines/add with self=true must make THIS row declared with that project —
// the name the UI never types and the name the instance list reports are the
// same string because both come from LocalMachineName.
func TestInstancesSelfProjectJoinFollowsMachinesAddSelf(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "storefront"}}})

	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Self: true, Project: "storefront"}, nil)
	self := listInstances(t, s)[0]
	if !self.Declared || self.Project != "storefront" {
		t.Fatalf("self declared into storefront must read back so, got %+v", self)
	}

	global := ""
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: LocalMachineName(), Project: &global}, nil)
	self = listInstances(t, s)[0]
	if !self.Declared || self.Project != "" {
		t.Fatalf("moved to global: declared with project \"\", got %+v", self)
	}
}

func TestInstancesRefusesNonAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w := httptest.NewRecorder()
	s.handleAPIInstances(w, httptest.NewRequest(http.MethodGet, apitypes.InstancesPath, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read got %d", w.Code)
	}
}

func TestInstancesPrimaryWithReplicas(t *testing.T) {
	cfg := &config.Config{
		PeerID:         "gw-a",
		ConfigPrimary:  true,
		LocalInterface: "192.168.1.1",
		Peers: []config.Peer{
			{ID: "gw-b", WGAddr: "10.100.0.2:8080"},
			{ID: "gw-c", WGAddr: "10.100.0.3:8080"},
		},
		Projects: []config.Project{{Name: "storefront"}},
		Machines: []config.Machine{{Name: "gw-c", Project: "storefront"}},
	}
	rows := instancesFrom(cfg, "gw-a-host", "v1", PeerSyncStatusSnapshot{})
	if len(rows) != 3 {
		t.Fatalf("self + 2 peers is 3 rows, got %+v", rows)
	}
	if rows[0].Name != "gw-a-host" || !rows[0].Self || rows[0].Role != apitypes.InstanceRolePrimary || rows[0].PeerID != "gw-a" {
		t.Fatalf("self is the primary, got %+v", rows[0])
	}
	if rows[0].Sync != nil {
		t.Fatalf("a primary pulls from nobody, got %+v", rows[0].Sync)
	}
	for _, r := range rows {
		if r.PrimaryID != "gw-a" {
			t.Fatalf("every row names the same primary, got %+v", r)
		}
	}
	for i, want := range []string{"gw-b", "gw-c"} {
		r := rows[i+1]
		if r.Self || r.Name != want || r.Role != apitypes.InstanceRoleReplica || r.Version != "" || r.Sync != nil {
			t.Fatalf("peer %d must be replica %s with nothing observed, got %+v", i, want, r)
		}
	}
	if rows[1].Address != "10.100.0.2:8080" {
		t.Fatalf("a peer's address is its wg_addr, got %q", rows[1].Address)
	}
	// The join, per row: gw-c has a Machine record, gw-b does not.
	if rows[1].Declared || rows[1].Project != "" {
		t.Fatalf("gw-b has no Machine record, got %+v", rows[1])
	}
	if !rows[2].Declared || rows[2].Project != "storefront" {
		t.Fatalf("gw-c is owned by storefront, got %+v", rows[2])
	}
	if rows[0].Declared {
		t.Fatalf("self's hostname has no record, got %+v", rows[0])
	}
}

func TestInstancesReplica(t *testing.T) {
	cfg := &config.Config{
		PeerID: "gw-b",
		Peers: []config.Peer{
			{ID: "gw-a", WGAddr: "10.100.0.1:8080", Primary: true},
			{ID: "gw-c", WGAddr: "10.100.0.3:8080"},
		},
		Machines: []config.Machine{{Name: "gw-b-host"}},
	}
	ok := time.Unix(1_790_000_000, 0)
	snap := PeerSyncStatusSnapshot{PullCount: 7, LastSuccessAt: ok, LastError: "dial tcp: timeout"}
	rows := instancesFrom(cfg, "gw-b-host", "v1", snap)
	self := rows[0]
	if self.Role != apitypes.InstanceRoleReplica || self.PrimaryID != "gw-a" {
		t.Fatalf("self is a replica of gw-a, got %+v", self)
	}
	if self.Sync == nil || self.Sync.PullCount != 7 || self.Sync.LastSuccessAt != ok.Unix() || self.Sync.LastError != "dial tcp: timeout" {
		t.Fatalf("a replica reports its pull loop, got %+v", self.Sync)
	}
	if !self.Declared || self.Project != "" {
		t.Fatalf("declared global, got %+v", self)
	}
	if rows[1].Role != apitypes.InstanceRolePrimary || rows[2].Role != apitypes.InstanceRoleReplica {
		t.Fatalf("peer roles come from Peer.Primary, got %+v / %+v", rows[1], rows[2])
	}

	// Never synced is Sync present with a zero — not Sync absent.
	fresh := instancesFrom(cfg, "gw-b-host", "v1", PeerSyncStatusSnapshot{})[0]
	if fresh.Sync == nil || fresh.Sync.LastSuccessAt != 0 {
		t.Fatalf("a replica that never pulled still reports, as 0, got %+v", fresh.Sync)
	}
}
