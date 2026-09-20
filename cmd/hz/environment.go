package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runEnvironment reads the rungs. Like `hz project`, it adds no state of its own: it
// joins the declared environments to the services that name them, and shows the rows
// that have no rung so the work is visible rather than absent.
func runEnvironment(c *client, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "ls", "list":
		return environmentList(c, args)
	case "show":
		return environmentShow(c, args)
	default:
		return fmt.Errorf("unknown env subcommand: %s (want ls or show)", sub)
	}
}

func fetchEnvironments(c *client) ([]apitypes.EnvironmentResp, error) {
	var out []apitypes.EnvironmentResp
	if err := c.do("GET", "/api/v1/environments", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// envRow is one line of the report: a declared environment, or the "(none)" bucket for
// services in a project that name no rung. Declared is what separates the two — an
// undeclared bucket has no posture to print and is not a thing you can promote.
type envRow struct {
	name      string
	posture   string
	from      string
	version   string
	declared  bool
	services  []string
	projectID string
}

type envGroup struct {
	project string
	rows    []envRow
}

// groupEnvironments joins declared environments to the services naming them. Two kinds
// of row end up without a declaration and both are legal: a service in a project that
// names no environment, and a service that names no project at all. They print as
// "(none)" under their project and under "(unassigned)" respectively, which is the same
// answer `hz project ls` gives and the same reason — the first run should show you the
// work.
func groupEnvironments(c *client) ([]envGroup, error) {
	envs, err := fetchEnvironments(c)
	if err != nil {
		return nil, err
	}
	services, err := fetchServices(c)
	if err != nil {
		return nil, err
	}

	byProject := map[string]map[string]*envRow{}
	row := func(project, name string) *envRow {
		if byProject[project] == nil {
			byProject[project] = map[string]*envRow{}
		}
		if byProject[project][name] == nil {
			byProject[project][name] = &envRow{name: name, projectID: project}
		}
		return byProject[project][name]
	}

	for _, e := range envs {
		r := row(e.Project, e.Name)
		r.posture, r.from, r.version, r.declared = e.Posture, e.From, e.Version, true
	}
	for _, s := range services {
		project, env := s.Project, s.Environment
		if project == "" {
			project = "(unassigned)"
			// A project-less service's environment string resolves against nothing —
			// ValidateEnvironments deliberately does not check it — so it is not a rung
			// and must not be printed as one.
			env = ""
		}
		if env == "" {
			env = "(none)"
		}
		r := row(project, env)
		r.services = append(r.services, s.Name)
	}

	out := make([]envGroup, 0, len(byProject))
	for project, rows := range byProject {
		g := envGroup{project: project}
		for _, r := range rows {
			sort.Strings(r.services)
			g.rows = append(g.rows, *r)
		}
		// Undeclared last within a project, for the same reason unassigned sorts last
		// between projects: a bucket is a worklist, not a rung.
		sort.Slice(g.rows, func(i, j int) bool {
			if g.rows[i].declared != g.rows[j].declared {
				return g.rows[i].declared
			}
			return g.rows[i].name < g.rows[j].name
		})
		out = append(out, g)
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

// dash renders an empty optional field as an em dash. A blank column reads as "the tool
// failed to fetch it"; a dash reads as "there is none", which is what it means.
func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func environmentList(c *client, args []string) error {
	fs := flag.NewFlagSet("env ls", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	groups, err := groupEnvironments(c)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		fmt.Println("No environments and no services.")
		return nil
	}
	for _, g := range groups {
		fmt.Printf("%s\n", g.project)
		fmt.Printf("  %-20s  %-9s  %-14s  %-12s  %s\n", "ENVIRONMENT", "POSTURE", "FROM", "VERSION", "SERVICES")
		for _, r := range g.rows {
			fmt.Printf("  %-20s  %-9s  %-14s  %-12s  %d\n",
				r.name, dash(r.posture), dash(r.from), dash(r.version), len(r.services))
		}
		fmt.Println()
	}
	fmt.Println("hz env show <project>/<name> lists the services in one environment.")
	return nil
}

func environmentShow(c *client, args []string) error {
	fs := flag.NewFlagSet("env show", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hz env show <project>/<name>")
	}
	project, name, ok := strings.Cut(fs.Arg(0), "/")
	if !ok || project == "" || name == "" {
		return fmt.Errorf("usage: hz env show <project>/<name> — an environment name is unique per project, so both halves are needed")
	}
	groups, err := groupEnvironments(c)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.project != project {
			continue
		}
		for _, r := range g.rows {
			if r.name != name {
				continue
			}
			fmt.Printf("%s/%s\n", g.project, r.name)
			if !r.declared {
				fmt.Println("  not a declared environment — these services name no rung")
			}
			fmt.Printf("  posture          %s\n", dash(r.posture))
			fmt.Printf("  promotes from    %s\n", dash(r.from))
			fmt.Printf("  declared version %s\n", dash(r.version))
			if len(r.services) == 0 {
				fmt.Println("  services         none yet")
				return nil
			}
			fmt.Printf("  services (%d)\n", len(r.services))
			for _, n := range r.services {
				fmt.Printf("    %s\n", n)
			}
			return nil
		}
	}
	return fmt.Errorf("no environment %q in project %q — `hz env ls` lists what exists", name, project)
}
