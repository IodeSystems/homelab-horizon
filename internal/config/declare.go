package config

import (
	"fmt"
	"sort"
	"strings"
)

// Declaring the model, rather than importing one.
//
// ApplyImport writes a whole tree at once from evidence; SetFeed writes one
// payload onto a node that already exists. Neither of them can CREATE the node,
// so until this file landed the only way to declare a project or a rung was
// `hz import --execute` or an editor on config.json — and step 2 of the
// acceptance walkthrough in plan/architecture.md ("redline declares its own
// environments: staging, prod") had no command at all.
//
// The order these enforce is the one legacy_compat_test.go pins from the other
// side: DECLARE, THEN ASSIGN. Save refuses a service naming a project or a rung
// nobody declared, so the declaration has to be writable on its own, before
// anything points at it.
//
// Every writer here VALIDATES THE WHOLE CONFIG before it returns. That is the
// point of putting them in this package rather than in the CLI: a write that
// leaves a config Save would refuse surfaces later, from a different writer, as
// a validation error about something the operator did not touch. Refusing here
// names the thing they did touch.

// Dependant is one record that points at the thing being removed, and how.
//
// Kind and Name are separate from How because the first two identify a record
// an operator can go and look at, and the third is the sentence explaining why
// it is in the way. A removal that said only "3 things depend on this" would
// leave them to find out which.
type Dependant struct {
	// Kind is "project", "environment" or "service".
	Kind string `json:"kind"`
	// Name identifies the record: a project name, "<project>/<environment>",
	// or a service name.
	Name string `json:"name"`
	// How says what the relationship is, in a sentence.
	How string `json:"how"`
}

func (d Dependant) String() string { return d.Kind + " " + d.Name + " — " + d.How }

// sortDependants orders by kind then name so two identical configs produce an
// identical list. Kind order is the tree's: projects, then their rungs, then
// what sits on them.
func sortDependants(list []Dependant) {
	rank := map[string]int{"project": 0, "environment": 1, "service": 2}
	sort.SliceStable(list, func(i, j int) bool {
		if rank[list[i].Kind] != rank[list[j].Kind] {
			return rank[list[i].Kind] < rank[list[j].Kind]
		}
		return list[i].Name < list[j].Name
	})
}

// AddProject declares a project, optionally under a parent.
//
// It writes nothing else: a project holds no services and no feed when it is
// created, and that is the state the root is in for the whole of step 1 of the
// walkthrough. A declared, empty project changes no rendered artifact — no DNS
// record, no HAProxy backend, no apt source — which is why this command does not
// need a dry run and `hz feed set` does.
func (c *Config) AddProject(name, parent string) error {
	name = strings.TrimSpace(name)
	parent = strings.TrimSpace(parent)
	if name == "" {
		return fmt.Errorf("a project needs a name")
	}
	for _, p := range c.Projects {
		if p.Name == name {
			return fmt.Errorf("project %q already exists — `hz project show %s` is what it holds", name, name)
		}
	}
	if parent != "" && !c.hasProject(parent) {
		return fmt.Errorf("no project %q to be the parent of %q — declare the parent first, or leave --parent off to make %s a root%s",
			parent, name, name, c.projectHint())
	}

	next := c.copyForWrite()
	next.Projects = append(next.Projects, Project{Name: name, Parent: parent})
	if err := next.validateModel(); err != nil {
		return err
	}
	c.adopt(next)
	return nil
}

// ProjectRemoval reports what removing a project would take (`removes`, which
// always begins with the project itself) and what stands in the way (`blocked`).
//
// Without cascade, ANY dependant blocks. That is deliberate: removing a project
// that still has children, rungs or services would leave a config Save refuses,
// and the operator would meet it as a validation error naming a record they
// never mentioned. Naming the dependants here means the refusal is about the
// command they actually typed.
//
// With cascade, nothing blocks and `removes` is the whole set — the descendant
// subtree, every rung under it, and every service that would be UNASSIGNED
// (which is legal and keeps the service working, but is still a change to a
// record the operator did not name, so it is listed).
func (c *Config) ProjectRemoval(name string, cascade bool) (removes, blocked []Dependant, err error) {
	if !c.hasProject(name) {
		return nil, nil, fmt.Errorf("no project %q — `hz project ls` lists what exists%s", name, c.projectHint())
	}

	doomed := map[string]bool{name: true}
	if cascade {
		// Walk down until nothing new is added. Bounded by the project count for
		// the same reason ValidateProjects' upward walk is: a cycle cannot add a
		// new name more times than there are names.
		for range c.Projects {
			grew := false
			for _, p := range c.Projects {
				if p.Parent != "" && doomed[p.Parent] && !doomed[p.Name] {
					doomed[p.Name] = true
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	// One pass over each record kind. Without cascade `doomed` holds only the
	// target, so the same walk yields the DIRECT dependants — which is exactly
	// the set that blocks.
	removes = []Dependant{{Kind: "project", Name: name, How: "the project being removed"}}
	add := func(d Dependant) {
		if cascade {
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	for _, p := range c.Projects {
		if p.Name == name {
			continue
		}
		if cascade && !doomed[p.Name] {
			continue
		}
		if !cascade && p.Parent != name {
			continue
		}
		add(Dependant{Kind: "project", Name: p.Name, How: "names " + p.Parent + " as its parent"})
	}
	for _, e := range c.Environments {
		if !doomed[e.Project] {
			continue
		}
		add(Dependant{Kind: "environment", Name: e.Project + "/" + e.Name, How: "is a rung of " + e.Project})
	}
	for _, svc := range c.Services {
		if svc.Project == "" || !doomed[svc.Project] {
			continue
		}
		where := svc.Project
		if svc.Environment != "" {
			where += "/" + svc.Environment
		}
		d := Dependant{Kind: "service", Name: svc.Name, How: "is assigned to " + where}
		if cascade {
			d.How += " and would be left UNASSIGNED (legal, and it keeps working)"
		}
		add(d)
	}
	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
}

// RemoveProject removes a project, and with cascade its descendants and their
// rungs, unassigning every service that named one. It refuses while anything
// depends on it and cascade is off, naming what.
func (c *Config) RemoveProject(name string, cascade bool) ([]Dependant, error) {
	removes, blocked, err := c.ProjectRemoval(name, cascade)
	if err != nil {
		return nil, err
	}
	if len(blocked) > 0 {
		return nil, blockedError("project", name, blocked)
	}

	doomed := map[string]bool{}
	for _, d := range removes {
		if d.Kind == "project" {
			doomed[d.Name] = true
		}
	}

	next := c.copyForWrite()
	projects := make([]Project, 0, len(next.Projects))
	for _, p := range next.Projects {
		if !doomed[p.Name] {
			projects = append(projects, p)
		}
	}
	environments := make([]Environment, 0, len(next.Environments))
	for _, e := range next.Environments {
		if !doomed[e.Project] {
			environments = append(environments, e)
		}
	}
	for i := range next.Services {
		if doomed[next.Services[i].Project] {
			next.Services[i].Project = ""
			next.Services[i].Environment = ""
		}
	}
	next.Projects, next.Environments = projects, environments
	if err := next.validateModel(); err != nil {
		return nil, err
	}
	c.adopt(next)
	return removes, nil
}

// AddEnvironment declares a rung.
//
// Posture is required and closed: an environment whose posture nothing ranks
// would compare as -1 against every other rung, which reads as "below dev" and
// makes every promotion into it look upward. PostureRank is the ordering, so a
// value outside Postures is refused here with the three named rather than stored
// and discovered later.
func (c *Config) AddEnvironment(e Environment) error {
	e.Project = strings.TrimSpace(e.Project)
	e.Name = strings.TrimSpace(e.Name)
	e.Posture = strings.TrimSpace(e.Posture)
	e.From = strings.TrimSpace(e.From)
	e.Version = strings.TrimSpace(e.Version)

	if e.Name == "" {
		return fmt.Errorf("an environment needs a name")
	}
	if e.Project == "" {
		return fmt.Errorf("environment %q needs a project — an environment belongs to one, and every project gets to have a %q", e.Name, e.Name)
	}
	if !c.hasProject(e.Project) {
		return fmt.Errorf("no project %q to declare %q in — declare the project first (`hz project add %s`)%s",
			e.Project, e.Name, e.Project, c.projectHint())
	}
	if c.hasEnvironment(e.Project, e.Name) {
		return fmt.Errorf("environment %q already exists in project %q — `hz env set %s/%s` changes one",
			e.Name, e.Project, e.Project, e.Name)
	}
	if err := checkPosture(e.Posture); err != nil {
		return err
	}
	from, err := c.normalizeFrom(e.Project, e.Name, e.From)
	if err != nil {
		return err
	}
	e.From = from

	next := c.copyForWrite()
	next.Environments = append(next.Environments, e)
	if err := next.validateModel(); err != nil {
		return err
	}
	c.adopt(next)
	return nil
}

// EnvironmentPatch is a partial update to a rung. A nil field is "leave this
// alone"; a non-nil one is the new value.
//
// Partial rather than whole-record for the opposite of the reason SetFeed
// replaces whole. A feed RESOLVES — a half-updated one makes "where did this
// value come from" unanswerable — whereas an environment's three fields are
// independent facts about one rung, and making an operator restate the posture
// and the promotion edge in order to bump a version is how a promotion edge gets
// dropped by accident.
type EnvironmentPatch struct {
	Posture *string
	From    *string
	Version *string
}

// Empty reports a patch that changes nothing, so a caller can refuse it rather
// than write a no-op and report success.
func (p EnvironmentPatch) Empty() bool {
	return p.Posture == nil && p.From == nil && p.Version == nil
}

// SetEnvironment applies a patch to one rung and returns it as it now reads.
func (c *Config) SetEnvironment(project, name string, patch EnvironmentPatch) (Environment, error) {
	idx := -1
	for i, e := range c.Environments {
		if e.Project == project && e.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Environment{}, fmt.Errorf("no environment %q in project %q — `hz env ls` lists what exists%s",
			name, project, c.environmentHint(project))
	}
	if patch.Empty() {
		return Environment{}, fmt.Errorf("nothing to set on %s/%s — pass at least one of --posture, --from or --version", project, name)
	}

	next := c.copyForWrite()
	e := next.Environments[idx]
	if patch.Posture != nil {
		posture := strings.TrimSpace(*patch.Posture)
		if err := checkPosture(posture); err != nil {
			return Environment{}, err
		}
		e.Posture = posture
	}
	if patch.From != nil {
		from, err := c.normalizeFrom(project, name, strings.TrimSpace(*patch.From))
		if err != nil {
			return Environment{}, err
		}
		e.From = from
	}
	if patch.Version != nil {
		e.Version = strings.TrimSpace(*patch.Version)
	}
	next.Environments[idx] = e
	if err := next.validateModel(); err != nil {
		return Environment{}, err
	}
	c.adopt(next)
	return e, nil
}

// EnvironmentRemoval reports what removing a rung would take and what stands in
// the way. Two kinds of record depend on a rung and they fail differently:
// a service assigned to it makes the config unsaveable, and an environment
// promoting FROM it loses its promotion edge — which is the authority model, not
// a detail, so cascade lists each edge it would cut.
func (c *Config) EnvironmentRemoval(project, name string, cascade bool) (removes, blocked []Dependant, err error) {
	if !c.hasEnvironment(project, name) {
		return nil, nil, fmt.Errorf("no environment %q in project %q — `hz env ls` lists what exists%s",
			name, project, c.environmentHint(project))
	}
	key := project + "/" + name
	removes = []Dependant{{Kind: "environment", Name: key, How: "the environment being removed"}}

	for _, e := range c.Environments {
		if e.Project != project || e.From != name {
			continue
		}
		d := Dependant{
			Kind: "environment", Name: e.Project + "/" + e.Name,
			How: "promotes from " + name + ", and that edge would be cut",
		}
		if cascade {
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	for _, svc := range c.Services {
		if svc.Project != project || svc.Environment != name {
			continue
		}
		d := Dependant{Kind: "service", Name: svc.Name, How: "is on " + key}
		if cascade {
			d.How += " and would be left on no rung (legal, and it keeps working)"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
}

// RemoveEnvironment removes a rung. With cascade it also clears the promotion
// edges into it and takes the services on it off their rung; without cascade it
// refuses while either exists, naming them.
func (c *Config) RemoveEnvironment(project, name string, cascade bool) ([]Dependant, error) {
	removes, blocked, err := c.EnvironmentRemoval(project, name, cascade)
	if err != nil {
		return nil, err
	}
	if len(blocked) > 0 {
		return nil, blockedError("environment", project+"/"+name, blocked)
	}

	next := c.copyForWrite()
	environments := make([]Environment, 0, len(next.Environments))
	for _, e := range next.Environments {
		if e.Project == project && e.Name == name {
			continue
		}
		if e.Project == project && e.From == name {
			e.From = ""
		}
		environments = append(environments, e)
	}
	for i := range next.Services {
		if next.Services[i].Project == project && next.Services[i].Environment == name {
			next.Services[i].Environment = ""
		}
	}
	next.Environments = environments
	if err := next.validateModel(); err != nil {
		return nil, err
	}
	c.adopt(next)
	return removes, nil
}

// --- the shared plumbing ----------------------------------------------------

// blockedError renders a refusal that names every record in the way. The whole
// value of refusing rather than cascading is in this string: an operator who is
// told "3 things depend on it" has to go and find them.
func blockedError(kind, name string, blocked []Dependant) error {
	lines := make([]string, 0, len(blocked))
	for _, d := range blocked {
		lines = append(lines, "  "+d.String())
	}
	return fmt.Errorf("%s %s still has %d dependant(s), so removing it would leave a config that cannot be saved:\n%s\nRemove or move them first, or pass cascade to take them with it",
		kind, name, len(blocked), strings.Join(lines, "\n"))
}

// checkPosture refuses anything PostureRank does not rank, and names the three.
func checkPosture(posture string) error {
	if posture == "" {
		return fmt.Errorf("a posture is required: one of %s (in that order — a rung with no posture cannot be compared against any other)",
			strings.Join(Postures, ", "))
	}
	if PostureRank(posture) < 0 {
		return fmt.Errorf("posture %q is not one of %s — the three are the ladder, and a value outside it ranks below dev against every rung",
			posture, strings.Join(Postures, ", "))
	}
	return nil
}

// normalizeFrom checks a promotion source and returns it in the form the record
// stores: a BARE name. From is a name within the project, not an address —
// ValidateEnvironments looks it up as envKey{e.Project, e.From} — so accepting
// the qualified spelling an operator naturally types and storing it verbatim
// would write an edge that resolves to nothing.
//
// It refuses a source that is not a rung of the same project and lists the ones
// that are. ValidateEnvironments catches this too, and catches the cycles this
// does not look for — but it can only say the name does not exist, and the
// answer an operator needs is which names do.
func (c *Config) normalizeFrom(project, name, from string) (string, error) {
	if from == "" {
		return "", nil // no edge is legal; nothing may be promoted into this rung
	}
	// A "<project>/<name>" spelling is accepted only when the project half is
	// this one: promotion is within a project (CheckPromotion refuses across),
	// so the qualified form can only ever name a sibling.
	if p, n, ok := strings.Cut(from, "/"); ok {
		if p != project {
			return "", fmt.Errorf("--from %q names project %q, but promotion is within one project — %s/%s can only promote from a rung of %s",
				from, p, project, name, project)
		}
		from = n
	}
	if from == name {
		return "", fmt.Errorf("%s/%s cannot promote from itself", project, name)
	}
	if !c.hasEnvironment(project, from) {
		return "", fmt.Errorf("no environment %q in project %q to promote from%s",
			from, project, c.environmentHint(project))
	}
	return from, nil
}

// validateModel runs the two validators Save runs over the project tree, so a
// writer here cannot produce a config that Save would later refuse. ValidateFeeds
// is not run: nothing in this file touches a feed, and a feed that was already
// broken is not this command's to report.
func (c *Config) validateModel() error {
	if err := c.ValidateProjects(); err != nil {
		return err
	}
	return c.ValidateEnvironments()
}

// copyForWrite returns a config with fresh Projects/Environments/Services
// slices, for the reason ApplyImport gives: a Config is copied shallowly in
// several places, so writing through the existing backing array would mutate the
// config another goroutine is still serving.
func (c *Config) copyForWrite() *Config {
	next := *c
	next.Projects = append([]Project(nil), c.Projects...)
	next.Environments = append([]Environment(nil), c.Environments...)
	next.Services = append([]Service(nil), c.Services...)
	return &next
}

// adopt publishes a validated copy's three slices onto the receiver. Only after
// validateModel has passed — a config is never left half-written.
func (c *Config) adopt(next *Config) {
	c.Projects, c.Environments, c.Services = next.Projects, next.Environments, next.Services
}

func (c *Config) hasProject(name string) bool {
	for _, p := range c.Projects {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (c *Config) hasEnvironment(project, name string) bool {
	for _, e := range c.Environments {
		if e.Project == project && e.Name == name {
			return true
		}
	}
	return false
}

// projectHint lists the declared projects, so "no project X" is followed by the
// names that would have worked. Empty when there are none — and then it says so,
// because "no project X" with no list reads as a typo when the real answer is
// that this config declares no projects at all.
func (c *Config) projectHint() string {
	if len(c.Projects) == 0 {
		return ". This config declares no projects at all; `hz project add <name>` makes the first one"
	}
	names := make([]string, 0, len(c.Projects))
	for _, p := range c.Projects {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return ". Declared: " + strings.Join(names, ", ")
}

// environmentHint lists the rungs of one project, for the same reason.
func (c *Config) environmentHint(project string) string {
	names := make([]string, 0, len(c.Environments))
	for _, e := range c.Environments {
		if e.Project == project {
			names = append(names, e.Name)
		}
	}
	if len(names) == 0 {
		return ". Project " + project + " declares no environments yet"
	}
	sort.Strings(names)
	return ". Declared in " + project + ": " + strings.Join(names, ", ")
}
