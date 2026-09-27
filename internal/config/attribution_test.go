package config

import (
	"strings"
	"testing"
)

// Attribution: every section of the system is attributed to a project, or is
// global (plan/design/ui.md, Decision 1 amendment 6). "" is global and always
// legal; a named project has to be declared; removing a project re-attributes
// what named it to global and never deletes it.

func strp(s string) *string { return &s }

// attributedEstate declares two projects (storefront under acme) and one
// record of each attributed kind naming storefront, plus a global one of each.
func attributedEstate() *Config {
	return &Config{
		Projects: []Project{{Name: "acme"}, {Name: "storefront", Parent: "acme"}},
		Machines: []Machine{
			{Name: "app-1", Project: "storefront"},
			{Name: "gw-1"},
		},
		VPNProjects:   map[string]string{"alice-phone": "storefront"},
		ServiceChecks: []ServiceCheck{{Name: "shop-ping", Type: "ping", Target: "10.0.0.5", Project: "storefront"}, {Name: "nas", Type: "ping", Target: "10.0.0.6"}},
		IPBans:        []IPBan{{IP: "198.51.100.7", CreatedAt: 1, Project: "storefront"}, {IP: "203.0.113.9", CreatedAt: 1}},
		PortExclusions: []PortRange{
			{From: 18000, To: 18010, Project: "storefront"},
			{From: 19000},
		},
	}
}

func TestEveryAttributionMustNameADeclaredProject(t *testing.T) {
	if err := attributedEstate().ValidateProjects(); err != nil {
		t.Fatalf("the fixture is a legal config: %v", err)
	}

	cases := []struct {
		kind  string
		wrong func(c *Config)
	}{
		{"machine", func(c *Config) { c.Machines[0].Project = "nope" }},
		{"VPN client", func(c *Config) { c.VPNProjects = map[string]string{"alice-phone": "nope"} }},
		{"check", func(c *Config) { c.ServiceChecks[0].Project = "nope" }},
		{"ban", func(c *Config) { c.IPBans[0].Project = "nope" }},
		{"port exclusion", func(c *Config) { c.PortExclusions[0].Project = "nope" }},
	}
	for _, tc := range cases {
		c := attributedEstate()
		tc.wrong(c)
		err := c.ValidateProjects()
		if err == nil {
			t.Errorf("%s naming an undeclared project was accepted", tc.kind)
			continue
		}
		if !strings.Contains(err.Error(), tc.kind) || !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("%s: the refusal does not name the record and the project: %v", tc.kind, err)
		}
		// Save is the chokepoint; it must refuse the same config.
		if err := Save(t.TempDir()+"/config.json", c); err == nil {
			t.Errorf("%s: Save wrote a config naming an undeclared project", tc.kind)
		}
	}
}

func TestGlobalIsEmptyAndAlwaysLegal(t *testing.T) {
	c := &Config{
		Machines:       []Machine{{Name: "gw-1"}},
		ServiceChecks:  []ServiceCheck{{Name: "nas"}},
		IPBans:         []IPBan{{IP: "203.0.113.9"}},
		PortExclusions: []PortRange{{From: 19000}},
	}
	if err := c.ValidateProjects(); err != nil {
		t.Fatalf("a config with no projects and every record global must validate: %v", err)
	}
	if err := c.CheckProjectRef(""); err != nil {
		t.Fatalf("\"\" is global and must pass CheckProjectRef: %v", err)
	}
	if err := c.CheckProjectRef("storefront"); err == nil {
		t.Fatal("CheckProjectRef accepted an undeclared project")
	}
}

func TestAClientsAttributionFollowsRenameAndDiesWithDelete(t *testing.T) {
	c := attributedEstate()
	served := c.VPNProjects // the map a concurrent reader would still hold

	c.RenamePeerProject("alice-phone", "alice-pixel")
	if got := c.PeerProject("alice-pixel"); got != "storefront" {
		t.Fatalf("rename dropped the attribution: %q", got)
	}
	if got := c.PeerProject("alice-phone"); got != "" {
		t.Fatalf("rename left the old name attributed: %q", got)
	}
	if served["alice-phone"] != "storefront" || len(served) != 1 {
		t.Fatalf("SetPeerProject wrote through the served map: %v", served)
	}

	c.DeletePeerProject("alice-pixel")
	if got := c.PeerProject("alice-pixel"); got != "" {
		t.Fatalf("delete left an attribution a reused name would inherit: %q", got)
	}
	if c.VPNProjects != nil {
		t.Fatalf("an empty attribution map should be nil so it is omitted on the wire: %v", c.VPNProjects)
	}
}

func TestSetMachineChangesTheOwnerWithoutReAdding(t *testing.T) {
	c := attributedEstate()

	m, err := c.SetMachine("gw-1", MachinePatch{Project: strp("acme")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "acme" {
		t.Fatalf("owner not set: %+v", m)
	}
	// nil leaves it alone.
	if m, err = c.SetMachine("gw-1", MachinePatch{Note: strp("the gateway")}); err != nil || m.Project != "acme" || m.Note != "the gateway" {
		t.Fatalf("a note-only patch touched the owner: %+v %v", m, err)
	}
	// "" makes it global.
	if m, err = c.SetMachine("gw-1", MachinePatch{Project: strp("")}); err != nil || m.Project != "" {
		t.Fatalf("clearing the owner: %+v %v", m, err)
	}
	if _, err := c.SetMachine("gw-1", MachinePatch{Project: strp("nope")}); err == nil {
		t.Fatal("SetMachine accepted an undeclared project")
	}
	if got, _ := c.FindMachine("gw-1"); got.Project != "" {
		t.Fatalf("a refused SetMachine was half-written: %+v", got)
	}
	if _, err := c.SetMachine("nowhere", MachinePatch{Project: strp("acme")}); err == nil {
		t.Fatal("SetMachine invented a machine")
	}
	if _, err := c.SetMachine("gw-1", MachinePatch{}); err == nil {
		t.Fatal("an empty patch should be refused rather than read as success")
	}

	// The whole model is validated: a multi-homed machine cannot lose its note.
	c.Machines = append(c.Machines, Machine{Name: "ci-1", Segments: []string{"a", "b"}, Note: "bridges"})
	if _, err := c.SetMachine("ci-1", MachinePatch{Note: strp("")}); err == nil {
		t.Fatal("SetMachine cleared the reason on a multi-homed machine")
	}
}

func TestAddMachineRefusesAnUndeclaredOwner(t *testing.T) {
	c := attributedEstate()
	if err := c.AddMachine(Machine{Name: "app-2", Project: "nope"}); err == nil {
		t.Fatal("AddMachine accepted an undeclared project")
	}
	if err := c.AddMachine(Machine{Name: "app-2", Project: " storefront "}); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.FindMachine("app-2"); m.Project != "storefront" {
		t.Fatalf("project not trimmed and stored: %+v", m)
	}
}

// Without cascade every attributed record blocks, by name.
func TestAttributedRecordsBlockProjectRemoval(t *testing.T) {
	c := attributedEstate()
	c.Projects = append(c.Projects, Project{Name: "lonely"})
	_, blocked, err := c.ProjectRemoval("storefront", false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"machine":        "app-1",
		"vpn-client":     "alice-phone",
		"check":          "shop-ping",
		"ban":            "198.51.100.7",
		"port-exclusion": "18000-18010",
	}
	if len(blocked) != len(want) {
		t.Fatalf("blocked = %v, want one of each of %v", blocked, want)
	}
	for _, d := range blocked {
		if want[d.Kind] != d.Name {
			t.Errorf("unexpected dependant %s", d)
		}
		if !strings.Contains(d.How, "attributed to storefront") {
			t.Errorf("%s does not say how it depends", d)
		}
	}
	if _, err := c.RemoveProject("storefront", false); err == nil {
		t.Fatal("RemoveProject removed a project with attributed records and no cascade")
	}
	// A project nothing is attributed to is still removable without cascade.
	if _, err := c.RemoveProject("lonely", false); err != nil {
		t.Fatalf("an unattributed project should remove cleanly: %v", err)
	}
}

// With cascade they are RE-ATTRIBUTED TO GLOBAL — kept, never deleted — and the
// removal list says so. Removing the PARENT reaches records of the child.
func TestCascadeReattributesToGlobalAndDeletesNothing(t *testing.T) {
	c := attributedEstate()
	before := struct{ machines, checks, bans, excl int }{len(c.Machines), len(c.ServiceChecks), len(c.IPBans), len(c.PortExclusions)}

	removes, err := c.RemoveProject("acme", true)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, d := range removes[1:] {
		kinds[d.Kind] = true
		if d.Kind == "project" || d.Kind == "environment" || d.Kind == "service" {
			continue
		}
		if !strings.Contains(d.How, "RE-ATTRIBUTED TO GLOBAL") || !strings.Contains(d.How, "not deleted") {
			t.Errorf("%s does not say it is kept and re-attributed", d)
		}
	}
	for _, k := range []string{"machine", "vpn-client", "check", "ban", "port-exclusion"} {
		if !kinds[k] {
			t.Errorf("cascade list has no %s", k)
		}
	}

	if len(c.Machines) != before.machines || len(c.ServiceChecks) != before.checks ||
		len(c.IPBans) != before.bans || len(c.PortExclusions) != before.excl {
		t.Fatalf("cascade deleted an attributed record: %+v", c)
	}
	if m, _ := c.FindMachine("app-1"); m.Project != "" {
		t.Errorf("machine still attributed: %+v", m)
	}
	if c.PeerProject("alice-phone") != "" {
		t.Errorf("VPN client still attributed")
	}
	if c.ServiceChecks[0].Project != "" || c.IPBans[0].Project != "" || c.PortExclusions[0].Project != "" {
		t.Errorf("a check, ban or exclusion is still attributed: %+v %+v %+v", c.ServiceChecks[0], c.IPBans[0], c.PortExclusions[0])
	}
	if err := c.ValidateProjects(); err != nil {
		t.Fatalf("cascade left a config Save refuses: %v", err)
	}
}

// RemoveProject must not write through the slices a served config still reads.
func TestCascadeDoesNotMutateTheServedConfig(t *testing.T) {
	served := attributedEstate()
	next := *served // what updateConfig does: a shallow copy
	if _, err := next.RemoveProject("storefront", true); err != nil {
		t.Fatal(err)
	}
	if served.Machines[0].Project != "storefront" || served.ServiceChecks[0].Project != "storefront" ||
		served.IPBans[0].Project != "storefront" || served.PortExclusions[0].Project != "storefront" ||
		served.VPNProjects["alice-phone"] != "storefront" {
		t.Fatalf("the served config was rewritten in place: %+v", served)
	}
}
