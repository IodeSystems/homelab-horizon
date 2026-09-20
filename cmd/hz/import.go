package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz import` proposes a project tree for a gateway that predates one.
//
// The interaction model is `hz cm promote`'s, deliberately and without variation:
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

const importUsage = `usage: hz import [--execute] [--merge]

Reads the services already in this config and proposes a project tree for them:
projects, environments, and which service belongs on which rung. Every proposal
names the evidence it came from. A service nothing explains is left UNASSIGNED,
which is legal and which is what it already is.

Prints the plan and writes NOTHING unless --execute is given.

  --merge     add to a config that already declares projects. Existing projects,
              rungs and service assignments are kept exactly as they are; only
              what is missing is added. Required to --execute over an existing
              tree — an import that silently reorganises one is the destructive
              case.
  --execute   actually write the plan.
`

func runImport(c *client, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, importUsage) }
	merge := fs.Bool("merge", false, "add to an existing tree instead of refusing")
	execute := fs.Bool("execute", false, "write the plan; without it this is a dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("hz import takes no arguments, got %q\n%s", fs.Arg(0), importUsage)
	}

	var plan apitypes.ImportPlanResp
	if err := c.do(http.MethodGet, "/api/v1/import", nil, &plan); err != nil {
		return err
	}
	printImportPlan(plan)

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
		fmt.Println("\nThere is nothing to import. That is an answer, not a failure: nothing in")
		fmt.Println("this config names a project, and every service goes on working unassigned.")
		return nil
	}

	if !*execute {
		fmt.Printf("\nDry run: nothing was written. Re-run with --execute to apply this plan.\n")
		fmt.Printf("Plan %s — an --execute is refused if the config changes before it runs.\n", plan.Fingerprint)
		return nil
	}

	var resp apitypes.ImportApplyResp
	req := apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint, Merge: *merge}
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

// printImportPlan renders the proposal so it can be read, pasted into a ticket
// and argued with WITHOUT a second tool. Every proposed row is followed by the
// evidence for it, indented under it, because a reason printed somewhere else is
// a reason nobody reads beside the thing it justifies.
func printImportPlan(plan apitypes.ImportPlanResp) {
	fmt.Println("hz import — a proposed project tree, built only from evidence already in this config")

	// Every section prints, empty or not. A section that vanishes when it has
	// nothing in it leaves the reader to work out whether hz found none or never
	// looked, and those are different answers.
	fmt.Printf("\nPROJECTS (%d)\n", len(plan.Projects))
	if len(plan.Projects) == 0 {
		fmt.Println("  Nothing. No domain suffix in this config groups two services together.")
	}
	for _, p := range plan.Projects {
		if p.Parent != "" {
			fmt.Printf("  %-26s  parent: %s\n", p.Name, p.Parent)
		} else {
			fmt.Printf("  %s\n", p.Name)
		}
		fmt.Printf("      %s\n", p.Reason)
	}

	fmt.Printf("\nENVIRONMENTS (%d)\n", len(plan.Environments))
	if len(plan.Environments) == 0 {
		fmt.Println("  Nothing. An environment belongs to a project, so a posture word on a")
		fmt.Println("  service with no project cannot become one.")
	}
	for _, e := range plan.Environments {
		// Name and posture both, always. An environment named "prod" may
		// honestly sit at staging's posture, and a screen that shows one of
		// them has shown the wrong one.
		fmt.Printf("  %-26s  posture %s\n", e.Project+" / "+e.Name, e.Posture)
		fmt.Printf("      %s\n", e.Reason)
	}

	fmt.Printf("\nASSIGNED (%d)\n", len(plan.Assignments))
	if len(plan.Assignments) == 0 {
		fmt.Println("  Nothing. No evidence in this config places a service in a project.")
	}
	for _, a := range plan.Assignments {
		target := a.Project
		if a.Environment != "" {
			target += " / " + a.Environment
		}
		fmt.Printf("  %-26s -> %s\n", a.Service, target)
		fmt.Printf("      project  %s\n", a.ProjectReason)
		switch {
		case a.EnvironmentReason != "":
			fmt.Printf("      rung     %s\n", a.EnvironmentReason)
		case a.Environment == "":
			fmt.Printf("      rung     none — no environment word in its name or hostnames\n")
		}
	}

	fmt.Printf("\nUNASSIGNED (%d) — left exactly as they are\n", len(plan.Unassigned))
	if len(plan.Unassigned) == 0 {
		fmt.Println("  None.")
	}
	for _, u := range plan.Unassigned {
		fmt.Printf("  %s\n", u.Service)
		fmt.Printf("      %s\n", u.Reason)
		if u.Note != "" {
			fmt.Printf("      %s\n", u.Note)
		}
	}
	if len(plan.Unassigned) > 0 {
		fmt.Println("  A service with no project is explicitly legal and keeps working unchanged.")
		fmt.Println("  It is listed here because a wrong project is worse than none: a service on")
		fmt.Println("  the wrong rung resolves the wrong config later, and nothing looks wrong now.")
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
