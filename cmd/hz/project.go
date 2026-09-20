package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runProject groups what is already there. It adds no state of its own: every
// service already belonged to a project and an environment, spelled into its
// hostname. This reads the two fields that now say so.
func runProject(c *client, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "ls", "list":
		return projectList(c, args)
	case "show":
		return projectShow(c, args)
	default:
		return fmt.Errorf("unknown project subcommand: %s (want ls or show)", sub)
	}
}

type grouped struct {
	project string
	envs    map[string][]string // environment -> service names
}

// groupServices buckets services by project and environment. Services declaring
// neither land under "(unassigned)", which on an existing config is all of them —
// that is the point: the first run shows you the work, not an empty tree.
//
// declared seeds the buckets so a project that exists and holds nothing still
// appears. That is the state the root is in for the whole of step 1 — it is
// declared to carry the package feed, and it may never hold a service at all.
// Listing only what services mention would make it invisible the moment after
// you create it.
func groupServices(c *client, declared []string) ([]grouped, error) {
	list, err := fetchServices(c)
	if err != nil {
		return nil, err
	}
	byProject := map[string]map[string][]string{}
	for _, name := range declared {
		byProject[name] = map[string][]string{}
	}
	for _, s := range list {
		proj := s.Project
		if proj == "" {
			proj = "(unassigned)"
		}
		env := s.Environment
		if env == "" {
			env = "(none)"
		}
		if byProject[proj] == nil {
			byProject[proj] = map[string][]string{}
		}
		byProject[proj][env] = append(byProject[proj][env], s.Name)
	}
	out := make([]grouped, 0, len(byProject))
	for p, envs := range byProject {
		for _, names := range envs {
			sort.Strings(names)
		}
		out = append(out, grouped{project: p, envs: envs})
	}
	// Unassigned last: it is a worklist, not a project, and it should not head the report.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].project == "(unassigned)") != (out[j].project == "(unassigned)") {
			return out[j].project == "(unassigned)"
		}
		return out[i].project < out[j].project
	})
	return out, nil
}

// projectNames is the declared tree, by name. Kept separate from the grouping
// so a config with no projects at all still behaves exactly as before.
func projectNames(projects []apitypes.ProjectResp) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Name)
	}
	return out
}

func projectList(c *client, args []string) error {
	fs := flag.NewFlagSet("project ls", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	projects, err := fetchProjects(c)
	if err != nil {
		return err
	}
	groups, err := groupServices(c, projectNames(projects))
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		fmt.Println("No projects and no services.")
		return nil
	}
	for _, g := range groups {
		envs := make([]string, 0, len(g.envs))
		total := 0
		for e, names := range g.envs {
			envs = append(envs, e)
			total += len(names)
		}
		sort.Strings(envs)
		feed := ""
		if p := findProject(projects, g.project); p != nil && p.ResolvedFeed != nil {
			feed = "  feed: " + feedProvenance(p)
		}
		if total == 0 {
			fmt.Printf("%-24s no services yet%s\n", g.project, feed)
			continue
		}
		fmt.Printf("%-24s %d service(s) across %d environment(s): %s%s\n",
			g.project, total, len(g.envs), strings.Join(envs, ", "), feed)
	}
	fmt.Println("\nhz project show <name> lists the services and the feed it installs from.")
	return nil
}

func projectShow(c *client, args []string) error {
	fs := flag.NewFlagSet("project show", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hz project show <project>")
	}
	want := fs.Arg(0)
	projects, err := fetchProjects(c)
	if err != nil {
		return err
	}
	groups, err := groupServices(c, projectNames(projects))
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.project != want {
			continue
		}
		envs := make([]string, 0, len(g.envs))
		for e := range g.envs {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		fmt.Printf("%s\n", g.project)
		if p := findProject(projects, want); p != nil {
			if p.Parent != "" {
				fmt.Printf("  Parent: %s\n", p.Parent)
			}
			printFeed(p, "  ")
		}
		if len(envs) == 0 {
			fmt.Println("  No services yet.")
			return nil
		}
		for _, e := range envs {
			fmt.Printf("  %s\n", e)
			for _, n := range g.envs[e] {
				fmt.Printf("    %s\n", n)
			}
		}
		return nil
	}
	return fmt.Errorf("no project %q and no services in one — `hz project ls` lists what exists", want)
}
