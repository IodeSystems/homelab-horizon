package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `hz import` as an operator meets it: a proposal that names its evidence, an
// unassigned list that says why, and nothing written until it is applied.
//
// Placeholders throughout — homelab-horizon is a public repo, so no real
// hostname appears here. `<our-co>.<tld>` is a two-label stand-in so the
// root-project case is exercised; `<our-domain>` is the one-label stand-in
// legacy_compat_test.go already uses.

// legacyServices is the grouped gateway: two real projects spelled into
// hostnames, one service that only a shared backend places, one that nothing
// places, and one with no domains at all.
func legacyServices() []Service {
	proxy := func(backend string, internal bool) *ProxyConfig {
		return &ProxyConfig{Backend: backend, InternalOnly: internal}
	}
	return []Service{
		{Name: "git", Domains: []string{"git.intern.<our-co>.<tld>"}, Proxy: proxy("<gw>:3000", false)},
		{Name: "mirror", Domains: []string{"mirror.intern.<our-co>.<tld>"}, Proxy: proxy("<gw>:3100", false)},
		{Name: "idp", Domains: []string{"idp.intern.<our-co>.<tld>"}, Proxy: proxy("<gw>:8080", true)},
		// Same host:port as git — the same listening process under a second name.
		// Its own hostname groups it with nothing.
		{Name: "registry", Domains: []string{"registry.<our-co>.<tld>"}, Proxy: proxy("<gw>:3000", false)},
		{Name: "web", Domains: []string{"web.storefront.<our-co>.<tld>"}, Proxy: proxy("<gw>:6400", false)},
		{Name: "web-staging", Domains: []string{"staging.storefront.<our-co>.<tld>"}, Proxy: proxy("<gw>:6402", false)},
		{Name: "api-prod", Domains: []string{"api.prod.storefront.<our-co>.<tld>"}, Proxy: proxy("<app-1>:6400", false)},
		{Name: "grafana", Domains: []string{"grafana.<our-co>.<tld>"}, Proxy: proxy("<gw>:3001", false)},
		{Name: "metrics", Proxy: proxy("<gw>:9090", false)},
	}
}

func assignmentOf(t *testing.T, p ImportPlan, service string) ImportAssignment {
	t.Helper()
	for _, a := range p.Assignments {
		if a.Service == service {
			return a
		}
	}
	t.Fatalf("service %q is not assigned; plan assigns %+v", service, p.Assignments)
	return ImportAssignment{}
}

func unassignedOf(t *testing.T, p ImportPlan, service string) ImportUnassigned {
	t.Helper()
	for _, u := range p.Unassigned {
		if u.Service == service {
			return u
		}
	}
	t.Fatalf("service %q is not unassigned; plan leaves %+v", service, p.Unassigned)
	return ImportUnassigned{}
}

func projectOf(t *testing.T, p ImportPlan, name string) ImportProject {
	t.Helper()
	for _, pr := range p.Projects {
		if pr.Name == name {
			return pr
		}
	}
	t.Fatalf("no project %q proposed; plan proposes %+v", name, p.Projects)
	return ImportProject{}
}

// TestImportProposesATreeAndNamesEveryReason is the whole command in one
// assertion set: what it proposes, what it refuses to propose, and that nothing
// is proposed without a stated reason beside it.
func TestImportProposesATreeAndNamesEveryReason(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	plan := cfg.ProposeImport()

	// The root names itself from the suffix every service already sits under,
	// and holds no service: it is where a feed goes, not where work goes.
	root := projectOf(t, plan, "<our-co>")
	if root.Parent != "" {
		t.Errorf("the root has a parent: %+v", root)
	}
	if !strings.Contains(root.Reason, "<our-co>.<tld>") {
		t.Errorf("the root must name the suffix it came from, got %q", root.Reason)
	}
	for _, name := range []string{"intern", "storefront"} {
		p := projectOf(t, plan, name)
		if p.Parent != "<our-co>" {
			t.Errorf("project %q should hang off the root, got parent %q", name, p.Parent)
		}
		if !strings.Contains(p.Reason, "domain suffix") {
			t.Errorf("project %q must name its evidence, got %q", name, p.Reason)
		}
	}
	if len(plan.Projects) != 3 {
		t.Fatalf("want 3 projects, got %+v", plan.Projects)
	}

	// Every proposal carries a reason. This is the property, not a spot check:
	// a row without one is a guess wearing a plan's clothes.
	for _, p := range plan.Projects {
		if strings.TrimSpace(p.Reason) == "" {
			t.Errorf("project %q proposed with no reason", p.Name)
		}
	}
	for _, e := range plan.Environments {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("environment %s/%s proposed with no reason", e.Project, e.Name)
		}
	}
	for _, a := range plan.Assignments {
		if strings.TrimSpace(a.ProjectReason) == "" {
			t.Errorf("service %q assigned to %q with no reason", a.Service, a.Project)
		}
		if a.Environment != "" && strings.TrimSpace(a.EnvironmentReason) == "" {
			t.Errorf("service %q put on rung %q with no reason", a.Service, a.Environment)
		}
	}
	for _, u := range plan.Unassigned {
		if strings.TrimSpace(u.Reason) == "" {
			t.Errorf("service %q left alone with no reason", u.Service)
		}
	}

	// The suffix does the work for the six services whose hostnames say so.
	for _, tc := range []struct{ service, project string }{
		{"git", "intern"}, {"mirror", "intern"}, {"idp", "intern"},
		{"web", "storefront"}, {"web-staging", "storefront"}, {"api-prod", "storefront"},
	} {
		if got := assignmentOf(t, plan, tc.service).Project; got != tc.project {
			t.Errorf("%s -> %q, want %q", tc.service, got, tc.project)
		}
	}

	// registry's own hostname groups it with nothing; its BACKEND does. A
	// byte-identical host:port is the same listening process, not a guess.
	reg := assignmentOf(t, plan, "registry")
	if reg.Project != "intern" {
		t.Errorf("registry -> %q, want intern via its backend", reg.Project)
	}
	if !strings.Contains(reg.ProjectReason, "<gw>:3000") || !strings.Contains(reg.ProjectReason, "git") {
		t.Errorf("registry's reason must name the backend and the service it shares it with, got %q", reg.ProjectReason)
	}

	// Posture words name a RUNG, and only where there is a project to hang it on.
	staging := assignmentOf(t, plan, "web-staging")
	if staging.Environment != "staging" {
		t.Errorf("web-staging environment = %q, want staging", staging.Environment)
	}
	prod := assignmentOf(t, plan, "api-prod")
	if prod.Environment != "prod" {
		t.Errorf("api-prod environment = %q, want prod", prod.Environment)
	}
	if got := assignmentOf(t, plan, "git").Environment; got != "" {
		t.Errorf("git has no posture word anywhere; environment = %q", got)
	}
	if len(plan.Environments) != 2 {
		t.Fatalf("want 2 declared rungs, got %+v", plan.Environments)
	}
	for _, e := range plan.Environments {
		if e.Project != "storefront" {
			t.Errorf("rung %s/%s: only storefront has posture words", e.Project, e.Name)
		}
		if e.Name == "staging" && e.Posture != "staging" {
			t.Errorf("staging posture = %q", e.Posture)
		}
		if e.Name == "prod" && e.Posture != "prod" {
			t.Errorf("prod posture = %q", e.Posture)
		}
	}

	// Two left alone, each for a different, stated reason.
	graf := unassignedOf(t, plan, "grafana")
	if !strings.Contains(graf.Reason, "only service under") {
		t.Errorf("grafana's reason = %q", graf.Reason)
	}
	met := unassignedOf(t, plan, "metrics")
	if !strings.Contains(met.Reason, "no domains") {
		t.Errorf("metrics' reason = %q", met.Reason)
	}

	services, assigned, unassigned := plan.Counts()
	if services != 9 || assigned != 7 || unassigned != 2 {
		t.Fatalf("counts = %d services, %d assigned, %d unassigned; want 9/7/2", services, assigned, unassigned)
	}
}

// TestImportRejectedSignalsAreReported: hz looked at the backend host and at
// internal_only and used neither. Saying nothing about them is indistinguishable
// from not having looked, so the plan reports what it rejected and the numbers
// from this config.
func TestImportRejectedSignalsAreReported(t *testing.T) {
	plan := (&Config{Services: legacyServices()}).ProposeImport()
	byName := map[string]string{}
	for _, s := range plan.Signals {
		byName[s.Name] = s.Detail
	}
	host, ok := byName["backend host"]
	if !ok {
		t.Fatalf("eight services share backend host <gw> and nothing said so: %+v", plan.Signals)
	}
	if !strings.Contains(host, "<gw>") || !strings.Contains(host, "machine") {
		t.Errorf("backend-host rejection should name the host and why, got %q", host)
	}
	internal, ok := byName["internal_only"]
	if !ok {
		t.Fatalf("one service is internal_only and nothing said so: %+v", plan.Signals)
	}
	if !strings.Contains(internal, "1 of 9") {
		t.Errorf("internal_only rejection should carry this config's numbers, got %q", internal)
	}
	// The suffix signal WAS used here, so it must not appear as rejected.
	if _, rejected := byName["domain suffix"]; rejected {
		t.Errorf("the suffix signal grouped six services; it must not be reported as unused: %q", byName["domain suffix"])
	}
}

// TestImportProposesNothingWhenEverySuffixNamesOneService is the config from
// legacy_compat_test.go — one domain, a subdomain per service. There is no tree
// in it, and proposing a project per service would write the hostnames back as
// if there were.
func TestImportProposesNothingWhenEverySuffixNamesOneService(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "git", Domains: []string{"git.<our-domain>"}},
		{Name: "idp", Domains: []string{"idp.<our-domain>"}},
		{Name: "app", Domains: []string{"app.<our-domain>"}},
		{Name: "staging", Domains: []string{"staging.<our-domain>"}},
	}}
	plan := cfg.ProposeImport()

	if len(plan.Projects) != 0 {
		t.Fatalf("nothing in this config groups; got projects %+v", plan.Projects)
	}
	if len(plan.Environments) != 0 {
		t.Fatalf("an environment belongs to a project and there are none; got %+v", plan.Environments)
	}
	if len(plan.Assignments) != 0 {
		t.Fatalf("got assignments %+v", plan.Assignments)
	}
	if len(plan.Unassigned) != 4 {
		t.Fatalf("all four services should be left alone, got %+v", plan.Unassigned)
	}

	// The service literally named "staging" carries a posture word and it is
	// still unusable, because a rung belongs to a project.
	st := unassignedOf(t, plan, "staging")
	if !strings.Contains(st.Note, "staging") || !strings.Contains(st.Note, "belongs to a project") {
		t.Errorf("the posture hint should be reported AND explained away, got note %q", st.Note)
	}

	var suffix string
	for _, s := range plan.Signals {
		if s.Name == "domain suffix" {
			suffix = s.Detail
		}
	}
	if !strings.Contains(suffix, "<our-domain>") {
		t.Fatalf("the plan must say why the suffix signal produced nothing, got %q", suffix)
	}
}

// TestImportProposesARootWithNoServicesUnderIt: the common gateway shape — every
// service a subdomain of one company domain. Nothing groups, so nothing is
// assigned, and the root is still worth declaring: it is where the package feed
// goes (architecture.md, step 1) and it is the only honest project in the file.
func TestImportProposesARootWithNoServicesUnderIt(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "git", Domains: []string{"git.<our-co>.<tld>"}},
		{Name: "idp", Domains: []string{"idp.<our-co>.<tld>"}},
	}}
	plan := cfg.ProposeImport()
	if len(plan.Projects) != 1 || plan.Projects[0].Name != "<our-co>" {
		t.Fatalf("want one root project, got %+v", plan.Projects)
	}
	if len(plan.Assignments) != 0 {
		t.Fatalf("a root nobody was grouped into must hold nothing, got %+v", plan.Assignments)
	}
	if len(plan.Unassigned) != 2 {
		t.Fatalf("both services stay unassigned, got %+v", plan.Unassigned)
	}
}

// TestImportRefusesToStraddleTwoProjects: one backend, two projects, is a real
// contradiction in the config. Resolving it by picking is exactly the failure
// this command exists to avoid, so NEITHER service is assigned and both are told
// what collided.
func TestImportRefusesToStraddleTwoProjects(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "a1", Domains: []string{"a1.alpha.<our-co>.<tld>"}, Proxy: &ProxyConfig{Backend: "<gw>:1000"}},
		{Name: "a2", Domains: []string{"a2.alpha.<our-co>.<tld>"}, Proxy: &ProxyConfig{Backend: "<gw>:1001"}},
		{Name: "b1", Domains: []string{"b1.beta.<our-co>.<tld>"}, Proxy: &ProxyConfig{Backend: "<gw>:1000"}},
		{Name: "b2", Domains: []string{"b2.beta.<our-co>.<tld>"}, Proxy: &ProxyConfig{Backend: "<gw>:1002"}},
	}}
	plan := cfg.ProposeImport()

	for _, name := range []string{"a1", "b1"} {
		u := unassignedOf(t, plan, name)
		if !strings.Contains(u.Reason, "<gw>:1000") {
			t.Errorf("%s should be told which backend collided, got %q", name, u.Reason)
		}
		if !strings.Contains(u.Reason, "alpha") || !strings.Contains(u.Reason, "beta") {
			t.Errorf("%s should be told which two projects collided, got %q", name, u.Reason)
		}
	}
	// The services that did not straddle anything are untouched by the collision.
	if got := assignmentOf(t, plan, "a2").Project; got != "alpha" {
		t.Errorf("a2 -> %q, want alpha", got)
	}
	if got := assignmentOf(t, plan, "b2").Project; got != "beta" {
		t.Errorf("b2 -> %q, want beta", got)
	}
}

// TestPostureWordsMatchWholeTokensOnly: "reproduction" contains "prod" and means
// nothing of the kind. A substring match here puts a service on the production
// rung, which is the most expensive wrong answer this command can give.
func TestPostureWordsMatchWholeTokensOnly(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "reproduction", Domains: []string{"repro.lab.<our-co>.<tld>"}},
		{Name: "protodeck", Domains: []string{"proto.lab.<our-co>.<tld>"}},
		{Name: "devtools", Domains: []string{"devtools.lab.<our-co>.<tld>"}},
		{Name: "api-dev", Domains: []string{"api.lab.<our-co>.<tld>"}},
		// A second sub-tree, so `lab` is a suffix some services share rather
		// than one they all do. Grouping needs two branches to be a partition.
		{Name: "elsewhere", Domains: []string{"x.other.<our-co>.<tld>"}},
	}}
	plan := cfg.ProposeImport()
	for _, name := range []string{"reproduction", "protodeck", "devtools"} {
		if got := assignmentOf(t, plan, name).Environment; got != "" {
			t.Errorf("%s was put on rung %q by a substring match", name, got)
		}
	}
	// The one real token still matches, or the test above proves nothing.
	if got := assignmentOf(t, plan, "api-dev").Environment; got != "dev" {
		t.Errorf("api-dev environment = %q, want dev", got)
	}
}

// TestTwoPostureWordsLeaveNoEnvironment: a service whose name says one rung and
// whose hostname says another is a contradiction, not a tie. Picking either puts
// it somewhere nobody said.
func TestTwoPostureWordsLeaveNoEnvironment(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "api-staging", Domains: []string{"api.prod.shop.<our-co>.<tld>"}},
		{Name: "web", Domains: []string{"web.shop.<our-co>.<tld>"}},
		{Name: "elsewhere", Domains: []string{"x.mall.<our-co>.<tld>"}},
	}}
	plan := cfg.ProposeImport()
	a := assignmentOf(t, plan, "api-staging")
	if a.Project != "shop" {
		t.Fatalf("the project evidence is unaffected by the rung being unclear, got %q", a.Project)
	}
	if a.Environment != "" {
		t.Fatalf("api-staging was put on rung %q despite two words", a.Environment)
	}
	if !strings.Contains(a.EnvironmentReason, "staging") || !strings.Contains(a.EnvironmentReason, "prod") {
		t.Errorf("the reason must name both words, got %q", a.EnvironmentReason)
	}
	if len(plan.Environments) != 0 {
		t.Errorf("no rung should be declared from a contradiction, got %+v", plan.Environments)
	}
}

// TestImportPlanIsDeterministic: the plan is read in a dry run and applied
// later. Map iteration order leaking into it would make the two differ for no
// reason anybody could see, and make the fingerprint worthless.
func TestImportPlanIsDeterministic(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	first := cfg.ProposeImport()
	want, _ := json.Marshal(first)
	for i := 0; i < 20; i++ {
		got, _ := json.Marshal(cfg.ProposeImport())
		if string(got) != string(want) {
			t.Fatalf("run %d differs:\n %s\n %s", i, want, got)
		}
	}
	if first.Fingerprint() == "" {
		t.Fatal("a plan must fingerprint")
	}
	// And a different config must not fingerprint the same, or the guard the
	// fingerprint exists for never fires.
	other := (&Config{Services: legacyServices()[:4]}).ProposeImport()
	if other.Fingerprint() == first.Fingerprint() {
		t.Fatal("two different plans share a fingerprint")
	}
}

// TestImportAppliesInOneSave is the deploy-gate half: declarations and
// assignments land together, and the config that results SAVES. Save runs all
// three validators, and a service naming a project declared in the same write is
// exactly what legacy_compat_test.go shows is legal.
func TestImportAppliesInOneSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := &Config{Services: legacyServices()}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	plan := cfg.ProposeImport()
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("applying the plan: %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("the imported config must SAVE — one write, declarations and assignments together: %v", err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Projects) != 3 || len(again.Environments) != 2 {
		t.Fatalf("reload: projects=%+v environments=%+v", again.Projects, again.Environments)
	}
	assigned := 0
	for _, s := range again.Services {
		if s.Project != "" {
			assigned++
		}
		if s.Name == "grafana" && s.Project != "" {
			t.Errorf("grafana was left unassigned by the plan and came back in %q", s.Project)
		}
		if s.Name == "web-staging" && (s.Project != "storefront" || s.Environment != "staging") {
			t.Errorf("web-staging = %q/%q", s.Project, s.Environment)
		}
	}
	if assigned != 7 {
		t.Errorf("want 7 assigned services on disk, got %d", assigned)
	}
}

// TestImportOrderWithinTheSaveDoesNotMatter pins the claim ApplyImport rests on,
// rather than leaving it as a comment: the validators run over the FINISHED
// config, so a service may name a project that is appended to the same struct
// afterwards. If this ever stops being true, ApplyImport has to become two
// writes and this test is where that is discovered.
func TestImportOrderWithinTheSaveDoesNotMatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &Config{Services: []Service{{Name: "web"}}}
	// Assignment FIRST, against a config that declares neither rung nor project.
	cfg.Services[0].Project = "shop"
	cfg.Services[0].Environment = "staging"
	// Declarations appended after, in the same in-memory config, one Save.
	cfg.Projects = append(cfg.Projects, Project{Name: "shop"})
	cfg.Environments = append(cfg.Environments, Environment{Project: "shop", Name: "staging", Posture: "staging"})

	if err := Save(path, cfg); err != nil {
		t.Fatalf("declaring after assigning, in one Save, must be legal: %v", err)
	}
}

// TestImportRefusesAnExistingTree: a second `--execute` must not silently
// reorganise what the first one built. That is the destructive case, and it is
// the one that happens by accident.
func TestImportRefusesAnExistingTree(t *testing.T) {
	cfg := &Config{
		Projects: []Project{{Name: "already-here"}},
		Services: legacyServices(),
	}
	plan := cfg.ProposeImport()
	err := cfg.ApplyImport(plan, false)
	if err == nil {
		t.Fatal("import overwrote a config that already declares a project")
	}
	if !strings.Contains(err.Error(), "already declares") {
		t.Errorf("the refusal should say what it found, got %q", err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("a refused import must change nothing, got %+v", cfg.Projects)
	}
}

// TestImportMergeAddsAndNeverMoves: merge is additive. An existing project keeps
// its parent and its feed, an existing rung keeps its posture, and a service
// that already names a project is never re-filed by an import.
func TestImportMergeAddsAndNeverMoves(t *testing.T) {
	services := legacyServices()
	// git is already filed, by hand, somewhere the plan disagrees with.
	for i := range services {
		if services[i].Name == "git" {
			services[i].Project = "hand-filed"
		}
	}
	cfg := &Config{
		Projects: []Project{
			{Name: "hand-filed", Feed: &Feed{URL: "<registry>", Suite: "noble", Component: "main"}},
			// The plan proposes storefront too; this one must survive untouched.
			{Name: "storefront", Parent: "hand-filed"},
		},
		Services: services,
	}
	plan := cfg.ProposeImport()
	if err := cfg.ApplyImport(plan, true); err != nil {
		t.Fatalf("merge: %v", err)
	}

	byName := map[string]Project{}
	for _, p := range cfg.Projects {
		byName[p.Name] = p
	}
	if got := byName["hand-filed"]; got.Feed == nil || got.Feed.Suite != "noble" {
		t.Errorf("merge clobbered an existing project's feed: %+v", got)
	}
	if got := byName["storefront"]; got.Parent != "hand-filed" {
		t.Errorf("merge re-parented an existing project: %+v", got)
	}
	if _, ok := byName["intern"]; !ok {
		t.Errorf("merge should still ADD what does not exist: %+v", cfg.Projects)
	}
	for _, s := range cfg.Services {
		if s.Name == "git" && s.Project != "hand-filed" {
			t.Errorf("merge moved an already-filed service to %q", s.Project)
		}
	}

	dir := t.TempDir()
	if err := Save(filepath.Join(dir, "config.json"), cfg); err != nil {
		t.Fatalf("a merged config must save: %v", err)
	}
}

// TestImportOfAnEmptyConfigIsEmpty: a gateway with no services at all proposes
// nothing and applies cleanly, rather than inventing a root out of no domains.
func TestImportOfAnEmptyConfigIsEmpty(t *testing.T) {
	cfg := &Config{}
	plan := cfg.ProposeImport()
	if len(plan.Projects)+len(plan.Environments)+len(plan.Assignments)+len(plan.Unassigned) != 0 {
		t.Fatalf("an empty config proposed %+v", plan)
	}
	if err := cfg.ApplyImport(plan, false); err != nil {
		t.Fatalf("applying an empty plan: %v", err)
	}
}

// TestApplyImportRejectsAPlanForAnotherConfig: the plan is read at one moment
// and applied at another. A plan naming a service this config does not have is
// a plan from a different config, and applying the rest of it would half-import
// something nobody read.
func TestApplyImportRejectsAPlanForAnotherConfig(t *testing.T) {
	cfg := &Config{Services: []Service{{Name: "web"}}}
	plan := ImportPlan{
		Projects:    []ImportProject{{Name: "shop", Reason: "test"}},
		Assignments: []ImportAssignment{{Service: "gone", Project: "shop", ProjectReason: "test"}},
	}
	err := cfg.ApplyImport(plan, false)
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("want a refusal naming the missing service, got %v", err)
	}
}

// TestSetFeedWritesTheFeedAndValidatesIt is the writer the feed never had:
// everything else about a feed reads, and declaring one meant hand-editing JSON
// on the gateway.
func TestSetFeedWritesTheFeedAndValidatesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := &Config{Projects: []Project{{Name: "acme"}, {Name: "shop", Parent: "acme"}}}

	if err := cfg.SetFeed("nope", Feed{URL: "<registry>", Suite: "noble", Component: "main"}); err == nil {
		t.Fatal("setting a feed on a project that does not exist was accepted")
	}
	// Validation is the feed's own, so an unusable feed is refused HERE rather
	// than at the next Save by something else.
	if err := cfg.SetFeed("acme", Feed{URL: "<registry>", Component: "main"}); err == nil {
		t.Fatal("a feed with no suite was accepted")
	}
	if cfg.Projects[0].Feed != nil {
		t.Fatalf("a refused SetFeed wrote something: %+v", cfg.Projects[0].Feed)
	}

	if err := cfg.SetFeed("acme", Feed{URL: "<registry>", Suite: "noble", Component: "main", KeyID: "<fingerprint>"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The point of declaring it on the root: the child resolves to it.
	resolved, from, err := again.ResolveFeed("shop")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Suite != "noble" || from != "acme" {
		t.Fatalf("shop resolved %+v from %q", resolved, from)
	}

	// Whole-record replacement, never a field merge: a half-updated feed makes
	// "where did this value come from" unanswerable.
	if err := cfg.SetFeed("acme", Feed{URL: "<other-registry>", Suite: "jammy", Component: "contrib"}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Projects[0].Feed; got.KeyID != "" {
		t.Errorf("a replacement kept the old key: %+v", got)
	}
}

// TestSetFeedDoesNotShareItsBackingArray guards the copy ApplyImport and SetFeed
// both make. A Config is copied shallowly in the server on every write, so a
// write through the original slice would change the config another request is
// still serving.
func TestSetFeedDoesNotShareItsBackingArray(t *testing.T) {
	cfg := &Config{Projects: []Project{{Name: "acme"}}}
	before := append([]Project(nil), cfg.Projects...)
	snapshot := cfg.Projects

	if err := cfg.SetFeed("acme", Feed{URL: "<registry>", Suite: "noble", Component: "main"}); err != nil {
		t.Fatal(err)
	}
	if snapshot[0].Feed != nil {
		t.Error("SetFeed wrote through the slice the caller still holds")
	}
	if before[0].Feed != nil {
		t.Error("SetFeed wrote through a copy of the original")
	}
	if cfg.Projects[0].Feed == nil {
		t.Error("SetFeed wrote nothing")
	}
}

// TestImportDoesNotDependOnDomainCase: hostnames are case-insensitive, and a
// config with a capitalised domain must group with its lowercase neighbours
// rather than becoming its own project.
func TestImportDoesNotDependOnDomainCase(t *testing.T) {
	cfg := &Config{Services: []Service{
		{Name: "a", Domains: []string{"A.Shop.<our-co>.<tld>"}},
		{Name: "b", Domains: []string{"b.shop.<our-co>.<tld>"}},
		{Name: "c", Domains: []string{"c.mall.<our-co>.<tld>"}},
	}}
	plan := cfg.ProposeImport()
	if got := assignmentOf(t, plan, "a").Project; got != "shop" {
		t.Fatalf("a -> %q, want shop", got)
	}
	if got := assignmentOf(t, plan, "b").Project; got != "shop" {
		t.Fatalf("b -> %q, want shop", got)
	}
}

// TestImportNeverWritesDuringAProposal is the dry-run contract at the level
// below the CLI: ProposeImport is a read. If it ever starts mutating, the CLI's
// "nothing was written" line becomes a lie no flag can fix.
func TestImportNeverWritesDuringAProposal(t *testing.T) {
	cfg := &Config{Services: legacyServices()}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg.ProposeImport()
	after, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("ProposeImport changed the config:\n%s\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "config.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected: %v", err)
	}
}
