package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Build agents: the credentials allowed to start builds, one project each
// (operator, 2026-10-02 — build numbers come FROM hz).
//
// A BUILD AGENT IS NOT A MACHINE, AND NOT AN hz INSTANCE. The operator, verbatim:
// "A HZ Instance is NOT a Build Machine", and "it's build agent - as a single
// machine may have multiple registered project build agents". A Machine is a
// box hz projects a network and units onto, and it may name one owning
// project. A build agent is a token registered to number the builds of ONE
// project. Tying the two (the B1 builder named a declared machine) made the
// operator declare the dev box as a machine owned by one project, which then
// listed that box as the project's.
//
// So an agent carries no machine reference. Host is a free-text label of the
// box it runs on, for grouping ("3 agents on host desktop"); it is never
// resolved against machines, and removing a machine never touches an agent
// (TestRemovingAMachineLeavesTheBuildAgent).
//
// hz mints its token once (POST /api/v1/build-agents) and keeps only the
// sha256 here, the way an instance token's sha256 sits on MachineHZ. It is
// declared state, so it lives in config.json and peer-syncs; the builds
// themselves are observations and live in hz.db.
//
// The token is the agent's only power: start, log, state, finish and artifact
// upload, for its project (internal/server/handlers_api_builds.go).

// BuildAgent is one registered build agent.
type BuildAgent struct {
	// Name is the agent's identity: what a build records as who started it,
	// and what the API addresses it by. Unique among agents; it shares no
	// namespace with machines.
	Name string `json:"name"`
	// Project is the one project this agent builds. Registration, not
	// ownership: a project does not own the box the agent runs on.
	Project string `json:"project"`
	// Host labels the box the agent runs on. Free text, optional, never a
	// Machine reference; several agents may share one.
	Host string `json:"host,omitempty"`
	// Note is free text for the operator.
	Note string `json:"note,omitempty"`
	// TokenSHA256 is the sha256 (hex) of the agent's token. The token itself is
	// shown once, at mint, and never stored. "" is an agent with no token yet
	// (only a migration writes one): it authenticates nothing until minted.
	TokenSHA256 string `json:"token_sha256,omitempty"`
}

// LegacyBuilder is the pre-2026-10-02 `builders` record: {machine, projects[],
// token_sha256}. Read only, by MigrateLegacyBuilders; nothing writes it.
type LegacyBuilder struct {
	Machine     string   `json:"machine"`
	Name        string   `json:"name,omitempty"`
	Projects    []string `json:"projects"`
	TokenSHA256 string   `json:"token_sha256"`
}

// MigrateLegacyBuilders turns every `builders` record into build agents and
// clears the legacy key, so the next Save writes `build_agents` only. One
// agent per project: the FIRST project keeps the builder's name and token
// digest — so the token in use keeps working with no re-mint — and each other
// project becomes `<name>-<project>` with NO token and a note saying so. Host
// is the old name, which was the machine the builder ran on.
//
// A migrated name that collides with an existing agent is an error, not a
// rename: the operator has to see it.
func (c *Config) MigrateLegacyBuilders() error {
	if len(c.LegacyBuilders) == 0 {
		c.LegacyBuilders = nil
		return nil
	}
	agents := append([]BuildAgent(nil), c.BuildAgents...)
	taken := map[string]bool{}
	for _, a := range agents {
		taken[a.Name] = true
	}
	for _, old := range c.LegacyBuilders {
		name := strings.TrimSpace(old.Name)
		if name == "" {
			name = strings.TrimSpace(old.Machine)
		}
		if len(old.Projects) == 0 {
			return fmt.Errorf("legacy builder %q grants no project, so it migrates to no build agent — remove it from `builders` in config.json", name)
		}
		for i, p := range old.Projects {
			a := BuildAgent{Name: name, Project: p, Host: name, TokenSHA256: old.TokenSHA256}
			if len(old.Projects) > 1 {
				a.Note = fmt.Sprintf("migrated from builder %s (granted %s): one agent per project now", name, strings.Join(old.Projects, ", "))
			}
			if i > 0 {
				a.Name = name + "-" + p
				a.TokenSHA256 = ""
				a.Note = fmt.Sprintf("MIGRATED WITH NO TOKEN from builder %s (granted %s) — the old token stays with agent %s. Mint this one: `hz build-agent add %s --project %s`",
					name, strings.Join(old.Projects, ", "), name, a.Name, p)
			}
			if taken[a.Name] {
				return fmt.Errorf("legacy builder %q migrates to build agent %q, which already exists — rename one in config.json", name, a.Name)
			}
			taken[a.Name] = true
			agents = append(agents, a)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	c.BuildAgents = agents
	c.LegacyBuilders = nil
	return nil
}

// buildAgentNameRe is the machine-name charset (the one the nested bootstrap
// enforces, internal/server/handlers_nested_bootstrap.go). An agent name is
// also a URL path segment: DELETE /api/v1/build-agents/{name}.
var buildAgentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// CheckBuildAgentName refuses a name outside the machine-name charset.
func CheckBuildAgentName(name string) error {
	if !buildAgentNameRe.MatchString(name) {
		return fmt.Errorf("build agent name %q must be letters, digits, '.', '_' or '-', start with a letter or digit, and be at most 63 characters", name)
	}
	return nil
}

// FindBuildAgent is the agent named name, if any.
func (c *Config) FindBuildAgent(name string) (BuildAgent, bool) {
	for _, a := range c.BuildAgents {
		if a.Name == name {
			return a, true
		}
	}
	return BuildAgent{}, false
}

// SetBuildAgent registers agent a.Name for a.Project with the sha256 of its
// newly minted token, or rotates the token of an existing agent. A rotate
// keeps the agent's host and note unless a new one is given, and refuses a
// different project: a token numbers ONE project's builds, so moving it is a
// new agent. rotated is true when the agent existed.
func (c *Config) SetBuildAgent(a BuildAgent) (rotated bool, err error) {
	a.Name = strings.TrimSpace(a.Name)
	a.Project = strings.TrimSpace(a.Project)
	a.Host = strings.TrimSpace(a.Host)
	a.Note = strings.TrimSpace(a.Note)
	if len(a.TokenSHA256) != 64 {
		return false, fmt.Errorf("build agent token digest must be 64 hex characters, got %d", len(a.TokenSHA256))
	}
	next := c.copyForWrite()
	replaced := false
	for i := range next.BuildAgents {
		old := next.BuildAgents[i]
		if old.Name != a.Name {
			continue
		}
		if old.Project != a.Project {
			return false, fmt.Errorf("build agent %q is registered for project %q, not %q — an agent builds one project; register another name for %s", a.Name, old.Project, a.Project, a.Project)
		}
		if a.Host == "" {
			a.Host = old.Host
		}
		if a.Note == "" {
			a.Note = old.Note
		}
		next.BuildAgents[i] = a
		replaced = true
	}
	if !replaced {
		next.BuildAgents = append(next.BuildAgents, a)
		sort.Slice(next.BuildAgents, func(i, j int) bool { return next.BuildAgents[i].Name < next.BuildAgents[j].Name })
	}
	if err := next.ValidateBuildAgents(); err != nil {
		return false, err
	}
	c.adopt(next)
	return replaced, nil
}

// RemoveBuildAgent revokes the agent's token and registration. Its builds stay
// in hz.db and its passed builds stay promotable (the registration is read
// when a build starts, not at promote).
func (c *Config) RemoveBuildAgent(name string) error {
	next := c.copyForWrite()
	out := next.BuildAgents[:0]
	found := false
	for _, a := range next.BuildAgents {
		if a.Name == name {
			found = true
			continue
		}
		out = append(out, a)
	}
	if !found {
		return fmt.Errorf("no build agent %q — `hz build-agent ls` lists them", name)
	}
	if len(out) == 0 {
		out = nil
	}
	next.BuildAgents = out
	c.adopt(next)
	return nil
}

// ValidateBuildAgents refuses an agent with no name or one outside the
// charset, a name used twice, no project or an undeclared one, or a token
// digest that is neither absent nor 64 characters. It does NOT read machines:
// an agent is not one, and Host is a label.
func (c *Config) ValidateBuildAgents() error {
	if len(c.LegacyBuilders) > 0 {
		return fmt.Errorf("the legacy `builders` key was not migrated to `build_agents` (MigrateLegacyBuilders)")
	}
	seen := map[string]bool{}
	for _, a := range c.BuildAgents {
		if a.Name == "" {
			return fmt.Errorf("a build agent has no name")
		}
		if err := CheckBuildAgentName(a.Name); err != nil {
			return err
		}
		if seen[a.Name] {
			return fmt.Errorf("build agent %q is declared twice", a.Name)
		}
		seen[a.Name] = true
		if a.Project == "" {
			return fmt.Errorf("build agent %q is registered for no project — `--project P` names what it builds", a.Name)
		}
		if !c.hasProject(a.Project) {
			return fmt.Errorf("build agent %q is registered for project %q, which is not declared%s", a.Name, a.Project, c.projectHint())
		}
		if a.TokenSHA256 != "" && len(a.TokenSHA256) != 64 {
			return fmt.Errorf("build agent %q has a malformed token digest", a.Name)
		}
	}
	return nil
}
