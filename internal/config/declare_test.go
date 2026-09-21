package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// tree is the shape plan/architecture.md's walkthrough describes: a root that
// declares the feed, a child that declares its own rungs, and a service on one
// of them.
func tree() *Config {
	return &Config{
		Projects: []Project{
			{Name: "iodesystems", Feed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main"}},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []Environment{
			{Project: "redline", Name: "staging", Posture: "staging", Version: "1.2.3"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Version: "1.2.1"},
		},
		Services: []Service{
			{Name: "app", Project: "redline", Environment: "staging"},
			{Name: "grafana"},
		},
	}
}

// TestDeclareThenAssignEndToEnd is the acceptance walkthrough, in the order the
// walkthrough gives it and with NO hand-edited JSON: declare the root, declare
// the child under it, declare the child's two rungs, THEN assign a service, then
// Save.
//
// The order is the whole test. Save runs ValidateProjects and
// ValidateEnvironments, so a service naming a project or a rung nobody declared
// is refused — which is what legacy_compat_test.go pins from the other side.
// Before these writers existed the declare half had no command at all, so the
// only way to reach this state was an editor on config.json.
func TestDeclareThenAssignEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{
		Services: []Service{{Name: "app", Domains: []string{"app.<our-domain>"}}},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("a config with no tree must save: %v", err)
	}

	// 1. the root, which declares the feed and may never hold a service.
	if err := cfg.AddProject("iodesystems", ""); err != nil {
		t.Fatalf("add root: %v", err)
	}
	// 2. the child, which inherits the feed and declares its own rungs.
	if err := cfg.AddProject("redline", "iodesystems"); err != nil {
		t.Fatalf("add child: %v", err)
	}
	if err := cfg.AddEnvironment(Environment{
		Project: "redline", Name: "staging", Posture: "staging", Version: "1.2.3",
	}); err != nil {
		t.Fatalf("add staging: %v", err)
	}
	if err := cfg.AddEnvironment(Environment{
		Project: "redline", Name: "prod", Posture: "prod", From: "staging",
	}); err != nil {
		t.Fatalf("add prod: %v", err)
	}

	// 3. only NOW may a service name them.
	cfg.Services[0].Project = "redline"
	cfg.Services[0].Environment = "staging"

	if err := Save(path, cfg); err != nil {
		t.Fatalf("declare-then-assign must SAVE — this is the walkthrough: %v", err)
	}

	// Positive control on the persistence, not just on the in-memory struct: a
	// writer that only touched the copy would pass every assertion above.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"iodesystems", "redline", "staging", "prod"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("saved config does not mention %q:\n%s", want, raw)
		}
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Projects) != 2 || len(back.Environments) != 2 {
		t.Fatalf("round trip lost records: %d project(s), %d environment(s)", len(back.Projects), len(back.Environments))
	}
	if back.Projects[1].Parent != "iodesystems" {
		t.Fatalf("redline's parent came back %q", back.Projects[1].Parent)
	}
	if back.Services[0].Project != "redline" || back.Services[0].Environment != "staging" {
		t.Fatalf("assignment came back %q/%q", back.Services[0].Project, back.Services[0].Environment)
	}

	// And the reverse order is still refused, so the test above proves an
	// ORDERING and not merely that Save accepts anything.
	stray := &Config{Services: []Service{{Name: "app", Project: "redline", Environment: "staging"}}}
	if err := Save(filepath.Join(t.TempDir(), "c.json"), stray); err == nil {
		t.Fatal("assigning before declaring must still be refused")
	}
}

func TestAddProjectRefusesDuplicateAndMissingParent(t *testing.T) {
	cfg := tree()
	if err := cfg.AddProject("redline", ""); err == nil {
		t.Fatal("a duplicate project name must be refused")
	}
	err := cfg.AddProject("veliode", "nope")
	if err == nil {
		t.Fatal("a parent that does not exist must be refused")
	}
	// The error has to list what WOULD have worked; "no project nope" alone
	// reads as a typo when the answer is which names exist.
	if !strings.Contains(err.Error(), "iodesystems") || !strings.Contains(err.Error(), "redline") {
		t.Fatalf("the refusal must name the declared projects, got: %v", err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("a refused add must write nothing, got %d project(s)", len(cfg.Projects))
	}
}

func TestAddProjectHintWhenNothingIsDeclared(t *testing.T) {
	cfg := &Config{}
	err := cfg.AddProject("redline", "iodesystems")
	if err == nil {
		t.Fatal("a parent that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "no projects at all") {
		t.Fatalf("with an empty tree the refusal must say so, got: %v", err)
	}
}

// TestAddEnvironmentRefusesAPostureNothingRanks is the closed-set rule.
// PostureRank returns -1 for anything outside Postures, and -1 compares below
// dev against every rung — so an unranked posture would make every promotion
// into that environment look upward.
func TestAddEnvironmentRefusesAPostureNothingRanks(t *testing.T) {
	cfg := tree()
	for _, bad := range []string{"", "production", "PROD", "qa"} {
		err := cfg.AddEnvironment(Environment{Project: "redline", Name: "qa", Posture: bad})
		if err == nil {
			t.Fatalf("posture %q must be refused", bad)
		}
		for _, p := range Postures {
			if !strings.Contains(err.Error(), p) {
				t.Fatalf("the refusal for %q must list %q; got: %v", bad, p, err)
			}
		}
		// It has to be THIS check that fires, not validateModel's backstop.
		// Deleting checkPosture leaves the rejection intact — ValidateEnvironments
		// rejects an unranked posture too, and also lists the three — so without
		// a phrase only checkPosture produces, this test passes over a dead
		// branch. (Found by sabotaging the rank check and watching it stay green.)
		want := "the three are the ladder"
		if bad == "" {
			want = "a posture is required"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("posture %q was refused by the validator, not by checkPosture: %v", bad, err)
		}
	}
	if len(cfg.Environments) != 2 {
		t.Fatalf("a refused add must write nothing, got %d", len(cfg.Environments))
	}
	// The positive control: the three that ARE ranked go in.
	for i, good := range Postures {
		if err := cfg.AddEnvironment(Environment{Project: "redline", Name: "rung" + good, Posture: good}); err != nil {
			t.Fatalf("posture %q (rank %d) must be accepted: %v", good, i, err)
		}
	}
}

func TestAddEnvironmentNeedsItsProjectFirst(t *testing.T) {
	cfg := tree()
	err := cfg.AddEnvironment(Environment{Project: "veliode", Name: "dev", Posture: "dev"})
	if err == nil {
		t.Fatal("a rung in a project that does not exist must be refused")
	}
	if !strings.Contains(err.Error(), "hz project add veliode") {
		t.Fatalf("the refusal must name the command that fixes it, got: %v", err)
	}
}

// TestFromMustNameARungOfTheSameProject covers the check that exists purely to
// beat the validator's message. ValidateEnvironments also rejects these, but it
// can only say the name does not exist; the useful answer is which names do.
func TestFromMustNameARungOfTheSameProject(t *testing.T) {
	cfg := tree()
	if err := cfg.AddProject("veliode", ""); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddEnvironment(Environment{Project: "veliode", Name: "beta", Posture: "staging"}); err != nil {
		t.Fatal(err)
	}

	// Another project's rung, spelled bare.
	err := cfg.AddEnvironment(Environment{Project: "redline", Name: "qa", Posture: "dev", From: "beta"})
	if err == nil {
		t.Fatal("--from naming another project's rung must be refused")
	}
	if !strings.Contains(err.Error(), "staging") || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("the refusal must list the rungs of redline, got: %v", err)
	}

	// Another project's rung, spelled qualified.
	err = cfg.AddEnvironment(Environment{Project: "redline", Name: "qa", Posture: "dev", From: "veliode/beta"})
	if err == nil {
		t.Fatal("--from naming another PROJECT must be refused")
	}
	if !strings.Contains(err.Error(), "within one project") {
		t.Fatalf("the refusal must say promotion is within one project, got: %v", err)
	}

	// The qualified spelling of a rung in THIS project is accepted and stored
	// bare — From is a name within the project, not an address.
	if err := cfg.AddEnvironment(Environment{
		Project: "redline", Name: "qa", Posture: "dev", From: "redline/staging",
	}); err != nil {
		t.Fatalf("redline/staging names a rung of redline and must be accepted: %v", err)
	}
	for _, e := range cfg.Environments {
		if e.Name == "qa" && e.From != "staging" {
			t.Fatalf("From stored as %q, want the bare name", e.From)
		}
	}
}

// TestSetEnvironmentLeavesUnnamedFieldsAlone is the patch contract: bumping a
// version must not drop a promotion edge, which is what a whole-record write
// would do to any caller that did not restate it.
func TestSetEnvironmentLeavesUnnamedFieldsAlone(t *testing.T) {
	cfg := tree()
	got, err := cfg.SetEnvironment("redline", "prod", EnvironmentPatch{Version: ptr("1.2.3")})
	if err != nil {
		t.Fatalf("set version: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("version is %q", got.Version)
	}
	if got.From != "staging" {
		t.Fatalf("bumping the version dropped the promotion edge: From is %q", got.From)
	}
	if got.Posture != "prod" {
		t.Fatalf("bumping the version changed the posture to %q", got.Posture)
	}

	// An empty non-nil value CLEARS, which is the other half of the contract —
	// without it a promotion edge could never be removed.
	got, err = cfg.SetEnvironment("redline", "prod", EnvironmentPatch{From: ptr("")})
	if err != nil {
		t.Fatalf("clear from: %v", err)
	}
	if got.From != "" {
		t.Fatalf("--from \"\" left From as %q", got.From)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("clearing From lost the version: %q", got.Version)
	}

	if _, err := cfg.SetEnvironment("redline", "prod", EnvironmentPatch{}); err == nil {
		t.Fatal("an empty patch must be refused rather than reported as a successful no-op")
	}
	if _, err := cfg.SetEnvironment("redline", "nope", EnvironmentPatch{Version: ptr("9")}); err == nil {
		t.Fatal("setting a rung that does not exist must be refused")
	}
	if _, err := cfg.SetEnvironment("redline", "prod", EnvironmentPatch{Posture: ptr("qa")}); err == nil {
		t.Fatal("setting an unranked posture must be refused")
	}
}

// TestSetEnvironmentLetsTheValidatorCatchACycle checks the division of labour:
// the CLI and these writers do not reimplement the cycle walk, and a cycle is
// still refused — by ValidateEnvironments, through validateModel.
func TestSetEnvironmentLetsTheValidatorCatchACycle(t *testing.T) {
	cfg := tree()
	// staging already has prod promoting from it; pointing staging back at prod
	// closes the loop.
	_, err := cfg.SetEnvironment("redline", "staging", EnvironmentPatch{From: ptr("prod")})
	if err == nil {
		t.Fatal("a promotion cycle must be refused")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("the refusal must say cycle, got: %v", err)
	}
	for _, e := range cfg.Environments {
		if e.Name == "staging" && e.From != "" {
			t.Fatalf("a refused set must write nothing; staging.From is %q", e.From)
		}
	}
}

// TestRemoveProjectRefusesAndNamesTheDependants is the rm decision. Refusing is
// right because the alternative leaves a config Save rejects, and the operator
// meets that as a validation error about a record they never mentioned.
func TestRemoveProjectRefusesAndNamesTheDependants(t *testing.T) {
	cfg := tree()
	_, err := cfg.RemoveProject("redline", false)
	if err == nil {
		t.Fatal("removing a project with rungs and a service must be refused")
	}
	for _, want := range []string{"redline/staging", "redline/prod", "app"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q; got:\n%v", want, err)
		}
	}
	if len(cfg.Projects) != 2 {
		t.Fatal("a refused removal must write nothing")
	}

	// The parent is refused too, for its child.
	_, err = cfg.RemoveProject("iodesystems", false)
	if err == nil || !strings.Contains(err.Error(), "redline") {
		t.Fatalf("removing a parent must be refused naming the child, got: %v", err)
	}

	// And a project nothing points at goes, with no cascade needed.
	if err := cfg.AddProject("empty", ""); err != nil {
		t.Fatal(err)
	}
	removed, err := cfg.RemoveProject("empty", false)
	if err != nil {
		t.Fatalf("removing an empty project: %v", err)
	}
	if len(removed) != 1 || removed[0].Name != "empty" {
		t.Fatalf("removed %+v, want just empty", removed)
	}
	if cfg.hasProject("empty") {
		t.Fatal("empty is still declared")
	}
}

// TestRemoveProjectCascadeTakesTheSubtreeAndListsIt pins that cascade is
// exhaustive AND honest: everything it took is in the returned list, including
// the services it left unassigned, which are a change to a record the operator
// did not name.
func TestRemoveProjectCascadeTakesTheSubtreeAndListsIt(t *testing.T) {
	cfg := tree()
	removed, err := cfg.RemoveProject("iodesystems", true)
	if err != nil {
		t.Fatalf("cascade: %v", err)
	}

	got := map[string]string{}
	for _, d := range removed {
		got[d.Name] = d.Kind
	}
	for name, kind := range map[string]string{
		"iodesystems":     "project",
		"redline":         "project",
		"redline/staging": "environment",
		"redline/prod":    "environment",
		"app":             "service",
	} {
		if got[name] != kind {
			t.Fatalf("cascade did not report %s %s; reported %+v", kind, name, removed)
		}
	}

	if len(cfg.Projects) != 0 || len(cfg.Environments) != 0 {
		t.Fatalf("cascade left %d project(s) and %d rung(s)", len(cfg.Projects), len(cfg.Environments))
	}
	if cfg.Services[0].Project != "" || cfg.Services[0].Environment != "" {
		t.Fatalf("app was left assigned to %q/%q", cfg.Services[0].Project, cfg.Services[0].Environment)
	}
	// Unassigned is legal, so the result has to be saveable — that is the whole
	// reason cascade unassigns rather than deleting the service.
	if err := Save(filepath.Join(t.TempDir(), "c.json"), cfg); err != nil {
		t.Fatalf("a cascaded removal must leave a saveable config: %v", err)
	}
}

func TestRemoveEnvironmentRefusesForServicesAndEdges(t *testing.T) {
	cfg := tree()
	_, err := cfg.RemoveEnvironment("redline", "staging", false)
	if err == nil {
		t.Fatal("removing a rung with a service on it must be refused")
	}
	if !strings.Contains(err.Error(), "app") {
		t.Fatalf("the refusal must name the service, got: %v", err)
	}
	// prod promotes FROM staging, so that edge blocks too — cutting it silently
	// would remove the authority statement, not a detail.
	if !strings.Contains(err.Error(), "redline/prod") {
		t.Fatalf("the refusal must name the rung that promotes from it, got: %v", err)
	}

	// prod itself has neither, so it goes.
	if _, err := cfg.RemoveEnvironment("redline", "prod", false); err != nil {
		t.Fatalf("removing an unreferenced rung: %v", err)
	}
	if cfg.hasEnvironment("redline", "prod") {
		t.Fatal("prod is still declared")
	}
	if _, _, err := cfg.EnvironmentRemoval("redline", "nope", false); err == nil {
		t.Fatal("removing a rung that does not exist must be refused")
	}
}

func TestRemoveEnvironmentCascadeClearsEdgesAndServices(t *testing.T) {
	cfg := tree()
	removed, err := cfg.RemoveEnvironment("redline", "staging", true)
	if err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if len(removed) != 3 {
		t.Fatalf("cascade reported %+v, want the rung, the edge and the service", removed)
	}
	for _, e := range cfg.Environments {
		if e.Name == "prod" && e.From != "" {
			t.Fatalf("prod still promotes from %q", e.From)
		}
	}
	if cfg.Services[0].Environment != "" {
		t.Fatalf("app is still on %q", cfg.Services[0].Environment)
	}
	// It keeps its project: a rung going away does not take the service out of
	// the project it is in.
	if cfg.Services[0].Project != "redline" {
		t.Fatalf("app lost its project: %q", cfg.Services[0].Project)
	}
	if err := Save(filepath.Join(t.TempDir(), "c.json"), cfg); err != nil {
		t.Fatalf("a cascaded removal must leave a saveable config: %v", err)
	}
}

// TestWritersNeverLeaveAConfigSaveWouldRefuse is the guard that makes the
// layering worth having: every writer validates the whole tree before it
// returns, so no sequence of them can produce a config the one chokepoint
// rejects.
func TestWritersNeverLeaveAConfigSaveWouldRefuse(t *testing.T) {
	cfg := tree()
	path := filepath.Join(t.TempDir(), "config.json")

	steps := []func() error{
		func() error { return cfg.AddProject("veliode", "iodesystems") },
		func() error { return cfg.AddEnvironment(Environment{Project: "veliode", Name: "dev", Posture: "dev"}) },
		func() error {
			return cfg.AddEnvironment(Environment{Project: "veliode", Name: "beta", Posture: "staging", From: "dev"})
		},
		func() error {
			_, err := cfg.SetEnvironment("veliode", "beta", EnvironmentPatch{Version: ptr("0.9")})
			return err
		},
		func() error { _, err := cfg.RemoveEnvironment("veliode", "beta", true); return err },
		func() error { _, err := cfg.RemoveProject("veliode", true); return err },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if err := Save(path, cfg); err != nil {
			t.Fatalf("after step %d the config no longer saves: %v", i, err)
		}
	}
}

// TestWritersDoNotMutateTheConfigTheyWereGivenOnFailure pins the copy-on-write
// rule ApplyImport states: a Config is copied shallowly in several places, so a
// writer that wrote through the existing backing array would mutate the config
// another goroutine is still serving.
func TestWritersDoNotMutateTheConfigTheyWereGivenOnFailure(t *testing.T) {
	cfg := tree()
	shallow := *cfg // what s.cfg() hands a handler

	if err := cfg.AddProject("redline", ""); err == nil {
		t.Fatal("duplicate must fail")
	}
	if _, err := cfg.RemoveProject("redline", false); err == nil {
		t.Fatal("blocked removal must fail")
	}
	if len(shallow.Projects) != 2 || len(shallow.Environments) != 2 {
		t.Fatal("a failed write changed the shallow copy's slices")
	}

	// And a SUCCESSFUL write must replace the slice rather than write through
	// it, so the older copy still reads what it read.
	if err := cfg.AddProject("veliode", ""); err != nil {
		t.Fatal(err)
	}
	if len(shallow.Projects) != 2 {
		t.Fatalf("the older copy now sees %d project(s); the write went through the shared array", len(shallow.Projects))
	}
	if _, err := cfg.RemoveEnvironment("redline", "prod", false); err != nil {
		t.Fatal(err)
	}
	if len(shallow.Environments) != 2 || shallow.Environments[1].Name != "prod" {
		t.Fatalf("the older copy's environments were rewritten: %+v", shallow.Environments)
	}
}
