package config

import (
	"reflect"
	"strings"
	"testing"
)

// THE FOUR THINGS THAT MUST BE TRUE ABOUT A MACHINE.
//
// The first two are absences, and an absence is the hardest thing to keep: a
// field nobody added is indistinguishable from a field somebody removed, and a
// year from now "why has a machine no project?" is a reasonable question with a
// wrong obvious answer. So they are asserted by reflection over the struct
// rather than left to the comment that explains them.

// A machine has no project and no environment. plan/design/architecture.md: "an
// environment never modifies a machine; it is a coordinate of an instance." The
// gateway in plan/design/example-projection.md §3 hosts instances from two projects, so
// a Project field would be false for that row on the day it was added.
func TestAMachineCarriesNoProjectAndNoEnvironment(t *testing.T) {
	forbidden := []string{"project", "environment", "env", "posture", "rung"}
	tp := reflect.TypeOf(Machine{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.ToLower(tp.Field(i).Name)
		for _, bad := range forbidden {
			if name == bad {
				t.Fatalf("Machine has a %s field. An environment is a coordinate of an INSTANCE; "+
					"one machine hosts instances from several projects, so this field is false for the gateway "+
					"the moment it exists (plan/design/architecture.md, \"Instance, not machine, carries the environment\")",
					tp.Field(i).Name)
			}
		}
	}
}

// Nor an observed version. That settled onto the registration in migration 0011
// because it belongs to an instance and several instances share a box — a
// machine-level column reports a half-finished rollout as finished.
func TestAMachineCarriesNoObservedVersion(t *testing.T) {
	tp := reflect.TypeOf(Machine{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.ToLower(tp.Field(i).Name)
		if strings.Contains(name, "version") || strings.Contains(name, "observed") {
			t.Fatalf("Machine has a %s field. The observed version belongs to an INSTANCE (migration 0011); "+
				"several instances share a box, so a machine-level one reports a half-finished rollout as finished",
				tp.Field(i).Name)
		}
	}
}

// Multi-segment is legal — ci-1 in plan/design/example-projection.md §3 is in two on
// purpose — and it is visible: the record says so, and the config can enumerate
// every machine that bridges.
func TestAMultiSegmentMachineIsLegalAndListable(t *testing.T) {
	c := &Config{}
	if err := c.AddMachine(Machine{Name: "app-1", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{
		Name:     "ci-1",
		Segments: []string{"seg:intern", "seg:storefront"},
		Note:     "publishes packages, deploys storefront",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "an-1", Segments: []string{"seg:analytics"}}); err != nil {
		t.Fatal(err)
	}

	bridges := c.MultiSegmentMachines()
	if len(bridges) != 1 || bridges[0].Name != "ci-1" {
		t.Fatalf("the multi-homed enumeration came back %+v", bridges)
	}
	if !bridges[0].MultiHomed() {
		t.Fatal("a machine in two segments does not report itself multi-homed")
	}
	// Blast radius is the union, so both names have to survive the round trip.
	if got := strings.Join(bridges[0].Segments, ","); got != "seg:intern,seg:storefront" {
		t.Fatalf("the union is not enumerable: %q", got)
	}
	// And a single-segment machine is not swept into the list.
	if single, _ := c.FindMachine("app-1"); single.MultiHomed() {
		t.Fatal("a machine in one segment reported itself multi-homed")
	}
}

// A machine that bridges segments has to say why. architecture.md: "bad design
// but possible" becomes "possible, visible, and it has to be explained" — and a
// reason nobody is obliged to give is a reason nobody gives.
func TestABridgingMachineMustSayWhy(t *testing.T) {
	c := &Config{}
	err := c.AddMachine(Machine{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"}})
	if err == nil {
		t.Fatal("a machine was declared in two segments with no reason")
	}
	for _, want := range []string{"ci-1", "seg:intern", "seg:storefront", "--note"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if len(c.Machines) != 0 {
		t.Fatal("a refused declaration was written anyway")
	}

	// One segment needs no explanation.
	if err := c.AddMachine(Machine{Name: "app-1", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatalf("a single-segment machine was refused: %v", err)
	}
}

// Validated on Save, beside the other four validators. Hand-edited JSON is the
// path that skips AddMachine, so the chokepoint has to catch it too.
func TestSaveRefusesABadMachine(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"no name": {Machines: []Machine{{Name: " "}}},
		"declared twice": {Machines: []Machine{
			{Name: "app-1", Segments: []string{"a"}},
			{Name: "app-1", Segments: []string{"b"}},
		}},
		"a segment with no name": {Machines: []Machine{{Name: "app-1", Segments: []string{"a", " "}}}},
		"the same segment twice": {Machines: []Machine{{Name: "app-1", Segments: []string{"a", "a"}}}},
		"bridging with no reason": {Machines: []Machine{
			{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"}},
		}},
	} {
		if err := cfg.ValidateMachines(); err == nil {
			t.Fatalf("%s: ValidateMachines accepted it", name)
		}
		if err := Save(t.TempDir()+"/config.json", cfg); err == nil {
			t.Fatalf("%s: Save wrote it", name)
		}
	}

	// The positive control: the valid shape of every one of those is accepted,
	// so the test above is not passing because Save refuses everything.
	ok := &Config{Machines: []Machine{
		{Name: "app-1", Segments: []string{"seg:storefront"}},
		{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"}, Note: "deploys both"},
		{Name: "no-segments-yet"},
	}}
	if err := ok.ValidateMachines(); err != nil {
		t.Fatalf("a valid machine set was refused: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", ok); err != nil {
		t.Fatalf("Save refused a valid machine set: %v", err)
	}
}

// A second machine cannot take an existing name, because the name is the key
// its agent credential is stored under: two records answering to one credential
// is an identity hz cannot resolve.
func TestAMachineNameIsTakenOnce(t *testing.T) {
	c := &Config{}
	if err := c.AddMachine(Machine{Name: "gw-1"}); err != nil {
		t.Fatal(err)
	}
	err := c.AddMachine(Machine{Name: "gw-1", Segments: []string{"seg:other"}})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate machine name was accepted: %v", err)
	}
	if len(c.Machines) != 1 {
		t.Fatalf("the config holds %d machines", len(c.Machines))
	}
}

// Removal refuses while the machine's agent credential exists, and NAMES it —
// the convention `hz project rm` and `hz env rm` set. A credential left behind
// authenticates for a machine hz no longer declares.
func TestRemovingAnEnrolledMachineIsRefusedAndNamesTheCredential(t *testing.T) {
	c := &Config{}
	if err := c.AddMachine(Machine{Name: "app-1", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}

	removes, blocked, err := c.MachineRemoval("app-1", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Kind != "credential" || blocked[0].Name != "app-1" {
		t.Fatalf("the enrolment did not block the removal: %+v", blocked)
	}
	if !strings.Contains(blocked[0].How, "authenticate") {
		t.Fatalf("the blocker does not say what is wrong: %q", blocked[0].How)
	}
	if len(removes) != 1 {
		t.Fatalf("a blocked removal proposed to remove %d things", len(removes))
	}
	if _, err := c.RemoveMachine("app-1", true, false); err == nil {
		t.Fatal("an enrolled machine was removed without cascade")
	}
	if _, ok := c.FindMachine("app-1"); !ok {
		t.Fatal("a refused removal removed the machine anyway")
	}

	// Cascade lists the credential as something it TAKES, and goes through.
	removes, blocked, err = c.MachineRemoval("app-1", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("cascade still blocked: %+v", blocked)
	}
	if len(removes) != 2 || removes[1].Kind != "credential" {
		t.Fatalf("cascade did not list the credential it takes: %+v", removes)
	}
	if !strings.Contains(removes[1].How, "REVOKED") {
		t.Fatalf("cascade does not say the credential is revoked: %q", removes[1].How)
	}
	if _, err := c.RemoveMachine("app-1", true, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindMachine("app-1"); ok {
		t.Fatal("cascade left the machine behind")
	}
}

// An unenrolled machine has nothing depending on it and comes straight out —
// the positive control for the refusal above.
func TestRemovingAnUnenrolledMachineNeedsNoCascade(t *testing.T) {
	c := &Config{}
	if err := c.AddMachine(Machine{Name: "spare"}); err != nil {
		t.Fatal(err)
	}
	removes, err := c.RemoveMachine("spare", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removes) != 1 || removes[0].Name != "spare" {
		t.Fatalf("removal reported %+v", removes)
	}
	if len(c.Machines) != 0 {
		t.Fatal("the machine is still declared")
	}
}

// Removing something that was never declared is an error that lists what is,
// the way every other removal answers it.
func TestRemovingAnUndeclaredMachineListsTheDeclaredOnes(t *testing.T) {
	c := &Config{}
	if _, _, err := c.MachineRemoval("ghost", false, false); err == nil ||
		!strings.Contains(err.Error(), "declares no machines at all") {
		t.Fatalf("an empty config's refusal should say so: %v", err)
	}
	if err := c.AddMachine(Machine{Name: "gw-1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.MachineRemoval("ghost", false, false)
	if err == nil || !strings.Contains(err.Error(), "gw-1") {
		t.Fatalf("the refusal does not list what exists: %v", err)
	}
}

// Declaring a machine leaves the tree alone, and a tree write leaves the
// machines alone. copyForWrite/adopt grew a fourth slice; this is the test that
// catches one of them being forgotten.
func TestMachineWritesAndTreeWritesDoNotEraseEachOther(t *testing.T) {
	c := &Config{}
	if err := c.AddProject("storefront", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "app-1", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddEnvironment(Environment{Project: "storefront", Name: "prod", Posture: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindMachine("app-1"); !ok {
		t.Fatal("declaring an environment dropped the machine")
	}
	if err := c.AddMachine(Machine{Name: "app-2", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 || len(c.Environments) != 1 {
		t.Fatalf("declaring a machine disturbed the tree: %d projects, %d environments",
			len(c.Projects), len(c.Environments))
	}
	if _, err := c.RemoveMachine("app-1", false, false); err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 || len(c.Environments) != 1 {
		t.Fatal("removing a machine disturbed the tree")
	}
}

// A machine record survives a save/load round trip, including the empty-segment
// case that json omitempty could quietly turn into something else.
func TestMachinesRoundTripThroughTheConfigFile(t *testing.T) {
	path := t.TempDir() + "/config.json"
	in := &Config{Machines: []Machine{
		{Name: "gw-1", Segments: []string{"seg:intern", "seg:storefront", "seg:people"}, Note: "the hub; it is hz"},
		{Name: "spare"},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Machines) != 2 {
		t.Fatalf("loaded %d machines", len(out.Machines))
	}
	gw, ok := out.FindMachine("gw-1")
	if !ok || len(gw.Segments) != 3 || gw.Note == "" || !gw.MultiHomed() {
		t.Fatalf("gw-1 came back %+v", gw)
	}
	spare, ok := out.FindMachine("spare")
	if !ok || len(spare.Segments) != 0 || spare.MultiHomed() {
		t.Fatalf("spare came back %+v", spare)
	}
}
