package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// `hz machine add --self` and what it opens downstream.
//
// A gateway ran hz with ZERO machines declared, which made every machine-shaped
// answer hz gave about itself vacuous: it could not project its own config, it
// could not be a segment member, `hz machine ls` was empty on a box that is
// itself a machine. The one thing that must NOT change while closing that is
// declare-then-enrol — so this is an admin-gated write by an operator, and the
// only thing hz supplies is the NAME of the box it is already running on.

func selfAdd(t *testing.T, s *Server, req apitypes.MachineAddReq) apitypes.MachineResp {
	t.Helper()
	var out apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", req, &out)
	return out
}

// The verb, end to end: it declares THIS box, under hz's own name for it, with
// no segment and no note invented for it.
func TestMachineAddSelfDeclaresTheGateway(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	out := selfAdd(t, s, apitypes.MachineAddReq{Self: true})
	if out.Name != LocalMachineName() {
		t.Fatalf("--self declared %q; hz is running on %q", out.Name, LocalMachineName())
	}
	if out.AlreadyDeclared {
		t.Fatal("the first --self reported the machine as already declared")
	}
	if len(out.Segments) != 0 || out.Note != "" || out.MultiHomed {
		t.Fatalf("--self invented network membership: %+v", out)
	}
	if out.Enrolled {
		t.Fatal("declaring a machine enrolled it; declare-then-enrol is two acts")
	}

	// It is in the config, which is what `hz machine ls` reads.
	list := listMachines(t, s)
	if len(list) != 1 || list[0].Name != LocalMachineName() {
		t.Fatalf("the declared gateway is not in the listing: %+v", list)
	}
}

// IDEMPOTENCE. `--self` asserts a state, so a second run reports the state and
// writes nothing. It must not surface AddMachine's duplicate-name refusal:
// "machine %q already exists" as a 400 reads as a failure when the thing the
// operator asked for is true.
func TestMachineAddSelfTwiceSaysAlreadyDeclared(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	first := selfAdd(t, s, apitypes.MachineAddReq{Self: true})
	second := selfAdd(t, s, apitypes.MachineAddReq{Self: true})

	if !second.AlreadyDeclared {
		t.Fatal("the second --self did not report the machine as already declared")
	}
	if second.Name != first.Name {
		t.Fatalf("the second run answered about %q, the first about %q", second.Name, first.Name)
	}
	if got := listMachines(t, s); len(got) != 1 {
		t.Fatalf("a duplicate was written: %+v", got)
	}

	// The POSITIVE CONTROL for the branch above: a NAMED add of the same
	// machine is still refused, so the idempotence is scoped to the verb whose
	// job is to reach a state rather than applied to every declaration.
	msg := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: first.Name})
	if !strings.Contains(msg, "already exists") {
		t.Fatalf("a named re-declaration was not refused by name: %s", msg)
	}
}

// Self and a name together are two answers to one question, and hz picks
// neither.
func TestMachineAddSelfWithANameIsRefused(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	msg := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Self: true, Name: "app-1"})
	if !strings.Contains(msg, "self and a name were both given") {
		t.Fatalf("the refusal does not explain the conflict: %s", msg)
	}
	if got := listMachines(t, s); len(got) != 0 {
		t.Fatalf("the refused request wrote something: %+v", got)
	}
}

// DECLARE-THEN-ENROL IS UNCHANGED. --self names ONE box — the one hz runs on —
// and confers nothing on any other. A machine nobody declared is still refused
// a credential, with the same message.
func TestSelfDoesNotLetAnyOtherBoxInTheDoor(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	selfAdd(t, s, apitypes.MachineAddReq{Self: true})

	body, _ := json.Marshal(agent.EnrollRequest{Machine: "some-other-box"})
	w := httptest.NewRecorder()
	s.handleAPIAgentEnroll(w, asAdmin(s, http.MethodPost, "/api/v1/agent/enroll", string(body)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("an undeclared box was not refused enrolment: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hz declares no machine named") {
		t.Fatalf("the refusal changed: %s", w.Body.String())
	}
}

// THE AGREEMENT THAT MATTERS, from hz's end: the machine --self declares is the
// machine the enrolment endpoint accepts. cmd/hz-agent/selfname_test.go pins the
// other half — that the agent asks for this exact name.
func TestTheGatewayDeclaredBySelfCanEnrol(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	out := selfAdd(t, s, apitypes.MachineAddReq{Self: true})

	body, _ := json.Marshal(agent.EnrollRequest{Machine: out.Name})
	w := httptest.NewRecorder()
	s.handleAPIAgentEnroll(w, asAdmin(s, http.MethodPost, "/api/v1/agent/enroll", string(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("the box hz just declared itself as cannot enrol: %d %s", w.Code, w.Body.String())
	}
	var resp agent.EnrollResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Machine != out.Name || resp.Secret == "" {
		t.Fatalf("enrolment answered %+v", resp)
	}
}

// WHAT CHANGES DOWNSTREAM, and the whole reason for declaring the box at all.
//
// Before: Project() raised a SectionMachine gap — "no machine record declares
// <host>" — and every segment answer for the gateway was vacuous.
//
// After: the projection answers FOR it. With no instances and no segments the
// honest answer is an empty one, and the three-state discipline is what makes
// it readable: an empty section with no gap beside it is hz saying NOTHING IS
// WANTED HERE, and an empty section WITH one is hz saying it does not know.
// This pins which of the two the gateway gets for each.
func TestTheDeclaredGatewayProjectsAsWantingNothing(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	out := selfAdd(t, s, apitypes.MachineAddReq{Self: true})

	mc, err := projection.Project(projection.Global{Config: s.cfg()}, out.Name)
	if err != nil {
		t.Fatal(err)
	}

	// THE GAP THAT CLOSED. It is the one this whole change exists to remove.
	for _, g := range mc.Unresolved {
		if g.Section == projection.SectionMachine {
			t.Fatalf("hz still says it does not know this machine: %s", g.Why)
		}
	}

	// NO SEGMENTS IS AN OPINION, NOT A GAP. A machine in no segment peers with
	// nothing, and hz knows that rather than failing to work it out. If this
	// ever starts raising a gap, "nothing is wanted here" becomes "hz does not
	// know", which is the exact confusion the discipline exists to prevent.
	if len(mc.Segments) != 0 {
		t.Fatalf("segments were invented for a machine that declares none: %+v", mc.Segments)
	}
	if mc.Unresolvable(projection.SectionSegments) {
		t.Fatalf("an empty segment list came with a gap beside it, so it reads as hz not knowing: %+v", mc.Unresolved)
	}

	// NOTHING TO HOST IS ALSO AN ANSWER. No instance is registered on this box,
	// so packages, feeds and units are empty — deliberately, with no gap.
	if len(mc.Units) != 0 || len(mc.Feeds) != 0 {
		t.Fatalf("a machine hosting nothing was given units or feeds: %+v", mc)
	}
	if mc.Unresolvable(projection.SectionInstances) {
		t.Fatalf("an empty instance list came with a gap beside it: %+v", mc.Unresolved)
	}

	// And it is addressed to the right box.
	if mc.Machine != LocalMachineName() {
		t.Fatalf("projection is for %q, hz runs on %q", mc.Machine, LocalMachineName())
	}
}

// The same thing over the AGENT ROUTE, which is where it reaches the box: the
// locally-read sections are composed on as they always were, and the machine
// gap is gone from the payload.
func TestTheDeclaredGatewayIsServedItsOwnProjection(t *testing.T) {
	s, _ := agentTestServer(t)
	out := selfAdd(t, s, apitypes.MachineAddReq{Self: true})

	w := agentGET(t, s, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var d agent.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Machine != out.Name {
		t.Fatalf("payload addressed to %q, want %q", d.Machine, out.Name)
	}
	if d.Model == nil {
		t.Fatal("the gateway got no projection")
	}
	for _, g := range d.Model.Unresolved {
		if g.Section == projection.SectionMachine {
			t.Fatalf("the gateway's own payload still says hz does not know this machine: %s", g.Why)
		}
	}
	// The gateway's advantage is unchanged: hz can read this box's files, so
	// the rendered sections are still attached.
	if d.HAProxy == nil {
		t.Fatal("hz stopped composing its own rendered config onto its own payload")
	}
}
