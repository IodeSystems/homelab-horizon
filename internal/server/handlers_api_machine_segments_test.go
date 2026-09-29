package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// machines/set changes segment membership: add, remove, and the two refusals
// that keep the model whole — a multi-homed machine with no note (CLAUDE.md
// invariant 10) and leaving a segment the machine is still addressed on (a
// member entry must be claimed by its machine).

func segmentSetServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Projects: []config.Project{{Name: "iode"}},
		Machines: []config.Machine{
			{Name: "gw-1", Segments: []string{"iode-net"}},
			{Name: "app-1"},
		},
		Segments: []config.Segment{
			{Name: "iode-net", Project: "iode", CIDR: "10.42.0.0/24", Interface: "wg-iode",
				Members: []config.SegmentMember{{Machine: "gw-1", Address: "10.42.0.1", Hub: true, Endpoint: "gw.example.invalid:51820"}}},
			{Name: "lab-net", Project: "iode", CIDR: "10.43.0.0/24", Interface: "wg-lab"},
		},
	}
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return newTestServer(t, cfg)
}

func setSegments(names ...string) *[]string {
	out := append([]string{}, names...)
	return &out
}

func TestMachineSetAddsAndRemovesSegments(t *testing.T) {
	s := segmentSetServer(t)

	var m apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Segments: setSegments("iode-net")}, &m)
	if strings.Join(m.Segments, ",") != "iode-net" {
		t.Fatalf("set did not join app-1 to iode-net: %+v", m)
	}
	if got, _ := s.cfg().FindMachine("app-1"); strings.Join(got.Segments, ",") != "iode-net" {
		t.Fatalf("the join was answered and not written: %+v", got)
	}

	// Omitted leaves membership alone.
	note := "a lab box"
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Note: &note}, &m)
	if strings.Join(m.Segments, ",") != "iode-net" {
		t.Fatalf("a note-only set touched the membership: %+v", m)
	}

	// Two segments with a note (already on the machine) is legal.
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Segments: setSegments("iode-net", "lab-net")}, &m)
	if !m.MultiHomed {
		t.Fatalf("app-1 is in two segments and does not read as multi-homed: %+v", m)
	}

	// Empty list: in no segment. app-1 is unaddressed everywhere, so it may.
	// A fresh value to decode into: MachineResp omits an empty list, and
	// decoding into m would keep the previous answer's segments.
	var none apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Segments: setSegments()}, &none)
	if len(none.Segments) != 0 {
		t.Fatalf("an empty segment list left memberships behind: %+v", none)
	}
	if got, _ := s.cfg().FindMachine("app-1"); len(got.Segments) != 0 {
		t.Fatalf("an empty segment list was not written: %+v", got)
	}
}

func TestMachineSetRefusesAMultiHomedMachineWithNoNote(t *testing.T) {
	s := segmentSetServer(t)
	msg := postDeclareErr(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Segments: setSegments("iode-net", "lab-net")})
	if !strings.Contains(msg, "note") {
		t.Fatalf("the refusal does not say a note is what is missing: %s", msg)
	}
	if got, _ := s.cfg().FindMachine("app-1"); len(got.Segments) != 0 {
		t.Fatalf("a refused set was written: %+v", got)
	}

	// The same request with its reason goes through.
	note := "CI runner that deploys both"
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Note: &note, Segments: setSegments("iode-net", "lab-net")}, nil)
}

func TestMachineSetRefusesToLeaveASegmentItIsAddressedOn(t *testing.T) {
	s := segmentSetServer(t)
	msg := postDeclareErr(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "gw-1", Segments: setSegments()})
	if !strings.Contains(msg, "--unaddress gw-1") {
		t.Fatalf("the refusal does not name the command that unaddresses it: %s", msg)
	}
	got, _ := s.cfg().FindMachine("gw-1")
	if strings.Join(got.Segments, ",") != "iode-net" {
		t.Fatalf("a refused leave was written: %+v", got)
	}
	seg, _ := s.cfg().FindSegment("iode-net")
	if _, ok := seg.Member("gw-1"); !ok {
		t.Fatal("the refused leave deleted gw-1's member entry")
	}
}

func TestMachineSetRefusesAnUndeclaredSegment(t *testing.T) {
	s := segmentSetServer(t)
	msg := postDeclareErr(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "app-1", Segments: setSegments("nowhere-net")})
	if !strings.Contains(msg, "nowhere-net") {
		t.Fatalf("the refusal does not name the segment: %s", msg)
	}
}
