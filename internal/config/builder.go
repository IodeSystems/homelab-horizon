package config

import (
	"fmt"
	"sort"
	"strings"
)

// Builders: the machines allowed to start builds, and for which projects
// (operator, 2026-10-02 — build numbers come FROM hz).
//
// A builder is a DECLARED machine (#7: declare, then enrol — a box cannot make
// itself a builder) granted for one or more projects. hz mints its token once
// (POST /api/v1/builders) and keeps only the sha256 here, the way an instance
// token's sha256 sits on MachineHZ. It is declared state, so it lives in
// config.json and peer-syncs; the builds themselves are observations and live
// in hz.db.
//
// The token is the builder's only power: start, log, state, finish and
// artifact upload, for its granted projects (internal/server/handlers_api_builds.go).

// Builder is one builder grant.
type Builder struct {
	Machine  string   `json:"machine"`
	Projects []string `json:"projects"`
	// TokenSHA256 is the sha256 (hex) of the builder token. The token itself
	// is shown once, at mint, and never stored.
	TokenSHA256 string `json:"token_sha256"`
}

// Grants reports whether this builder may build project.
func (b Builder) Grants(project string) bool {
	for _, p := range b.Projects {
		if p == project {
			return true
		}
	}
	return false
}

// FindBuilder is the builder record of machine, if any.
func (c *Config) FindBuilder(machine string) (Builder, bool) {
	for _, b := range c.Builders {
		if b.Machine == machine {
			return b, true
		}
	}
	return Builder{}, false
}

func normalizeProjects(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// SetBuilder grants machine the projects and records the sha256 of its newly
// minted token, replacing any earlier one — re-minting rotates the token and
// replaces the grant. rotated is true when the machine was a builder already.
func (c *Config) SetBuilder(machine string, projects []string, sha string) (rotated bool, err error) {
	machine = strings.TrimSpace(machine)
	if len(sha) != 64 {
		return false, fmt.Errorf("builder token digest must be 64 hex characters, got %d", len(sha))
	}
	b := Builder{Machine: machine, Projects: normalizeProjects(projects), TokenSHA256: sha}
	next := c.copyForWrite()
	replaced := false
	for i := range next.Builders {
		if next.Builders[i].Machine == machine {
			next.Builders[i] = b
			replaced = true
		}
	}
	if !replaced {
		next.Builders = append(next.Builders, b)
		sort.Slice(next.Builders, func(i, j int) bool { return next.Builders[i].Machine < next.Builders[j].Machine })
	}
	if err := next.ValidateBuilders(); err != nil {
		return false, err
	}
	c.adopt(next)
	return replaced, nil
}

// RemoveBuilder revokes machine's builder token and grant. Its builds stay in
// hz.db, but none of them is promotable while no builder of that machine is
// granted for its project (the provenance check reads the grant at promote).
func (c *Config) RemoveBuilder(machine string) error {
	next := c.copyForWrite()
	out := next.Builders[:0]
	found := false
	for _, b := range next.Builders {
		if b.Machine == machine {
			found = true
			continue
		}
		out = append(out, b)
	}
	if !found {
		return fmt.Errorf("no builder %q — `hz builder ls` lists them", machine)
	}
	if len(out) == 0 {
		out = nil
	}
	next.Builders = out
	c.adopt(next)
	return nil
}

// ValidateBuilders refuses a builder that names an undeclared machine or
// project, grants nothing, appears twice, or carries no token digest.
func (c *Config) ValidateBuilders() error {
	seen := map[string]bool{}
	for _, b := range c.Builders {
		if b.Machine == "" {
			return fmt.Errorf("a builder names no machine")
		}
		if seen[b.Machine] {
			return fmt.Errorf("builder %q is declared twice", b.Machine)
		}
		seen[b.Machine] = true
		if _, ok := c.FindMachine(b.Machine); !ok {
			return fmt.Errorf("builder %q names no declared machine — declare it first (`hz machine add %s`): a box cannot make itself a builder%s",
				b.Machine, b.Machine, c.machineHint())
		}
		if len(b.Projects) == 0 {
			return fmt.Errorf("builder %q is granted no project — `--project P` names what it may build", b.Machine)
		}
		for _, p := range b.Projects {
			if !c.hasProject(p) {
				return fmt.Errorf("builder %q is granted project %q, which is not declared%s", b.Machine, p, c.projectHint())
			}
		}
		if len(b.TokenSHA256) != 64 {
			return fmt.Errorf("builder %q has no token digest", b.Machine)
		}
	}
	return nil
}
