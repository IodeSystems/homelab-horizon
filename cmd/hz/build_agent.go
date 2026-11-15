package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The build registry's operator commands: build agents (`hz build-agent`) and
// the build-number seed (`hz builds seed`). An agent's own calls — start, log,
// state, finish — are its build script's, with its agent token, not this
// CLI's.

const buildAgentUsage = `usage:
  hz build-agent add <name> --project P [--host H] [--note TEXT]
      Register a build agent for ONE project and print its token ONCE (hz
      keeps only its sha256). An agent is not a machine and not an hz
      instance: the name is its own, and --host is a free-text label of the
      box it runs on (several agents may share one). Re-running rotates the
      token — the old one stops working — and keeps host and note unless given.
  hz build-agent rm <name>
      Revoke the agent's token. Its builds stay recorded, and its passed builds
      stay promotable: the registration counts at build time.
  hz build-agent ls
      The build agents, by host: name, project, and whether it has a token.

  hz builder ...   DEPRECATED alias of hz build-agent, for one release.
`

const buildsUsage = `usage:
  hz builds ls <project>
      The project's builds, newest first: number, version, commit, agent,
      result (running, passed, failed, or abandoned after 2h with no activity).
  hz builds seed <project> --next N
      Raise the project's next build number to N — for runs numbered before
      hz numbered them and reported nowhere. hz refuses to lower it.
`

// runBuilder is the DEPRECATED `hz builder`: it says so and runs `hz build-agent`.
func runBuilder(c *client, args []string) error {
	fmt.Fprintln(os.Stderr, "hz builder is deprecated and goes in the next release: use hz build-agent")
	return runBuildAgent(c, args)
}

func runBuildAgent(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", buildAgentUsage)
	}
	switch args[0] {
	case "add":
		return buildAgentAdd(c, args[1:])
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("%s", buildAgentUsage)
		}
		var out apitypes.BuildAgentRemovedResp
		if err := c.do(http.MethodDelete, "/api/v1/build-agents/"+url.PathEscape(args[1]), nil, &out); err != nil {
			return err
		}
		fmt.Printf("Build agent %s revoked: its token no longer authenticates\n", out.Name)
		return nil
	case "ls":
		var out []apitypes.BuildAgentResp
		if err := c.do(http.MethodGet, "/api/v1/build-agents", nil, &out); err != nil {
			return err
		}
		if len(out) == 0 {
			fmt.Println("No build agents. `hz build-agent add <name> --project P` registers one.")
			return nil
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
		for _, a := range out {
			host := a.Host
			if host == "" {
				host = "(no host)"
			}
			token := ""
			if !a.HasToken {
				token = "  NO TOKEN — mint one with `hz build-agent add " + a.Name + " --project " + a.Project + "`"
			}
			fmt.Printf("%-20s %-24s %s%s\n", host, a.Name, a.Project, token)
		}
		return nil
	default:
		return fmt.Errorf("unknown build-agent subcommand: %s\n%s", args[0], buildAgentUsage)
	}
}

func buildAgentAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("build-agent add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, buildAgentUsage) }
	var projects repeatedFlag
	fs.Var(&projects, "project", "the one project this agent builds")
	host := fs.String("host", "", "a free-text label of the box the agent runs on")
	note := fs.String("note", "", "free text for the operator")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || len(projects) == 0 {
		return fmt.Errorf("%s", buildAgentUsage)
	}
	if len(projects) > 1 {
		return fmt.Errorf("a build agent builds ONE project, and this names %d (%s) — register one agent per project", len(projects), strings.Join(projects, ", "))
	}
	var out apitypes.BuildAgentTokenResp
	req := apitypes.BuildAgentReq{Name: pos[0], Project: projects[0], Host: *host, Note: *note}
	if err := c.do(http.MethodPost, "/api/v1/build-agents", req, &out); err != nil {
		return err
	}
	what := "Build agent token"
	if out.Rotated {
		what = "NEW build agent token (the old one no longer works)"
	}
	on := ""
	if out.Host != "" {
		on = " on host " + out.Host
	}
	fmt.Fprintf(os.Stderr, "%s for %s%s, project %s — shown once; hz keeps only its sha256:\n", what, out.Name, on, out.Project)
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
			agent := b.Agent
			if agent == "" {
				agent = b.Machine // a server from before `agent`
			}
			fmt.Printf("#%-6d %-20s %s  %-20s %-9s %s\n", b.Number, b.Version, b.Commit[:min(12, len(b.Commit))], agent, b.Result, b.ArtifactSHA256)
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
