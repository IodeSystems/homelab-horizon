package main

import (
	"strings"
	"testing"

	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz machine` as an operator meets it, over the same stub `hz project` and
// `hz env` are tested against: a REAL config, the REAL writers, and a real
// Save at every write.

// The walkthrough: declare a single-homed box, declare a bridge with its
// reason, and read both back.
func TestMachineAddAndList(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := runMachine(c, []string{"add", "app-1", "--segment", "seg:storefront"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Declared machine app-1") || !strings.Contains(out, "seg:storefront") {
		t.Fatalf("add did not report what it declared:\n%s", out)
	}
	// The absence is stated, not left to be inferred from a missing column.
	if !strings.Contains(out, "no project and no environment") {
		t.Fatalf("add does not say a machine has no project:\n%s", out)
	}
	// And it says how to enrol, because declaring is what confers that right.
	if !strings.Contains(out, "hz-agent enroll") {
		t.Fatalf("add does not say how to enrol the box:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"add", "ci-1",
			"--segment", "seg:intern", "--segment", "seg:storefront",
			"--note", "publishes packages, deploys storefront"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "MULTI-HOMED") || !strings.Contains(out, "publishes packages") {
		t.Fatalf("a bridge was declared without being flagged or explained:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"app-1", "ci-1", "MULTI-HOMED", "seg:intern", "UNION"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the listing does not mention %q:\n%s", want, out)
		}
	}
	// A machine listing has no project, environment or version column, and says
	// so rather than leaving a reader to wonder where they went.
	if !strings.Contains(out, "no observed version") {
		t.Fatalf("the listing does not explain the absences:\n%s", out)
	}
}

// The enumeration architecture.md asks for by name: every machine that bridges
// segments, what it bridges, and why.
func TestMachineListMultiHomedEnumeratesTheBridges(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{Machines: []hzconfig.Machine{
		{Name: "app-1", Segments: []string{"seg:storefront"}},
		{Name: "app-2", Segments: []string{"seg:storefront"}},
		{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"},
			Note: "publishes packages, deploys storefront"},
		{Name: "gw-1", Segments: []string{"seg:intern", "seg:storefront", "seg:people"},
			Note: "the hub; it is hz"},
	}})
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := runMachine(c, []string{"ls", "--multi-homed"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "2 machine(s) bridge segments") {
		t.Fatalf("the count is wrong or missing:\n%s", out)
	}
	for _, want := range []string{"ci-1", "gw-1", "publishes packages", "the hub; it is hz", "UNION"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--multi-homed does not mention %q:\n%s", want, out)
		}
	}
	// The single-homed ones are not in the list; that is what the flag is for.
	if strings.Contains(out, "app-1") || strings.Contains(out, "app-2") {
		t.Fatalf("--multi-homed listed a single-segment machine:\n%s", out)
	}

	// The positive control for the filter: without the flag they are all there.
	all := captureStdout(t, func() {
		if err := runMachine(c, []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"app-1", "app-2", "ci-1", "gw-1"} {
		if !strings.Contains(all, want) {
			t.Fatalf("the plain listing lost %q:\n%s", want, all)
		}
	}
}

// Nothing to show is a sentence, not a blank screen.
func TestMachineListSaysWhenThereAreNone(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := runMachine(c, []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No machines declared") || !strings.Contains(out, "hz machine add") {
		t.Fatalf("an empty listing does not say what to do:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"ls", "--multi-homed"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No machine is in more than one segment") {
		t.Fatalf("an empty bridge listing does not say so:\n%s", out)
	}
}

// show renders one machine, and names the three things a machine deliberately
// does not have — an operator looking for a project column has to find the
// answer, not the absence.
func TestMachineShow(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{Machines: []hzconfig.Machine{
		{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"}, Note: "deploys both"},
	}})
	s.enrolled["ci-1"] = true
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := runMachine(c, []string{"show", "ci-1"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"ci-1", "seg:intern", "MULTI-HOMED", "deploys both",
		"denied by default", "project", "environment", "observed version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "enrolled         yes") {
		t.Fatalf("show does not report the enrolment:\n%s", out)
	}

	err := captureStdoutErr(t, func() error { return runMachine(c, []string{"show", "ghost"}) })
	if err == nil || !strings.Contains(err.Error(), "ci-1") {
		t.Fatalf("show of an unknown machine does not list what exists: %v", err)
	}
}

// rm mirrors `hz project rm` exactly: dry run by default, refused while the
// credential exists, and --cascade opt-in and listed first.
func TestMachineRmIsADryRunAndRefusesWhileEnrolled(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{Machines: []hzconfig.Machine{
		{Name: "app-1", Segments: []string{"seg:storefront"}},
	}})
	s.enrolled["app-1"] = true
	c := s.start(t)

	// Refused, and it names the credential.
	var refusal error
	out := captureStdout(t, func() { refusal = runMachine(c, []string{"rm", "app-1", "--confirm"}) })
	if refusal == nil {
		t.Fatal("an enrolled machine was removed")
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "credential") {
		t.Fatalf("the refusal does not name the dependant:\n%s", out)
	}
	if len(s.cfg.Machines) != 1 {
		t.Fatal("a refused removal removed the machine")
	}

	// Cascade without --confirm lists what it would take and writes nothing.
	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"rm", "app-1", "--cascade"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Dry run: nothing was written") {
		t.Fatalf("the dry run did not say it wrote nothing:\n%s", out)
	}
	if !strings.Contains(out, "credential") || !strings.Contains(out, "REVOKED") {
		t.Fatalf("the dry run did not list the credential it takes:\n%s", out)
	}
	if len(s.cfg.Machines) != 1 || !s.enrolled["app-1"] {
		t.Fatal("a dry run wrote something")
	}

	// And with both it goes.
	out = captureStdout(t, func() {
		if err := runMachine(c, []string{"rm", "app-1", "--cascade", "--confirm"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Removed machine app-1") {
		t.Fatalf("the removal did not report itself:\n%s", out)
	}
	if len(s.cfg.Machines) != 0 || s.enrolled["app-1"] {
		t.Fatal("the machine or its credential survived a confirmed cascade")
	}
}

// An unenrolled machine has nothing depending on it and needs no cascade — the
// positive control for the refusal above.
func TestMachineRmNeedsNoCascadeWhenNothingDependsOnIt(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{Machines: []hzconfig.Machine{{Name: "spare"}}})
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := runMachine(c, []string{"rm", "spare", "--confirm"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Removed machine spare") {
		t.Fatalf("an unenrolled machine was not removed:\n%s", out)
	}
	if len(s.cfg.Machines) != 0 {
		t.Fatal("the machine is still declared")
	}
}

// The CLI decides nothing locally: every write goes to the server, which is
// what keeps one writer for config.json.
func TestMachineWritesGoThroughTheServer(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)

	captureStdout(t, func() {
		if err := runMachine(c, []string{"add", "app-1", "--segment", "seg:storefront"}); err != nil {
			t.Fatal(err)
		}
		if err := runMachine(c, []string{"rm", "app-1", "--confirm"}); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{"/api/v1/machines/add", "/api/v1/machines/rm"}
	if strings.Join(s.posts, ",") != strings.Join(want, ",") {
		t.Fatalf("the CLI posted %v, want %v", s.posts, want)
	}
}

// A bridge declared with no reason is refused by the server and the CLI shows
// the refusal rather than swallowing it.
func TestMachineAddSurfacesTheBridgeRefusal(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)

	err := captureStdoutErr(t, func() error {
		return runMachine(c, []string{"add", "ci-1", "--segment", "seg:intern", "--segment", "seg:storefront"})
	})
	if err == nil {
		t.Fatal("a bridge with no reason was declared")
	}
	if !strings.Contains(err.Error(), "--note") {
		t.Fatalf("the refusal does not say how to fix it: %v", err)
	}
	if len(s.cfg.Machines) != 0 {
		t.Fatal("the refused machine was written")
	}
}

func TestMachineUnknownSubcommand(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	err := runMachine(c, []string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "want ls, show, add or rm") {
		t.Fatalf("an unknown subcommand does not list the real ones: %v", err)
	}
}
