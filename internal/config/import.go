package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Importing a gateway that predates the project tree.
//
// The starting fact is legacy_compat_test.go: a config written before projects,
// environments and feeds existed loads and SAVES unchanged. Nothing is broken and
// nothing has to move, so this is not a migration. It is a PROPOSAL — hz reads
// what is already in the config, says what it can support with evidence, and
// leaves everything else exactly as it found it.
//
// The rule the whole file exists to enforce: **a wrong project assignment is
// worse than none.** A service on the wrong rung resolves the wrong config later,
// and nothing about that looks wrong at the time it is written. So there is no
// confidence score and no heuristic ranking. Either a proposal can name the
// evidence it came from — and it does, in the output, beside the proposal — or the
// service is proposed as UNASSIGNED, which ValidateProjects explicitly permits and
// the CLI already sorts last.

// ImportPlan is what an import would do. It is a pure function of the config
// (ProposeImport), so the plan an operator reads in a dry run is the plan
// ApplyImport writes — the Fingerprint is how a caller proves that between the
// two.
type ImportPlan struct {
	// Projects to declare, roots first, each with the evidence for it.
	Projects []ImportProject `json:"projects"`
	// Environments to declare. An environment belongs to a project, so this is
	// empty for a plan that proposes no projects, however many posture words the
	// service names contain.
	Environments []ImportEnvironment `json:"environments"`
	// Assignments are the services this plan would move into a project.
	Assignments []ImportAssignment `json:"assignments"`
	// Unassigned are the services it deliberately leaves alone, each with the
	// reason no proposal could be made. This is not a failure list: an unassigned
	// service is a legal, working service.
	Unassigned []ImportUnassigned `json:"unassigned"`
	// Signals are the things that were looked at and produced nothing, so an
	// operator can tell "hz did not consider this" from "hz considered it and it
	// said nothing here".
	Signals []ImportSignal `json:"signals,omitempty"`
}

// ImportProject is a project to declare, and why.
type ImportProject struct {
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
	Reason string `json:"reason"`
}

// ImportEnvironment is a rung to declare. Name and Posture stay separate for the
// reason Environment says: a project may name its rung "beta" and sit at
// staging's posture, and collapsing the two would force a relabel nobody asked
// for.
type ImportEnvironment struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Posture string `json:"posture"`
	Reason  string `json:"reason"`
}

// ImportAssignment moves one service onto one rung. Environment may be empty:
// project evidence and posture evidence are independent, and a service with a
// project and no posture word gets the project only.
type ImportAssignment struct {
	Service     string `json:"service"`
	Project     string `json:"project"`
	Environment string `json:"environment,omitempty"`
	// ProjectReason and EnvironmentReason are separate strings because they come
	// from different evidence and either can be the one an operator disputes.
	ProjectReason     string `json:"projectReason"`
	EnvironmentReason string `json:"environmentReason,omitempty"`
}

// ImportUnassigned is a service left alone, and why. Note carries context that is
// not evidence — something an operator can act on that hz will not act on itself.
type ImportUnassigned struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
	Note    string `json:"note,omitempty"`
}

// ImportSignal is a thing that was examined and not used. It is reported because
// silence about a signal is indistinguishable from not having looked.
type ImportSignal struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// Counts summarises a plan in the one line an operator quotes.
func (p ImportPlan) Counts() (services, assigned, unassigned int) {
	return len(p.Assignments) + len(p.Unassigned), len(p.Assignments), len(p.Unassigned)
}

// Fingerprint identifies the plan's content. A dry run prints it and an execute
// carries it back, so a config that changed between the two is refused rather
// than silently applying a different plan from the one that was read.
func (p ImportPlan) Fingerprint() string {
	b, err := json.Marshal(p)
	if err != nil {
		// Every field is a string or a slice of structs of strings; there is no
		// input that fails to marshal. Fail closed rather than return a
		// fingerprint that could collide.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// postureWords maps a word that appears in service names and hostnames to the
// posture it implies. The word becomes the environment's NAME and the mapping
// its POSTURE, which is why "beta" is here at all: it is a real environment name
// sitting at staging's isolation level (plan/example-projection.md, analytics).
//
// Matched as a whole token, never as a substring: "reproduction" contains "prod"
// and means nothing of the kind.
var postureWords = map[string]string{
	"dev":     "dev",
	"test":    "dev",
	"stage":   "staging",
	"staging": "staging",
	"beta":    "staging",
	"prod":    "prod",
}

// postureFor returns the posture a known environment word implies.
func postureFor(word string) (string, bool) {
	p, ok := postureWords[word]
	return p, ok
}

// tokenize splits an identifier into the words a posture match may see.
// Everything that is not a letter or a digit separates, so "web-staging",
// "web_staging" and "web.staging" all yield "staging" and "prod2" does not yield
// "prod".
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// domainLabels splits a hostname into labels, lowercased, with a trailing root
// dot removed.
func domainLabels(d string) []string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return nil
	}
	return strings.Split(d, ".")
}

// commonDomainSuffix returns the longest run of trailing labels every domain
// shares. It is the suffix that cannot tell any two services apart, which is
// exactly what makes it useful twice over: nothing below it is evidence, and the
// suffix itself — when it is a real domain rather than a bare TLD — is the one
// honest name for a root project.
func commonDomainSuffix(domains [][]string) []string {
	if len(domains) == 0 {
		return nil
	}
	common := domains[0]
	for _, d := range domains[1:] {
		n := 0
		for n < len(common) && n < len(d) && common[len(common)-1-n] == d[len(d)-1-n] {
			n++
		}
		common = common[len(common)-n:]
	}
	return common
}

// groupKey is the suffix one label longer than the config-wide common suffix:
// the shortest suffix of this domain that does NOT belong to every service.
// Returns "" for a domain that is the common suffix itself.
func groupKey(labels, common []string) string {
	if len(labels) <= len(common) {
		return ""
	}
	return strings.Join(labels[len(labels)-len(common)-1:], ".")
}

// importSvc is one service reduced to the facts a proposal may rest on.
type importSvc struct {
	idx          int
	name         string
	keys         []string // distinct group suffixes across its domains, sorted
	hasDomains   bool
	backend      string // proxy backend, host:port, verbatim
	backendHost  string // the host half of it
	words        []string
	internalOnly bool
}

// ProposeImport reads the config and proposes a project tree for it. It mutates
// nothing; ApplyImport is the half that writes.
//
// Four signals were considered. Two are used and two are not, and the plan says
// which is which rather than leaving an operator to infer it from an empty
// section:
//
//	USED  domain suffix      services under a suffix that is shared by some of
//	                         them and not by all of them are one project. A suffix
//	                         every service has cannot tell them apart; a suffix
//	                         only one service has is its own hostname, which names
//	                         the service, not a project.
//	USED  identical backend  two services whose proxy backend is the same host:port
//	                         are the same listening process, so they are the same
//	                         project. It can only JOIN a project the suffix already
//	                         named — a backend is a machine address and has no
//	                         project name in it.
//	USED  posture word       a known word in a service name or hostname names an
//	                         ENVIRONMENT, never a project. Useless on a service with
//	                         no project, because an environment belongs to one.
//	NOT   backend host       a host is a machine, and one machine hosts several
//	                         projects (plan/example-projection.md, gw-1).
//	NOT   internal_only      exposure is neither a project nor a posture: an
//	                         internal-only admin tool is production.
func (c *Config) ProposeImport() ImportPlan {
	plan := ImportPlan{}

	svcs, common := importFacts(c.Services)
	if len(svcs) == 0 {
		return plan
	}

	// --- projects, from the domain suffix -----------------------------------
	groups := map[string][]int{}
	for _, s := range svcs {
		if len(s.keys) == 1 {
			groups[s.keys[0]] = append(groups[s.keys[0]], s.idx)
		}
	}
	// A suffix only one service sits under is that service's own hostname. It
	// names the service, not a project, and declaring a project per service
	// would write the hostnames back as if they were a tree. So a group needs
	// two members to be a group at all.
	//
	// This does cost the real single-service project (plan/example-projection.md
	// has three of them, client-a..c). Nothing in a legacy config distinguishes
	// one of those from a service that simply has a hostname, and the asymmetry
	// decides it: the operator adds a project hz did not propose in one command,
	// and finds a service silently filed under the wrong one much later.
	for key, members := range groups {
		if len(members) < 2 {
			delete(groups, key)
		}
	}
	grouping := len(groups) > 0

	names := projectNamesForGroups(groups)
	project := map[int]string{}       // service index -> project
	projectReason := map[int]string{} // service index -> why
	for key, members := range groups {
		for _, idx := range members {
			project[idx] = names[key]
			projectReason[idx] = fmt.Sprintf("%d service(s) share the domain suffix %s", len(members), key)
		}
	}

	// --- identical backends, which join a project but cannot name one -------
	joinByBackend(svcs, project, projectReason)

	// --- the root, which names itself and assigns nothing -------------------
	root, rootReason := rootProject(common, names)
	if root != "" {
		plan.Projects = append(plan.Projects, ImportProject{Name: root, Reason: rootReason})
	}
	for _, key := range sortedKeys(groups) {
		plan.Projects = append(plan.Projects, ImportProject{
			Name:   names[key],
			Parent: root,
			Reason: fmt.Sprintf("%d service(s) share the domain suffix %s", len(groups[key]), key),
		})
	}

	// --- environments, from posture words, only where there is a project ----
	envOf, envReason := environmentsFor(svcs, project)
	plan.Environments = declaredEnvironments(svcs, project, envOf)

	// --- the two lists ------------------------------------------------------
	for _, s := range svcs {
		p, ok := project[s.idx]
		if !ok {
			plan.Unassigned = append(plan.Unassigned, unassignedFor(s, common, grouping, projectReason[s.idx]))
			continue
		}
		plan.Assignments = append(plan.Assignments, ImportAssignment{
			Service:           s.name,
			Project:           p,
			Environment:       envOf[s.idx],
			ProjectReason:     projectReason[s.idx],
			EnvironmentReason: envReason[s.idx],
		})
	}
	sort.Slice(plan.Assignments, func(i, j int) bool { return plan.Assignments[i].Service < plan.Assignments[j].Service })
	sort.Slice(plan.Unassigned, func(i, j int) bool { return plan.Unassigned[i].Service < plan.Unassigned[j].Service })

	plan.Signals = unusedSignals(svcs, common, grouping)
	return plan
}

// importFacts reduces the services to the facts a proposal may rest on, and
// returns the domain suffix every one of them shares.
func importFacts(services []Service) ([]importSvc, []string) {
	var all [][]string
	for _, svc := range services {
		for _, d := range svc.Domains {
			if labels := domainLabels(d); len(labels) > 0 {
				all = append(all, labels)
			}
		}
	}
	common := commonDomainSuffix(all)

	out := make([]importSvc, 0, len(services))
	for i, svc := range services {
		s := importSvc{idx: i, name: svc.Name}
		seenKey := map[string]bool{}
		words := map[string]bool{}
		for _, w := range tokenize(svc.Name) {
			if _, ok := postureFor(w); ok {
				words[w] = true
			}
		}
		for _, d := range svc.Domains {
			labels := domainLabels(d)
			if len(labels) == 0 {
				continue
			}
			s.hasDomains = true
			if k := groupKey(labels, common); k != "" && !seenKey[k] {
				seenKey[k] = true
				s.keys = append(s.keys, k)
			}
			// Labels every domain shares cannot tell services apart, so they are
			// not read for posture either: a company whose domain is literally
			// prod.example.com would otherwise put every service in prod.
			distinguishing := len(labels) - len(common)
			if distinguishing < 0 {
				distinguishing = 0
			}
			for _, label := range labels[:distinguishing] {
				for _, w := range tokenize(label) {
					if _, ok := postureFor(w); ok {
						words[w] = true
					}
				}
			}
		}
		sort.Strings(s.keys)
		for w := range words {
			s.words = append(s.words, w)
		}
		sort.Strings(s.words)
		if svc.Proxy != nil {
			s.internalOnly = svc.Proxy.InternalOnly
			s.backend = strings.ToLower(strings.TrimSpace(svc.Proxy.Backend))
			if s.backend != "" {
				if host, _, err := net.SplitHostPort(s.backend); err == nil {
					s.backendHost = host
				} else {
					s.backendHost = s.backend
				}
			}
		}
		out = append(out, s)
	}
	return out, common
}

// projectNamesForGroups names each suffix group after its leftmost label, which
// is the part of the hostname that is not shared. Two groups can reduce to the
// same leftmost label (storefront.a.com and storefront.b.com); when they do, both
// keep their full suffix as the name rather than one of them silently absorbing
// the other's services.
func projectNamesForGroups(groups map[string][]int) map[string]string {
	short := map[string][]string{}
	for key := range groups {
		label := strings.SplitN(key, ".", 2)[0]
		short[label] = append(short[label], key)
	}
	out := map[string]string{}
	for key := range groups {
		label := strings.SplitN(key, ".", 2)[0]
		if len(short[label]) == 1 {
			out[key] = label
		} else {
			out[key] = key
		}
	}
	return out
}

// joinByBackend pulls a service into the project its process-mates are already
// in. Two services whose proxy backend is byte-identical are the same listening
// process — the `git` and `registry` row of plan/example-projection.md — so this
// is not an inference about intent, it is the same program under two names.
//
// A cluster that straddles two projects is a genuine contradiction, and the
// resolution is to assign NONE of it: the whole point is that a wrong assignment
// costs more than an absent one.
func joinByBackend(svcs []importSvc, project, projectReason map[int]string) {
	byBackend := map[string][]importSvc{}
	for _, s := range svcs {
		if s.backend != "" {
			byBackend[s.backend] = append(byBackend[s.backend], s)
		}
	}
	for _, backend := range sortedKeys(byBackend) {
		cluster := byBackend[backend]
		if len(cluster) < 2 {
			continue
		}
		seen := map[string]string{} // project -> a service name in it
		for _, s := range cluster {
			if p, ok := project[s.idx]; ok {
				seen[p] = s.name
			}
		}
		if len(seen) > 1 {
			names := sortedKeys(seen)
			for _, s := range cluster {
				delete(project, s.idx)
				projectReason[s.idx] = fmt.Sprintf(
					"backend %s is shared with %s, and the domain suffixes put them in different projects (%s)",
					backend, otherNames(cluster, s.name), strings.Join(names, " vs "))
			}
			continue
		}
		if len(seen) != 1 {
			continue
		}
		var p, peer string
		for k, v := range seen {
			p, peer = k, v
		}
		for _, s := range cluster {
			if _, ok := project[s.idx]; ok {
				continue
			}
			project[s.idx] = p
			projectReason[s.idx] = fmt.Sprintf(
				"backend %s is byte-identical to %s, which is in %s — the same listening process", backend, peer, p)
		}
	}
}

// otherNames lists the cluster members that are not `self`, for an error that
// has to name what it collided with.
func otherNames(cluster []importSvc, self string) string {
	var out []string
	for _, s := range cluster {
		if s.name != self {
			out = append(out, s.name)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// rootProject proposes the one project that can be named without assigning a
// single service to it: the domain every service already sits under.
//
// It is worth proposing on its own. A root is where the package feed is declared
// (architecture.md, "Definition of done" step 1), and on the common single-domain
// gateway — every service a subdomain of one company domain — it is the ONLY
// honest project in the config. It holds nothing, which is exactly the state the
// root is in for the whole of step 1.
//
// A bare TLD is not a root: "com" is not an organisation.
func rootProject(common []string, names map[string]string) (string, string) {
	if len(common) < 2 {
		return "", ""
	}
	suffix := strings.Join(common, ".")
	name := common[0]
	for _, n := range names {
		if n == name {
			name = suffix
			break
		}
	}
	return name, fmt.Sprintf("every domain in this config ends in %s", suffix)
}

// environmentsFor picks each assigned service's rung from the posture words in
// its name and hostnames.
//
// Two different words on one service is a contradiction, not a tie to break:
// "api-staging.prod.example.com" is either, and guessing puts a service on the
// wrong rung, which is the failure this file exists to avoid.
func environmentsFor(svcs []importSvc, project map[int]string) (map[int]string, map[int]string) {
	envOf := map[int]string{}
	reason := map[int]string{}
	for _, s := range svcs {
		if _, ok := project[s.idx]; !ok {
			continue
		}
		switch len(s.words) {
		case 0:
		case 1:
			envOf[s.idx] = s.words[0]
			reason[s.idx] = fmt.Sprintf("%q appears in its name or a hostname; posture %s",
				s.words[0], postureWords[s.words[0]])
		default:
			reason[s.idx] = fmt.Sprintf("no environment: %s all appear in its name or hostnames, and a service is on one rung",
				strings.Join(quoteAll(s.words), " and "))
		}
	}
	return envOf, reason
}

// declaredEnvironments turns the per-service rungs into the Environment records
// that have to exist before any service may name one.
func declaredEnvironments(svcs []importSvc, project, envOf map[int]string) []ImportEnvironment {
	type key struct{ project, name string }
	members := map[key][]string{}
	for _, s := range svcs {
		env, ok := envOf[s.idx]
		if !ok {
			continue
		}
		k := key{project[s.idx], env}
		members[k] = append(members[k], s.name)
	}
	out := make([]ImportEnvironment, 0, len(members))
	for k, names := range members {
		sort.Strings(names)
		out = append(out, ImportEnvironment{
			Project: k.project,
			Name:    k.name,
			Posture: postureWords[k.name],
			Reason:  fmt.Sprintf("%q names the rung %s sits on", k.name, strings.Join(names, ", ")),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// unassignedFor says why one service is being left alone. Every branch names
// something an operator can check in the config they already have.
//
// known is the reason a signal already recorded — a backend cluster that
// straddled two projects writes one — and it wins, because it is the specific
// answer and everything below it is the general one.
func unassignedFor(s importSvc, common []string, grouping bool, known string) ImportUnassigned {
	u := ImportUnassigned{Service: s.name}
	switch {
	case known != "":
		u.Reason = known
	case !s.hasDomains:
		u.Reason = "no domains, so no suffix to group it by, and nothing else names a project"
	case len(s.keys) > 1:
		u.Reason = fmt.Sprintf("its domains fall under %s — a service is in one project, and hz will not pick",
			strings.Join(s.keys, " and "))
	case len(s.keys) == 0:
		u.Reason = fmt.Sprintf("its only domain IS %s, the suffix every service shares", strings.Join(common, "."))
	case !grouping:
		u.Reason = fmt.Sprintf("no domain suffix groups it with another service: %s names this service, not a project", s.keys[0])
	default:
		u.Reason = fmt.Sprintf("%s is the only service under %s", s.name, s.keys[0])
	}
	if len(s.words) > 0 {
		u.Note = fmt.Sprintf("posture hint %s found, and unusable: an environment belongs to a project and this service has none",
			strings.Join(quoteAll(s.words), ", "))
	}
	return u
}

// unusedSignals reports what was examined and rejected, with the numbers from
// THIS config so the rejection is checkable rather than a stock sentence.
func unusedSignals(svcs []importSvc, common []string, grouping bool) []ImportSignal {
	var out []ImportSignal

	if !grouping {
		detail := "no suffix is shared by more than one service"
		if len(common) > 0 {
			detail = fmt.Sprintf("every service has its own subdomain under %s, so the suffix names the service rather than a project",
				strings.Join(common, "."))
		}
		out = append(out, ImportSignal{Name: "domain suffix", Detail: detail})
	}

	hosts := map[string][]string{}
	for _, s := range svcs {
		if s.backendHost != "" {
			hosts[s.backendHost] = append(hosts[s.backendHost], s.name)
		}
	}
	var shared []string
	for _, h := range sortedKeys(hosts) {
		if len(hosts[h]) > 1 {
			shared = append(shared, fmt.Sprintf("%s (%d)", h, len(hosts[h])))
		}
	}
	if len(shared) > 0 {
		out = append(out, ImportSignal{
			Name: "backend host",
			Detail: fmt.Sprintf("%s — a host is a machine, and one machine hosts several projects, so co-location is not evidence",
				strings.Join(shared, ", ")),
		})
	}

	internal := 0
	for _, s := range svcs {
		if s.internalOnly {
			internal++
		}
	}
	if internal > 0 {
		out = append(out, ImportSignal{
			Name: "internal_only",
			Detail: fmt.Sprintf("%d of %d service(s) — exposure is neither a project nor a posture; an internal-only admin tool is production",
				internal, len(svcs)),
		})
	}
	return out
}

// ApplyImport writes a plan into the config.
//
// ORDERING. Declarations and assignments go in together, and that is the whole
// requirement: Save runs ValidateProjects and ValidateEnvironments over the
// FINISHED config, so a service naming a project declared in the same Save is
// legal and the order of the fields within the struct does not matter.
// legacy_compat_test.go pins the rule from the other side — a service naming a
// rung nobody declared is refused loudly — and TestImportOrderWithinTheSaveDoesNotMatter
// pins this side of it by writing the assignments first.
//
// merge=false refuses a config that already declares projects. An import that
// silently reorganises an existing tree is the destructive case, and it is the
// one a second `hz import --execute` would otherwise be.
//
// merge=true is additive only: an existing project or rung of the same name is
// left exactly as it is, and a service that already names a project is never
// moved. An import can add to a tree; it may not rewrite one.
func (c *Config) ApplyImport(p ImportPlan, merge bool) error {
	if len(c.Projects) > 0 && !merge {
		return fmt.Errorf("this config already declares %d project(s); refusing to import over a tree that exists. Pass merge to add to it — existing projects, rungs and assignments are kept", len(c.Projects))
	}

	// Fresh slices rather than in-place writes: a Config is copied shallowly in
	// several places, so assigning through the existing backing array would
	// mutate the config another goroutine is still serving.
	projects := append([]Project(nil), c.Projects...)
	environments := append([]Environment(nil), c.Environments...)
	services := append([]Service(nil), c.Services...)

	haveProject := map[string]bool{}
	for _, existing := range projects {
		haveProject[existing.Name] = true
	}
	haveEnv := map[envKey]bool{}
	for _, existing := range environments {
		haveEnv[envKey{existing.Project, existing.Name}] = true
	}
	byName := map[string]int{}
	for i, svc := range services {
		byName[svc.Name] = i
	}

	for _, want := range p.Projects {
		if haveProject[want.Name] {
			continue
		}
		haveProject[want.Name] = true
		projects = append(projects, Project{Name: want.Name, Parent: want.Parent})
	}
	for _, want := range p.Environments {
		k := envKey{want.Project, want.Name}
		if haveEnv[k] {
			continue
		}
		haveEnv[k] = true
		environments = append(environments, Environment{Project: want.Project, Name: want.Name, Posture: want.Posture})
	}
	for _, want := range p.Assignments {
		i, ok := byName[want.Service]
		if !ok {
			return fmt.Errorf("the plan assigns service %q, which is not in this config — re-read the plan", want.Service)
		}
		if services[i].Project != "" {
			continue // already placed; an import adds, it does not move
		}
		services[i].Project = want.Project
		services[i].Environment = want.Environment
	}

	c.Projects, c.Environments, c.Services = projects, environments, services

	// Validated here as well as in Save, so a caller that holds the config in
	// memory before writing it finds out now rather than after it has published
	// a tree it cannot persist.
	if err := c.ValidateProjects(); err != nil {
		return err
	}
	if err := c.ValidateEnvironments(); err != nil {
		return err
	}
	return c.ValidateFeeds()
}

// SetFeed declares the package repository a project's machines install from.
//
// A writer for this is the missing half of the feed: everything else about it
// reads — `hz feed ls`, `hz feed show`, ResolveFeed — and declaring one meant
// hand-editing the config JSON on the gateway. Whole-record replacement, never a
// field-level merge, for the reason ResolveFeed gives: a feed wins whole, and a
// half-updated one would make "where did this value come from" unanswerable.
func (c *Config) SetFeed(project string, f Feed) error {
	for i := range c.Projects {
		if c.Projects[i].Name != project {
			continue
		}
		if err := f.Validate(); err != nil {
			return err
		}
		projects := append([]Project(nil), c.Projects...)
		feed := f
		projects[i].Feed = &feed
		c.Projects = projects
		return nil
	}
	return fmt.Errorf("no project %q — `hz project ls` lists what exists", project)
}

// sortedKeys is the deterministic-iteration helper every map in this file needs:
// a plan whose rows move between two identical runs is a plan nobody can diff.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteAll(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, fmt.Sprintf("%q", w))
	}
	return out
}
