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
	"github.com/iodesystems/homelab-horizon/internal/wgkey"
)

// THE GAP ITEM 15 LEFT, CLOSED AT HZ'S END. The projection could resolve a
// membership to an interface, an address and a peer set and still had to say it
// could not emit a `[Peer]` block, because nothing filled
// SegmentMember.PublicKey. These tests own hz's half: what an enrolment may put
// there, and — much more importantly — what it may NOT overwrite.

// keyedEstate is one segment with a hub and a spoke, both addressed and
// neither keyed, plus a machine that is IN the segment with no address.
func keyedEstate(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, &config.Config{})
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, nil)
	for _, name := range []string{"gw-1", "app-1", "late-box"} {
		postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
			apitypes.MachineAddReq{Name: name, Segments: []string{"iode-net"}}, nil)
	}
	postDeclare(t, s, s.handleAPISegmentAdd, "/api/v1/segments/add", apitypes.SegmentAddReq{
		Name: "iode-net", Project: "iodesystems", CIDR: "10.42.0.0/24", Interface: "wg-iode",
		Members: []apitypes.SegmentMemberAddReq{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true, Endpoint: "hz.example.com:51820"},
			{Machine: "app-1", Address: "10.42.0.2"},
		},
	}, nil)
	return s
}

// enrol posts one enrolment through the real handler and returns hz's answer.
func enrol(t *testing.T, s *Server, req agent.EnrollRequest) agent.EnrollResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAPIAgentEnroll(w, asAdmin(s, http.MethodPost, "/api/v1/agent/enroll", string(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("enrol returned %d: %s", w.Code, w.Body.String())
	}
	var out agent.EnrollResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func keyOn(t *testing.T, s *Server, segment, machine string) string {
	t.Helper()
	seg, ok := s.cfg().FindSegment(segment)
	if !ok {
		t.Fatalf("no segment %q", segment)
	}
	mem, ok := seg.Member(machine)
	if !ok {
		t.Fatalf("%s is not addressed on %s", machine, segment)
	}
	return mem.PublicKey
}

func statusFor(t *testing.T, resp agent.EnrollResponse, segment string) agent.SegmentKeyResult {
	t.Helper()
	for _, k := range resp.SegmentKeys {
		if k.Segment == segment {
			return k
		}
	}
	t.Fatalf("hz said nothing about %s: %+v", segment, resp.SegmentKeys)
	return agent.SegmentKeyResult{}
}

// A BOX'S PUBLIC KEY REACHES HZ AND LANDS ON ITS SegmentMember. This is the
// whole point: after it, hz holds everything a `[Peer]` block needs.
func TestEnrolmentRecordsTheReportedSegmentKey(t *testing.T) {
	s := keyedEstate(t)
	_, pub, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	resp := enrol(t, s, agent.EnrollRequest{
		Machine:     "app-1",
		SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: pub}},
	})
	if got := statusFor(t, resp, "iode-net").Status; got != agent.SegmentKeyRecorded {
		t.Fatalf("hz said %q, want recorded: %+v", got, resp.SegmentKeys)
	}
	if got := keyOn(t, s, "iode-net", "app-1"); got != pub {
		t.Fatalf("hz holds %q for app-1, want %q", got, pub)
	}
	// It lands on THIS member and nowhere else — the key is per (machine,
	// segment), and a write that splashed onto the hub would be a peer set
	// where two machines answer to one key.
	if got := keyOn(t, s, "iode-net", "gw-1"); got != "" {
		t.Fatalf("enrolling app-1 keyed gw-1 too: %q", got)
	}

	// RE-ENROLLING WITH THE SAME KEY WRITES NOTHING. `hz-agent install` enrols
	// every time, so the steady state has to be a no-op rather than a rewrite.
	again := enrol(t, s, agent.EnrollRequest{
		Machine:     "app-1",
		SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: pub}},
	})
	if got := statusFor(t, again, "iode-net").Status; got != agent.SegmentKeyUnchanged {
		t.Fatalf("re-reporting the same key said %q, want unchanged", got)
	}
	if got := keyOn(t, s, "iode-net", "app-1"); got != pub {
		t.Fatalf("a no-op report changed the key to %q", got)
	}
}

// A DIFFERENT KEY IS AN IMPOSTOR UNTIL SOMEBODY SAYS OTHERWISE. hz cannot tell
// a rotation from another box claiming this machine's peering, and the silent
// answer to that is a peer takeover: whoever gets their key recorded receives
// the traffic. So hz keeps what it holds and says so, and a rotation has to be
// declared.
func TestAChangedSegmentKeyIsRefusedUntilItIsDeclaredARotation(t *testing.T) {
	s := keyedEstate(t)
	_, first, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: first}},
	})

	// The impostor: same machine name, different key, no rotation declared.
	resp := enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: second}},
	})
	result := statusFor(t, resp, "iode-net")
	if result.Status != agent.SegmentKeyConflict {
		t.Fatalf("hz accepted a changed key silently: %+v", result)
	}
	if !strings.Contains(result.Detail, "--rotate-keys") {
		t.Fatalf("the conflict does not name what would resolve it: %q", result.Detail)
	}
	if got := keyOn(t, s, "iode-net", "app-1"); got != first {
		t.Fatalf("hz replaced the key it holds with %q despite refusing", got)
	}
	// The caller can find it without reading every entry — an ignored slice is
	// how this would come to be silent again.
	if len(resp.Conflicts()) != 1 {
		t.Fatalf("Conflicts() = %+v", resp.Conflicts())
	}

	// DECLARED, it goes through. This is the recovery path for a rebuilt box,
	// and without it the refusal above would be a dead end.
	rotated := enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", RotateKeys: true,
		SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: second}},
	})
	if got := statusFor(t, rotated, "iode-net").Status; got != agent.SegmentKeyRecorded {
		t.Fatalf("a declared rotation said %q, want recorded", got)
	}
	if got := keyOn(t, s, "iode-net", "app-1"); got != second {
		t.Fatalf("after a declared rotation hz holds %q, want %q", got, second)
	}
}

// The three reports hz cannot act on, each with its own answer, because the
// operator's next step is different for each and "failed" would be the same
// word for all three.
func TestTheReportsHZCannotAct0n(t *testing.T) {
	_, pub, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		machine string
		key     agent.SegmentKey
		want    string
		says    string
	}{
		"a key that is not a key": {
			machine: "app-1",
			key:     agent.SegmentKey{Segment: "iode-net", PublicKey: "not-a-wireguard-key"},
			want:    agent.SegmentKeyInvalid,
			says:    "wg pubkey",
		},
		"a segment hz does not declare": {
			machine: "app-1",
			key:     agent.SegmentKey{Segment: "seg:nowhere", PublicKey: pub},
			want:    agent.SegmentKeyUnknown,
			says:    "no segment",
		},
		"a membership with no address": {
			machine: "late-box",
			key:     agent.SegmentKey{Segment: "iode-net", PublicKey: pub},
			want:    agent.SegmentKeyUnaddressed,
			says:    "hz segment set",
		},
	} {
		s := keyedEstate(t)
		resp := enrol(t, s, agent.EnrollRequest{Machine: tc.machine, SegmentKeys: []agent.SegmentKey{tc.key}})
		got := statusFor(t, resp, tc.key.Segment)
		if got.Status != tc.want {
			t.Errorf("%s: hz said %q, want %q (%s)", name, got.Status, tc.want, got.Detail)
			continue
		}
		if !strings.Contains(got.Detail, tc.says) {
			t.Errorf("%s: the answer does not say what to do: %q", name, got.Detail)
		}
		// None of the three wrote anything.
		if tc.machine == "app-1" && tc.key.Segment == "iode-net" {
			if held := keyOn(t, s, "iode-net", "app-1"); held != "" {
				t.Errorf("%s: hz stored %q anyway", name, held)
			}
		}
	}
}

// THE CREDENTIAL IS STILL ISSUED WHEN A KEY IS REFUSED. `hz-agent install`
// enrols every time, so a box must never be left unable to authenticate because
// its key was disputed — and a disputed key must never be the thing that makes
// an operator re-run enrolment blind.
func TestAKeyConflictDoesNotCostTheBoxItsCredential(t *testing.T) {
	s := keyedEstate(t)
	_, first, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	issued := enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: first}},
	})
	if issued.Secret == "" {
		t.Fatal("hz issued no credential")
	}

	// A conflicting key, from a box that holds no credential at all.
	conflicted := enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: second}},
	})
	if conflicted.Secret == "" && !conflicted.AlreadyEnrolled {
		t.Fatal("a key conflict cost the enrolment its credential")
	}
	if len(conflicted.Conflicts()) != 1 {
		t.Fatalf("the conflict was not reported: %+v", conflicted.SegmentKeys)
	}

	// And the already-enrolled path reports keys too: an install re-run on a
	// keyed box has to be able to see what hz holds. The credential to present
	// is the one hz issued LAST — the conflicted request carried no hash, so hz
	// re-minted, and using the first secret here would test the mint path again
	// rather than the steady one.
	held := conflicted.Secret
	if held == "" {
		held = issued.Secret
	}
	steady := enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", CurrentHash: agent.HashSecret(held),
		SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: first}},
	})
	if !steady.AlreadyEnrolled {
		t.Fatalf("a matching hash minted again: %+v", steady)
	}
	if got := statusFor(t, steady, "iode-net").Status; got != agent.SegmentKeyUnchanged {
		t.Fatalf("the already-enrolled path said %q about the key", got)
	}
}

// HZ NEVER HOLDS A PRIVATE KEY. It cannot be checked by inspecting the value —
// a private key is the same 44 characters as a public one — so it is checked
// where it can be: what hz WROTE, against the private half the box kept. The
// record has one key field, it is filled from what the request's one key field
// carried, and the whole saved config is scanned for the private half.
func TestHZStoresThePublicHalfAndTheConfigNeverContainsThePrivateOne(t *testing.T) {
	s := keyedEstate(t)
	private, public, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	enrol(t, s, agent.EnrollRequest{
		Machine: "app-1", SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: public}},
	})

	if got := keyOn(t, s, "iode-net", "app-1"); got != public {
		t.Fatalf("hz stored %q, want the reported public key", got)
	}
	saved, err := json.Marshal(s.cfg())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), private) {
		t.Fatal("the private key is in hz's config")
	}
	if !strings.Contains(string(saved), public) {
		t.Fatal("the public key is NOT in hz's config — this test would pass on a no-op")
	}
}

// THE GAP CLOSES, END TO END, THROUGH THE REAL PIPELINE. Everything above
// checks one record; this checks the thing the record was for. Two boxes enrol
// and report their keys through the real handler, and the projection stops
// saying it cannot emit a `[Peer]` block — because the only reason it ever said
// so was that nothing filled these fields.
//
// BOTH DIRECTIONS, in one test, so a green run cannot mean the gap was simply
// deleted: before the keys it fires, after them it does not.
func TestEnrolmentClosesTheProjectionsKeylessPeerGap(t *testing.T) {
	s := keyedEstate(t)

	gapAbout := func(machine string) string {
		t.Helper()
		mc, err := projection.Project(s.projectionGlobal(s.cfg()), machine)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range mc.Unresolved {
			if g.Section == projection.SectionSegments && strings.Contains(g.Why, "[Peer]") {
				return g.Why
			}
		}
		return ""
	}

	// BEFORE. app-1 resolves — interface, address, peers — and still cannot
	// peer, because gw-1 has no key.
	before := gapAbout("app-1")
	if before == "" {
		t.Fatal("precondition: an unkeyed estate raised no keyless-peer gap, so this test proves nothing")
	}
	if !strings.Contains(before, "gw-1") {
		t.Fatalf("the gap does not name the peer hz cannot peer with: %q", before)
	}

	// ENROL BOTH, each reporting its own key, exactly as `hz-agent enroll` does.
	for _, machine := range []string{"gw-1", "app-1"} {
		_, pub, err := wgkey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		resp := enrol(t, s, agent.EnrollRequest{
			Machine:     machine,
			SegmentKeys: []agent.SegmentKey{{Segment: "iode-net", PublicKey: pub}},
		})
		if got := statusFor(t, resp, "iode-net").Status; got != agent.SegmentKeyRecorded {
			t.Fatalf("%s's key came back %q", machine, got)
		}
	}

	// AFTER. Nothing about the peer set changed; only the keys did.
	for _, machine := range []string{"gw-1", "app-1"} {
		if why := gapAbout(machine); why != "" {
			t.Errorf("%s: every peer is keyed and hz still says it cannot peer: %q", machine, why)
		}
	}

	// And the rest of what a [Peer] block needs is on the record, so "the gap
	// cleared" is a statement about hz holding the fields rather than about hz
	// having stopped complaining.
	seg, _ := s.cfg().FindSegment("iode-net")
	for _, m := range seg.Members {
		if !wgkey.Valid(m.PublicKey) || m.Address == "" {
			t.Errorf("%s is still not peerable: %+v", m.Machine, m)
		}
	}
	hub, _ := seg.Hub()
	if hub.Endpoint == "" {
		t.Error("the hub has no endpoint, so a spoke still has nothing to dial")
	}
}
