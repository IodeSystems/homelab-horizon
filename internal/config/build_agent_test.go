package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func agentFixture() *Config {
	return &Config{
		Projects: []Project{{Name: "redline"}, {Name: "storefront"}},
		Machines: []Machine{{Name: "desktop", Project: "redline"}, {Name: "gw-1"}},
	}
}

var digest64 = strings.Repeat("a", 64)

// A build agent's fields are exactly these. No Machine field under any
// spelling: an agent is not a machine (build_agent.go), and Host is a label.
func TestABuildAgentCarriesNoMachine(t *testing.T) {
	var got []string
	ty := reflect.TypeOf(BuildAgent{})
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	want := []string{"Name", "Project", "Host", "Note", "TokenSHA256"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildAgent fields = %v, want %v", got, want)
	}
	for _, f := range got {
		if strings.Contains(strings.ToLower(f), "machine") {
			t.Fatalf("BuildAgent has a machine field: %s", f)
		}
	}
}

// legacyLiveShape is config.json's builder section EXACTLY as B1 wrote it on
// the live gateway (names from the brief; the digest is a placeholder): a
// builder naming the declared machine `desktop`, owned by redline.
const legacyLiveShape = `{
  "projects": [{"name": "redline"}],
  "machines": [{"name": "desktop", "project": "redline"}],
  "builders": [
    {
      "machine": "desktop",
      "projects": ["redline"],
      "token_sha256": "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"
    }
  ]
}`

// The old shape loads as one agent with the SAME name and token digest, host
// set to the old machine name; Save writes build_agents and no builders key;
// and the saved file reloads to the same agent.
func TestTheOldBuilderShapeMigrates(t *testing.T) {
	cfg, err := LoadFromJSON([]byte(legacyLiveShape))
	if err != nil {
		t.Fatal(err)
	}
	want := []BuildAgent{{Name: "desktop", Project: "redline", Host: "desktop", TokenSHA256: digest64}}
	if !reflect.DeepEqual(cfg.BuildAgents, want) || cfg.LegacyBuilders != nil {
		t.Fatalf("migrated = %+v legacy=%+v, want %+v", cfg.BuildAgents, cfg.LegacyBuilders, want)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"build_agents"`) || strings.Contains(string(raw), `"builders"`) {
		t.Fatalf("Save did not write the new shape only:\n%s", raw)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.BuildAgents, want) {
		t.Fatalf("reloaded = %+v", again.BuildAgents)
	}
}

// A config decoded WITHOUT LoadFromJSON (a backup restore unmarshals straight
// into a Config) is migrated by Save, not refused and not dropped.
func TestSaveMigratesAConfigDecodedDirectly(t *testing.T) {
	cfg := agentFixture()
	cfg.LegacyBuilders = []LegacyBuilder{{Machine: "desktop", Projects: []string{"redline"}, TokenSHA256: digest64}}
	if err := Save(filepath.Join(t.TempDir(), "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if a, ok := cfg.FindBuildAgent("desktop"); !ok || a.TokenSHA256 != digest64 {
		t.Fatalf("after Save = %+v", cfg.BuildAgents)
	}
}

// A builder granted several projects becomes one agent per project. The first
// keeps the name and the token; the rest are `<name>-<project>`, tokenless,
// with a note that says so.
func TestAMultiProjectBuilderSplits(t *testing.T) {
	cfg := agentFixture()
	cfg.LegacyBuilders = []LegacyBuilder{{Machine: "desktop", Projects: []string{"redline", "storefront"}, TokenSHA256: digest64}}
	if err := cfg.MigrateLegacyBuilders(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.BuildAgents) != 2 {
		t.Fatalf("agents = %+v", cfg.BuildAgents)
	}
	first, _ := cfg.FindBuildAgent("desktop")
	if first.Project != "redline" || first.TokenSHA256 != digest64 || first.Host != "desktop" {
		t.Fatalf("first = %+v", first)
	}
	extra, ok := cfg.FindBuildAgent("desktop-storefront")
	if !ok || extra.Project != "storefront" || extra.TokenSHA256 != "" || extra.Host != "desktop" ||
		!strings.Contains(extra.Note, "NO TOKEN") || !strings.Contains(extra.Note, "hz build-agent add desktop-storefront --project storefront") {
		t.Fatalf("extra = %+v", extra)
	}
	if err := cfg.ValidateBuildAgents(); err != nil {
		t.Fatalf("a tokenless migrated agent must validate: %v", err)
	}
}

func TestAMigrationCollisionIsRefused(t *testing.T) {
	cfg := agentFixture()
	cfg.BuildAgents = []BuildAgent{{Name: "desktop", Project: "storefront", TokenSHA256: digest64}}
	cfg.LegacyBuilders = []LegacyBuilder{{Machine: "desktop", Projects: []string{"redline"}, TokenSHA256: digest64}}
	if err := cfg.MigrateLegacyBuilders(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision: %v", err)
	}
}

func TestBuildAgentValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent BuildAgent
		want  string
	}{
		{"no name", BuildAgent{Project: "redline", TokenSHA256: digest64}, "has no name"},
		{"bad charset", BuildAgent{Name: "desk top", Project: "redline", TokenSHA256: digest64}, "must be letters"},
		{"slash", BuildAgent{Name: "a/b", Project: "redline", TokenSHA256: digest64}, "must be letters"},
		{"no project", BuildAgent{Name: "ci-1", TokenSHA256: digest64}, "no project"},
		{"undeclared project", BuildAgent{Name: "ci-1", Project: "nosuch", TokenSHA256: digest64}, "not declared"},
	} {
		c := agentFixture()
		if _, err := c.SetBuildAgent(tc.agent); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
		if len(c.BuildAgents) != 0 {
			t.Errorf("%s: a refused agent was kept: %+v", tc.name, c.BuildAgents)
		}
	}
	c := agentFixture()
	c.BuildAgents = []BuildAgent{{Name: "ci-1", Project: "redline"}, {Name: "ci-1", Project: "storefront"}}
	if err := c.ValidateBuildAgents(); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("duplicate name: %v", err)
	}
	c.BuildAgents = []BuildAgent{{Name: "ci-1", Project: "redline", TokenSHA256: "short"}}
	if err := c.ValidateBuildAgents(); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("bad digest: %v", err)
	}
	// A name need not be, and is never checked against, a machine; nor is Host.
	c = agentFixture()
	if _, err := c.SetBuildAgent(BuildAgent{Name: "laptop-ci", Project: "redline", Host: "no-such-box", TokenSHA256: digest64}); err != nil {
		t.Fatalf("an agent on an undeclared host: %v", err)
	}
}

// Several agents may share a host. A rotate keeps host and note; a different
// project on the same name is refused.
func TestSetBuildAgentRotatesAndKeepsOneProject(t *testing.T) {
	c := agentFixture()
	for _, a := range []BuildAgent{
		{Name: "desktop", Project: "redline", Host: "desktop", Note: "dev box", TokenSHA256: digest64},
		{Name: "desktop-storefront", Project: "storefront", Host: "desktop", TokenSHA256: digest64},
	} {
		if rotated, err := c.SetBuildAgent(a); err != nil || rotated {
			t.Fatalf("add %s: rotated=%v %v", a.Name, rotated, err)
		}
	}
	rotated, err := c.SetBuildAgent(BuildAgent{Name: "desktop", Project: "redline", TokenSHA256: strings.Repeat("b", 64)})
	if err != nil || !rotated {
		t.Fatalf("rotate: rotated=%v %v", rotated, err)
	}
	a, _ := c.FindBuildAgent("desktop")
	if a.TokenSHA256 != strings.Repeat("b", 64) || a.Host != "desktop" || a.Note != "dev box" {
		t.Fatalf("after rotate = %+v", a)
	}
	if _, err := c.SetBuildAgent(BuildAgent{Name: "desktop", Project: "storefront", TokenSHA256: digest64}); err == nil || !strings.Contains(err.Error(), "builds one project") {
		t.Fatalf("project change: %v", err)
	}
	if err := c.RemoveBuildAgent("desktop"); err != nil || len(c.BuildAgents) != 1 {
		t.Fatalf("remove: %v %+v", err, c.BuildAgents)
	}
	if err := c.RemoveBuildAgent("desktop"); err == nil {
		t.Fatal("removing an agent twice")
	}
}

// The live step after deploy: the operator removes machine `desktop` by hand.
// That must neither be blocked by the agent named desktop nor touch it.
func TestRemovingAMachineLeavesTheBuildAgent(t *testing.T) {
	c, err := LoadFromJSON([]byte(legacyLiveShape))
	if err != nil {
		t.Fatal(err)
	}
	removes, blocked, err := c.MachineRemoval("desktop", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(removes, blocked...) {
		if d.Kind == "build-agent" || d.Kind == "builder" {
			t.Fatalf("machine removal names the agent: %+v", d)
		}
	}
	if _, err := c.RemoveMachine("desktop", false, false); err != nil {
		t.Fatalf("removing machine desktop: %v", err)
	}
	a, ok := c.FindBuildAgent("desktop")
	if !ok || a.TokenSHA256 != digest64 || a.Project != "redline" || a.Host != "desktop" {
		t.Fatalf("after removing the machine = %+v %v", a, ok)
	}
	if err := Save(filepath.Join(t.TempDir(), "config.json"), c); err != nil {
		t.Fatalf("the config without the machine must save: %v", err)
	}
}

// Removing a project is blocked by its agents, and the cascade removes them.
func TestRemovingAProjectRemovesItsAgents(t *testing.T) {
	c := agentFixture()
	for _, a := range []BuildAgent{
		{Name: "ci-red", Project: "redline", TokenSHA256: digest64},
		{Name: "ci-store", Project: "storefront", TokenSHA256: digest64},
	} {
		if _, err := c.SetBuildAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	c.Machines = []Machine{{Name: "gw-1"}}
	if _, err := c.RemoveProject("redline", false); err == nil || !strings.Contains(err.Error(), "build-agent") {
		t.Fatalf("project removal not blocked by its agent: %v", err)
	}
	if _, err := c.RemoveProject("redline", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindBuildAgent("ci-red"); ok {
		t.Fatal("the cascade kept the removed project's agent")
	}
	if _, ok := c.FindBuildAgent("ci-store"); !ok {
		t.Fatal("the cascade removed another project's agent")
	}
}
