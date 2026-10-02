package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The build registry's operator commands: builders (`hz builder`) and the
// build-number seed (`hz builds seed`). A builder's own calls — start, log,
// state, finish — are its build script's, with its builder token, not this
// CLI's.

const builderUsage = `usage:
  hz builder add <machine> --project P [--project Q]...
      Make a DECLARED machine a builder for these projects and print its token
      ONCE (hz keeps only its sha256). Re-running rotates the token — the old
      one stops working — and replaces the grant.
  hz builder rm <machine>
      Revoke the builder's token and grant. Its builds stay recorded; none of
      them is promotable while no builder of that machine is granted again.
  hz builder ls
      The builders and the projects each may build.
`

const buildsUsage = `usage:
  hz builds ls <project>
      The project's builds, newest first: number, version, commit, machine,
      result (running, passed, failed, or abandoned after 2h with no activity).
  hz builds seed <project> --next N
      Raise the project's next build number to N — for runs numbered before
      hz numbered them and reported nowhere. hz refuses to lower it.
`

func runBuilder(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", builderUsage)
	}
	switch args[0] {
	case "add":
		return builderAdd(c, args[1:])
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("%s", builderUsage)
		}
		var out apitypes.BuilderRemovedResp
		if err := c.do(http.MethodDelete, "/api/v1/builders/"+url.PathEscape(args[1]), nil, &out); err != nil {
			return err
		}
		fmt.Printf("Builder %s revoked: its token no longer authenticates\n", out.Machine)
		return nil
	case "ls":
		var out []apitypes.BuilderResp
		if err := c.do(http.MethodGet, "/api/v1/builders", nil, &out); err != nil {
			return err
		}
		if len(out) == 0 {
			fmt.Println("No builders. `hz builder add <machine> --project P` makes a declared machine one.")
			return nil
		}
		for _, b := range out {
			fmt.Printf("%-24s %s\n", b.Machine, strings.Join(b.Projects, ", "))
		}
		return nil
	default:
		return fmt.Errorf("unknown builder subcommand: %s\n%s", args[0], builderUsage)
	}
}

func builderAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("builder add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, builderUsage) }
	var projects repeatedFlag
	fs.Var(&projects, "project", "a project this builder may build (repeatable)")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || len(projects) == 0 {
		return fmt.Errorf("%s", builderUsage)
	}
	var out apitypes.BuilderTokenResp
	if err := c.do(http.MethodPost, "/api/v1/builders", apitypes.BuilderReq{Machine: pos[0], Projects: projects}, &out); err != nil {
		return err
	}
	what := "Builder token"
	if out.Rotated {
		what = "NEW builder token (the old one no longer works)"
	}
	fmt.Fprintf(os.Stderr, "%s for %s, granted %s — shown once; hz keeps only its sha256:\n", what, out.Machine, strings.Join(out.Projects, ", "))
	fmt.Println(out.Token)
	return nil
}

func runBuilds(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", buildsUsage)
	}
	switch args[0] {
	case "ls":
		if len(args) != 2 {
			return fmt.Errorf("%s", buildsUsage)
		}
		var out []apitypes.BuildResp
		if err := c.do(http.MethodGet, "/api/v1/builds?project="+url.QueryEscape(args[1]), nil, &out); err != nil {
			return err
		}
		if len(out) == 0 {
			fmt.Printf("No builds in %s.\n", args[1])
			return nil
		}
		for _, b := range out {
			fmt.Printf("#%-6d %-20s %s  %-20s %-9s %s\n", b.Number, b.Version, b.Commit[:min(12, len(b.Commit))], b.Machine, b.Result, b.ArtifactSHA256)
		}
		return nil
	case "seed":
		fs := flag.NewFlagSet("builds seed", flag.ContinueOnError)
		fs.Usage = func() { fmt.Fprint(os.Stderr, buildsUsage) }
		next := fs.Int64("next", 0, "the next build number")
		pos, err := positional(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *next < 1 {
			return fmt.Errorf("%s", buildsUsage)
		}
		var out apitypes.BuildSeedResp
		if err := c.do(http.MethodPost, "/api/v1/builds/seed", apitypes.BuildSeedReq{Project: pos[0], Next: *next}, &out); err != nil {
			return err
		}
		fmt.Printf("%s's next build number: %d (was %d)\n", out.Project, out.Next, out.Previous)
		return nil
	default:
		return fmt.Errorf("unknown builds subcommand: %s\n%s", args[0], buildsUsage)
	}
}
