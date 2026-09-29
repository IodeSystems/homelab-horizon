package config

import (
	"strings"
	"testing"
)

// Environment.Upstream and Machine.HZ (plan/plan.md Tier 1b, N3): a nested hz
// is a Machine that runs hz, and a rung placed in it names that machine.

// nestedEstate is redline's shape: iodesystems declares redline/prod, which is
// placed in redline-prod-hz; build-1 is an ordinary machine with no marker.
func nestedEstate(t *testing.T) *Config {
	t.Helper()
	c := &Config{}
	if err := c.AddProject("redline", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "redline-prod-hz", Project: "redline", HZ: &MachineHZ{URL: " https://hz.prod.redline.example "}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "build-1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "staging", Posture: "staging"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnUpstreamNamingANestedHZIsAccepted(t *testing.T) {
	c := nestedEstate(t)
	if m, _ := c.FindMachine("redline-prod-hz"); m.HZ == nil || m.HZ.URL != "https://hz.prod.redline.example" {
		t.Fatalf("the hz URL was not stored trimmed: %+v", m.HZ)
	}
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatalf("an Upstream naming a declared machine with an hz marker must be legal: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("Save refused it: %v", err)
	}
}

// Two refusals with two different fixes, so each must say which.
func TestAnUpstreamMustNameADeclaredMachineThatRunsHZ(t *testing.T) {
	c := nestedEstate(t)

	err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "ghost-hz"})
	if err == nil || !strings.Contains(err.Error(), "names no declared machine") || !strings.Contains(err.Error(), "redline-prod-hz") {
		t.Fatalf("an undeclared upstream must be refused, naming the machines that could be one; got %v", err)
	}

	err = c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "build-1"})
	if err == nil || !strings.Contains(err.Error(), "no hz marker") || !strings.Contains(err.Error(), "build-1") {
		t.Fatalf("a machine without an hz marker must be refused as an upstream, saying so; got %v", err)
	}

	// The same through set, and through Save for a config written by hand.
	upstream := "build-1"
	if _, err := c.SetEnvironment("redline", "staging", EnvironmentPatch{Upstream: &upstream}); err == nil {
		t.Fatal("set accepted an upstream with no hz marker")
	}
	c.Environments[0].Upstream = "build-1"
	if err := Save(t.TempDir()+"/config.json", c); err == nil || !strings.Contains(err.Error(), "no hz marker") {
		t.Fatalf("Save must refuse an upstream with no hz marker; got %v", err)
	}
}

func TestSetEnvironmentSetsAndClearsTheUpstream(t *testing.T) {
	c := nestedEstate(t)
	upstream := "redline-prod-hz"
	e, err := c.SetEnvironment("redline", "staging", EnvironmentPatch{Upstream: &upstream})
	if err != nil || e.Upstream != "redline-prod-hz" {
		t.Fatalf("set upstream: %+v %v", e, err)
	}
	clear := ""
	e, err = c.SetEnvironment("redline", "staging", EnvironmentPatch{Upstream: &clear})
	if err != nil || e.Upstream != "" {
		t.Fatalf("a non-nil empty upstream must clear it: %+v %v", e, err)
	}
}

func TestAnHZURLMustBeHTTPWithAHost(t *testing.T) {
	for _, bad := range []string{"", "   ", "hz.example", "ftp://hz.example", "https://", "https:///path", "://x"} {
		c := &Config{}
		if err := c.AddMachine(Machine{Name: "n", HZ: &MachineHZ{URL: bad}}); err == nil {
			t.Errorf("hz URL %q was accepted", bad)
		}
	}
	for _, good := range []string{"http://10.0.0.5:8080", "https://hz.example/", "https://[fd00::1]:8443"} {
		c := &Config{}
		if err := c.AddMachine(Machine{Name: "n", HZ: &MachineHZ{URL: good}}); err != nil {
			t.Errorf("hz URL %q was refused: %v", good, err)
		}
	}
}

// Set: nil leaves the marker, a value sets it, ClearHZ removes it — and
// clearing it off a machine a rung names as its Upstream is refused by name.
func TestSetMachineHZIsExplicit(t *testing.T) {
	c := nestedEstate(t)
	note := "the build box"
	m, err := c.SetMachine("redline-prod-hz", MachinePatch{Note: &note})
	if err != nil || m.HZ == nil {
		t.Fatalf("a patch without HZ must leave the marker alone: %+v %v", m, err)
	}
	m, err = c.SetMachine("build-1", MachinePatch{HZ: &MachineHZ{URL: "https://build-1.example"}})
	if err != nil || m.HZ == nil || m.HZ.URL != "https://build-1.example" {
		t.Fatalf("set hz: %+v %v", m, err)
	}
	if _, err := c.SetMachine("build-1", MachinePatch{HZ: &MachineHZ{URL: "nope"}}); err == nil {
		t.Fatal("set accepted a bad hz URL")
	}
	if _, err := c.SetMachine("build-1", MachinePatch{HZ: &MachineHZ{URL: "https://x.example"}, ClearHZ: true}); err == nil {
		t.Fatal("set and clear at once must be refused")
	}
	if m, err = c.SetMachine("build-1", MachinePatch{ClearHZ: true}); err != nil || m.HZ != nil {
		t.Fatalf("clear hz: %+v %v", m, err)
	}

	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatal(err)
	}
	_, err = c.SetMachine("redline-prod-hz", MachinePatch{ClearHZ: true})
	if err == nil || !strings.Contains(err.Error(), "redline/prod") {
		t.Fatalf("clearing the marker of an upstream must be refused, naming the rung; got %v", err)
	}
}

// Removing the machine an Upstream names BLOCKS without cascade, naming the
// rung — and cascade CLEARS the Upstream and keeps the rung.
func TestRemovingAnUpstreamMachineClearsTheUpstreamNeverTheRung(t *testing.T) {
	c := nestedEstate(t)
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Version: "2.0.0", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatal(err)
	}

	_, blocked, err := c.MachineRemoval("redline-prod-hz", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Kind != "environment" || blocked[0].Name != "redline/prod" {
		t.Fatalf("the rung naming it must block the removal, got %+v", blocked)
	}
	if _, err := c.RemoveMachine("redline-prod-hz", false, false); err == nil {
		t.Fatal("removal without cascade went through")
	}

	removes, err := c.RemoveMachine("redline-prod-hz", false, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range removes {
		if d.Kind == "environment" && d.Name == "redline/prod" && strings.Contains(d.How, "CLEARED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("cascade must list the Upstream it clears, got %+v", removes)
	}
	e, err := c.LookupEnvironment("redline", "prod")
	if err != nil {
		t.Fatalf("cascade deleted the rung: %v", err)
	}
	if e.Upstream != "" || e.From != "staging" || e.Version != "2.0.0" || e.Posture != "prod" {
		t.Fatalf("cascade must clear Upstream and nothing else, got %+v", e)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the cascaded config is not saveable: %v", err)
	}
}
