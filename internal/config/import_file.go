package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The import plan as a FILE an operator edits.
//
// WHY THIS EXISTS. A dry run against a real 33-service estate assigned every
// service and was wrong: that estate is flat — every service sits at
// <name>.<our-co>.<tld> — so the domain suffix cannot tell one application from
// another, and thirty services collapsed into one project. The environment
// grouping inherited the coarseness and proposed a single `dev` rung carrying
// three unrelated applications. Config promotion flows along an environment, so
// executing that would have been actively wrong rather than merely coarse.
//
// The conclusion was not "fix the heuristic". The operator knows the structure
// and the config does not contain it. So ProposeImport stays exactly what it is
// — a good STARTING POINT and a bad VERDICT — and this file is how the operator
// corrects it before it is written:
//
//	hz import --plan-out tree.json    write the proposal out
//	hz import --from tree.json        dry run the edited file
//	hz import --from tree.json --execute
//
// It is the same pipeline. ImportFile converts to an ImportPlan and ApplyImport
// writes it; nothing here is a second way to change the config.
//
// WHAT THE FILE DELIBERATELY LEAVES OUT, and why:
//
//   - The EVIDENCE strings (ImportProject.Reason, ImportAssignment.ProjectReason,
//     ImportEnvironment.Reason, ImportUnassigned.Reason/Note). They are hz's
//     account of how it reached a row. The moment the operator moves that row
//     they are a lie sitting beside it — and a wrong reason printed next to a
//     proposal is worse than no reason, which is the whole argument import.go
//     makes for printing them beside the row in the first place. They are output,
//     not input. `hz import` with no flags reprints them against the live config.
//   - The SIGNALS section, for the same reason: it reports what hz examined and
//     rejected in THIS config. Editing it changes nothing.
//   - The FINGERPRINT. It identifies a plan hz computed; a hand-edited file is by
//     definition a different plan, so carrying one would either be stale or be
//     ignored. The guard it provided — "the config changed between the dry run
//     and the write" — is replaced by something stronger below: the file must
//     account for every service in the config BY NAME, so drift is refused
//     naming the service that appeared or vanished rather than as an opaque hash
//     mismatch.
//
// What is left is exactly the decisions: which projects exist, which rungs exist
// and what posture each sits at, and where every service goes.

// importFileVersion is the only version this code reads. It is required rather
// than defaulted: a file with no version is far more likely to be something else
// entirely than an old plan file, and there are no old plan files.
const importFileVersion = 1

// ImportFile is the editable form of an ImportPlan.
//
// Two service lists rather than one list with an optional project, because a
// half-filled row is the failure an operator makes at 2am: `{"service": "x",
// "project": ""}` reads as an edit in progress and a deliberate "leave this one
// alone" identically. A name in Unassigned cannot be half-anything.
type ImportFile struct {
	// Version must be importFileVersion.
	Version int `json:"version"`
	// Readme is written by --plan-out and IGNORED on read. It carries the
	// editing rules into the file, because the operator editing it at 2am is not
	// the person who read the help text.
	Readme []string `json:"_readme,omitempty"`
	// Projects to declare. Order is irrelevant; a parent may appear after its
	// child.
	Projects []ImportFileProject `json:"projects"`
	// Environments to declare. Each belongs to exactly one project.
	Environments []ImportFileEnvironment `json:"environments"`
	// Assign places a service in a project, optionally on a rung.
	Assign []ImportFilePlacement `json:"assign"`
	// Unassigned names the services this plan deliberately leaves with no
	// project. A service with no project is legal and keeps working unchanged;
	// saying so explicitly is how the file distinguishes that from an omission.
	Unassigned []string `json:"unassigned"`
}

// ImportFileProject declares a project. Parent is optional and names another
// project in the same file.
type ImportFileProject struct {
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
}

// ImportFileEnvironment declares a rung. Project is required — an environment
// belongs to a project, and this field is the thing that makes the
// cross-project rung inexpressible.
//
// Name and Posture stay separate for the reason Environment gives: a project may
// call its rung "beta" and sit at staging's posture, and collapsing the two
// would force a relabel nobody asked for.
type ImportFileEnvironment struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Posture string `json:"posture"`
}

// ImportFilePlacement puts one service in one project, optionally on one rung.
// Environment is resolved against THIS service's project, never globally: every
// project gets to have a "dev".
type ImportFilePlacement struct {
	Service     string `json:"service"`
	Project     string `json:"project"`
	Environment string `json:"environment,omitempty"`
}

// importFileReadme is the instruction block --plan-out writes into the file. It
// is regenerated on every write, so it can never contradict the code.
var importFileReadme = []string{
	"This is an hz import plan. Edit it, then: hz import --from <this file>",
	"",
	"  projects      the tree. `parent` names another project in this file.",
	"  environments  the rungs. Every rung belongs to exactly ONE project, and a",
	"                service may only name a rung declared under its own project.",
	"                `posture` is one of: dev, staging, prod.",
	"  assign        where each service goes. `environment` is optional.",
	"  unassigned    services deliberately left with no project. That is legal and",
	"                they keep working unchanged.",
	"",
	"Every service in the config must appear EXACTLY ONCE, in `assign` or in",
	"`unassigned`. A service you delete from both is refused by name rather than",
	"silently left alone — and that same rule is what catches a service added to",
	"or removed from the gateway since this file was written.",
	"",
	"The evidence hz gave for each proposal is NOT in this file: once you move a",
	"row, hz's reason for the old one is a lie sitting beside it. Run `hz import`",
	"with no flags to see the proposal and its reasons against the live config.",
}

// ImportFileFor renders a plan as the editable file. It is the exact inverse of
// PlanFromImportFile over everything the file carries, which
// TestImportPlanFileIsAFixedPoint pins.
func ImportFileFor(p ImportPlan) ImportFile {
	f := ImportFile{
		Version:      importFileVersion,
		Readme:       append([]string(nil), importFileReadme...),
		Projects:     make([]ImportFileProject, 0, len(p.Projects)),
		Environments: make([]ImportFileEnvironment, 0, len(p.Environments)),
		Assign:       make([]ImportFilePlacement, 0, len(p.Assignments)),
		Unassigned:   make([]string, 0, len(p.Unassigned)),
	}
	for _, x := range p.Projects {
		f.Projects = append(f.Projects, ImportFileProject{Name: x.Name, Parent: x.Parent})
	}
	for _, x := range p.Environments {
		f.Environments = append(f.Environments, ImportFileEnvironment{
			Project: x.Project, Name: x.Name, Posture: x.Posture,
		})
	}
	for _, x := range p.Assignments {
		f.Assign = append(f.Assign, ImportFilePlacement{
			Service: x.Service, Project: x.Project, Environment: x.Environment,
		})
	}
	for _, x := range p.Unassigned {
		f.Unassigned = append(f.Unassigned, x.Service)
	}
	return f
}

// Marshal renders the file for writing.
//
// Hand-laid out rather than json.MarshalIndent, for two reasons that are both
// about the person editing it:
//
//   - ONE ROW PER LINE. MarshalIndent puts every field on its own line, so the
//     33-service estate that motivated this becomes 200 lines of punctuation and
//     an assignment cannot be read, moved or deleted as one thing. Compact rows
//     make "tree.json:14:" name a row rather than a brace, and make a diff of
//     two plans readable.
//   - NO HTML ESCAPING. encoding/json turns < > & into < > & by
//     default, which is meaningless in a config file and turns a hostname or an
//     ampersand in a project name into something the operator did not type.
//
// Each ROW still goes through encoding/json, so the escaping of the values
// themselves is the standard library's and not hand-rolled.
func (f ImportFile) Marshal() ([]byte, error) {
	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  %q: %d,\n", "version", f.Version)

	if len(f.Readme) > 0 {
		b.WriteString("  \"_readme\": [\n")
		for i, line := range f.Readme {
			row, err := compactJSON(line)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "    %s%s\n", row, comma(i, len(f.Readme)))
		}
		b.WriteString("  ],\n")
	}

	sections := []struct {
		name string
		rows []any
	}{
		{"projects", anyRows(f.Projects)},
		{"environments", anyRows(f.Environments)},
		{"assign", anyRows(f.Assign)},
		{"unassigned", anyRows(f.Unassigned)},
	}
	for s, section := range sections {
		if len(section.rows) == 0 {
			fmt.Fprintf(&b, "  %q: []%s\n", section.name, comma(s, len(sections)))
			continue
		}
		fmt.Fprintf(&b, "  %q: [\n", section.name)
		for i, v := range section.rows {
			row, err := compactJSON(v)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "    %s%s\n", row, comma(i, len(section.rows)))
		}
		fmt.Fprintf(&b, "  ]%s\n", comma(s, len(sections)))
	}
	b.WriteString("}\n")
	return []byte(b.String()), nil
}

// anyRows widens a typed slice so the section table above can hold all four.
func anyRows[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// comma is the separator a JSON array element needs, which is none on the last.
func comma(i, n int) string {
	if i == n-1 {
		return ""
	}
	return ","
}

// compactJSON renders one value on one line, with HTML escaping off.
func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ParseImportFile reads the file. Unknown fields are REFUSED rather than
// ignored: `"envronment": "dev"` silently dropped would take a service off its
// rung and look exactly like a file that never named one, which is the quiet
// wrongness this whole feature exists to stop.
func ParseImportFile(raw []byte) (ImportFile, error) {
	var f ImportFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return ImportFile{}, importFileParseError(err)
	}
	// A second document after the first is a paste accident, not a plan.
	if dec.More() {
		return ImportFile{}, &ImportFileError{Msg: "there is more than one JSON document in this file; an import plan is a single object"}
	}
	return f, nil
}

// importFileParseError turns encoding/json's message into one that says what to
// do, and keeps the offending field name so a caller can find the line.
func importFileParseError(err error) error {
	msg := err.Error()
	const unknown = "json: unknown field "
	if i := strings.Index(msg, unknown); i >= 0 {
		field := strings.Trim(msg[i+len(unknown):], `"`)
		return &ImportFileError{
			Token: field,
			Msg: fmt.Sprintf("unknown field %q. An unknown field is refused rather than ignored — a misspelt %q would silently drop what it was meant to set. The fields are: version, projects, environments, assign, unassigned",
				field, field),
		}
	}
	return &ImportFileError{Msg: "this file is not readable as JSON: " + msg}
}

// ImportFileError is a validation failure that knows which identifier in the
// file it is about. A caller holding the raw bytes turns that into a line
// number (AnnotateImportFileError); a caller that only has the parsed struct —
// the API server, which receives it decoded — still gets a message that names
// the thing.
type ImportFileError struct {
	// Token is the identifier to look for in the raw file: a service, project,
	// environment or field name. Empty when the fault is not about one thing.
	Token string
	Msg   string
}

func (e *ImportFileError) Error() string { return e.Msg }

// AnnotateImportFileError prefixes a validation failure with `path:line:` when
// the fault can be traced to one identifier in the raw bytes.
//
// The line is found by searching for the quoted token rather than by tracking
// byte offsets through the decoder. That is approximate — a project and a
// service may share a name — and it is still the difference between "fix line
// 47" and "find it yourself" in a file with 33 services in it. It is never
// load-bearing: the message names the thing with or without a line.
func AnnotateImportFileError(raw []byte, path string, err error) error {
	if err == nil {
		return nil
	}
	var ife *ImportFileError
	if !asImportFileError(err, &ife) || ife.Token == "" {
		return fmt.Errorf("%s: %w", path, err)
	}
	if line := lineOfToken(raw, ife.Token); line > 0 {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// asImportFileError is errors.As without the import, kept local because the
// only wrapping this package does to these is fmt.Errorf("%w").
func asImportFileError(err error, out **ImportFileError) bool {
	for err != nil {
		if e, ok := err.(*ImportFileError); ok {
			*out = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// lineOfToken returns the 1-based line of the first `"token"` in raw, or 0.
func lineOfToken(raw []byte, token string) int {
	needle := `"` + token + `"`
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return 0
}

// Validate checks everything about the file that can be checked WITHOUT the
// config: shape, internal references, and the one structural mistake that
// produces a valid-looking config that is quietly wrong.
//
// Save's validators run over the finished config and catch the structural
// errors. They do not catch a file that is internally coherent and says the
// wrong thing — a mis-typed project name declares a project nobody wanted, and a
// rung declared under the wrong project passes ValidateEnvironments as long as
// the services naming it are in that project too. So this validates the FILE,
// not the config it would produce.
func (f ImportFile) Validate() error {
	if f.Version != importFileVersion {
		return &ImportFileError{
			Token: "version",
			Msg: fmt.Sprintf("this file declares version %d; hz reads version %d. Re-run `hz import --plan-out` to write a current one",
				f.Version, importFileVersion),
		}
	}
	projects, err := f.validateProjects()
	if err != nil {
		return err
	}
	envs, err := f.validateEnvironments(projects)
	if err != nil {
		return err
	}
	if err := f.validatePlacements(projects); err != nil {
		return err
	}
	return f.validateRungOwnership(envs)
}

// validateProjects: named, unique, and every parent declared here with no cycle.
func (f ImportFile) validateProjects() (map[string]string, error) {
	parent := map[string]string{}
	for _, p := range f.Projects {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, &ImportFileError{Token: "projects", Msg: "a project in `projects` has no name"}
		}
		if _, dup := parent[name]; dup {
			return nil, &ImportFileError{Token: name, Msg: fmt.Sprintf("project %q is declared twice in `projects`", name)}
		}
		parent[name] = strings.TrimSpace(p.Parent)
	}
	for name, up := range parent {
		if up == "" {
			continue
		}
		if up == name {
			return nil, &ImportFileError{Token: name, Msg: fmt.Sprintf("project %q names itself as its parent", name)}
		}
		if _, ok := parent[up]; !ok {
			return nil, &ImportFileError{
				Token: name,
				Msg: fmt.Sprintf("project %q names parent %q, which is not declared in this file. Every project a row refers to has to be declared here — hz will not invent one from a typo",
					name, up),
			}
		}
		// Bounded by the number of projects: a cycle cannot be longer than that
		// without repeating, so exceeding it IS the cycle. Same shape as
		// ValidateProjects.
		seen, cur := 0, up
		for cur != "" {
			if seen > len(f.Projects) {
				return nil, &ImportFileError{Token: name, Msg: fmt.Sprintf("project %q is in a parent cycle", name)}
			}
			cur = parent[cur]
			seen++
		}
	}
	return parent, nil
}

// validateEnvironments: named, owned by a project declared here, unique within
// that project, and at one of the three postures.
func (f ImportFile) validateEnvironments(projects map[string]string) (map[envKey]bool, error) {
	out := map[envKey]bool{}
	for _, e := range f.Environments {
		name, project := strings.TrimSpace(e.Name), strings.TrimSpace(e.Project)
		if name == "" {
			return nil, &ImportFileError{Token: "environments", Msg: "an environment in `environments` has no name"}
		}
		if project == "" {
			return nil, &ImportFileError{
				Token: name,
				Msg:   fmt.Sprintf("environment %q has no project. An environment belongs to exactly one project; there is no global rung", name),
			}
		}
		if _, ok := projects[project]; !ok {
			return nil, &ImportFileError{
				Token: name,
				Msg: fmt.Sprintf("environment %q names project %q, which is not declared in `projects` in this file",
					name, project),
			}
		}
		if PostureRank(e.Posture) < 0 {
			return nil, &ImportFileError{
				Token: name,
				Msg: fmt.Sprintf("environment %q in project %q has posture %q, which is not one of %s. The posture is the isolation rung, and promotion is ordered by it — an unranked one would make every promotion into it look upward",
					name, project, e.Posture, strings.Join(Postures, ", ")),
			}
		}
		k := envKey{project, name}
		if out[k] {
			return nil, &ImportFileError{
				Token: name,
				Msg:   fmt.Sprintf("environment %q is declared twice in project %q", name, project),
			}
		}
		out[k] = true
	}
	return out, nil
}

// validatePlacements: every service named once across both lists, and every
// project a row names declared here.
func (f ImportFile) validatePlacements(projects map[string]string) error {
	seen := map[string]string{} // service -> which list claimed it
	for _, a := range f.Assign {
		service, project := strings.TrimSpace(a.Service), strings.TrimSpace(a.Project)
		if service == "" {
			return &ImportFileError{Token: "assign", Msg: "a row in `assign` has no service"}
		}
		if where, dup := seen[service]; dup {
			return &ImportFileError{
				Token: service,
				Msg:   fmt.Sprintf("service %q appears twice (%s and `assign`). A service is in one place", service, where),
			}
		}
		seen[service] = "`assign`"
		if project == "" {
			return &ImportFileError{
				Token: service,
				Msg: fmt.Sprintf("service %q is in `assign` with no project. To leave it with no project — which is legal — move its name to `unassigned`; an empty project here reads as an edit somebody did not finish",
					service),
			}
		}
		if _, ok := projects[project]; !ok {
			return &ImportFileError{
				Token: service,
				Msg: fmt.Sprintf("service %q names project %q, which is not declared in `projects` in this file. A mis-typed project name would otherwise declare a project nobody wanted and file this service under it",
					service, project),
			}
		}
	}
	for _, service := range f.Unassigned {
		service = strings.TrimSpace(service)
		if service == "" {
			return &ImportFileError{Token: "unassigned", Msg: "an entry in `unassigned` is empty"}
		}
		if where, dup := seen[service]; dup {
			return &ImportFileError{
				Token: service,
				Msg:   fmt.Sprintf("service %q appears twice (%s and `unassigned`). A service is in one place", service, where),
			}
		}
		seen[service] = "`unassigned`"
	}
	return nil
}

// validateRungOwnership is the load-bearing one.
//
// THE BUG IT MAKES INEXPRESSIBLE. The dry run that motivated this file proposed
// one `dev` rung under one project carrying three unrelated applications. The
// operator's first correction is to split those applications into projects — and
// the natural half-edit is to move the SERVICES and leave the rung where it was.
// The result is an environment owned by one project and named by services in
// several. Config promotion flows along an environment, so those services would
// promote together: one rung, one version, one `hz config promote`, three
// applications moved.
//
// WHAT DOWNSTREAM ALREADY DOES, AND WHY IT IS NOT ENOUGH. ValidateEnvironments
// looks a service's rung up as envKey{service.Project, service.Environment}, so
// it does refuse this — measured, by deleting the call below and re-running the
// tests: the plan is rejected, with `service "analytics-dev" names environment
// "dev" in project "analytics", which does not exist`. That names ONE service,
// describes the fault as a MISSING rung, and says nothing about the rung being
// shared or about which other services are on it. An operator reading it adds a
// rung called `dev` under `analytics` and is none the wiser about `shop`.
//
// So the refusal belongs here, where the whole file is in view: it names every
// project involved, the services each contributed, which of them declares no
// such rung, and what a shared rung would do at promotion time.
func (f ImportFile) validateRungOwnership(envs map[envKey]bool) error {
	// For every rung NAME a placement refers to, which projects name it.
	byName := map[string][]rungRef{}
	order := []string{}
	for _, a := range f.Assign {
		env := strings.TrimSpace(a.Environment)
		if env == "" {
			continue
		}
		project := strings.TrimSpace(a.Project)
		refs, known := byName[env]
		if !known {
			order = append(order, env)
		}
		found := false
		for i := range refs {
			if refs[i].project == project {
				refs[i].services = append(refs[i].services, a.Service)
				found = true
				break
			}
		}
		if !found {
			refs = append(refs, rungRef{project: project, services: []string{a.Service}})
		}
		byName[env] = refs
	}

	for _, env := range order {
		refs := byName[env]
		var missing []rungRef
		for _, r := range refs {
			if !envs[envKey{r.project, env}] {
				missing = append(missing, r)
			}
		}
		if len(missing) == 0 {
			continue
		}
		// Which projects DO declare a rung of this name. If one does and a
		// service in another project names it, that is the cross-project rung.
		var owners []string
		for k := range envs {
			if k.name == env {
				owners = append(owners, k.project)
			}
		}
		sort.Strings(owners)

		if len(owners) == 0 {
			r := missing[0]
			return &ImportFileError{
				Token: r.services[0],
				Msg: fmt.Sprintf("service %q names environment %q, which is not declared in `environments` at all. Declare the rung before a service may stand on it — `%s` needs a row {\"project\": %q, \"name\": %q, \"posture\": \"...\"}",
					r.services[0], env, "environments", r.project, env),
			}
		}
		return &ImportFileError{
			Token: missing[0].services[0],
			Msg:   crossProjectRungMessage(env, owners, refs, envs),
		}
	}
	return nil
}

// rungRef is one project's claim on a rung name, and the services that made it.
type rungRef struct {
	project  string
	services []string
}

// crossProjectRungMessage spells out a rung that several projects claim.
//
// It lists EVERY project involved and the services each contributed — the ones
// that declared the rung and the ones that only stand on it — because the
// operator's question on reading this is "which of my edits disagree", and a
// message naming one service answers it for one of them. The projects that
// declare no rung of this name are the ones that have to change.
func crossProjectRungMessage(env string, owners []string, refs []rungRef, envs map[envKey]bool) string {
	rows := append([]rungRef(nil), refs...)
	// A project that DECLARES this rung but has no service on it is still part
	// of the disagreement: it is where the rung currently lives.
	for _, owner := range owners {
		found := false
		for _, r := range rows {
			if r.project == owner {
				found = true
				break
			}
		}
		if !found {
			rows = append(rows, rungRef{project: owner})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].project < rows[j].project })

	var b strings.Builder
	fmt.Fprintf(&b, "environment %q belongs to exactly one project, and this file spreads it over %d:\n", env, len(rows))
	for _, r := range rows {
		names := append([]string(nil), r.services...)
		sort.Strings(names)
		on := strings.Join(names, ", ")
		if on == "" {
			on = "(no service stands on it)"
		}
		state := "rung declared here"
		if !envs[envKey{r.project, env}] {
			state = fmt.Sprintf("NO rung %q is declared in %q", env, r.project)
		}
		fmt.Fprintf(&b, "      %-24s %-34s %s\n", r.project+" / "+env, on, state)
	}
	fmt.Fprintf(&b, "  %q is declared under %s and nowhere else.\n", env, strings.Join(quoteAll(owners), " and "))
	b.WriteString("  Config promotion flows along an environment: one rung carrying services from\n")
	b.WriteString("  several projects promotes all of them together, on one version, from one\n")
	b.WriteString("  command. Either declare a separate rung of this name under each project that\n")
	b.WriteString("  needs one, or put these services in one project.")
	return b.String()
}

// ValidateAgainstServices is the half of validation that needs to know what
// exists. Two rules, and they are the same rule seen from both ends:
//
//   - a service the file names that is not in the config is refused, because a
//     typo in a service name would otherwise be an assignment silently applied
//     to nothing;
//   - a service in the config the file does not name is ALSO refused, rather
//     than silently left unassigned.
//
// The second is the one worth arguing. A file that omits a service cannot be
// told apart from a file whose author deleted the wrong line, and the outcome of
// guessing is the one this whole feature exists to prevent: a service quietly
// left out of the tree, which looks exactly like a service deliberately left
// out. `unassigned` exists so the deliberate case can be SAID.
//
// It pays for itself twice: it is also the drift guard. The proposal path
// refuses an --execute whose fingerprint no longer matches; a file is
// hand-edited so its fingerprint means nothing, but a service added to or
// removed from the gateway since --plan-out wrote the file is refused here BY
// NAME, which is a strictly better message than a hash mismatch.
func (f ImportFile) ValidateAgainstServices(services []string) error {
	have := make(map[string]bool, len(services))
	for _, name := range services {
		have[name] = true
	}
	named := map[string]bool{}
	for _, a := range f.Assign {
		name := strings.TrimSpace(a.Service)
		named[name] = true
		if !have[name] {
			return &ImportFileError{
				Token: name,
				Msg: fmt.Sprintf("`assign` names service %q, which is not in this config. `hz service list` shows what is. (If the service was removed from the gateway since this file was written, delete its row.)",
					name),
			}
		}
	}
	for _, name := range f.Unassigned {
		name = strings.TrimSpace(name)
		named[name] = true
		if !have[name] {
			return &ImportFileError{
				Token: name,
				Msg: fmt.Sprintf("`unassigned` names service %q, which is not in this config. `hz service list` shows what is. (If the service was removed from the gateway since this file was written, delete its name.)",
					name),
			}
		}
	}

	var missing []string
	for _, name := range services {
		if !named[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &ImportFileError{
			Msg: fmt.Sprintf("this file does not account for %d service(s) in the config: %s.\n  Every service must appear exactly once, in `assign` or in `unassigned`. A\n  service left out of both cannot be told apart from one whose row was deleted by\n  mistake, and the cost of guessing is a service quietly outside the tree. Put it\n  in `unassigned` to say, on purpose, that it has no project — that is legal and\n  it keeps working unchanged.\n  (This is also how hz notices the gateway changed since this file was written.)",
				len(missing), strings.Join(quoteAll(missing), ", ")),
		}
	}
	return nil
}

// ServiceNames lists every service in the config, in config order.
func (c *Config) ServiceNames() []string {
	out := make([]string, 0, len(c.Services))
	for _, svc := range c.Services {
		out = append(out, svc.Name)
	}
	return out
}

// ValidateImportFile runs both halves against this config.
func (c *Config) ValidateImportFile(f ImportFile) error {
	if err := f.Validate(); err != nil {
		return err
	}
	return f.ValidateAgainstServices(c.ServiceNames())
}

// PlanFromImportFile converts a validated file into the plan ApplyImport writes.
//
// The reasons come out EMPTY, and that is the honest answer: the operator's
// authority for a row is that they wrote it, and inventing "declared in
// tree.json" for every row would put thirty-three identical sentences under
// thirty-three proposals and teach the reader to skip the reason column. The
// renderer prints no reason line when there is none.
func (c *Config) PlanFromImportFile(f ImportFile) (ImportPlan, error) {
	if err := c.ValidateImportFile(f); err != nil {
		return ImportPlan{}, err
	}
	return f.plan(), nil
}

// plan converts without validating. Unexported: every caller outside this file
// goes through PlanFromImportFile, so a file can never reach ApplyImport
// unchecked.
func (f ImportFile) plan() ImportPlan {
	p := ImportPlan{
		Projects:     make([]ImportProject, 0, len(f.Projects)),
		Environments: make([]ImportEnvironment, 0, len(f.Environments)),
		Assignments:  make([]ImportAssignment, 0, len(f.Assign)),
		Unassigned:   make([]ImportUnassigned, 0, len(f.Unassigned)),
	}
	for _, x := range f.Projects {
		p.Projects = append(p.Projects, ImportProject{Name: x.Name, Parent: x.Parent})
	}
	for _, x := range f.Environments {
		p.Environments = append(p.Environments, ImportEnvironment{
			Project: x.Project, Name: x.Name, Posture: x.Posture,
		})
	}
	for _, x := range f.Assign {
		p.Assignments = append(p.Assignments, ImportAssignment{
			Service: x.Service, Project: x.Project, Environment: x.Environment,
		})
	}
	for _, x := range f.Unassigned {
		p.Unassigned = append(p.Unassigned, ImportUnassigned{Service: x})
	}
	return p
}
