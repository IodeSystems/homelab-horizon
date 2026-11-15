package main

import (
	"strings"
	"testing"

	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz service assign` / `hz service unassign` as an operator meets them. The
// stub is the declare stub: a REAL config behind the REAL writers, saved to a
// real file, so the refusals below are the ones hz would actually give.

func assignFixture(t *testing.T) *declareStub {
	t.Helper()
	return newDeclareStub(t, &hzconfig.Config{
		Projects: []hzconfig.Project{
			{Name: "iodesystems"},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []hzconfig.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
		},
		Services: []hzconfig.Service{
			{Name: "app", Domains: []string{"app.example.com"}, Project: "redline", Environment: "staging"},
			{Name: "git", Domains: []string{"git.example.com"}},
		},
	})
}

func serviceInStub(t *testing.T, stub *declareStub, name string) hzconfig.Service {
	t.Helper()
	for _, svc := range stub.cfg.Services {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("no service %q", name)
	return hzconfig.Service{}
}

// TestAssignAcceptsAProjectWithNoRung: a bare project name is the whole
// address, not a half-typed one. Plenty of services are not on a ladder.
func TestAssignAcceptsAProjectWithNoRung(t *testing.T) {
	stub := assignFixture(t)
	c := stub.start(t)

	out := captureStdout(t, func() {
		if err := runService(c, []string{"assign", "git", "iodesystems"}); err != nil {
			t.Fatalf("assign to a project with no rung: %v", err)
		}
	})
	if svc := serviceInStub(t, stub, "git"); svc.Project != "iodesystems" || svc.Environment != "" {
		t.Fatalf("git is on %q/%q", svc.Project, svc.Environment)
	}
	// It has to SAY that no rung is a deliberate state, or the operator reads
	// the missing half as a failure.
	for _, want := range []string{"on no environment", "That is legal", "hz service assign git iodesystems/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output does not say %q:\n%s", want, out)
		}
	}
}

// TestUnassignClearsBothHalves pins the operation that a zero-value check
// cannot express. It is its own verb rather than `assign x ""` because a
// placement is one fact with two halves — there is no partial assign — and an
// empty positional is a typo far more often than an intention.
func TestUnassignClearsBothHalves(t *testing.T) {
	stub := assignFixture(t)
	c := stub.start(t)

	out := captureStdout(t, func() {
		if err := runService(c, []string{"unassign", "app"}); err != nil {
			t.Fatalf("unassign: %v", err)
		}
	})
	svc := serviceInStub(t, stub, "app")
	if svc.Project != "" || svc.Environment != "" {
		t.Fatalf("app is still on %q/%q", svc.Project, svc.Environment)
	}
	if len(svc.Domains) != 1 || svc.Domains[0] != "app.example.com" {
		t.Fatalf("unassign touched the domains: %+v", svc.Domains)
	}
	for _, want := range []string{"is now unassigned", "goes on working", "hz service assign app"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output does not say %q:\n%s", want, out)
		}
	}
	if strings.Join(stub.posts, ",") != "/api/v1/services/assign" {
		t.Fatalf("the CLI posted %v; the server owns the config", stub.posts)
	}
}

// TestAssignRefusalReachesTheOperator is the error that matters, asserted where
// the operator actually reads it: through the client, off the wire, as the text
// `hz` prints. The message has to name the missing record AND the command that
// declares it — declare-then-assign is not obvious, so "does not exist" alone
// leaves the operator with nothing to type.
func TestAssignRefusalReachesTheOperator(t *testing.T) {
	stub := assignFixture(t)
	c := stub.start(t)

	err := captureStdoutErr(t, func() error {
		return runService(c, []string{"assign", "git", "ebb"})
	})
	if err == nil {
		t.Fatal("assigning to a project nobody declared must fail")
	}
	for _, want := range []string{`"ebb"`, "hz project add ebb", "redline"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the project refusal does not mention %q:\n%s", want, err)
		}
	}

	err = captureStdoutErr(t, func() error {
		return runService(c, []string{"assign", "git", "redline/canary"})
	})
	if err == nil {
		t.Fatal("assigning to a rung nobody declared must fail")
	}
	for _, want := range []string{"canary", "hz env add redline/canary", "--posture", "staging"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the environment refusal does not mention %q:\n%s", want, err)
		}
	}

	if svc := serviceInStub(t, stub, "git"); svc.Project != "" || svc.Environment != "" {
		t.Fatalf("a refused assign wrote %q/%q", svc.Project, svc.Environment)
	}
}

// TestAssignArgumentShapes: the address is parsed before anything is sent, so a
// typo is answered by the command rather than by the server.
func TestAssignArgumentShapes(t *testing.T) {
	stub := assignFixture(t)
	c := stub.start(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", []string{"assign"}, "usage: hz service assign"},
		{"only a service", []string{"assign", "git"}, "usage: hz service assign"},
		{"too many", []string{"assign", "git", "redline", "extra"}, "usage: hz service assign"},
		{"empty project", []string{"assign", "git", "/staging"}, "names no project"},
		{"trailing slash", []string{"assign", "git", "redline/"}, "trailing slash"},
		{"unassign with no service", []string{"unassign"}, "usage: hz service unassign"},
		{"unassign with two", []string{"unassign", "git", "app"}, "usage: hz service unassign"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := captureStdoutErr(t, func() error { return runService(c, tc.args) })
			if err == nil {
				t.Fatalf("%v should have been refused", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal does not mention %q:\n%s", tc.want, err)
			}
		})
	}
	// None of them reached the server.
	if len(stub.posts) != 0 {
		t.Fatalf("a malformed address was sent anyway: %v", stub.posts)
	}
}

// TestServiceShowNamesTheAssignment: the read surface has carried the
// assignment since projects landed and this screen never showed it, so a
// service that WAS assigned looked identical to one that was not.
func TestServiceShowNamesTheAssignment(t *testing.T) {
	stub := assignFixture(t)
	c := stub.start(t)

	out := captureStdout(t, func() {
		if err := runService(c, []string{"show", "app"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Project:  redline/staging") {
		t.Fatalf("show does not name the assignment:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runService(c, []string{"show", "git"}); err != nil {
			t.Fatal(err)
		}
	})
	// "none" is an answer; a missing line is a question.
	for _, want := range []string{"Project:", "unassigned", "hz service assign"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show does not say %q for an unassigned service:\n%s", want, out)
		}
	}
}
