package config

import (
	"strings"
	"testing"
)

func builderFixture() *Config {
	return &Config{
		Projects: []Project{{Name: "redline"}, {Name: "storefront"}},
		Machines: []Machine{{Name: "build-1"}, {Name: "gw-1"}},
	}
}

var digest64 = strings.Repeat("a", 64)

// A builder is a declared machine, granted declared projects (#7).
func TestABuilderIsADeclaredMachine(t *testing.T) {
	c := builderFixture()
	if _, err := c.SetBuilder("laptop", []string{"redline"}, digest64); err == nil || !strings.Contains(err.Error(), "cannot make itself a builder") {
		t.Fatalf("undeclared machine: %v", err)
	}
	if _, err := c.SetBuilder("build-1", []string{"nosuch"}, digest64); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("undeclared project: %v", err)
	}
	if _, err := c.SetBuilder("build-1", nil, digest64); err == nil || !strings.Contains(err.Error(), "granted no project") {
		t.Fatalf("no project: %v", err)
	}
	if len(c.Builders) != 0 {
		t.Fatalf("a refused builder was kept: %+v", c.Builders)
	}
	rotated, err := c.SetBuilder("build-1", []string{"redline", " redline"}, digest64)
	if err != nil || rotated {
		t.Fatalf("first: rotated=%v %v", rotated, err)
	}
	b, _ := c.FindBuilder("build-1")
	if !b.Grants("redline") || b.Grants("storefront") || len(b.Projects) != 1 {
		t.Fatalf("builder = %+v", b)
	}
	rotated, err = c.SetBuilder("build-1", []string{"storefront", "redline"}, strings.Repeat("b", 64))
	if err != nil || !rotated {
		t.Fatalf("re-mint: rotated=%v %v", rotated, err)
	}
	b, _ = c.FindBuilder("build-1")
	if b.TokenSHA256 != strings.Repeat("b", 64) || len(c.Builders) != 1 || !b.Grants("storefront") {
		t.Fatalf("after rotation = %+v", c.Builders)
	}
	if err := c.RemoveBuilder("build-1"); err != nil || len(c.Builders) != 0 {
		t.Fatalf("remove: %v %+v", err, c.Builders)
	}
	if err := c.RemoveBuilder("build-1"); err == nil {
		t.Fatal("removing a builder twice")
	}
}

func TestRemovingABuildersMachineOrProject(t *testing.T) {
	c := builderFixture()
	if _, err := c.SetBuilder("build-1", []string{"redline", "storefront"}, digest64); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveMachine("build-1", false, false); err == nil || !strings.Contains(err.Error(), "builder") {
		t.Fatalf("machine removal not blocked by its builder: %v", err)
	}
	if _, err := c.RemoveProject("redline", false); err == nil || !strings.Contains(err.Error(), "builder") {
		t.Fatalf("project removal not blocked by a grant: %v", err)
	}
	if _, err := c.RemoveProject("redline", true); err != nil {
		t.Fatal(err)
	}
	b, ok := c.FindBuilder("build-1")
	if !ok || b.Grants("redline") || !b.Grants("storefront") {
		t.Fatalf("after the project cascade = %+v %v", b, ok)
	}
	if _, err := c.RemoveMachine("build-1", false, true); err != nil {
		t.Fatal(err)
	}
	if len(c.Builders) != 0 {
		t.Fatalf("the cascade kept the builder: %+v", c.Builders)
	}
	if err := c.ValidateBuilders(); err != nil {
		t.Fatal(err)
	}
}
