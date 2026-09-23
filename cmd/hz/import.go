package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz import` proposes a project tree for a gateway that predates one.
//
// The interaction model is `hz config promote`'s, deliberately and without variation:
// the whole plan is computed and printed first, a DRY RUN IS THE DEFAULT, and
// only the write is withheld until --execute. Two commands that both propose a
// change to the config should not have two different ideas of what running them
// means.
//
// Nothing here decides anything. The server computes the proposal from the
// config it holds; this renders it, including — at equal weight — the list of
// services it is leaving alone and why. A service with no evidence stays
// unassigned, which is legal, which is what every service in a legacy config
// already is, and which is the correct answer far more often than a guess.
//
// THE PROPOSAL IS A STARTING POINT AND NOT A VERDICT. A dry run against a real
// 33-service estate assigned every service and was wrong: that estate is flat —
// every service at <name>.<our-co>.<tld> — so the domain suffix cannot tell one
// application from another, and thirty services collapsed into one project. The
// environment grouping inherited the coarseness and put three unrelated
// applications on one `dev` rung, which config promotion would then have moved
// together. The config does not contain the structure; the operator does. So:
//
//	hz import --plan-out tree.json    write the proposal out as an editable file
//	hz import --from tree.json        dry run the edited file
//	hz import --from tree.json --execute
//
// Same pipeline, different source. internal/config/import_file.go is the file
// format and every rule it is checked against.

const importUsage = `usage: hz import [--plan-out FILE | --from FILE] [--execute] [--merge]

Reads the services already in this config and proposes a project tree for them:
projects, environments, and which service belongs on which rung. Every proposal
names the evidence it came from. A service nothing explains is left UNASSIGNED,
which is legal and which is what it already is.

Prints the plan and writes NOTHING to the config unless --execute is given.

The proposal is a STARTING POINT, not a verdict. hz can only see what is in the
config, and a flat estate — one domain, a subdomain per service — contains no
evidence of which services are one application. You know; the config does not.
So write the proposal out, correct it, and import the corrected file:

  hz import --plan-out tree.json      write the proposal as an editable file
  $EDITOR tree.json
  hz import --from tree.json          dry run the file (validated, nothing written)
  hz import --from tree.json --execute

  --plan-out FILE   write the proposal to FILE as an editable plan and stop.
                    Cannot be combined with --from or --execute.
  --from FILE       take the plan from FILE instead of from hz's own proposal.
                    The file is validated hard first: every service it names must
                    exist, every service in the config must appear exactly once
                    (in assign or in unassigned), every project and rung a row
                    refers to must be declared in the same file, a posture must
                    be one of dev/staging/prod, and a rung may not be spread
                    across projects. Failures name the line.
  --merge           add to a config that already declares projects. Existing
                    projects, rungs and service assignments are kept exactly as
                    they are; only what is missing is added. Required to
                    --execute over an existing tree — an import that silently
                    reorganises one is the destructive case.
  --execute         actually write the plan.
`

func runImport(c *client, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, importUsage) }
	merge := fs.Bool("merge", false, "add to an existing tree instead of refusing")
	execute := fs.Bool("execute", false, "write the plan; without it this is a dry run")
	planOut := fs.String("plan-out", "", "write the proposal to this file as an editable plan, and stop")
	from := fs.String("from", "", "take the plan from this file instead of from hz's proposal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("hz import takes no arguments, got %q\n%s", fs.Arg(0), importUsage)
	}
	if *planOut != "" && *from != "" {
		return fmt.Errorf("--plan-out writes hz's proposal and --from reads your edited one; pass one or the other\n%s", importUsage)
	}
	if *planOut != "" && *execute {
		return fmt.Errorf("--plan-out writes a file for you to edit, so there is nothing to execute yet. Write it, edit it, then `hz import --from %s --execute`\n%s", *planOut, importUsage)
	}

	// The proposal is fetched on every path. On --from it is not the plan, but
	// it is still the only place this client learns what services exist and
	// whether a tree is already declared — and both are needed to validate the
	// file before a byte goes over the wire.
	var proposal apitypes.ImportPlanResp
	if err := c.do(http.MethodGet, "/api/v1/import", nil, &proposal); err != nil {
		return err
	}

	if *planOut != "" {
		printImportPlan(proposal, "")
		return writePlanFile(*planOut, proposal)
	}

	plan := proposal
	var fileReq *apitypes.ImportFileReq
	if *from != "" {
		var err error
		if plan, fileReq, err = planFromFile(*from, proposal); err != nil {
			return err
		}
	}
	printImportPlan(plan, *from)

	if plan.ExistingProjects > 0 && !*merge {
		fmt.Printf("\nThis config already declares %d project(s). An import that silently\n", plan.ExistingProjects)
		fmt.Println("reorganises a tree somebody built is the destructive case, so --execute is")
		fmt.Println("refused without --merge. With --merge the existing projects, rungs and")
		fmt.Println("service assignments are kept and only what is missing is added.")
		if *execute {
			return fmt.Errorf("REFUSED: %d project(s) already declared; re-run with --merge to add to them", plan.ExistingProjects)
		}
	}

	if len(plan.Projects) == 0 && len(plan.Assignments) == 0 {
		if *from != "" {
			fmt.Printf("\n%s declares no project and assigns no service, so there is nothing to\n", *from)
			fmt.Println("import. Every service stays unassigned, which is legal and is what they are.")
			return nil
		}
		fmt.Println("\nThere is nothing to import. That is an answer, not a failure: nothing in")
		fmt.Println("this config names a project, and every service goes on working unassigned.")
		fmt.Println("\nIf there IS a tree here that hz cannot see — a flat estate hides one — write")
		fmt.Println("the proposal out and put it in by hand: `hz import --plan-out tree.json`.")
		return nil
	}

	if !*execute {
		fmt.Printf("\nDry run: nothing was written. Re-run with --execute to apply this plan.\n")
		if *from != "" {
			fmt.Printf("Plan read from %s. An --execute re-validates it against the config as it is\n", *from)
			fmt.Println("then: a service that appeared or vanished since is refused by name.")
		} else {
			fmt.Printf("Plan %s — an --execute is refused if the config changes before it runs.\n", plan.Fingerprint)
			fmt.Println("Disagree with it? `hz import --plan-out tree.json` writes it out to edit.")
		}
		return nil
	}

	var resp apitypes.ImportApplyResp
	req := apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint, Merge: *merge, Plan: fileReq}
	if fileReq != nil {
		// A fingerprint identifies a plan hz computed. This one is the
		// operator's, so there is nothing to compare it against.
		req.Fingerprint = ""
	}
	if err := c.do(http.MethodPost, "/api/v1/import", req, &resp); err != nil {
		return err
	}
	fmt.Printf("\nImported: %d project(s), %d environment(s), %d service(s) assigned.\n",
		resp.ProjectsAdded, resp.EnvironmentsAdded, resp.ServicesAssigned)
	if len(plan.Unassigned) > 0 {
		fmt.Printf("%d service(s) were left unassigned on purpose; `hz service edit` is how one moves.\n", len(plan.Unassigned))
	}
	fmt.Println("`hz project ls` shows the tree. A project with no feed installs from nowhere")
	fmt.Println("hz knows about — `hz feed set <project> --url ... --suite ... --component ...`.")
	return nil
}

// writePlanFile writes hz's proposal out as the file an operator edits, and
// says what to do with it. It writes the FILE and nothing else: the config is
// untouched until `--from ... --execute`.
func writePlanFile(path string, proposal apitypes.ImportPlanResp) error {
	raw, err := hzconfig.ImportFileFor(planFromResp(proposal)).Marshal()
	if err != nil {
		return fmt.Errorf("rendering the plan file: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	total := len(proposal.Assignments) + len(proposal.Unassigned)
	fmt.Printf("\nWrote %s — %d project(s), %d rung(s), %d service(s).\n", path, len(proposal.Projects), len(proposal.Environments), total)
	fmt.Println("Nothing was written to the config.")
	fmt.Println()
	fmt.Println("This is the proposal as a file you can correct. hz can only see what is in")
	fmt.Println("the config, and a flat estate — one domain, a subdomain per service — hides")
	fmt.Println("its structure from every signal hz has. You know which services are one")
	fmt.Println("application; the config does not say.")
	fmt.Println()
	fmt.Println("The evidence above is NOT in the file: once you move a row, hz's reason for")
	fmt.Println("the old one is a lie sitting beside it. Re-run `hz import` to see it again.")
	fmt.Println()
	fmt.Printf("  $EDITOR %s\n", path)
	fmt.Printf("  hz import --from %s            dry run it — validated, nothing written\n", path)
	fmt.Printf("  hz import --from %s --execute  apply it\n", path)
	return nil
}

// planFromFile reads the operator's edited plan and turns it into the same
// shape the renderer takes.
//
// It is validated HERE as well as on the server, and that is not belt and
// braces: only this side holds the raw bytes, so only this side can say
// "tree.json:14:" instead of naming the service and leaving the operator to
// find it in a file with 33 of them. The server validates again because the API
// is a surface of its own and a plan arriving from anywhere else has to meet the
// same bar.
func planFromFile(path string, proposal apitypes.ImportPlanResp) (apitypes.ImportPlanResp, *apitypes.ImportFileReq, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return apitypes.ImportPlanResp{}, nil, fmt.Errorf("reading the plan file: %w", err)
	}
	file, err := hzconfig.ParseImportFile(raw)
	if err != nil {
		return apitypes.ImportPlanResp{}, nil, hzconfig.AnnotateImportFileError(raw, path, err)
	}
	if err := file.Validate(); err != nil {
		return apitypes.ImportPlanResp{}, nil, hzconfig.AnnotateImportFileError(raw, path, err)
	}
	// The services this config has, as the server just reported them: the
	// proposal accounts for every one of them, assigned or not.
	services := make([]string, 0, len(proposal.Assignments)+len(proposal.Unassigned))
	for _, a := range proposal.Assignments {
		services = append(services, a.Service)
	}
	for _, u := range proposal.Unassigned {
		services = append(services, u.Service)
	}
	if err := file.ValidateAgainstServices(services); err != nil {
		return apitypes.ImportPlanResp{}, nil, hzconfig.AnnotateImportFileError(raw, path, err)
	}

	out := apitypes.ImportPlanResp{ExistingProjects: proposal.ExistingProjects}
	req := &apitypes.ImportFileReq{Version: file.Version, Unassigned: append([]string(nil), file.Unassigned...)}
	for _, p := range file.Projects {
		out.Projects = append(out.Projects, apitypes.ImportProjectResp{Name: p.Name, Parent: p.Parent})
		req.Projects = append(req.Projects, apitypes.ImportFileProjectReq{Name: p.Name, Parent: p.Parent})
	}
	for _, e := range file.Environments {
		out.Environments = append(out.Environments, apitypes.ImportEnvironmentResp{Project: e.Project, Name: e.Name, Posture: e.Posture})
		req.Environments = append(req.Environments, apitypes.ImportFileEnvironmentReq{Project: e.Project, Name: e.Name, Posture: e.Posture})
	}
	for _, a := range file.Assign {
		out.Assignments = append(out.Assignments, apitypes.ImportAssignmentResp{Service: a.Service, Project: a.Project, Environment: a.Environment})
		req.Assign = append(req.Assign, apitypes.ImportFilePlacementReq{Service: a.Service, Project: a.Project, Environment: a.Environment})
	}
	for _, name := range file.Unassigned {
		out.Unassigned = append(out.Unassigned, apitypes.ImportUnassignedResp{Service: name})
	}
	return out, req, nil
}

// planFromResp converts the wire proposal back to the internal plan so
// ImportFileFor can render it. The round trip is exact over everything the file
// carries; what it drops is the evidence, on purpose.
func planFromResp(p apitypes.ImportPlanResp) hzconfig.ImportPlan {
	out := hzconfig.ImportPlan{}
	for _, x := range p.Projects {
		out.Projects = append(out.Projects, hzconfig.ImportProject{Name: x.Name, Parent: x.Parent})
	}
	for _, x := range p.Environments {
		out.Environments = append(out.Environments, hzconfig.ImportEnvironment{Project: x.Project, Name: x.Name, Posture: x.Posture})
	}
	for _, x := range p.Assignments {
		out.Assignments = append(out.Assignments, hzconfig.ImportAssignment{Service: x.Service, Project: x.Project, Environment: x.Environment})
	}
	for _, x := range p.Unassigned {
		out.Unassigned = append(out.Unassigned, hzconfig.ImportUnassigned{Service: x.Service})
	}
	return out
}

// printImportPlan renders the plan so it can be read, pasted into a ticket and
// argued with WITHOUT a second tool. Every proposed row is followed by the
// evidence for it, indented under it, because a reason printed somewhere else is
// a reason nobody reads beside the thing it justifies.
//
// `from` names the file a plan was read from, and is empty for hz's own
// proposal. A file carries no evidence — the operator's authority for a row is
// that they wrote it — so the reason lines are simply absent rather than filled
// with thirty-three copies of "declared in tree.json", which would teach the
// reader to skip the column that matters on the OTHER path.
func printImportPlan(plan apitypes.ImportPlanResp, from string) {
	if from != "" {
		fmt.Printf("hz import — the project tree declared in %s, as hz reads it\n", from)
	} else {
		fmt.Println("hz import — a proposed project tree, built only from evidence already in this config")
	}

	// Every section prints, empty or not. A section that vanishes when it has
	// nothing in it leaves the reader to work out whether hz found none or never
	// looked, and those are different answers.
	fmt.Printf("\nPROJECTS (%d)\n", len(plan.Projects))
	if len(plan.Projects) == 0 {
		if from != "" {
			fmt.Printf("  Nothing. %s declares no project.\n", from)
		} else {
			fmt.Println("  Nothing. No domain suffix in this config groups two services together.")
		}
	}
	for _, p := range plan.Projects {
		if p.Parent != "" {
			fmt.Printf("  %-26s  parent: %s\n", p.Name, p.Parent)
		} else {
			fmt.Printf("  %s\n", p.Name)
		}
		printReason("", p.Reason)
	}

	fmt.Printf("\nENVIRONMENTS (%d)\n", len(plan.Environments))
	if len(plan.Environments) == 0 {
		if from != "" {
			fmt.Printf("  Nothing. %s declares no rung, so every assigned service lands on its\n", from)
			fmt.Println("  project and no environment. That is legal.")
		} else {
			fmt.Println("  Nothing. An environment belongs to a project, so a posture word on a")
			fmt.Println("  service with no project cannot become one.")
		}
	}
	for _, e := range plan.Environments {
		// Name and posture both, always. An environment named "prod" may
		// honestly sit at staging's posture, and a screen that shows one of
		// them has shown the wrong one.
		fmt.Printf("  %-26s  posture %s\n", e.Project+" / "+e.Name, e.Posture)
		printReason("", e.Reason)
	}

	fmt.Printf("\nASSIGNED (%d)\n", len(plan.Assignments))
	if len(plan.Assignments) == 0 {
		if from != "" {
			fmt.Printf("  Nothing. %s puts no service in a project.\n", from)
		} else {
			fmt.Println("  Nothing. No evidence in this config places a service in a project.")
		}
	}
	for _, a := range plan.Assignments {
		target := a.Project
		if a.Environment != "" {
			target += " / " + a.Environment
		}
		fmt.Printf("  %-26s -> %s\n", a.Service, target)
		printReason("project  ", a.ProjectReason)
		switch {
		case a.EnvironmentReason != "":
			printReason("rung     ", a.EnvironmentReason)
		case a.Environment == "" && from == "":
			fmt.Printf("      rung     none — no environment word in its name or hostnames\n")
		}
	}

	fmt.Printf("\nUNASSIGNED (%d) — left exactly as they are\n", len(plan.Unassigned))
	if len(plan.Unassigned) == 0 {
		fmt.Println("  None.")
	}
	for _, u := range plan.Unassigned {
		fmt.Printf("  %s\n", u.Service)
		printReason("", u.Reason)
		printReason("", u.Note)
	}
	if len(plan.Unassigned) > 0 {
		fmt.Println("  A service with no project is explicitly legal and keeps working unchanged.")
		if from != "" {
			fmt.Printf("  %s names each of these on purpose: a service it did not mention at all\n", from)
			fmt.Println("  would have been refused, because an omission and a decision look alike.")
		} else {
			fmt.Println("  It is listed here because a wrong project is worse than none: a service on")
			fmt.Println("  the wrong rung resolves the wrong config later, and nothing looks wrong now.")
		}
	}

	if len(plan.Signals) > 0 {
		fmt.Printf("\nSIGNALS EXAMINED AND NOT USED\n")
		for _, s := range plan.Signals {
			fmt.Printf("  %-14s %s\n", s.Name, s.Detail)
		}
	}

	total := len(plan.Assignments) + len(plan.Unassigned)
	fmt.Printf("\n%d service(s), %d assigned, %d unassigned.\n", total, len(plan.Assignments), len(plan.Unassigned))
}

// printReason prints an evidence line, and prints NOTHING when there is no
// evidence. A plan read from a file has none — the operator's authority for a
// row is that they wrote it — and an empty indented line under every row would
// read as a reason hz failed to produce.
func printReason(label, reason string) {
	if reason == "" {
		return
	}
	fmt.Printf("      %s%s\n", label, reason)
}

// --- feed set ---------------------------------------------------------------

const feedSetUsage = `usage: hz feed set <project> --url URL --suite SUITE --component COMP [--key-id ID] [--execute]

Declares the apt-style package repository a project's machines install from. A
descendant with no feed of its own inherits this one; a descendant that declares
its own replaces it WHOLE (see hz feed show).

Prints what it would set and writes NOTHING unless --execute is given.

  --url        registry base (required)
  --suite      suite, e.g. noble (required)
  --component  component, e.g. main (required)
  --key-id     signing key fingerprint. Optional and strongly advised: an
               unsigned repository can be rewritten by anyone on the path and
               the install that results looks entirely normal.
  --execute    actually write it.
`

func feedSet(c *client, args []string) error {
	fs := flag.NewFlagSet("feed set", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, feedSetUsage) }
	url := fs.String("url", "", "registry base (required)")
	suite := fs.String("suite", "", "suite, e.g. noble (required)")
	component := fs.String("component", "", "component, e.g. main (required)")
	keyID := fs.String("key-id", "", "signing key fingerprint")
	execute := fs.Bool("execute", false, "write the feed; without it this is a dry run")

	// The project is positional and the flags may precede it, exactly as the cm
	// commands accept theirs.
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", feedSetUsage)
	}
	missing := []string{}
	for _, f := range []struct {
		name  string
		value string
	}{{"--url", *url}, {"--suite", *suite}, {"--component", *component}} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s is required: hz carries these four strings to the agent and the agent writes a sources entry; a feed missing one of them cannot be written into it",
			strings.Join(missing, " and "))
	}

	// What the project installs from NOW, so the change is a before/after rather
	// than an assertion. A project already inheriting a feed is the case where
	// this command's effect is easiest to misread.
	list, err := fetchProjects(c)
	if err != nil {
		return err
	}
	before := findProject(list, pos)
	if before == nil {
		return fmt.Errorf("no project %q — `hz project ls` lists what exists, and `hz import` proposes a tree for a gateway that has none", pos)
	}

	fmt.Printf("feed set %s\n", pos)
	fmt.Printf("  now        %s\n", feedProvenance(before))
	if before.ResolvedFeed != nil {
		fmt.Printf("             %s %s/%s\n", before.ResolvedFeed.URL, before.ResolvedFeed.Suite, before.ResolvedFeed.Component)
	}
	fmt.Printf("  url        %s\n", *url)
	fmt.Printf("  suite      %s\n", *suite)
	fmt.Printf("  component  %s\n", *component)
	if *keyID != "" {
		fmt.Printf("  key        %s\n", *keyID)
	} else {
		fmt.Printf("  key        (none)\n")
		fmt.Println("             An unsigned repository can be rewritten by anyone on the path,")
		fmt.Println("             and the install that results looks entirely normal.")
	}
	if before.Feed != nil {
		fmt.Println("\nThis REPLACES the feed this project declares, whole — a feed wins whole when")
		fmt.Println("it resolves, so there is no field-level merge to half-apply.")
	}

	if !*execute {
		fmt.Println("\nDry run: nothing was written. Re-run with --execute to set it.")
		return nil
	}

	var resp apitypes.ProjectResp
	req := apitypes.FeedSetReq{Project: pos, URL: *url, Suite: *suite, Component: *component, KeyID: *keyID}
	if err := c.do(http.MethodPost, "/api/v1/projects/feed", req, &resp); err != nil {
		return err
	}
	fmt.Println()
	printFeed(&resp, "")
	return nil
}
