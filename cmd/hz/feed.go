package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runFeed reads the package feed a project's machines install from. It is a
// read-only view: the feed is declared in the config, and what this command
// exists to answer is not "what does it say" but "which project said it".
func runFeed(c *client, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "show":
		return feedShow(c, args)
	case "", "ls", "list":
		return feedList(c, args)
	default:
		return fmt.Errorf("unknown feed subcommand: %s (want ls or show)", sub)
	}
}

func fetchProjects(c *client) ([]apitypes.ProjectResp, error) {
	var out []apitypes.ProjectResp
	if err := c.do("GET", "/api/v1/projects", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func findProject(list []apitypes.ProjectResp, name string) *apitypes.ProjectResp {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// feedProvenance is the line this command exists for: a feed with no source
// beside it is a value you cannot act on, because changing it means editing
// the project that declared it and that may not be this one.
func feedProvenance(p *apitypes.ProjectResp) string {
	switch {
	case p.ResolvedFeed == nil:
		return "none"
	case p.FeedFrom == p.Name:
		return "declared here"
	default:
		return "inherited from " + p.FeedFrom
	}
}

func feedShow(c *client, args []string) error {
	fs := flag.NewFlagSet("feed show", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hz feed show <project>")
	}
	want := fs.Arg(0)
	list, err := fetchProjects(c)
	if err != nil {
		return err
	}
	p := findProject(list, want)
	if p == nil {
		return fmt.Errorf("no project %q — `hz project ls` lists what exists", want)
	}
	printFeed(p, "")
	return nil
}

// printFeed writes the resolved feed and its provenance, indented by prefix so
// `hz project show` can nest it under the project name.
func printFeed(p *apitypes.ProjectResp, prefix string) {
	if p.ResolvedFeed == nil {
		fmt.Printf("%sFeed: none\n", prefix)
		fmt.Printf("%s  No project from %s up to the root declares one. That is legal —\n", prefix, p.Name)
		fmt.Printf("%s  machines here install from nowhere hz knows about. Declare a feed on\n", prefix)
		fmt.Printf("%s  this project or an ancestor to change that.\n", prefix)
		return
	}
	f := p.ResolvedFeed
	fmt.Printf("%sFeed: %s\n", prefix, feedProvenance(p))
	fmt.Printf("%s  url        %s\n", prefix, f.URL)
	fmt.Printf("%s  suite      %s\n", prefix, f.Suite)
	fmt.Printf("%s  component  %s\n", prefix, f.Component)
	if f.KeyID != "" {
		fmt.Printf("%s  key        %s\n", prefix, f.KeyID)
		return
	}
	fmt.Printf("%s  key        (none)\n", prefix)
	fmt.Printf("%s  An unsigned repository can be rewritten by anyone on the path, and\n", prefix)
	fmt.Printf("%s  the install that results looks entirely normal. Set key_id on the\n", prefix)
	fmt.Printf("%s  feed declared by %s.\n", prefix, p.FeedFrom)
}

func feedList(c *client, args []string) error {
	fs := flag.NewFlagSet("feed ls", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	list, err := fetchProjects(c)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No projects. A feed is declared on a project; there are none yet.")
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	fmt.Printf("%-24s  %-26s  %s\n", "PROJECT", "SOURCE", "FEED")
	for i := range list {
		p := &list[i]
		feed := "-"
		if p.ResolvedFeed != nil {
			feed = fmt.Sprintf("%s %s/%s", p.ResolvedFeed.URL, p.ResolvedFeed.Suite, p.ResolvedFeed.Component)
			if p.ResolvedFeed.KeyID == "" {
				feed += "  [unsigned]"
			}
		}
		fmt.Printf("%-24s  %-26s  %s\n", p.Name, feedProvenance(p), feed)
	}
	fmt.Println("\nhz feed show <project> explains one of them.")
	return nil
}
