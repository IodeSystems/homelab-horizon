package config

import (
	"strings"
	"testing"
)

// The gateway as a machine in its own model.
//
// THE MEASURED GAP. A live gateway ran hz, fronted 33 services across 8
// projects and 11 environments, and declared ZERO machines — because
// AddMachine's only caller was `hz machine add`, the importer never mentioned
// Machine, and enrolment REFUSES an undeclared machine. So hz could not project
// its own config (projection.Project needs a machine to name), the box hz runs
// on could not be a segment member, and `hz machine ls` was empty on a machine.
//
// THE RULE THAT DID NOT MOVE. Declare-then-enrol: a machine record is an
// operator's assertion, and hz issues a credential only for a machine it was
// told about. If a box could declare itself, anything that reached hz could
// write itself in and then ask for a credential. Nothing below weakens that —
// the gateway is PROPOSED, in a dry run, in an editable file, and deleting the
// row refuses it.

// selfHost is the stand-in for whatever os.Hostname says on the real gateway.
// This package never reads it: the identity is an INPUT (ProposeImportFor), so
// the test supplies one the same way the server supplies the kernel's answer.
const selfHost = "<gw>"

func machineOf(t *testing.T, p ImportPlan, name string) ImportMachine {
	t.Helper()
	for _, m := range p.Machines {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no machine %q proposed; plan proposes %+v", name, p.Machines)
	return ImportMachine{}
}

func signalSaying(p ImportPlan, substr string) bool {
	for _, s := range p.Signals {
		if strings.Contains(s.Detail, substr) {
			return true
		}
	}
	return false
}

// The proposal names the gateway, says why, and proposes NOTHING else about it.
func TestImportProposesTheGatewayAsAMachine(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	plan := cfg.ProposeImportFor(selfHost)

	if len(plan.Machines) != 1 {
		t.Fatalf("want exactly one machine proposed — the gateway — got %+v", plan.Machines)
	}
	m := machineOf(t, plan, selfHost)
	if strings.TrimSpace(m.Reason) == "" {
		t.Fatal("a machine was proposed with no evidence beside it; every row in this plan names its reason")
	}
	// The evidence has to be checkable against something the operator can see,
	// not a stock sentence.
	if !strings.Contains(m.Reason, selfHost) {
		t.Fatalf("the reason does not name the box it is about: %q", m.Reason)
	}

	// NO SEGMENTS INVENTED. ImportMachine carries a name and nothing else, so
	// this is structural rather than a value check — and that is the point:
	// a segment name would have to resolve to a Segment record the file cannot
	// declare, and a machine in no segment is legal.
	if plan.Assignments == nil && plan.Unassigned == nil {
		t.Fatal("the service half of the plan vanished when a machine was proposed")
	}

	// The service that fronts this box stays a service. Said in SIGNALS so the
	// reader who wonders "which service is the gateway then" gets an answer
	// rather than silence.
	if !signalSaying(plan, "no record joining a service to the machine it runs on") {
		t.Fatalf("nothing says a service fronting the gateway is still just a service: %+v", plan.Signals)
	}

	// And the rest of the plan is untouched by any of this.
	if len(plan.Assignments) == 0 || len(plan.Projects) == 0 {
		t.Fatalf("proposing a machine changed the tree proposal: %+v", plan)
	}
}

// A caller with no identity to offer proposes no machine. That is what keeps
// ProposeImport a pure function of the config: this package never asks the
// kernel who it is, because a Machine record is a declaration ABOUT a box and
// never an identity claim by the process reading one.
func TestAPlanWithNoIdentityProposesNoMachine(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	if plan := cfg.ProposeImport(); len(plan.Machines) != 0 {
		t.Fatalf("ProposeImport with no identity proposed %+v", plan.Machines)
	}
	if plan := cfg.ProposeImportFor("   "); len(plan.Machines) != 0 {
		t.Fatalf("a blank identity proposed %+v", plan.Machines)
	}
}

// A config with no services at all still gets the machine. The early return for
// an empty service list is about the TREE — there is nothing to group — and hz
// running on a box is a fact about the box, not about its services.
func TestTheGatewayIsProposedEvenWithNoServices(t *testing.T) {
	plan := (&Config{}).ProposeImportFor(selfHost)
	if len(plan.Machines) != 1 || plan.Machines[0].Name != selfHost {
		t.Fatalf("an empty config proposed %+v", plan.Machines)
	}
}

// ALREADY DECLARED IS A SIGNAL, NOT A ROW. A second import must not look like
// it has work to do.
func TestAnAlreadyDeclaredGatewayIsNotProposedAgain(t *testing.T) {
	cfg := &Config{
		Services: legacyServices(),
		Machines: []Machine{{Name: selfHost}},
	}
	plan := cfg.ProposeImportFor(selfHost)
	if len(plan.Machines) != 0 {
		t.Fatalf("the gateway was proposed again: %+v", plan.Machines)
	}
	// Silence would be indistinguishable from never having looked.
	if !signalSaying(plan, "already declared as a machine") {
		t.Fatalf("hz went quiet instead of saying it looked and found nothing to do: %+v", plan.Signals)
	}
}

// Applying the proposal declares the box, with no segment and no note — the
// state `hz machine add <name>` leaves, which ValidateMachines permits and
// which the projection reads as "this machine is in no segment", not as a gap.
func TestApplyingTheProposalDeclaresTheGateway(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	plan := cfg.ProposeImportFor(selfHost)
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	m, declared := cfg.FindMachine(selfHost)
	if !declared {
		t.Fatalf("the gateway is not declared after an import that proposed it: %+v", cfg.Machines)
	}
	if len(m.Segments) != 0 {
		t.Fatalf("the import invented segments: %+v", m.Segments)
	}
	if m.Note != "" {
		t.Fatalf("the import invented a note: %q — a note is required only of a multi-homed box", m.Note)
	}
	if m.MultiHomed() {
		t.Fatal("a machine with no segments reported itself multi-homed")
	}
	if err := cfg.ValidateMachines(); err != nil {
		t.Fatalf("the config an import wrote does not validate: %v", err)
	}
}

// IDEMPOTENCE. Running the import twice must not duplicate the machine and must
// not surface AddMachine's "already exists" refusal as a failed import.
//
// Both runs are exercised the way the server runs them: re-propose against the
// config as it now is, then apply. The second proposal has no machine row in it
// (above), and applying a plan that still carries one — a plan file written
// before the first run and applied after it — is the harder case, so that is
// tested too.
func TestImportingTheGatewayTwiceIsANoOp(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	stale := cfg.ProposeImportFor(selfHost) // written once, applied twice

	if err := cfg.ApplyImport(stale, false); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// merge=true because the first apply declared the tree; the machine half is
	// additive either way.
	if err := cfg.ApplyImport(stale, true); err != nil {
		t.Fatalf("re-applying a plan that names an already-declared machine must be a no-op, got: %v", err)
	}
	n := 0
	for _, m := range cfg.Machines {
		if m.Name == selfHost {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the gateway is declared %d times: %+v", n, cfg.Machines)
	}

	// And the fresh proposal against the written config carries no machine, so
	// a second `hz import` reports nothing to do rather than one machine to add.
	if again := cfg.ProposeImportFor(selfHost); len(again.Machines) != 0 {
		t.Fatalf("a second proposal still offers the machine: %+v", again.Machines)
	}
}

// REFUSABLE. The whole justification for proposing this at all is that the
// operator can say no, and saying no is deleting the row from the plan file.
func TestDeletingTheMachineRowRefusesTheGateway(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	file := ImportFileFor(cfg.ProposeImportFor(selfHost))
	if len(file.Machines) != 1 {
		t.Fatalf("the plan file does not carry the machine: %+v", file.Machines)
	}

	file.Machines = nil // the operator deleted the row
	plan, err := cfg.PlanFromImportFile(file)
	if err != nil {
		t.Fatalf("a file with no machines must be legal: %v", err)
	}
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, declared := cfg.FindMachine(selfHost); declared {
		t.Fatal("hz declared the gateway from a plan the operator had deleted it from")
	}
}

// The plan file carries the machine across the round trip, and carries no
// evidence for it — the same rule every other row follows: the moment the
// operator moves a row, hz's reason for it is a lie sitting beside it.
func TestThePlanFileCarriesTheMachineAndNoEvidenceForIt(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	raw, err := ImportFileFor(cfg.ProposeImportFor(selfHost)).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"machines": [`) {
		t.Fatalf("no machines section in the plan file:\n%s", raw)
	}
	if strings.Contains(string(raw), "hz is running on") {
		t.Fatalf("the evidence leaked into the file, where it becomes a lie the moment a row moves:\n%s", raw)
	}

	// A FIXED POINT, like the rest of the file: parse what was written and
	// write it again, byte for byte. A machines section that reformatted on
	// every round trip would make a plan nobody can diff.
	parsed, err := ParseImportFile(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	again, err := parsed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Fatalf("round trip changed the file:\n--- first\n%s\n--- second\n%s", raw, again)
	}

	// And the decisions survive it.
	if len(parsed.Machines) != 1 || parsed.Machines[0].Name != selfHost {
		t.Fatalf("the machine did not survive the round trip: %+v", parsed.Machines)
	}
}

// A plan file written BEFORE machines were proposable still reads. There are
// real ones on disk; the version is unchanged and the section is optional
// precisely so a bump does not refuse every one of them.
func TestAPlanFileWithNoMachinesSectionStillReads(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "projects": [{"name":"intern"}],
  "environments": [],
  "assign": [{"service":"git","project":"intern"}],
  "unassigned": ["mirror"]
}
`)
	f, err := ParseImportFile(raw)
	if err != nil {
		t.Fatalf("a file written before machines existed no longer parses: %v", err)
	}
	if len(f.Machines) != 0 {
		t.Fatalf("absent decoded as something: %+v", f.Machines)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// The file validator catches the two hand-edits it can catch, and names them.
func TestTheMachineSectionRefusesAnEmptyOrDuplicateName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  []ImportFileMachine
		wants string
	}{
		{"empty", []ImportFileMachine{{Name: "  "}}, "has no name"},
		{"duplicate", []ImportFileMachine{{Name: selfHost}, {Name: selfHost}}, "declared twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ImportFile{Version: 1, Machines: tc.rows}
			err := f.Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", tc.rows)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("the refusal does not say %q: %v", tc.wants, err)
			}
		})
	}
}

// A machine may be declared by an import over a config that already has some.
// Additive, skipping by name, exactly as projects are — which is why this needs
// no --merge of its own: adding a flat record reorganises nothing.
func TestImportAddsAMachineToAConfigThatAlreadyHasOne(t *testing.T) {
	cfg := &Config{
		Services: legacyServices(),
		Machines: []Machine{{Name: "app-1"}},
	}
	plan := cfg.ProposeImportFor(selfHost)
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(cfg.Machines) != 2 {
		t.Fatalf("want app-1 kept and the gateway added, got %+v", cfg.Machines)
	}
	if _, ok := cfg.FindMachine("app-1"); !ok {
		t.Fatal("the import removed a machine it did not propose")
	}
}
