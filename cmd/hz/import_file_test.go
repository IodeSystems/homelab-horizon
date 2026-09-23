package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz import --plan-out` / `--from` as an operator meets them: the proposal
// written out as a file, edited, validated with a line number, and applied.
//
// The stub serves a plan produced by the REAL planner, exactly as the rest of
// import_test.go does, so the file under test is the file hz would really write.

func planFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "tree.json")
}

// TestPlanOutWritesTheProposalAndTouchesNothing: --plan-out is a read. It
// prints the proposal, writes the file, and posts nothing.
func TestPlanOutWritesTheProposalAndTouchesNothing(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	path := planFilePath(t)

	out, err := runImportCapturing(t, stub.start(t), "--plan-out", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("--plan-out wrote to the config: %v", stub.posts)
	}
	if !strings.Contains(out, "Wrote "+path) || !strings.Contains(out, "Nothing was written to the config.") {
		t.Errorf("output must say what it wrote and what it did not:\n%s", out)
	}
	if !strings.Contains(out, "hz import --from "+path+" --execute") {
		t.Errorf("output must say how to apply it:\n%s", out)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no file: %v", err)
	}
	file, err := hzconfig.ParseImportFile(raw)
	if err != nil {
		t.Fatalf("hz wrote a file hz will not read: %v\n%s", err, raw)
	}
	if err := file.Validate(); err != nil {
		t.Fatalf("hz wrote a file hz refuses: %v\n%s", err, raw)
	}

	// The decisions are all there, and the evidence is not.
	proposal := stub.cfg.ProposeImport()
	if len(file.Assign) != len(proposal.Assignments) || len(file.Unassigned) != len(proposal.Unassigned) {
		t.Errorf("the file lost services: %d/%d assign, %d/%d unassigned",
			len(file.Assign), len(proposal.Assignments), len(file.Unassigned), len(proposal.Unassigned))
	}
	for _, reason := range []string{"share the domain suffix", "byte-identical", "names the rung"} {
		if strings.Contains(string(raw), reason) {
			t.Errorf("the file carries evidence (%q); it is output, not input:\n%s", reason, raw)
		}
	}
	if !strings.Contains(string(raw), "_readme") {
		t.Errorf("the file carries no instructions:\n%s", raw)
	}
}

// TestPlanOutThenFromIsANoOpRoundTrip is the contract from the other end: write
// the proposal, read it straight back with no edits, and the plan that reaches
// the server is the one hz proposed.
func TestPlanOutThenFromIsANoOpRoundTrip(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	c := stub.start(t)
	path := planFilePath(t)

	if _, err := runImportCapturing(t, c, "--plan-out", path); err != nil {
		t.Fatal(err)
	}
	out, err := runImportCapturing(t, c, "--from", path, "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 1 {
		t.Fatalf("want one POST, got %v", stub.posts)
	}
	var req apitypes.ImportApplyReq
	if err := json.Unmarshal([]byte(stub.posts[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Plan == nil {
		t.Fatal("--from posted no plan; the file was not the authority")
	}
	// A fingerprint identifies a plan hz computed, and this one is the
	// operator's. Sending both would make it unclear which one was applied.
	if req.Fingerprint != "" {
		t.Errorf("--from sent a fingerprint %q", req.Fingerprint)
	}

	proposal := stub.cfg.ProposeImport()
	if len(req.Plan.Projects) != len(proposal.Projects) {
		t.Fatalf("projects: %d over the wire, %d proposed", len(req.Plan.Projects), len(proposal.Projects))
	}
	for i, p := range proposal.Projects {
		if req.Plan.Projects[i].Name != p.Name || req.Plan.Projects[i].Parent != p.Parent {
			t.Errorf("project %d: %+v over the wire, %+v proposed", i, req.Plan.Projects[i], p)
		}
	}
	if len(req.Plan.Environments) != len(proposal.Environments) {
		t.Fatalf("environments: %d over the wire, %d proposed", len(req.Plan.Environments), len(proposal.Environments))
	}
	for i, e := range proposal.Environments {
		got := req.Plan.Environments[i]
		if got.Project != e.Project || got.Name != e.Name || got.Posture != e.Posture {
			t.Errorf("environment %d: %+v over the wire, %+v proposed", i, got, e)
		}
	}
	if len(req.Plan.Assign) != len(proposal.Assignments) {
		t.Fatalf("assignments: %d over the wire, %d proposed", len(req.Plan.Assign), len(proposal.Assignments))
	}
	for i, a := range proposal.Assignments {
		got := req.Plan.Assign[i]
		if got.Service != a.Service || got.Project != a.Project || got.Environment != a.Environment {
			t.Errorf("assignment %d: %+v over the wire, %+v proposed", i, got, a)
		}
	}
	if len(req.Plan.Unassigned) != len(proposal.Unassigned) {
		t.Fatalf("unassigned: %d over the wire, %d proposed", len(req.Plan.Unassigned), len(proposal.Unassigned))
	}
	for i, u := range proposal.Unassigned {
		if req.Plan.Unassigned[i] != u.Service {
			t.Errorf("unassigned %d: %q over the wire, %q proposed", i, req.Plan.Unassigned[i], u.Service)
		}
	}
}

// TestFromDryRunPrintsThePlanAndPostsNothing: --from without --execute is a dry
// run, same as every other path here.
func TestFromDryRunPrintsThePlanAndPostsNothing(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	c := stub.start(t)
	path := planFilePath(t)
	if _, err := runImportCapturing(t, c, "--plan-out", path); err != nil {
		t.Fatal(err)
	}

	out, err := runImportCapturing(t, c, "--from", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a dry run wrote: %v", stub.posts)
	}
	if !strings.Contains(out, "the project tree declared in "+path) {
		t.Errorf("the header must say where the plan came from:\n%s", out)
	}
	if !strings.Contains(out, "Dry run: nothing was written") {
		t.Errorf("it must say it is a dry run:\n%s", out)
	}
	// A file carries no evidence, so no reason line may be invented for it.
	for _, invented := range []string{"share the domain suffix", "declared in " + path + "\n"} {
		if strings.Contains(out, invented) {
			t.Errorf("a plan from a file was given evidence it does not have (%q):\n%s", invented, out)
		}
	}
	if !strings.Contains(out, "names each of these on purpose") {
		t.Errorf("the unassigned section must say the file said so deliberately:\n%s", out)
	}
}

// --- the failure the dry run found, at the command line ---------------------

// crossProjectEstate is the shape that broke the heuristic: a FLAT estate. Every
// service is a subdomain of one company domain, so no suffix can tell one
// application from another and everything the operator knows is invisible to hz.
func crossProjectEstate() *hzconfig.Config {
	p := func(port string) *hzconfig.ProxyConfig {
		return &hzconfig.ProxyConfig{Backend: "<gw>:" + port}
	}
	return &hzconfig.Config{Services: []hzconfig.Service{
		{Name: "analytics-dev", Domains: []string{"analytics-dev.<our-co>.<tld>"}, Proxy: p("6410")},
		{Name: "shop-dev", Domains: []string{"shop-dev.<our-co>.<tld>"}, Proxy: p("6411")},
		{Name: "shop-api-dev", Domains: []string{"shop-api-dev.<our-co>.<tld>"}, Proxy: p("6412")},
	}}
}

// TestFromRefusesARungSpreadAcrossProjects is the positive control for the whole
// feature, at the level an operator meets it.
//
// The file below is the operator's half-finished correction: they split the
// applications hz had collapsed into one project, and left the `dev` rung where
// hz put it. It is valid JSON, every name in it is real, and it says three
// applications share one promotion rung. It has to be refused, before anything
// is sent, naming the services that disagree and the line to fix.
func TestFromRefusesARungSpreadAcrossProjects(t *testing.T) {
	stub := &importStub{cfg: crossProjectEstate()}
	path := planFilePath(t)
	if err := os.WriteFile(path, []byte(`{
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
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runImportCapturing(t, stub.start(t), "--from", path, "--execute")
	if err == nil {
		t.Fatalf("a rung spread over three projects was accepted:\n%s", out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a refused plan was sent anyway: %v", stub.posts)
	}
	msg := err.Error()
	for _, want := range []string{
		path + ":12:",
		`environment "dev" belongs to exactly one project`,
		"analytics / dev",
		"shop / dev",
		"shop-api-dev, shop-dev",
		`NO rung "dev" is declared in "shop"`,
		"promotes all of them together",
		"declare a separate rung of this name under each project",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}
}

// TestFromAcceptsTheCorrectedFile is the other half of that control: the fix the
// refusal asks for goes through. Without this, a validator that refused
// everything would look identical.
func TestFromAcceptsTheCorrectedFile(t *testing.T) {
	stub := &importStub{cfg: crossProjectEstate()}
	path := planFilePath(t)
	if err := os.WriteFile(path, []byte(`{
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
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runImportCapturing(t, stub.start(t), "--from", path, "--execute")
	if err != nil {
		t.Fatalf("the corrected file was refused: %v\n%s", err, out)
	}
	if len(stub.posts) != 1 {
		t.Fatalf("want one POST, got %v", stub.posts)
	}
	var req apitypes.ImportApplyReq
	if err := json.Unmarshal([]byte(stub.posts[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Plan == nil || len(req.Plan.Environments) != 2 {
		t.Fatalf("the two separate rungs did not travel: %+v", req.Plan)
	}
	// And the structure hz could never have proposed is the one being applied:
	// three services, three projects, two rungs.
	if len(req.Plan.Projects) != 3 {
		t.Errorf("the operator's three projects did not travel: %+v", req.Plan.Projects)
	}
}

// --- the other hand-edits, end to end ---------------------------------------

// TestFromNamesTheLineOfEveryHandEdit: the validations an operator actually
// trips over, each from the command line, each naming the line.
func TestFromNamesTheLineOfEveryHandEdit(t *testing.T) {
	// One service per line, so a line number is checkable.
	const good = `{
  "version": 1,
  "projects": [
    { "name": "shop" }
  ],
  "environments": [
    { "project": "shop", "name": "dev", "posture": "dev" }
  ],
  "assign": [
    { "service": "shop-dev", "project": "shop", "environment": "dev" },
    { "service": "shop-api-dev", "project": "shop", "environment": "dev" }
  ],
  "unassigned": [
    "analytics-dev"
  ]
}
`
	cases := []struct {
		name  string
		file  string
		line  string
		wants []string
	}{
		{
			name:  "a mis-typed project name",
			file:  strings.Replace(good, `"service": "shop-api-dev", "project": "shop"`, `"service": "shop-api-dev", "project": "shopp"`, 1),
			line:  ":11:",
			wants: []string{`service "shop-api-dev" names project "shopp"`, "not declared in `projects`"},
		},
		{
			name:  "a posture that is not one of the three",
			file:  strings.Replace(good, `"posture": "dev"`, `"posture": "production"`, 1),
			line:  ":7:",
			wants: []string{`posture "production"`, "dev, staging, prod"},
		},
		{
			name:  "a service that does not exist",
			file:  strings.Replace(good, `"analytics-dev"`, `"analytics-dvv"`, 1),
			line:  ":14:",
			wants: []string{`service "analytics-dvv", which is not in this config`, "hz service list"},
		},
		{
			name:  "a service the file forgot",
			file:  strings.Replace(good, "\n    \"analytics-dev\"\n  ", "", 1),
			wants: []string{`does not account for 1 service(s)`, `"analytics-dev"`, "appear exactly once", "`unassigned`"},
		},
		{
			name:  "a misspelt field",
			file:  strings.Replace(good, `"environment": "dev" }`, `"envronment": "dev" }`, 1),
			line:  ":10:",
			wants: []string{`unknown field "envronment"`, "refused rather than ignored"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.file == good {
				t.Fatal("this row edits nothing; the Replace did not match")
			}
			stub := &importStub{cfg: crossProjectEstate()}
			path := planFilePath(t)
			if err := os.WriteFile(path, []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := runImportCapturing(t, stub.start(t), "--from", path, "--execute")
			if err == nil {
				t.Fatalf("accepted:\n%s\n%s", tc.file, out)
			}
			if len(stub.posts) != 0 {
				t.Fatalf("a refused plan was sent: %v", stub.posts)
			}
			if !strings.HasPrefix(err.Error(), path+tc.line) {
				t.Errorf("the refusal does not start %q:\n%s", path+tc.line, err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q:\n%s", want, err)
				}
			}
		})
	}

	// Positive control: the unedited file goes through, so every refusal above
	// is the validator firing rather than the fixture being broken.
	stub := &importStub{cfg: crossProjectEstate()}
	path := planFilePath(t)
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runImportCapturing(t, stub.start(t), "--from", path, "--execute"); err != nil {
		t.Fatalf("the unedited fixture was refused: %v\n%s", err, out)
	}
}

// TestPlanOutAndFromAndExecuteAreExclusive: three flags that all mean "where
// does the plan come from", and a combination that means two things at once has
// to be refused rather than silently favouring one.
func TestPlanOutAndFromAndExecuteAreExclusive(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	c := stub.start(t)
	path := planFilePath(t)

	if _, err := runImportCapturing(t, c, "--plan-out", path, "--from", path); err == nil {
		t.Error("--plan-out with --from was accepted")
	}
	out, err := runImportCapturing(t, c, "--plan-out", path, "--execute")
	if err == nil {
		t.Errorf("--plan-out with --execute was accepted:\n%s", out)
	} else if !strings.Contains(err.Error(), "--from "+path+" --execute") {
		t.Errorf("the refusal must say what to do instead, got %q", err)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a refused invocation posted: %v", stub.posts)
	}
}

// TestFromAMissingFileSaysSo: the commonest mistake of all.
func TestFromAMissingFileSaysSo(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	_, err := runImportCapturing(t, stub.start(t), "--from", filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("a missing plan file was accepted")
	}
	if !strings.Contains(err.Error(), "reading the plan file") {
		t.Errorf("the refusal must say what it could not read, got %q", err)
	}
}

// TestBareImportPointsAtThePlanFile: the operator whose estate is flat gets
// nothing useful out of the heuristic, and the command has to tell them where
// to go rather than leaving "nothing to import" as the last word.
func TestBareImportPointsAtThePlanFile(t *testing.T) {
	stub := &importStub{cfg: &hzconfig.Config{Services: []hzconfig.Service{
		{Name: "git", Domains: []string{"git.<our-domain>"}},
		{Name: "idp", Domains: []string{"idp.<our-domain>"}},
	}}}
	out, err := runImportCapturing(t, stub.start(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--plan-out") {
		t.Errorf("a config hz can propose nothing for must point at the file:\n%s", out)
	}
}
