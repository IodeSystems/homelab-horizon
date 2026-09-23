package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The import plan as a file an operator edits: what it carries, what it
// refuses, and the proof that writing it and reading it back changes nothing.
//
// Placeholders throughout — homelab-horizon is public, so no real hostname
// appears here. legacyServices() (import_test.go) is the shared fixture.

func mustParse(t *testing.T, raw []byte) ImportFile {
	t.Helper()
	f, err := ParseImportFile(raw)
	if err != nil {
		t.Fatalf("parse failed: %v\n%s", err, raw)
	}
	return f
}

// --- the round trip ---------------------------------------------------------

// TestImportPlanFileRoundTripsWithoutChangingTheOutcome is the contract
// `--plan-out` then `--from` with no edits has to meet: the same plan, and the
// same config after it is applied.
//
// "The same plan" is asserted on the DECISIONS — the projects, the rungs and
// where every service goes. The evidence strings are not in the file and do not
// come back; the assertion below that the two applied configs are byte-identical
// is what proves nothing that MATTERS was lost.
func TestImportPlanFileRoundTripsWithoutChangingTheOutcome(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	proposed := cfg.ProposeImport()

	raw, err := ImportFileFor(proposed).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err != nil {
		t.Fatalf("the proposal hz wrote is not a file hz will read: %v\n%s", err, raw)
	}

	if len(back.Projects) != len(proposed.Projects) {
		t.Fatalf("projects: proposed %d, round-tripped %d", len(proposed.Projects), len(back.Projects))
	}
	for i := range proposed.Projects {
		if back.Projects[i].Name != proposed.Projects[i].Name || back.Projects[i].Parent != proposed.Projects[i].Parent {
			t.Errorf("project %d changed: %+v -> %+v", i, proposed.Projects[i], back.Projects[i])
		}
	}
	if len(back.Environments) != len(proposed.Environments) {
		t.Fatalf("environments: proposed %d, round-tripped %d", len(proposed.Environments), len(back.Environments))
	}
	for i := range proposed.Environments {
		a, b := proposed.Environments[i], back.Environments[i]
		if a.Project != b.Project || a.Name != b.Name || a.Posture != b.Posture {
			t.Errorf("environment %d changed: %+v -> %+v", i, a, b)
		}
	}
	if len(back.Assignments) != len(proposed.Assignments) {
		t.Fatalf("assignments: proposed %d, round-tripped %d", len(proposed.Assignments), len(back.Assignments))
	}
	for i := range proposed.Assignments {
		a, b := proposed.Assignments[i], back.Assignments[i]
		if a.Service != b.Service || a.Project != b.Project || a.Environment != b.Environment {
			t.Errorf("assignment %d changed: %+v -> %+v", i, a, b)
		}
	}
	if len(back.Unassigned) != len(proposed.Unassigned) {
		t.Fatalf("unassigned: proposed %d, round-tripped %d", len(proposed.Unassigned), len(back.Unassigned))
	}
	for i := range proposed.Unassigned {
		if back.Unassigned[i].Service != proposed.Unassigned[i].Service {
			t.Errorf("unassigned %d changed: %q -> %q", i, proposed.Unassigned[i].Service, back.Unassigned[i].Service)
		}
	}

	// The assertion that actually matters: applying either plan produces the
	// same config, field for field.
	direct := &Config{Services: legacyServices()}
	if err := direct.ApplyImport(proposed, false); err != nil {
		t.Fatalf("applying the proposal: %v", err)
	}
	viaFile := &Config{Services: legacyServices()}
	if err := viaFile.ApplyImport(back, false); err != nil {
		t.Fatalf("applying the round-tripped plan: %v", err)
	}
	if !reflect.DeepEqual(direct.Projects, viaFile.Projects) {
		t.Errorf("projects differ after apply:\n direct %+v\n file   %+v", direct.Projects, viaFile.Projects)
	}
	if !reflect.DeepEqual(direct.Environments, viaFile.Environments) {
		t.Errorf("environments differ after apply:\n direct %+v\n file   %+v", direct.Environments, viaFile.Environments)
	}
	if !reflect.DeepEqual(direct.Services, viaFile.Services) {
		t.Errorf("services differ after apply:\n direct %+v\n file   %+v", direct.Services, viaFile.Services)
	}
}

// TestImportPlanFileIsAFixedPoint: file -> plan -> file is byte-identical. The
// round trip above loses the evidence once, by design; this pins that it loses
// nothing a SECOND time, so an operator who writes, reads, writes again does not
// watch the file drift under them.
func TestImportPlanFileIsAFixedPoint(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	first, err := ImportFileFor(cfg.ProposeImport()).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	plan, err := cfg.PlanFromImportFile(mustParse(t, first))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	second, err := ImportFileFor(plan).Marshal()
	if err != nil {
		t.Fatalf("marshal again: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("the file is not a fixed point:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestImportPlanFileCarriesNoEvidence pins the omission as a decision rather
// than an oversight: if somebody adds a reason field to the file later, this
// fails and they have to argue with the comment at the top of import_file.go.
func TestImportPlanFileCarriesNoEvidence(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	raw, err := ImportFileFor(cfg.ProposeImport()).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, banned := range []string{"reason", "projectReason", "environmentReason", "note", "signals", "fingerprint"} {
		if _, found := generic[banned]; found {
			t.Errorf("the editable file carries %q; evidence is output, not input", banned)
		}
	}
	if !strings.Contains(string(raw), "_readme") {
		t.Error("the file carries no instructions; the operator editing it is not the one who read the help")
	}
}

// --- the failure the dry run found ------------------------------------------

// crossProjectRungFile is the file form of the exact bug a dry run against a
// real 33-service estate produced: one `dev` rung, three unrelated applications
// standing on it. The operator has since split the applications into projects —
// and left the rung where the heuristic put it.
func crossProjectRungFile() []byte {
	return []byte(`{
  "version": 1,
  "projects": [
    { "name": "<our-co>" },
    { "name": "analytics", "parent": "<our-co>" },
    { "name": "shop", "parent": "<our-co>" }
  ],
  "environments": [
    { "project": "<our-co>", "name": "dev", "posture": "dev" }
  ],
  "assign": [
    { "service": "analytics-dev", "project": "analytics", "environment": "dev" },
    { "service": "shop-dev", "project": "shop", "environment": "dev" },
    { "service": "shop-api-dev", "project": "shop", "environment": "dev" }
  ],
  "unassigned": []
}`)
}

func crossProjectServices() []Service {
	return []Service{
		{Name: "analytics-dev", Domains: []string{"analytics-dev.<our-co>.<tld>"}},
		{Name: "shop-dev", Domains: []string{"shop-dev.<our-co>.<tld>"}},
		{Name: "shop-api-dev", Domains: []string{"shop-api-dev.<our-co>.<tld>"}},
	}
}

// TestImportFileRefusesACrossProjectRung is the positive control for the whole
// feature. The file below is internally consistent JSON, every name in it is
// real, and it is the wrong config: three applications on one promotion rung.
// It has to be refused, and the refusal has to name the services that disagree.
func TestImportFileRefusesACrossProjectRung(t *testing.T) {
	raw := crossProjectRungFile()
	cfg := &Config{Services: crossProjectServices()}

	_, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err == nil {
		t.Fatal("a rung carrying services from several projects was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		`environment "dev" belongs to exactly one project`,
		"analytics", "shop",
		"analytics-dev",
		"shop-api-dev, shop-dev",
		`NO rung "dev" is declared in "shop"`,
		"promotes all of them together",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}

	// And it names the line, so a 33-service file is editable.
	annotated := AnnotateImportFileError(raw, "tree.json", err)
	if !strings.HasPrefix(annotated.Error(), "tree.json:12:") {
		t.Errorf("the refusal does not name the line of the first disagreeing service, got:\n%s", annotated)
	}
}

// TestCrossProjectRungIsRefusedEvenWhenOnlyOneProjectStandsOnIt: the same fault
// with a single service. The rung lives under one project and the only service
// on it is in another — nothing "collides", and it is still a rung in the wrong
// place. The message must name the project that declares it AND the one that
// does not.
func TestCrossProjectRungIsRefusedEvenWhenOnlyOneProjectStandsOnIt(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "projects": [ { "name": "<our-co>" }, { "name": "shop", "parent": "<our-co>" } ],
  "environments": [ { "project": "<our-co>", "name": "dev", "posture": "dev" } ],
  "assign": [ { "service": "shop-dev", "project": "shop", "environment": "dev" } ],
  "unassigned": []
}`)
	cfg := &Config{Services: []Service{{Name: "shop-dev"}}}
	_, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err == nil {
		t.Fatal("a service standing on another project's rung was accepted")
	}
	for _, want := range []string{"<our-co> / dev", "shop / dev", "(no service stands on it)", `NO rung "dev" is declared in "shop"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%s", want, err)
		}
	}
}

// TestTheCorrectedCrossProjectFileIsAccepted is the other half of the positive
// control. The refusal above proves the validator FIRES; this proves it is not
// simply refusing everything — the fix it asks for is accepted.
func TestTheCorrectedCrossProjectFileIsAccepted(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "projects": [
    { "name": "<our-co>" },
    { "name": "analytics", "parent": "<our-co>" },
    { "name": "shop", "parent": "<our-co>" }
  ],
  "environments": [
    { "project": "analytics", "name": "dev", "posture": "dev" },
    { "project": "shop", "name": "dev", "posture": "dev" }
  ],
  "assign": [
    { "service": "analytics-dev", "project": "analytics", "environment": "dev" },
    { "service": "shop-dev", "project": "shop", "environment": "dev" },
    { "service": "shop-api-dev", "project": "shop", "environment": "dev" }
  ],
  "unassigned": []
}`)
	cfg := &Config{Services: crossProjectServices()}
	plan, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err != nil {
		t.Fatalf("the corrected file was refused: %v", err)
	}
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("the corrected file does not apply: %v", err)
	}
	// Two rungs called dev, one per project — every project gets to have one.
	if len(cfg.Environments) != 2 {
		t.Fatalf("expected two rungs, got %+v", cfg.Environments)
	}
	for _, svc := range cfg.Services {
		if svc.Environment != "dev" {
			t.Errorf("service %q is not on dev: %+v", svc.Name, svc)
		}
	}
	if err := cfg.ValidateEnvironments(); err != nil {
		t.Fatalf("the result does not validate: %v", err)
	}
}

// --- the rest of the validators ---------------------------------------------

// validFile is the smallest file that passes, for the table below to break one
// field of at a time.
const validFile = `{
  "version": 1,
  "projects": [ { "name": "<our-co>" }, { "name": "shop", "parent": "<our-co>" } ],
  "environments": [ { "project": "shop", "name": "dev", "posture": "dev" } ],
  "assign": [ { "service": "shop-web", "project": "shop", "environment": "dev" } ],
  "unassigned": [ "legacy-redirect" ]
}`

func validFileServices() []Service {
	return []Service{{Name: "shop-web"}, {Name: "legacy-redirect"}}
}

// TestImportFileValidationCatchesEachHandEdit walks every way a hand-edited
// file goes wrong. Each row states what a human did and what they must be told.
func TestImportFileValidationCatchesEachHandEdit(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		wants []string
	}{
		{
			name:  "a mis-typed project name in assign",
			file:  strings.Replace(validFile, `"project": "shop", "environment"`, `"project": "shopp", "environment"`, 1),
			wants: []string{`service "shop-web" names project "shopp"`, "not declared in `projects`"},
		},
		{
			name:  "a rung whose project does not exist",
			file:  strings.Replace(validFile, `{ "project": "shop", "name": "dev"`, `{ "project": "shopp", "name": "dev"`, 1),
			wants: []string{`environment "dev" names project "shopp"`, "not declared in `projects`"},
		},
		{
			name:  "a posture that is not one of the three",
			file:  strings.Replace(validFile, `"posture": "dev"`, `"posture": "production"`, 1),
			wants: []string{`posture "production"`, "dev, staging, prod"},
		},
		{
			name:  "a rung with no project",
			file:  strings.Replace(validFile, `{ "project": "shop", "name": "dev"`, `{ "project": "", "name": "dev"`, 1),
			wants: []string{`environment "dev" has no project`, "belongs to exactly one project"},
		},
		{
			name:  "a parent that does not exist",
			file:  strings.Replace(validFile, `"parent": "<our-co>"`, `"parent": "<our-corp>"`, 1),
			wants: []string{`project "shop" names parent "<our-corp>"`, "not declared in this file"},
		},
		{
			name:  "a project declared twice",
			file:  strings.Replace(validFile, `{ "name": "<our-co>" },`, `{ "name": "<our-co>" }, { "name": "shop" },`, 1),
			wants: []string{`project "shop" is declared twice`},
		},
		{
			name:  "a rung declared twice in one project",
			file:  strings.Replace(validFile, `"environments": [ `, `"environments": [ { "project": "shop", "name": "dev", "posture": "prod" }, `, 1),
			wants: []string{`environment "dev" is declared twice in project "shop"`},
		},
		{
			name:  "a service in assign with no project",
			file:  strings.Replace(validFile, `"project": "shop", "environment"`, `"project": "", "environment"`, 1),
			wants: []string{`service "shop-web" is in \u0060assign\u0060 with no project`, "move its name to `unassigned`"},
		},
		{
			name:  "a service in both lists",
			file:  strings.Replace(validFile, `[ "legacy-redirect" ]`, `[ "legacy-redirect", "shop-web" ]`, 1),
			wants: []string{`service "shop-web" appears twice`, "`assign`", "`unassigned`"},
		},
		{
			name:  "a service named twice in assign",
			file:  strings.Replace(validFile, `"assign": [ `, `"assign": [ { "service": "shop-web", "project": "shop" }, `, 1),
			wants: []string{`service "shop-web" appears twice`},
		},
		{
			name:  "a service that is not in the config",
			file:  strings.Replace(validFile, `"shop-web"`, `"shop-wev"`, 1),
			wants: []string{`names service "shop-wev", which is not in this config`, "hz service list"},
		},
		{
			name:  "a service the file forgot",
			file:  strings.Replace(validFile, `[ "legacy-redirect" ]`, `[]`, 1),
			wants: []string{`does not account for 1 service(s)`, `"legacy-redirect"`, "exactly once"},
		},
		{
			name:  "a rung nothing declares",
			file:  strings.Replace(validFile, `"environments": [ { "project": "shop", "name": "dev", "posture": "dev" } ]`, `"environments": []`, 1),
			wants: []string{`service "shop-web" names environment "dev"`, "not declared in `environments` at all"},
		},
		{
			name:  "no version",
			file:  strings.Replace(validFile, `"version": 1,`, ``, 1),
			wants: []string{"declares version 0", "hz reads version 1", "--plan-out"},
		},
	}

	cfg := &Config{Services: validFileServices()}
	// Positive control: the unmodified file passes, so a row that fails is
	// failing for the reason it names rather than because the fixture is broken.
	if _, err := cfg.PlanFromImportFile(mustParse(t, []byte(validFile))); err != nil {
		t.Fatalf("the unedited fixture does not validate: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.file == validFile {
				t.Fatal("this row edits nothing; the Replace did not match")
			}
			f, err := ParseImportFile([]byte(tc.file))
			if err == nil {
				_, err = cfg.PlanFromImportFile(f)
			}
			if err == nil {
				t.Fatalf("accepted:\n%s", tc.file)
			}
			for _, want := range tc.wants {
				want = strings.ReplaceAll(want, `\u0060`, "`")
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%s", want, err)
				}
			}
		})
	}
}

// TestImportFileRefusesAnUnknownField: a misspelt key silently dropped would
// take a service off its rung and read exactly like a file that never named one.
func TestImportFileRefusesAnUnknownField(t *testing.T) {
	raw := []byte(strings.Replace(validFile, `"environment": "dev"`, `"envronment": "dev"`, 1))
	_, err := ParseImportFile(raw)
	if err == nil {
		t.Fatal("a misspelt field was accepted and silently dropped")
	}
	if !strings.Contains(err.Error(), `unknown field "envronment"`) {
		t.Errorf("the refusal does not name the field: %v", err)
	}
	annotated := AnnotateImportFileError(raw, "tree.json", err)
	if !strings.Contains(annotated.Error(), "tree.json:5:") {
		t.Errorf("the refusal does not name the line: %v", annotated)
	}
}

// TestUnassignedIsSayableAndApplies: "this service has no project" is a
// decision the file can state, and stating it writes nothing.
func TestUnassignedIsSayableAndApplies(t *testing.T) {
	cfg := &Config{Services: validFileServices()}
	plan, err := cfg.PlanFromImportFile(mustParse(t, []byte(validFile)))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].Service != "legacy-redirect" {
		t.Fatalf("unassigned did not survive: %+v", plan.Unassigned)
	}
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, svc := range cfg.Services {
		if svc.Name == "legacy-redirect" && (svc.Project != "" || svc.Environment != "") {
			t.Errorf("an explicitly unassigned service was assigned anyway: %+v", svc)
		}
	}
	if err := cfg.ValidateProjects(); err != nil {
		t.Fatalf("the result does not validate: %v", err)
	}
}

// TestAServiceMayBeAssignedToAProjectWithNoRung: project without environment is
// a legal row, and the rung stays empty.
func TestAServiceMayBeAssignedToAProjectWithNoRung(t *testing.T) {
	raw := []byte(strings.Replace(validFile, `, "environment": "dev"`, ``, 1))
	cfg := &Config{Services: validFileServices()}
	plan, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err != nil {
		t.Fatalf("a project-only assignment was refused: %v", err)
	}
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, svc := range cfg.Services {
		if svc.Name == "shop-web" && (svc.Project != "shop" || svc.Environment != "") {
			t.Errorf("wrong placement: %+v", svc)
		}
	}
}

// TestImportFileRefusesAParentCycle: the bounded walk, same shape as
// ValidateProjects', so a cycle is a refusal rather than a hang.
func TestImportFileRefusesAParentCycle(t *testing.T) {
	raw := []byte(`{
  "version": 1,
  "projects": [ { "name": "a", "parent": "b" }, { "name": "b", "parent": "a" } ],
  "environments": [],
  "assign": [],
  "unassigned": [ "shop-web", "legacy-redirect" ]
}`)
	cfg := &Config{Services: validFileServices()}
	_, err := cfg.PlanFromImportFile(mustParse(t, raw))
	if err == nil || !strings.Contains(err.Error(), "parent cycle") {
		t.Fatalf("a parent cycle was not refused: %v", err)
	}
}

// TestImportFileNotesTheGatewayChanged: the file's exhaustiveness rule doubles
// as the drift guard the fingerprint gives the proposal path — and it names the
// service instead of a hash.
func TestImportFileNotesTheGatewayChanged(t *testing.T) {
	cfg := &Config{Services: validFileServices()}
	f := mustParse(t, []byte(validFile))

	// A service appeared on the gateway after --plan-out wrote the file.
	grown := &Config{Services: append(validFileServices(), Service{Name: "new-thing"})}
	_, err := grown.PlanFromImportFile(f)
	if err == nil || !strings.Contains(err.Error(), `"new-thing"`) {
		t.Fatalf("a service added since the file was written was not named: %v", err)
	}

	// One vanished.
	shrunk := &Config{Services: []Service{{Name: "shop-web"}}}
	_, err = shrunk.PlanFromImportFile(f)
	if err == nil || !strings.Contains(err.Error(), `"legacy-redirect"`) {
		t.Fatalf("a service removed since the file was written was not named: %v", err)
	}

	// Control: unchanged config still passes.
	if _, err := cfg.PlanFromImportFile(f); err != nil {
		t.Fatalf("the unchanged config was refused: %v", err)
	}
}

// TestImportFileRefusesASecondDocument: a paste accident is a refusal, not a
// half-read plan.
func TestImportFileRefusesASecondDocument(t *testing.T) {
	_, err := ParseImportFile([]byte(validFile + "\n" + validFile))
	if err == nil || !strings.Contains(err.Error(), "more than one JSON document") {
		t.Fatalf("two documents were accepted: %v", err)
	}
}

// TestAnnotateNamesTheFileEvenWithoutALine: a fault about no single identifier
// still says which file it is about.
func TestAnnotateNamesTheFileEvenWithoutALine(t *testing.T) {
	err := &ImportFileError{Msg: "something general"}
	got := AnnotateImportFileError([]byte(validFile), "tree.json", err).Error()
	if got != "tree.json: something general" {
		t.Errorf("got %q", got)
	}
	if AnnotateImportFileError(nil, "tree.json", nil) != nil {
		t.Error("nil became an error")
	}
}
