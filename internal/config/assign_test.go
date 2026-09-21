package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assignFixture is a declared tree with one service in it and one outside, so
// every case below is a move rather than a first assignment.
func assignFixture() *Config {
	return &Config{
		Projects: []Project{
			{Name: "iodesystems"},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
		},
		Services: []Service{
			{Name: "app", Domains: []string{"app.example.com"}, Project: "redline", Environment: "staging"},
			{Name: "git", Domains: []string{"git.example.com"}},
		},
	}
}

func serviceNamed(t *testing.T, c *Config, name string) Service {
	t.Helper()
	for _, s := range c.Services {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no service %q in the config", name)
	return Service{}
}

// TestAssignServicePlacesAndMoves is the operation the tree had no writer for:
// a service created after an import could never join a project, and one the
// import placed could never be moved.
func TestAssignServicePlacesAndMoves(t *testing.T) {
	cfg := assignFixture()

	got, err := cfg.AssignService("git", "redline", "prod")
	if err != nil {
		t.Fatalf("assigning git to a declared rung: %v", err)
	}
	if got.Project != "redline" || got.Environment != "prod" {
		t.Fatalf("the writer answered %q/%q", got.Project, got.Environment)
	}
	if svc := serviceNamed(t, cfg, "git"); svc.Project != "redline" || svc.Environment != "prod" {
		t.Fatalf("the config holds %q/%q", svc.Project, svc.Environment)
	}

	// A move, not just a first placement.
	if _, err := cfg.AssignService("app", "redline", "prod"); err != nil {
		t.Fatalf("moving app between rungs: %v", err)
	}
	if svc := serviceNamed(t, cfg, "app"); svc.Environment != "prod" {
		t.Fatalf("app did not move: %+v", svc)
	}
}

// TestAssignProjectWithoutARungIsLegal pins the shape the import planner
// already produces and the validators already permit: plenty of services are
// not on a ladder, and requiring a rung would force a fake one.
func TestAssignProjectWithoutARungIsLegal(t *testing.T) {
	cfg := assignFixture()
	if err := cfg.CheckAssignment("git", "iodesystems", ""); err != nil {
		t.Fatalf("a project with no environment must be legal: %v", err)
	}
	got, err := cfg.AssignService("git", "iodesystems", "")
	if err != nil {
		t.Fatalf("assigning to a project with no rung: %v", err)
	}
	if got.Project != "iodesystems" || got.Environment != "" {
		t.Fatalf("got %q/%q", got.Project, got.Environment)
	}

	// And it saves, which is the only claim that matters: Save runs the
	// validators that would refuse it.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("a service in a project and on no rung must SAVE: %v", err)
	}
}

// TestUnassignIsExpressible pins that clearing is a real operation and not an
// accident of zero values: both fields empty takes the service out of the tree,
// and the service is otherwise untouched.
func TestUnassignIsExpressible(t *testing.T) {
	cfg := assignFixture()
	got, err := cfg.AssignService("app", "", "")
	if err != nil {
		t.Fatalf("unassigning: %v", err)
	}
	if got.Project != "" || got.Environment != "" {
		t.Fatalf("unassign left %q/%q", got.Project, got.Environment)
	}
	svc := serviceNamed(t, cfg, "app")
	if svc.Project != "" || svc.Environment != "" {
		t.Fatalf("the config still holds %q/%q", svc.Project, svc.Environment)
	}
	// The rest of the record is the point: an unassigned service keeps working.
	if len(svc.Domains) != 1 || svc.Domains[0] != "app.example.com" {
		t.Fatalf("unassign touched the service's domains: %+v", svc.Domains)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("an unassigned service must SAVE — it is the legacy state: %v", err)
	}
}

// TestAssignToUndeclaredProjectNamesTheCommand is the error that matters.
//
// Declare-then-assign is not obvious, so this is the mistake an operator will
// actually make, and Save's own words for it — `service "x" names project "y",
// which does not exist` — are true and useless: they say what is wrong and not
// what to do. The refusal has to name the missing project AND the command that
// declares it.
func TestAssignToUndeclaredProjectNamesTheCommand(t *testing.T) {
	cfg := assignFixture()
	_, err := cfg.AssignService("git", "ebb", "")
	if err == nil {
		t.Fatal("assigning to a project nobody declared must be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		`"git"`,          // which service
		`"ebb"`,          // which project is missing
		"hz project add", // and how to make it
		"ebb",
		"redline", // ... plus what would have worked
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not mention %q:\n%s", want, msg)
		}
	}
	// A refused assignment writes nothing.
	if svc := serviceNamed(t, cfg, "git"); svc.Project != "" {
		t.Fatalf("a refused assign wrote %q anyway", svc.Project)
	}
}

// TestAssignToUndeclaredEnvironmentNamesTheCommand is the same bar for the rung.
func TestAssignToUndeclaredEnvironmentNamesTheCommand(t *testing.T) {
	cfg := assignFixture()
	_, err := cfg.AssignService("git", "redline", "canary")
	if err == nil {
		t.Fatal("assigning to a rung nobody declared must be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		`"git"`,
		`"canary"`,
		"hz env add redline/canary",
		"--posture",
		"staging", // the rungs that do exist
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not mention %q:\n%s", want, msg)
		}
	}
	if svc := serviceNamed(t, cfg, "git"); svc.Environment != "" {
		t.Fatalf("a refused assign wrote %q anyway", svc.Environment)
	}
}

// TestEnvironmentWithoutAProjectIsRefused pins the one combination that is not
// legal. ValidateEnvironments deliberately does not look at a project-less
// service's environment, so storing one would leave a field that silently means
// nothing — the refusal has to happen here or not at all.
func TestEnvironmentWithoutAProjectIsRefused(t *testing.T) {
	cfg := assignFixture()
	err := cfg.CheckAssignment("git", "", "prod")
	if err == nil {
		t.Fatal("an environment with no project must be refused")
	}
	for _, want := range []string{"unique per project", "hz service assign", "hz service unassign"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q:\n%s", want, err)
		}
	}

	// Proof that Save would NOT have caught it — which is why the check is here.
	stored := assignFixture()
	stored.Services[1].Environment = "prod"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, stored); err != nil {
		t.Fatalf("Save unexpectedly refuses a project-less environment now: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if serviceNamed(t, back, "git").Environment != "prod" {
		t.Fatal("the fixture did not reproduce the silently-meaningless field")
	}
	_ = os.Remove(path)
}

// TestAssignUnknownServiceNamesTheReadCommand: the service is the one thing the
// caller cannot mistype into a legal state, so the refusal points at the list.
func TestAssignUnknownServiceNamesTheReadCommand(t *testing.T) {
	cfg := assignFixture()
	_, err := cfg.AssignService("nope", "redline", "")
	if err == nil {
		t.Fatal("assigning a service that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "hz service list") {
		t.Fatalf("the refusal does not say how to find the name:\n%s", err)
	}
}

// TestAssignDoesNotShareBackingArrays mirrors the reason copyForWrite exists:
// a Config is copied shallowly in several places, so a writer that wrote through
// the existing backing array would mutate the config another goroutine is still
// serving.
func TestAssignDoesNotShareBackingArrays(t *testing.T) {
	cfg := assignFixture()
	served := *cfg // the shallow copy a reader is holding

	if _, err := cfg.AssignService("git", "redline", "prod"); err != nil {
		t.Fatal(err)
	}
	if served.Services[1].Project != "" {
		t.Fatalf("the assign wrote through a shared array: the reader now sees %q",
			served.Services[1].Project)
	}
}
