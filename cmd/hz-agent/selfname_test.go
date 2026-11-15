package main

import (
	"os"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/server"
)

// THE NAME AGREEMENT, pinned.
//
// hz now names one box for the operator: `hz machine add --self`, and the
// machine row `hz import` proposes, both take the name from hz's own identity.
// The agent takes ITS name from its own (agentFlags.machineName). If those two
// answers ever differ, the gateway is DECLARED as one machine and ENROLS as
// another: hz refuses the enrolment with "hz declares no machine named …" while
// `hz machine ls` shows a machine that looks right, and nothing in either
// message says the two strings differ. That is the highest-risk detail in the
// whole feature and it is invisible in code review, because the two functions
// live in different packages and neither mentions the other.
//
// So it is asserted rather than described. The test lives HERE, in the agent's
// package, because Go cannot import a main package: the comparison can only
// happen on this side, which is why internal/server exports LocalMachineName.
//
// Neither side is mocked. Both are asked, on this host, for their real answer —
// a test that stubbed os.Hostname would prove the stub agrees with itself.
func TestTheNameHZDeclaresIsTheNameTheAgentEnrolsWith(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Skipf("this host has no name to compare answers about: %v", err)
	}

	declared := server.LocalMachineName() // what `hz machine add --self` writes
	enrolling := (&agentFlags{}).machineName()

	if declared != enrolling {
		t.Fatalf("hz would declare this box as %q and the agent would enrol as %q.\n"+
			"A machine declared under one name and enrolled under another matches nothing, and hz's refusal\n"+
			"(\"hz declares no machine named …\") does not say the two strings differ.", declared, enrolling)
	}

	// And the shared answer is the KERNEL's, not a constant either side
	// invented. Both agreeing on "unknown" would satisfy the check above and
	// declare every gateway in the estate as the same machine.
	if declared != hostname {
		t.Fatalf("both sides agree on %q, which is not this host's name (%q)", declared, hostname)
	}
}

// --machine overrides the agent's answer, and that is deliberate: it is how a
// box whose hostname is not its estate name enrols. It is also the one way the
// agreement above can be broken on purpose, so the override is pinned as an
// override rather than left to look like drift.
func TestTheAgentsNameCanBeOverriddenOnPurpose(t *testing.T) {
	f := &agentFlags{machine: "app-1"}
	if got := f.machineName(); got != "app-1" {
		t.Fatalf("--machine did not override the hostname: %q", got)
	}
	// An operator using it has to declare the box under the SAME name, which is
	// what `hz machine add <name>` is for — --self is only correct when the
	// agent on that box is using its default.
	if server.LocalMachineName() == "app-1" {
		t.Skip("this host is literally called app-1; the distinction this test draws does not exist here")
	}
}
