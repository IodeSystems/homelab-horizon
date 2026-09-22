package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// THE ESTATE IS plan/example-projection.md §1–§3, TRANSCRIBED.
//
// That file is the spec for this function, so the tests run against it rather
// than against a fixture invented to suit the code: a gateway hosting two
// projects at once, an app box, a multi-homed CI runner that hosts nothing, a
// box with no instances, a prod-posture environment with no machine, and five
// projects that all call an environment "prod". Every awkward row in it is
// there on purpose, and a projection that only handles the happy path fails
// here rather than in a year on a real box.
//
// Names are placeholders because homelab-horizon is a public repo. The shape
// is real.

const (
	feedURL   = "https://packages.example.invalid/debian"
	feedSuite = "noble"
	feedComp  = "main"
	feedKey   = "AAAA1111BBBB2222"
)

func exampleEstate() *config.Config {
	return &config.Config{
		Projects: []config.Project{
			// The root carries the feed; everything under it inherits.
			{Name: "acme-co", Feed: &config.Feed{
				URL: feedURL, Suite: feedSuite, Component: feedComp, KeyID: feedKey,
			}},
			{Name: "intern", Parent: "acme-co"},
			{Name: "storefront", Parent: "acme-co"},
			{Name: "analytics", Parent: "acme-co"},
			{Name: "client-a", Parent: "acme-co"},
			{Name: "client-b", Parent: "acme-co"},
			{Name: "client-c", Parent: "acme-co"},
		},
		Environments: []config.Environment{
			{Project: "intern", Name: "prod", Posture: "prod"},

			{Project: "storefront", Name: "dev", Posture: "dev"},
			{Project: "storefront", Name: "staging", Posture: "staging", From: "dev", Version: "1.4.2"},
			{Project: "storefront", Name: "prod", Posture: "prod", From: "staging", Version: "1.4.0"},

			{Project: "analytics", Name: "beta", Posture: "staging", Version: "0.9.1"},
			{Project: "analytics", Name: "prod", Posture: "prod", From: "beta", Version: "0.9.0"},

			// Name is "prod", posture is staging. Two fields, two meanings.
			{Project: "client-a", Name: "prod", Posture: "staging", Version: "2.1.0"},
			{Project: "client-b", Name: "prod", Posture: "staging", Version: "1.0.7"},
			{Project: "client-c", Name: "prod", Posture: "staging", Version: "3.2.2"},
		},
		// §1's service rows. They carry the project, which is what makes an
		// instance's app coordinate resolvable: a registration's address is
		// environment/app/role with no project in it. `registry` and `git`
		// share a backend and differ by domain — two services, one process.
		Services: []config.Service{
			{Name: "git", Project: "intern", Environment: "prod", Domains: []string{"git.example.invalid", "code.example.invalid"}},
			{Name: "idp", Project: "intern", Environment: "prod", Domains: []string{"idp.example.invalid"}},
			{Name: "registry", Project: "intern", Environment: "prod", Domains: []string{"registry.example.invalid"}},
			{Name: "web", Project: "storefront", Environment: "staging", Domains: []string{"staging.example.invalid"}},
			{Name: "api", Project: "analytics", Environment: "beta", Domains: []string{"beta-api.example.invalid"}},
		},
		Machines: []config.Machine{
			{Name: "gw-1", Segments: []string{"seg:intern", "seg:storefront", "seg:analytics", "seg:people"},
				Note: "the hub: hz itself terminates every segment"},
			{Name: "app-1", Segments: []string{"seg:storefront"}},
			{Name: "app-2", Segments: []string{"seg:storefront"}},
			{Name: "an-1", Segments: []string{"seg:analytics"}},
			{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"},
				Note: "publishes packages, deploys storefront; joined seg:storefront in person"},
			// new-box is pending approval and is deliberately NOT declared.
		},
	}
}

// exampleInstances is §3's "hosts:" column, as registrations.
//
// Note what is NOT here: ci-1 (hosts nothing, permanently and by design) and
// new-box (pending approval). Both are rows the projection has to answer for.
func exampleInstances() []Instance {
	return []Instance{
		// gw-1: two projects on one machine, and three slots of one of them.
		{Machine: "gw-1", Environment: "prod", App: "git", Role: "app"},
		{Machine: "gw-1", Environment: "prod", App: "idp", Role: "app"},
		{Machine: "gw-1", Environment: "staging", App: "web", Role: "app"},
		{Machine: "gw-1", Environment: "staging", App: "web", Role: "next"},
		{Machine: "gw-1", Environment: "staging", App: "web", Role: "ops"},

		{Machine: "app-1", Environment: "prod", App: "web", Role: "app"},
		{Machine: "app-2", Environment: "prod", App: "web", Role: "app"},

		{Machine: "an-1", Environment: "beta", App: "api", Role: "app"},
	}
}

func mustProject(t *testing.T, g Global, machine string) MachineConfig {
	t.Helper()
	mc, err := Project(g, machine)
	if err != nil {
		t.Fatalf("Project(%q): %v", machine, err)
	}
	return mc
}

func gapFor(mc MachineConfig, section string) string {
	for _, g := range mc.Unresolved {
		if g.Section == section {
			return g.Why
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// §5: the worked example. This is the one the file shows the output of.
// ---------------------------------------------------------------------------

// app-1 is example-projection.md §5's subject: one segment, one instance of
// storefront/prod at 1.4.0, one unit. The version, the hold and the unit are
// the join working; the empty segment detail is the join that cannot.
func TestAppBoxMatchesTheWorkedExample(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	mc := mustProject(t, g, "app-1")

	if mc.Machine != "app-1" {
		t.Fatalf("machine = %q", mc.Machine)
	}
	if len(mc.Segments) != 1 || mc.Segments[0].Name != "seg:storefront" {
		t.Fatalf("segments = %+v, want the one membership §5 shows", mc.Segments)
	}
	if len(mc.Forwards) != 0 {
		t.Fatalf("forwards = %+v, want §5's empty list: a single-homed machine has nothing to cross", mc.Forwards)
	}

	want := map[string]Package{
		"storefront": {Name: "storefront", Version: "1.4.0", Hold: true},
		"hz-agent":   {Name: "hz-agent", Version: "0.5.1", Hold: true},
	}
	if len(mc.Packages) != len(want) {
		t.Fatalf("packages = %+v, want %d", mc.Packages, len(want))
	}
	for _, p := range mc.Packages {
		w, ok := want[p.Name]
		if !ok {
			t.Fatalf("unexpected package %+v", p)
		}
		if p != w {
			t.Fatalf("package %s = %+v, want %+v", p.Name, p, w)
		}
	}

	if len(mc.Units) != 1 || mc.Units[0] != (Unit{Name: "storefront@app.service", Enabled: true}) {
		t.Fatalf("units = %+v, want §5's storefront@app.service", mc.Units)
	}

	if len(mc.Feeds) != 1 {
		t.Fatalf("feeds = %+v, want the one the root declares", mc.Feeds)
	}
	if mc.Feeds[0].From != "acme-co" {
		t.Fatalf("feed came from %q, want acme-co — the root declares it and storefront inherits", mc.Feeds[0].From)
	}
	if mc.Feeds[0].URL != feedURL || mc.Feeds[0].Suite != feedSuite ||
		mc.Feeds[0].Component != feedComp || mc.Feeds[0].KeyID != feedKey {
		t.Fatalf("feed = %+v, want the root's four fields whole", mc.Feeds[0])
	}
}

// §5 shows a segment with an interface, an address and a peer set. hz cannot
// produce any of the three, and the test that matters is that it SAYS so:
// a membership with no interface must not read as "this machine wants no
// interface".
func TestASegmentMembershipIsNotAConfiguredInterface(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "app-1")

	seg := mc.Segments[0]
	if seg.Resolved {
		t.Fatal("a segment reported itself resolved; nothing resolves a segment name until item 15")
	}
	if seg.Interface != "" || seg.Address != "" || len(seg.Peers) > 0 {
		t.Fatalf("segment carries detail hz cannot know: %+v", seg)
	}
	why := gapFor(mc, SectionSegments)
	if why == "" {
		t.Fatal("an unresolved segment produced no gap — an empty interface is then indistinguishable from a wanted one")
	}
	if !strings.Contains(why, "item 15") {
		t.Fatalf("the segment gap does not name what would close it: %q", why)
	}
	if gapFor(mc, SectionHosts) == "" {
		t.Fatal("hosts is empty with no gap beside it, so hz appears to have decided this machine wants no /etc/hosts entries")
	}
}

// The JSON is the wire, so the honesty has to survive marshalling: `resolved`
// must be present and false rather than elided, and a computed-empty list must
// be [] rather than null.
func TestTheJSONSaysUnresolvedOutLoud(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances()}
	b, err := json.Marshal(mustProject(t, g, "app-1"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"resolved":false`, `"forwards":[]`, `"hosts":[]`, `"unresolved":[`} {
		if !strings.Contains(s, want) {
			t.Fatalf("payload does not carry %s: %s", want, s)
		}
	}
	if strings.Contains(s, `:null`) {
		t.Fatalf("a null crossed the wire; every empty section must be an empty list: %s", s)
	}
}

// ---------------------------------------------------------------------------
// §3's awkward rows
// ---------------------------------------------------------------------------

// gw-1 is "one machine, two projects" — the row that makes a Project field on
// a machine a lie. Two packages, two feeds collapsing to one source, and the
// three storefront slots as three units.
func TestTheGatewayHostsTwoProjectsAtOnce(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	mc := mustProject(t, g, "gw-1")

	names := map[string]string{}
	for _, p := range mc.Packages {
		names[p.Name] = p.Version
	}
	if names["storefront"] != "1.4.2" {
		t.Fatalf("storefront pinned to %q, want staging's 1.4.2 — gw-1 hosts the staging rung", names["storefront"])
	}
	if _, ok := names["hz-agent"]; !ok {
		t.Fatal("the agent's own package is missing")
	}
	// intern/prod declares no version, so there must be NO intern package and
	// a gap saying why. Inventing one would be hz telling a box to install
	// whatever the feed happens to hold.
	if v, ok := names["intern"]; ok {
		t.Fatalf("hz projected an intern package at %q, but intern/prod declares no version", v)
	}
	if why := gapFor(mc, SectionPackages); !strings.Contains(why, "declares no version") {
		t.Fatalf("no gap explains the missing intern package: %q", why)
	}

	// One feed, not two: both projects inherit the root's, and two identical
	// sources entries would be written twice.
	if len(mc.Feeds) != 1 {
		t.Fatalf("feeds = %+v, want one — both projects resolve to the root's declaration", mc.Feeds)
	}

	// The three storefront slots: app, next and the loopback-only ops.
	got := map[string]bool{}
	for _, u := range mc.Units {
		got[u.Name] = u.Enabled
	}
	for _, want := range []string{"storefront@app.service", "storefront@next.service", "storefront@ops.service"} {
		if !got[want] {
			t.Fatalf("units = %+v, want %s — three slots of one project are three units", mc.Units, want)
		}
	}
}

// THE FINDING, PINNED AS A TEST. example-projection.md §5 names a unit
// <project>@<role>.service, dropping the app coordinate — and §3's gateway
// hosts intern/prod/git/app AND intern/prod/idp/app, two apps of one project
// at one role. Both project to `intern@app.service`.
//
// The projection renders the name the spec gives and reports the collision
// rather than silently running one of the two boxes' worth of services. This
// test exists so that a future change to the naming is a deliberate change to
// the spec, not a quiet fix that makes §5 wrong.
func TestTheUnitNameInTheSpecCollidesOnTheGateway(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "gw-1")

	count := 0
	for _, u := range mc.Units {
		if u.Name == "intern@app.service" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("intern@app.service appears %d times; the projection must not emit a duplicate unit", count)
	}
	why := gapFor(mc, SectionUnits)
	if why == "" {
		t.Fatal("two instances collapsed into one unit with no gap: hz would silently run one service where the estate declares two")
	}
	for _, want := range []string{"intern@app.service", "prod/git/app", "prod/idp/app"} {
		if !strings.Contains(why, want) {
			t.Fatalf("the collision gap does not name %q: %q", want, why)
		}
	}
}

// ci-1: a segment, an agent, a healthy poll and NOTHING to host. The row
// example-projection.md calls "the state most likely to be mis-rendered as an
// alarm". Its projection is empty of packages and units, and that emptiness is
// an ANSWER — there must be no instances gap.
func TestTheCIRunnerHostsNothingAndThatIsAnAnswer(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	mc := mustProject(t, g, "ci-1")

	if len(mc.Units) != 0 {
		t.Fatalf("units = %+v, want none: ci-1 hosts no instance", mc.Units)
	}
	// The agent's own package is the only one a box with no instances wants.
	if len(mc.Packages) != 1 || mc.Packages[0].Name != AgentPackage {
		t.Fatalf("packages = %+v, want only the agent's", mc.Packages)
	}
	if why := gapFor(mc, SectionInstances); why != "" {
		t.Fatalf("hosting nothing was reported as something hz could not work out: %q", why)
	}
	if why := gapFor(mc, SectionPackages); why != "" {
		t.Fatalf("hosting nothing produced a packages gap: %q", why)
	}

	// Multi-homed, and the answer to "may it forward between its own
	// interfaces" is no. Empty here is the rule's own default, not a hole.
	if len(mc.Segments) != 2 {
		t.Fatalf("segments = %+v, want the two ci-1 bridges", mc.Segments)
	}
	if len(mc.Forwards) != 0 {
		t.Fatalf("forwards = %+v, want deny — a crossing is a declared exception and nothing declares one", mc.Forwards)
	}
}

// new-box is pending approval: an agent fingerprint and no machine record. hz
// still answers, and the answer says the record is what is missing.
func TestAnUndeclaredMachineGetsAnAnswerThatSaysSo(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "new-box")

	if mc.Machine != "new-box" {
		t.Fatalf("machine = %q", mc.Machine)
	}
	if len(mc.Segments) != 0 || len(mc.Units) != 0 {
		t.Fatalf("hz projected content for a machine it does not declare: %+v", mc)
	}
	why := gapFor(mc, SectionMachine)
	if !strings.Contains(why, "hz machine add new-box") {
		t.Fatalf("the gap does not name the command that fixes it: %q", why)
	}
}

// analytics/prod has a posture, a version and NO MACHINE — the majority state
// early on. Nothing should appear on any box for it, and nothing should be
// reported as a problem: an environment with no placement is not a gap in a
// machine's projection, it is simply not on that machine.
func TestAnEnvironmentWithNoMachineAppearsNowhere(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances()}
	for _, machine := range []string{"gw-1", "app-1", "app-2", "an-1", "ci-1"} {
		mc := mustProject(t, g, machine)
		for _, p := range mc.Packages {
			if p.Name == "analytics" && p.Version == "0.9.0" {
				t.Fatalf("%s was told to install analytics/prod's version; that rung has no machine", machine)
			}
		}
	}
	// an-1 runs the beta rung and only the beta rung.
	mc := mustProject(t, g, "an-1")
	for _, p := range mc.Packages {
		if p.Name == "analytics" && p.Version != "0.9.1" {
			t.Fatalf("an-1 pinned analytics to %q, want beta's 0.9.1", p.Version)
		}
	}
}

// ---------------------------------------------------------------------------
// The join that cannot be made
// ---------------------------------------------------------------------------

// THE FINDING THE APP COORDINATE ANSWERS. §1 has SIX projects declaring an
// environment called "prod", so the environment name alone identifies nothing
// — `prod/web/app` is ambiguous on its face. What resolves it is the app:
// `web` is a service, and a service carries its project.
//
// This test is the positive control for that join. If Service.Project ever
// stops being consulted, app-1 falls back to the ambiguous environment name
// and this fails.
func TestTheAppCoordinateSuppliesTheProjectTheAddressLacks(t *testing.T) {
	cfg := exampleEstate()

	// Precondition, asserted rather than assumed: "prod" is not an identity in
	// this estate.
	if n := len(cfg.EnvironmentsNamed("prod")); n < 2 {
		t.Fatalf("precondition: %d projects declare \"prod\"; the ambiguity this test is about does not exist", n)
	}
	if _, err := cfg.LookupEnvironment("", "prod"); err == nil {
		t.Fatal("precondition: the environment name \"prod\" resolved on its own, so nothing here is being tested")
	}

	mc := mustProject(t, Global{Config: cfg, Instances: exampleInstances()}, "app-1")
	if len(mc.Packages) == 0 {
		t.Fatalf("app-1 resolved nothing: %+v", mc.Unresolved)
	}
	if mc.Packages[0].Version != "1.4.0" {
		t.Fatalf("app-1 pinned to %q, want storefront/prod's 1.4.0", mc.Packages[0].Version)
	}

	// And the negative: take the service away and the address goes back to
	// being ambiguous, refused rather than guessed.
	cfg2 := exampleEstate()
	var kept []config.Service
	for _, s := range cfg2.Services {
		if s.Name != "web" {
			kept = append(kept, s)
		}
	}
	cfg2.Services = kept

	mc2 := mustProject(t, Global{Config: cfg2, Instances: exampleInstances()}, "app-1")
	if len(mc2.Units) != 0 || len(mc2.Packages) != 0 {
		t.Fatalf("hz guessed a project for an ambiguous environment name: units=%+v packages=%+v", mc2.Units, mc2.Packages)
	}
	why := gapFor(mc2, SectionInstances)
	if !strings.Contains(why, "prod/web/app") || !strings.Contains(why, "no project coordinate") {
		t.Fatalf("the ambiguity gap does not explain itself: %q", why)
	}
	if !strings.Contains(why, "hz service assign web") {
		t.Fatalf("the gap does not name what would fix it: %q", why)
	}
}

// One app name, two projects. Nothing in the address breaks the tie and hz
// must not pick — picking would install a client's version on another
// client's box.
func TestAnAppNamedByTwoProjectsIsRefused(t *testing.T) {
	cfg := exampleEstate()
	cfg.Services = append(cfg.Services,
		config.Service{Name: "web", Project: "client-a", Environment: "prod"})

	mc := mustProject(t, Global{Config: cfg, Instances: exampleInstances()}, "app-1")
	if len(mc.Packages) != 0 {
		t.Fatalf("hz picked between two projects claiming the app: %+v", mc.Packages)
	}
	why := gapFor(mc, SectionInstances)
	if !strings.Contains(why, "client-a") || !strings.Contains(why, "storefront") {
		t.Fatalf("the gap does not name both claimants: %q", why)
	}
}

// A service that names a project which does not declare the registration's
// rung. The project is known and the environment is not, which is a different
// answer from "which project" and has a different fix.
func TestAnAppOnARungItsProjectDoesNotDeclare(t *testing.T) {
	inst := append(exampleInstances(),
		Instance{Machine: "app-1", Environment: "qa", App: "web", Role: "app"})

	mc := mustProject(t, Global{Config: exampleEstate(), Instances: inst}, "app-1")
	why := gapFor(mc, SectionInstances)
	if !strings.Contains(why, "declares no environment \"qa\"") {
		t.Fatalf("the gap does not say the rung is missing: %q", why)
	}
	if !strings.Contains(why, "hz env add storefront/qa") {
		t.Fatalf("the gap does not name the command that declares it: %q", why)
	}
}

// ---------------------------------------------------------------------------
// Purity, as behaviour rather than as an import list
// ---------------------------------------------------------------------------

// The projection must give the same answer twice, and it must give an answer
// for a machine that does not exist anywhere. Both are what make the function
// usable on a fleet; seam_test.go guards the mechanism, this guards the claim.
func TestTheProjectionIsDeterministicAndNeedsNoMachine(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	for _, machine := range []string{"gw-1", "app-1", "ci-1", "nowhere-at-all"} {
		a, err := json.Marshal(mustProject(t, g, machine))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(mustProject(t, g, machine))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s projected differently twice:\n%s\n%s", machine, a, b)
		}
	}
}

// Map iteration order must not reach the output: a projection is hashed into a
// generation, so an unstable order would report a change on every other poll.
func TestOrderIsStableAcrossRuns(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	first, err := json.Marshal(mustProject(t, g, "gw-1"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := json.Marshal(mustProject(t, g, "gw-1"))
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differed:\n%s\n%s", i, first, again)
		}
	}
}

func TestTheEmptyMachineIsACallerMistake(t *testing.T) {
	if _, err := Project(Global{Config: exampleEstate()}, "  "); err == nil {
		t.Fatal("project(global, \"\") answered; there is no machine to answer for")
	}
}

// A nil config must not panic. hz boots before it has one and the drift screen
// asks for a projection on every read.
func TestANilConfigIsAnEmptyEstate(t *testing.T) {
	mc, err := Project(Global{}, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mc.Segments) != 0 || len(mc.Packages) != 0 || len(mc.Units) != 0 {
		t.Fatalf("an empty estate projected content: %+v", mc)
	}
	if !mc.Unresolvable(SectionMachine) {
		t.Fatal("an undeclared machine in an empty estate produced no gap")
	}
}

// Two environments of one project on one machine pin one package name to two
// versions. apt cannot hold two versions of one path, so hz must name the
// conflict rather than pick — picking would silently downgrade a box.
func TestTwoVersionsOfOnePackageIsNamedNotPicked(t *testing.T) {
	cfg := exampleEstate()
	inst := append(exampleInstances(),
		Instance{Machine: "app-1", Environment: "staging", App: "web", Role: "next"})

	mc := mustProject(t, Global{Config: cfg, Instances: inst}, "app-1")
	count := 0
	for _, p := range mc.Packages {
		if p.Name == "storefront" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("storefront appears %d times in the package list", count)
	}
	why := gapFor(mc, SectionPackages)
	if !strings.Contains(why, "two versions") {
		t.Fatalf("the version conflict was not reported: %q", why)
	}
	if !strings.Contains(why, "1.4.0") || !strings.Contains(why, "1.4.2") {
		t.Fatalf("the conflict gap does not name both versions: %q", why)
	}
}

// A project with no feed anywhere up its tree cannot tell a machine where its
// package comes from. The package is still declared — the version is known —
// and the missing source is said out loud.
func TestAProjectWithNoFeedSaysSo(t *testing.T) {
	cfg := exampleEstate()
	cfg.Projects[0].Feed = nil // the root's declaration, removed

	mc := mustProject(t, Global{Config: cfg, Instances: exampleInstances()}, "app-1")
	if len(mc.Feeds) != 0 {
		t.Fatalf("feeds = %+v, want none", mc.Feeds)
	}
	if why := gapFor(mc, SectionFeeds); !strings.Contains(why, "hz feed set storefront") {
		t.Fatalf("the missing feed does not name the command that declares one: %q", why)
	}
	// The package survives: hz knows the version, it just cannot say where
	// from. Dropping it would hide a known fact behind an unknown one.
	found := false
	for _, p := range mc.Packages {
		if p.Name == "storefront" && p.Version == "1.4.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a missing feed took the package with it: %+v", mc.Packages)
	}
}

// An agent version hz does not have must not become "install latest".
func TestNoAgentVersionMeansNoAgentPackage(t *testing.T) {
	mc := mustProject(t, Global{Config: exampleEstate(), Instances: exampleInstances()}, "app-1")
	for _, p := range mc.Packages {
		if p.Name == AgentPackage {
			t.Fatalf("hz projected an agent package with no version declared: %+v", p)
		}
	}
	for _, p := range mc.Packages {
		if p.Version == "" {
			t.Fatalf("a package crossed with no version: %+v — an unpinned package is not a projection", p)
		}
	}
}

// Every package hz declares is held. A version declared and not held is a
// version the box upgrades itself out of overnight.
func TestEveryDeclaredVersionIsHeld(t *testing.T) {
	g := Global{Config: exampleEstate(), Instances: exampleInstances(), AgentVersion: "0.5.1"}
	for _, machine := range []string{"gw-1", "app-1", "app-2", "an-1", "ci-1"} {
		for _, p := range mustProject(t, g, machine).Packages {
			if !p.Hold {
				t.Fatalf("%s: package %+v is pinned and not held", machine, p)
			}
		}
	}
}

// Serial is carried, not invented: a pure function has no monotonic anything.
func TestSerialIsCarriedIn(t *testing.T) {
	mc := mustProject(t, Global{Config: exampleEstate(), Serial: 47}, "app-1")
	if mc.Serial != 47 {
		t.Fatalf("serial = %d, want the 47 the caller passed", mc.Serial)
	}
}
