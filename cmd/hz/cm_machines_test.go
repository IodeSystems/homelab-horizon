package main

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// These drive the CLI against cmStub, whose machine routes read the SAME
// constants the real handlers read and enforce the SAME confirm rule. That is
// deliberate and it is the lesson from the five contract mismatches this
// feature already produced: a stub that answers whatever the CLI happens to
// send proves only that the CLI agrees with itself.

func stubMachine() apitypes.CMMachineResp {
	return apitypes.CMMachineResp{
		ID: "mch_1", Name: "box-1", EnrolledEnvironment: "prod",
		Fingerprint: "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF", CreatedAt: "2026-09-01T00:00:00Z",
		Registrations: []apitypes.CMRegistrationResp{
			{ID: "reg-1", Environment: "prod", App: "redline", Role: "app", State: configmgr.StateApproved},
			{ID: "reg-2", Environment: "prod", App: "redline", Role: "worker", State: configmgr.StatePending},
		},
		SecretKeys: []string{"NPM_TOKEN"},
	}
}

// The preview is the whole point of the prompt: an operator shown only a count
// cannot tell the box they rebuilt from a box that took its name. Every address
// and every secret key has to be on screen BEFORE the confirmation is asked
// for.
func TestRemoveShowsWhatItWillDestroyBeforeAsking(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine())
	c := s.start(t)
	withStdin(t, "box-1\n")

	out := captureStdout(t, func() {
		if err := cmRemove(c, []string{"box-1"}); err != nil {
			t.Fatalf("remove: %v", err)
		}
	})

	for _, want := range []string{
		"prod/redline/app", "prod/redline/worker", "NPM_TOKEN",
		"AAAA-BBBB-CCCC-DDDD-EEEE-FFFF", "holds a wrapped key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview does not mention %q:\n%s", want, out)
		}
	}
	// And it must not claim to revoke anything. A box that was ever approved
	// holds the environment key unwrapped on its own disk; an operator who
	// believes otherwise skips the rotation that would actually revoke it.
	if !strings.Contains(out, "does NOT revoke") {
		t.Errorf("the removal does not say it is not a revocation:\n%s", out)
	}
	if len(s.removed) != 1 || s.removed[0] != "box-1" {
		t.Fatalf("removed = %v, want [box-1]", s.removed)
	}
}

// The typed name is the guard and it has to be a real one: a mismatch sends no
// DELETE at all rather than one hz then refuses. Same rule as the fingerprint
// prompt — one shot, no retry loop, nothing sent.
func TestRemoveRefusesAMistypedNameAndSendsNothing(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine())
	c := s.start(t)
	withStdin(t, "box-2\n")

	captureStdout(t, func() {
		err := cmRemove(c, []string{"box-1"})
		if err == nil {
			t.Fatal("a mistyped name was accepted")
		}
		if !strings.Contains(err.Error(), "Nothing was removed") {
			t.Errorf("the refusal does not say nothing was removed: %v", err)
		}
	})
	if len(s.removed) != 0 {
		t.Fatalf("a DELETE was sent despite the mismatch: %v", s.removed)
	}
}

// Removing by id is legitimate — a script holds an id — and the confirm token
// still has to be the NAME, because that is what the server compares. The stub
// enforces the server's rule, so a CLI that echoed back the reference it was
// handed fails here rather than on the operator's box.
func TestRemoveByIDConfirmsOnTheResolvedName(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine())
	c := s.start(t)
	withStdin(t, "box-1\n")

	captureStdout(t, func() {
		if err := cmRemove(c, []string{"mch_1"}); err != nil {
			t.Fatalf("remove by id: %v", err)
		}
	})
	if len(s.removed) != 1 || s.removed[0] != "box-1" {
		t.Fatalf("removed = %v, want [box-1]", s.removed)
	}
}

// --yes is for scripts and skips only the PROMPT. The preview still prints,
// because a scripted removal's output is the only record of what it took.
func TestRemoveYesStillPrintsThePreview(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine())
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmRemove(c, []string{"box-1", "--yes"}); err != nil {
			t.Fatalf("remove --yes: %v", err)
		}
	})
	if !strings.Contains(out, "NPM_TOKEN") || !strings.Contains(out, "does NOT revoke") {
		t.Errorf("--yes skipped the preview:\n%s", out)
	}
	if len(s.removed) != 1 {
		t.Fatalf("removed = %v, want one", s.removed)
	}
}

// Removing something not enrolled — a typo, or the same removal run twice — is
// an ordinary outcome and must read like one, pointing at the listing rather
// than surfacing a bare 404.
func TestRemoveUnknownMachineSaysWhereToLook(t *testing.T) {
	s := newCMStub()
	c := s.start(t)

	captureStdout(t, func() {
		err := cmRemove(c, []string{"box-nope"})
		if err == nil {
			t.Fatal("removing an unknown machine succeeded")
		}
		if !strings.Contains(err.Error(), "hz cm machines") {
			t.Errorf("the error does not point at the listing: %v", err)
		}
	})
	if len(s.removed) != 0 {
		t.Fatalf("a DELETE was sent for an unknown machine: %v", s.removed)
	}
}

// The listing is where an operator finds the name the re-enrol refusal told
// them to remove, so it has to carry the name and the fingerprint that
// distinguishes two enrolments of it.
func TestMachinesListsEnrolledBoxes(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine())
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmMachines(c, nil); err != nil {
			t.Fatalf("machines: %v", err)
		}
	})
	if !strings.Contains(out, "box-1") || !strings.Contains(out, "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF") {
		t.Errorf("the listing is missing the machine or its fingerprint:\n%s", out)
	}
	// One of the two registrations is approved.
	if !strings.Contains(out, "1/2") {
		t.Errorf("the listing does not show approved/total registrations:\n%s", out)
	}
}
