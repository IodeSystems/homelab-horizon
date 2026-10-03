package config

import (
	"strings"
	"testing"
)

// Environment.Upstream and the HZInstance record (B3, 2026-10-02; N3 before
// it): a nested hz is an Hz Instance, not a Machine, and a rung placed in it
// names that instance.

// nestedEstate is redline's shape: iodesystems declares redline/prod, which is
// placed in the hz instance redline-prod-hz; build-1 is an ordinary machine.
func nestedEstate(t *testing.T) *Config {
	t.Helper()
	c := &Config{}
	if err := c.AddProject("redline", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddHZInstance(HZInstance{Name: "redline-prod-hz", Project: "redline", URL: " https://hz.prod.redline.example "}); err != nil {
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

func TestAnUpstreamNamingAnHZInstanceIsAccepted(t *testing.T) {
	c := nestedEstate(t)
	if h, _ := c.FindHZInstance("redline-prod-hz"); h.URL != "https://hz.prod.redline.example" {
		t.Fatalf("the hz URL was not stored trimmed: %+v", h)
	}
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatalf("an Upstream naming a declared hz instance must be legal: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("Save refused it: %v", err)
	}
}

// An Upstream resolves against hz_instances ONLY: a machine of that name is
// not an hz, whatever runs on it.
func TestAnUpstreamMustNameADeclaredHZInstance(t *testing.T) {
	c := nestedEstate(t)

	err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "ghost-hz"})
	if err == nil || !strings.Contains(err.Error(), "names no declared hz instance") || !strings.Contains(err.Error(), "redline-prod-hz") {
		t.Fatalf("an undeclared upstream must be refused, naming the instances that could be one; got %v", err)
	}

	err = c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "build-1"})
	if err == nil || !strings.Contains(err.Error(), "names no declared hz instance") || !strings.Contains(err.Error(), "build-1") {
		t.Fatalf("a MACHINE must be refused as an upstream; got %v", err)
	}

	// The same through set, and through Save for a config written by hand.
	upstream := "build-1"
	if _, err := c.SetEnvironment("redline", "staging", EnvironmentPatch{Upstream: &upstream}); err == nil {
		t.Fatal("set accepted a machine as an upstream")
	}
	c.Environments[0].Upstream = "build-1"
	if err := Save(t.TempDir()+"/config.json", c); err == nil || !strings.Contains(err.Error(), "names no declared hz instance") {
		t.Fatalf("Save must refuse an upstream naming no instance; got %v", err)
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
		if err := c.AddHZInstance(HZInstance{Name: "n", URL: bad}); err == nil {
			t.Errorf("hz URL %q was accepted", bad)
		}
	}
	for _, good := range []string{"http://10.0.0.5:8080", "https://hz.example/", "https://[fd00::1]:8443"} {
		c := &Config{}
		if err := c.AddHZInstance(HZInstance{Name: "n", URL: good}); err != nil {
			t.Errorf("hz URL %q was refused: %v", good, err)
		}
	}
}

// Name charset, uniqueness among instances, a declared owner.
func TestAnHZInstanceIsValidated(t *testing.T) {
	c := nestedEstate(t)
	for name, tc := range map[string]struct {
		h    HZInstance
		want string
	}{
		"charset":   {HZInstance{Name: "bad name", URL: "http://192.0.2.1:8080"}, "must be letters"},
		"duplicate": {HZInstance{Name: "redline-prod-hz", URL: "http://192.0.2.1:8080"}, "already exists"},
		"project":   {HZInstance{Name: "x-hz", Project: "nope", URL: "http://192.0.2.1:8080"}, `no project "nope"`},
		"vpn":       {HZInstance{Name: "x-hz", URL: "http://192.0.2.1:8080", VPNClient: "ghost"}, "no such client"},
	} {
		if err := c.AddHZInstance(tc.h); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// A machine may share an instance's name: separate records, separate
	// namespaces (the migration keeps a carrier that holds more than the
	// marker under the same name).
	if err := c.AddMachine(Machine{Name: "redline-prod-hz"}); err != nil {
		t.Fatalf("a machine sharing an instance's name was refused: %v", err)
	}
}

// Removing an instance an Upstream names BLOCKS without cascade, naming the
// rung — and cascade CLEARS the Upstream and keeps the rung. The dry run is
// the same call without the write.
func TestRemovingAnUpstreamInstanceClearsTheUpstreamNeverTheRung(t *testing.T) {
	c := nestedEstate(t)
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Version: "2.0.0", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatal(err)
	}

	_, blocked, err := c.HZInstanceRemoval("redline-prod-hz", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Kind != "environment" || blocked[0].Name != "redline/prod" {
		t.Fatalf("the rung naming it must block the removal, got %+v", blocked)
	}
	if _, err := c.RemoveHZInstance("redline-prod-hz", false); err == nil {
		t.Fatal("removal without cascade went through")
	}
	if _, ok := c.FindHZInstance("redline-prod-hz"); !ok {
		t.Fatal("a refused removal removed the instance")
	}

	removes, err := c.RemoveHZInstance("redline-prod-hz", true)
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

// Removing a MACHINE never touches an Upstream: nothing names a machine as
// one any more.
func TestRemovingAMachineNeverTouchesAnUpstream(t *testing.T) {
	c := nestedEstate(t)
	if err := c.AddEnvironment(Environment{Project: "redline", Name: "prod", Posture: "prod", Upstream: "redline-prod-hz"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "redline-prod-hz"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveMachine("redline-prod-hz", false, false); err != nil {
		t.Fatalf("a machine sharing the instance's name must remove alone: %v", err)
	}
	if e, _ := c.LookupEnvironment("redline", "prod"); e.Upstream != "redline-prod-hz" {
		t.Fatalf("removing a machine changed an Upstream: %+v", e)
	}
	if _, ok := c.FindHZInstance("redline-prod-hz"); !ok {
		t.Fatal("removing a machine removed the hz instance")
	}
}
