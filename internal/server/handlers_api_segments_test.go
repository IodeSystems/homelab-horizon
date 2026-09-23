package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
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

func strp(s string) *string { return &s }

// The gap `ls|show|add|rm` left, over the write surface: a membership declared
// after the segment gets an ADDRESS, without removing the segment and declaring
// it again. Every write goes through updateConfig → Save, so a green run also
// proves each intermediate state was saveable.
func TestSegmentSetAddressesALateMembership(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "gw-1", Segments: []string{"iode-net"}}, nil)
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{{Machine: "gw-1", Address: "10.42.0.1", Hub: true}},
	}, nil)
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "late-box", Segments: []string{"iode-net"}}, nil)

	if list := listSegments(t, s); len(list[0].Unaddressed) != 1 {
		t.Fatalf("late-box is not reported unaddressed: %+v", list[0])
	}

	var out apitypes.SegmentSetResp
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set", apitypes.SegmentSetReq{
		Name:    "iode-net",
		Members: []apitypes.SegmentMemberSetReq{{Machine: "late-box", Address: strp("10.42.0.7")}},
	}, &out)
	if !out.OK {
		t.Fatalf("a set that strands nobody did not write: %+v", out)
	}
	if len(out.Changes) != 1 || !strings.Contains(out.Changes[0], "10.42.0.7") {
		t.Fatalf("the change was not reported: %+v", out.Changes)
	}
	if out.Segment == nil || len(out.Segment.Members) != 2 || len(out.Segment.Unaddressed) != 0 {
		t.Fatalf("the segment came back %+v", out.Segment)
	}
	// The DERIVED peer set is recomputed and returned — the client never has to
	// work out that addressing a member gave it a peer.
	for _, m := range out.Segment.Members {
		if m.Machine == "late-box" && (len(m.Peers) != 1 || m.Peers[0] != "gw-1") {
			t.Fatalf("the newly addressed member peers with %+v", m.Peers)
		}
	}

	// It persisted: the READ endpoint agrees, which is what proves updateConfig
	// ran rather than the handler answering from a copy it threw away.
	list := listSegments(t, s)
	if len(list[0].Members) != 2 || len(list[0].Unaddressed) != 0 {
		t.Fatalf("the set did not persist: %+v", list[0])
	}

	// A membership the machine does not claim cannot be addressed: the refusal
	// says where membership IS declared rather than only that it is missing.
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "outsider"}, nil)
	msg := postDeclareErr(t, s, s.handleAPISegmentSet, "/api/v1/segments/set", apitypes.SegmentSetReq{
		Name:    "iode-net",
		Members: []apitypes.SegmentMemberSetReq{{Machine: "outsider", Address: strp("10.42.0.8")}},
	})
	for _, want := range []string{"outsider", "does not name segment", "declared on the MACHINE"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not say %q: %s", want, msg)
		}
	}
}

// Moving the hub comes back as a TOPOLOGY change with the whole peer rewiring
// attached, not as a field that quietly flipped. Peers is derived hub and spoke,
// so it is the one change here that is invisible in a diff of the record.
func TestSegmentSetReportsTheHubRewiring(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	for _, m := range []string{"gw-1", "gw-2", "spoke"} {
		postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
			apitypes.MachineAddReq{Name: m, Segments: []string{"iode-net"}}, nil)
	}
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true},
			{Machine: "gw-2", Address: "10.42.0.2"},
			{Machine: "spoke", Address: "10.42.0.3"},
		},
	}, nil)

	var out apitypes.SegmentSetResp
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", Hub: strp("gw-2")}, &out)
	if !out.OK || out.HubMove == nil {
		t.Fatalf("the hub move was not reported: %+v", out)
	}
	if out.HubMove.From != "gw-1" || out.HubMove.To != "gw-2" {
		t.Fatalf("the hub move reads %+v", out.HubMove)
	}
	if len(out.HubMove.Peers) != 3 {
		t.Fatalf("the rewiring covers %d members: %+v", len(out.HubMove.Peers), out.HubMove.Peers)
	}
	for _, p := range out.HubMove.Peers {
		if len(p.Before) == 0 || len(p.After) == 0 {
			t.Fatalf("a rewiring row has no before or no after: %+v", p)
		}
		if strings.Join(p.Before, ",") == strings.Join(p.After, ",") {
			t.Fatalf("an unchanged peer set was reported as rewired: %+v", p)
		}
	}
	// The returned segment agrees with the reported rewiring.
	for _, m := range out.Segment.Members {
		if m.Machine == "gw-2" && (!m.Hub || len(m.Peers) != 2) {
			t.Fatalf("the new hub reads %+v", m)
		}
		if m.Machine == "gw-1" && (m.Hub || len(m.Peers) != 1) {
			t.Fatalf("the old hub reads %+v", m)
		}
	}
	// A hub that is not an addressed member is refused, and the refusal lists
	// who IS addressed.
	msg := postDeclareErr(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", Hub: strp("nobody")})
	if !strings.Contains(msg, "nobody") || !strings.Contains(msg, "Addressed:") {
		t.Fatalf("the refusal does not list the addressed members: %s", msg)
	}
}

// A range that would strand a member BLOCKS and names each address, the way a
// removal blocks and names each dependant; --cascade unaddresses them and is a
// DRY RUN until --confirm. The dry run runs the real write against a copy, so
// what it prints is what --confirm writes.
func TestSegmentSetCIDRStrandsAreBlockedThenCascadedThenConfirmed(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	for _, m := range []string{"gw-1", "spoke"} {
		postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
			apitypes.MachineAddReq{Name: m, Segments: []string{"iode-net"}}, nil)
	}
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true},
			{Machine: "spoke", Address: "10.42.0.2"},
		},
	}, nil)

	// Blocked, both named, nothing written.
	var out apitypes.SegmentSetResp
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", CIDR: strp("10.99.0.0/24"), Confirm: true}, &out)
	if out.OK || len(out.Blocked) != 2 {
		t.Fatalf("a confirmed range change was not blocked by the stranded members: %+v", out)
	}
	names := []string{out.Blocked[0].Name, out.Blocked[1].Name}
	sort.Strings(names)
	if names[0] != "gw-1" || names[1] != "spoke" {
		t.Fatalf("the blockers are %+v", out.Blocked)
	}
	if listSegments(t, s)[0].CIDR != "10.42.0.0/24" {
		t.Fatal("a blocked range change was written anyway")
	}

	// Cascade, no confirm: the dry run lists what it would unaddress and writes
	// nothing.
	out = apitypes.SegmentSetResp{}
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", CIDR: strp("10.99.0.0/24"), Cascade: true}, &out)
	if out.OK || len(out.Strands) != 2 {
		t.Fatalf("the cascade dry run answered %+v", out)
	}
	if !strings.Contains(out.Strands[0].How, "UNADDRESSED") {
		t.Fatalf("the dry run does not say what cascade would do: %q", out.Strands[0].How)
	}
	if listSegments(t, s)[0].CIDR != "10.42.0.0/24" {
		t.Fatal("a dry run wrote the range")
	}

	// Cascade + confirm: written, and both machines are still IN the segment,
	// unaddressed.
	out = apitypes.SegmentSetResp{}
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", CIDR: strp("10.99.0.0/24"), Cascade: true, Confirm: true}, &out)
	if !out.OK || len(out.Strands) != 2 {
		t.Fatalf("the confirmed cascade answered %+v", out)
	}
	seg := listSegments(t, s)[0]
	if seg.CIDR != "10.99.0.0/24" || len(seg.Members) != 0 {
		t.Fatalf("the segment reads %+v", seg)
	}
	sort.Strings(seg.Unaddressed)
	if len(seg.Unaddressed) != 2 || seg.Unaddressed[0] != "gw-1" {
		t.Fatalf("cascade did not leave them in the segment: %+v", seg.Unaddressed)
	}
	for _, name := range []string{"gw-1", "spoke"} {
		m := machineByName(t, listMachines(t, s), name)
		if len(m.Segments) != 1 || m.Segments[0] != "iode-net" {
			t.Fatalf("cascade took %s out of the segment: %+v", name, m)
		}
	}
}

// The DRY RUN runs the real write against a copy, so a change that the
// validator will refuse is refused on the dry run too — which is the one thing
// a dry run exists to prevent meeting on the confirmed one.
func TestSegmentSetDryRunRefusesWhatTheWriteWouldRefuse(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	for _, m := range []string{"gw-1", "spoke"} {
		postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
			apitypes.MachineAddReq{Name: m, Segments: []string{"iode-net"}}, nil)
	}
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true},
			{Machine: "spoke", Address: "10.42.0.2"},
		},
	}, nil)

	// Cascade would strand only the HUB, leaving a segment with a member and no
	// hub — refused, on the DRY RUN, not later.
	msg := postDeclareErr(t, s, s.handleAPISegmentSet, "/api/v1/segments/set",
		apitypes.SegmentSetReq{Name: "iode-net", CIDR: strp("10.42.0.2/31"), Cascade: true})
	if !strings.Contains(msg, "no hub") {
		t.Fatalf("the dry run did not refuse the hubless result: %s", msg)
	}
	if listSegments(t, s)[0].CIDR != "10.42.0.0/24" {
		t.Fatal("a refused dry run wrote the range")
	}

	// And the same change with a new hub named in the SAME command is accepted.
	var out apitypes.SegmentSetResp
	postDeclare(t, s, s.handleAPISegmentSet, "/api/v1/segments/set", apitypes.SegmentSetReq{
		Name: "iode-net", CIDR: strp("10.42.0.2/31"), Hub: strp("spoke"), Cascade: true, Confirm: true,
	}, &out)
	if !out.OK {
		t.Fatalf("a complete renumber was refused: %+v", out)
	}
	seg := listSegments(t, s)[0]
	if seg.CIDR != "10.42.0.2/31" || len(seg.Members) != 1 || seg.Members[0].Machine != "spoke" || !seg.Members[0].Hub {
		t.Fatalf("the segment reads %+v", seg)
	}
}
