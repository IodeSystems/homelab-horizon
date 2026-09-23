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

func listSegments(t *testing.T, s *Server) []apitypes.SegmentResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPISegments(w, asAdmin(s, http.MethodGet, "/api/v1/segments", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.SegmentResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The walkthrough plan/upstream-and-promotion.md §5 asks for, over the write
// surface: an iodesystems segment with the gateway as its hub, and
// redline-prod-hz on it as a CLIENT. Every write goes through updateConfig,
// which calls Save, so a green run also proves each intermediate state was
// saveable.
func TestSegmentWalkthroughOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "gw-1", Segments: []string{"iode-net"}}, nil)
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "redline-prod-hz", Segments: []string{"iode-net"}}, nil)

	var seg apitypes.SegmentResp
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true, Endpoint: "hz.example.com:51820"},
			{Machine: "redline-prod-hz", Address: "10.42.0.2"},
		},
	}, &seg)

	if len(seg.Members) != 2 {
		t.Fatalf("the segment came back with %d members: %+v", len(seg.Members), seg)
	}
	// THE PEER SET IS DERIVED SERVER-SIDE. A client pairing members up itself
	// would be a second answer free to disagree with the one hz computes.
	var hub, client apitypes.SegmentMemberResp
	for _, m := range seg.Members {
		if m.Hub {
			hub = m
		} else {
			client = m
		}
	}
	if hub.Machine != "gw-1" || len(hub.Peers) != 1 || hub.Peers[0] != "redline-prod-hz" {
		t.Fatalf("the hub's peers are %+v", hub)
	}
	if client.Machine != "redline-prod-hz" || len(client.Peers) != 1 || client.Peers[0] != "gw-1" {
		t.Fatalf("the client's peers are %+v", client)
	}
	if client.PublicKey != "" {
		t.Fatalf("hz invented a public key: %q", client.PublicKey)
	}

	// A membership declared after the segment is a MEMBER with no address, and
	// it says so rather than being invisible.
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "late-box", Segments: []string{"iode-net"}}, nil)
	list := listSegments(t, s)
	if len(list) != 1 {
		t.Fatalf("want 1 segment, got %+v", list)
	}
	if len(list[0].Unaddressed) != 1 || list[0].Unaddressed[0] != "late-box" {
		t.Fatalf("the unaddressed membership is not reported: %+v", list[0])
	}

	// And a machine cannot join a segment that does not exist, now that one
	// does. The refusal names the machine, the name and what is declared.
	msg := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "stray", Segments: []string{"typo-net"}})
	for _, want := range []string{"stray", "typo-net", "iode-net"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not name %q: %s", want, msg)
		}
	}
}

// Removal refuses while a machine is a member and NAMES every one; cascade
// drops the memberships and leaves the machines declared. Same contract as
// `hz project rm` and `hz machine rm`, over the same RemovalResp shape.
func TestSegmentRemovalOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "gw-1", Segments: []string{"iode-net"}}, nil)
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{{Machine: "gw-1", Address: "10.42.0.1", Hub: true}},
	}, nil)

	// No cascade, no confirm: blocked, and nothing written.
	var out apitypes.RemovalResp
	postDeclare(t, s, s.handleAPISegmentRm, "/api/v1/segments/rm",
		apitypes.SegmentRmReq{Name: "iode-net", Confirm: true}, &out)
	if out.OK || len(out.Blocked) != 1 || out.Blocked[0].Name != "gw-1" {
		t.Fatalf("a confirmed removal was not blocked by the member: %+v", out)
	}
	if len(listSegments(t, s)) != 1 {
		t.Fatal("a blocked removal removed the segment anyway")
	}

	// Cascade + confirm: gone, and the machine survives in no segment.
	out = apitypes.RemovalResp{}
	postDeclare(t, s, s.handleAPISegmentRm, "/api/v1/segments/rm",
		apitypes.SegmentRmReq{Name: "iode-net", Cascade: true, Confirm: true}, &out)
	if !out.OK || len(out.Removes) != 2 {
		t.Fatalf("cascade answered %+v", out)
	}
	if len(listSegments(t, s)) != 0 {
		t.Fatal("the segment survived its own removal")
	}
	machines := listMachines(t, s)
	m := machineByName(t, machines, "gw-1")
	if len(m.Segments) != 0 {
		t.Fatalf("cascade left gw-1 naming %v", m.Segments)
	}
}
