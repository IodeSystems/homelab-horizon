package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Declaring the model from the command line.
//
// `hz project ls/show` and `hz env ls/show` read a tree that, until this landed,
// only `hz import --execute` or an editor on config.json could create. Step 2 of
// the acceptance walkthrough in plan/architecture.md — "redline declares its own
// environments: staging, prod" — had no command.
//
// DRY RUN, PER COMMAND, on purpose rather than for consistency.
//
//	add / set   write immediately. A declared project holds nothing and changes
//	            no rendered artifact — no DNS record, no HAProxy backend, no apt
//	            source — and `hz project rm` takes it straight back out. Making
//	            it two invocations would turn a two-command walkthrough into
//	            four and teach the operator that --execute is a formality, which
//	            is exactly what it must not be on `feed set` and `import`.
//	feed set    dry run by default (already). It changes what every machine in a
//	            subtree installs from, silently, later, on a box that is not in
//	            front of you.
//	import      dry run by default (already). It is wide: it moves services.
//	rm          dry run by default. It is the one irreversible command here —
//	            a removed project takes its feed with it, and `hz project add`
//	            cannot put a feed back. So `hz project rm x` always PRINTS what
//	            it would take and writes nothing; --confirm writes.
//
// REFUSING BEATS CASCADING. A project with children, rungs or services, or a
// rung with services on it, is refused with every dependant named. The
// alternative — removing it anyway — leaves a config Save rejects, and the
// operator meets that as a validation error about a record they never mentioned.
// --cascade exists, is opt-in, and lists everything it will take first.

const projectAddUsage = `usage: hz project add <name> [--parent <project>]

Declares a project. A project groups services, and later machines and config;
it is also where a package feed is declared, which every descendant without one
of its own inherits.

Writes immediately — a declared project holds nothing and changes no DNS record,
no HAProxy backend and no apt source until something names it.

  --parent P   declare it under an existing project. Omit to make it a root.
`

func projectAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, projectAddUsage) }
	parent := fs.String("parent", "", "declare it under an existing project")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", projectAddUsage)
	}

	var out apitypes.ProjectResp
	req := apitypes.ProjectAddReq{Name: name, Parent: *parent}
	if err := c.do(http.MethodPost, "/api/v1/projects/add", req, &out); err != nil {
		return err
	}
	fmt.Printf("Declared project %s", out.Name)
	if out.Parent != "" {
		fmt.Printf(" under %s", out.Parent)
	}
	fmt.Println(".")
	printFeed(&out, "  ")
	fmt.Println("\nIt holds no services yet. `hz env add " + out.Name +
		"/<name> --posture <dev|staging|prod>` declares a rung under it.")
	return nil
}

const projectRmUsage = `usage: hz project rm <name> [--cascade] [--confirm]

Removes a project. Prints what that would take and writes NOTHING unless
--confirm is given: a project takes its package feed with it, and 'project add'
cannot put a feed back.

Refuses while anything depends on it — a child project, a rung, or a service
assigned to it — and names every one. Removing it anyway would leave a config
that cannot be saved, and the operator would meet that later as a validation
error about a record they never touched.

  --cascade   take the descendant projects and their rungs too, and leave the
              services on them UNASSIGNED (legal, and they keep working). What
              it will take is listed before it does.
  --confirm   actually remove it.
`

func projectRm(c *client, args []string) error {
	fs := flag.NewFlagSet("project rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, projectRmUsage) }
	cascade := fs.Bool("cascade", false, "take descendants, their rungs, and unassign their services")
	confirm := fs.Bool("confirm", false, "actually remove it; without this it is a dry run")

	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("%s", projectRmUsage)
	}

	var out apitypes.RemovalResp
	req := apitypes.ProjectRmReq{Name: name, Cascade: *cascade, Confirm: *confirm}
	if err := c.do(http.MethodPost, "/api/v1/projects/rm", req, &out); err != nil {
		return err
	}
	return renderRemoval("project", name, *cascade, *confirm, out)
}

const envAddUsage = `usage: hz env add <project>/<name> --posture <dev|staging|prod> [--from <env>] [--version <v>]

Declares a rung in a project. The project must exist first — Save refuses a
service naming a rung nobody declared, so the order is declare, then assign.

Writes immediately: a declared rung holds nothing until a service names it.

  --posture P  dev, staging or prod. Required, and closed: the three ARE the
               ordering (dev < staging < prod), and a value outside them ranks
               below dev against every rung, which makes every promotion into
               it look upward. Name and posture stay separate on purpose — an
               environment called "prod" may honestly sit at staging's posture.
  --from E     the rung in this project that promotes INTO this one. It is the
               whole authority model: a rung with no --from is one nobody has
               said may be promoted into, from anywhere.
  --version V  the declared desired version. Opaque — a Debian version, an image
               tag, a git sha. hz shows the drift against what an instance
               reports; it does not close it.
`

func envAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("env add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envAddUsage) }
	posture := fs.String("posture", "", "dev, staging or prod (required)")
	from := fs.String("from", "", "the rung in this project that promotes into this one")
	version := fs.String("version", "", "declared desired version")

	addr, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if addr == "" {
		addr = fs.Arg(0)
	}
	project, name, err := splitEnvAddr(addr, "add")
	if err != nil {
		return err
	}
	if strings.TrimSpace(*posture) == "" {
		return fmt.Errorf("--posture is required: dev, staging or prod. A rung with no posture cannot be compared against any other, so every promotion into it would read as upward")
	}

	var out apitypes.EnvironmentResp
	req := apitypes.EnvironmentAddReq{
		Project: project, Name: name, Posture: *posture, From: *from, Version: *version,
	}
	if err := c.do(http.MethodPost, "/api/v1/environments/add", req, &out); err != nil {
		return err
	}
	fmt.Printf("Declared %s/%s.\n", out.Project, out.Name)
	printEnvironment(out, "  ")
	return nil
}

const envSetUsage = `usage: hz env set <project>/<name> [--posture <p>] [--from <env>] [--version <v>]

Changes a rung that already exists. Only the flags you pass are touched — the
others keep the value they have, so bumping a version cannot drop a promotion
edge by omission.

Writes immediately. Pass an EMPTY value to clear an optional field:
  --from ""      remove the promotion edge into this rung
  --version ""   remove the declared version

  --posture P  dev, staging or prod. Cannot be cleared: every rung has one.
  --from E     a rung in the SAME project (promotion is within one project).
  --version V  the declared desired version.
`

func envSet(c *client, args []string) error {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envSetUsage) }
	posture := fs.String("posture", "", "dev, staging or prod")
	from := fs.String("from", "", "a rung in the same project; empty clears the edge")
	version := fs.String("version", "", "declared desired version; empty clears it")

	addr, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if addr == "" {
		addr = fs.Arg(0)
	}
	project, name, err := splitEnvAddr(addr, "set")
	if err != nil {
		return err
	}

	// fs.Visit reports only the flags actually on the command line, which is the
	// whole patch contract: absent means "leave it alone" and `--from ""` means
	// "clear it". A zero-value check could not tell those apart, and would make
	// clearing a promotion edge impossible to express.
	req := apitypes.EnvironmentSetReq{Project: project, Name: name}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "posture":
			req.Posture = posture
		case "from":
			req.From = from
		case "version":
			req.Version = version
		}
	})
	if req.Posture == nil && req.From == nil && req.Version == nil {
		return fmt.Errorf("nothing to set on %s/%s — pass at least one of --posture, --from or --version\n%s",
			project, name, envSetUsage)
	}

	var out apitypes.EnvironmentResp
	if err := c.do(http.MethodPost, "/api/v1/environments/set", req, &out); err != nil {
		return err
	}
	fmt.Printf("Set %s/%s.\n", out.Project, out.Name)
	printEnvironment(out, "  ")
	return nil
}

const envRmUsage = `usage: hz env rm <project>/<name> [--cascade] [--confirm]

Removes a rung. Prints what that would take and writes NOTHING unless --confirm
is given.

Refuses while a service is on it, or while another rung promotes from it, and
names every one. Removing it anyway would leave a config that cannot be saved,
or would cut a promotion edge nobody mentioned.

  --cascade   take the services off this rung (they keep their project, and keep
              working) and cut the promotion edges into it. What it will take is
              listed before it does.
  --confirm   actually remove it.
`

func envRm(c *client, args []string) error {
	fs := flag.NewFlagSet("env rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envRmUsage) }
	cascade := fs.Bool("cascade", false, "take the services off this rung and cut the edges into it")
	confirm := fs.Bool("confirm", false, "actually remove it; without this it is a dry run")

	addr, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if addr == "" {
		addr = fs.Arg(0)
	}
	project, name, err := splitEnvAddr(addr, "rm")
	if err != nil {
		return err
	}

	var out apitypes.RemovalResp
	req := apitypes.EnvironmentRmReq{Project: project, Name: name, Cascade: *cascade, Confirm: *confirm}
	if err := c.do(http.MethodPost, "/api/v1/environments/rm", req, &out); err != nil {
		return err
	}
	return renderRemoval("environment", project+"/"+name, *cascade, *confirm, out)
}

// splitEnvAddr parses the <project>/<name> address every env write takes.
//
// Both halves are required and always will be: an environment name is unique
// per project, not globally, because every project gets to have a "prod". A
// bare name is not an identity.
func splitEnvAddr(addr, verb string) (project, name string, err error) {
	project, name, ok := strings.Cut(addr, "/")
	if !ok || project == "" || name == "" {
		return "", "", fmt.Errorf("usage: hz env %s <project>/<name> — an environment name is unique per project, not globally, so both halves are needed", verb)
	}
	return project, name, nil
}

// printEnvironment renders one rung the way `hz env show` does, so a write and a
// read of the same record look the same.
func printEnvironment(e apitypes.EnvironmentResp, prefix string) {
	fmt.Printf("%sposture          %s\n", prefix, dash(e.Posture))
	fmt.Printf("%spromotes from    %s\n", prefix, dash(e.From))
	fmt.Printf("%sdeclared version %s\n", prefix, dash(e.Version))
	if e.From == "" {
		fmt.Printf("%sNo promotion edge into this rung — nothing may be promoted into it from\n", prefix)
		fmt.Printf("%sanywhere until one is declared (`hz env set %s/%s --from <rung>`).\n", prefix, e.Project, e.Name)
	}
}

// renderRemoval prints the one answer both removals give: what is in the way, or
// what would go, or what went. The server computes all three — a client that
// re-derived the dependants from the read endpoints would be a second reader of
// the same facts, free to disagree with the one that writes them.
func renderRemoval(kind, name string, cascade, confirm bool, out apitypes.RemovalResp) error {
	if len(out.Blocked) > 0 {
		fmt.Printf("REFUSED: %s %s has %d dependant(s).\n\n", kind, name, len(out.Blocked))
		printDependants(out.Blocked)
		fmt.Println("\nRemoving it would leave a config hz cannot save, and you would meet that")
		fmt.Println("later as a validation error about a record you never touched. Remove or move")
		fmt.Printf("each of the above first, or re-run with --cascade to take them with it.\n")
		return fmt.Errorf("%s %s still has %d dependant(s)", kind, name, len(out.Blocked))
	}

	if !confirm {
		fmt.Printf("hz %s rm %s would remove:\n\n", kindCommand(kind), name)
		printDependants(out.Removes)
		if out.Feed != nil {
			fmt.Printf("\nAnd the package feed %s declares, which goes with it:\n", name)
			fmt.Printf("  %s %s/%s\n", out.Feed.URL, out.Feed.Suite, out.Feed.Component)
			fmt.Println("  `hz project add` cannot put a feed back — copy it now if you want it.")
		}
		fmt.Println("\nDry run: nothing was written. Re-run with --confirm to remove it.")
		return nil
	}

	if !out.OK {
		// Belt and braces: a confirmed run that wrote nothing and named no
		// blocker would otherwise report success.
		return fmt.Errorf("the server did not remove %s %s and gave no reason — nothing was written", kind, name)
	}
	fmt.Printf("Removed %s %s.\n\n", kind, name)
	printDependants(out.Removes)
	if cascade && len(out.Removes) > 1 {
		fmt.Println("\n--cascade took everything above. A service left unassigned is legal and")
		fmt.Println("keeps working exactly as it did; `hz project ls` shows it under (unassigned).")
	}
	return nil
}

// kindCommand maps the record kind to the command that removes it, so the dry
// run echoes something the operator can retype.
func kindCommand(kind string) string {
	if kind == "environment" {
		return "env"
	}
	return kind
}

func printDependants(list []apitypes.DependantResp) {
	if len(list) == 0 {
		fmt.Println("  (nothing)")
		return
	}
	for _, d := range list {
		fmt.Printf("  %-12s %s\n", d.Kind, d.Name)
		fmt.Printf("      %s\n", d.How)
	}
}
