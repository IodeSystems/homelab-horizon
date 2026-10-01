package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// N4a: apply, hold and the nested hz's instance token
// (internal/server/handlers_api_apply.go).

const envApplyUsage = `usage: hz env apply <project> <env> --version V

Makes <env> RUN its newest promotion: GET /api/v1/deploys/desired answers the
newest apply, and a nested hz pulls that on its next poll. A promote alone
changes nothing a box pulls — a rung is held by default.

Refused unless V is the version of the NEWEST promotion into <env> (an apply
never picks a build — promote first, with --allow-downgrade for a rollback),
and unless that promotion's artifact is uploaded to hz. Re-applying the
applied promotion records nothing new.
`

const envHoldUsage = `usage: hz env hold <project> <env> --reason "why"
       hz env unhold <project> <env>

A hold is the emergency stop on top of an apply. GET /api/v1/deploys/desired
carries it ("hold": {by, reason, at}) and its ETag changes, so whoever acts on
desired sees it on the next poll and must not flip while it is set. --reason is
required: whoever lifts it needs to know why.
`

const machineHZTokenUsage = `usage: hz machine hz-token <name>

Mints the instance token a nested hz (a machine with an hz marker) uses to
pull from this one: desired state, the applied artifact, and forwarded deploy
reports — for rungs whose upstream is <name>, and nothing else. Shown ONCE;
hz keeps only its sha256. A re-mint replaces it and the old token stops
working. Put it in the child's upstream.token_file (0600).
`

// positional parses flags that may sit before, between or after the
// positionals, the way envPromote does.
func positional(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func envApply(c *client, args []string) error {
	fs := flag.NewFlagSet("env apply", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envApplyUsage) }
	version := fs.String("version", "", "the newest promoted version")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *version == "" {
		return fmt.Errorf("%s", envApplyUsage)
	}
	var out apitypes.ApplyResp
	req := apitypes.ApplyReq{Project: pos[0], Environment: pos[1], Version: *version}
	if err := c.do(http.MethodPost, "/api/v1/environments/apply", req, &out); err != nil {
		return err
	}
	if out.Existing {
		fmt.Printf("Already applied: %s/%s runs %s (apply #%d, promotion #%d) — nothing recorded\n",
			req.Project, req.Environment, out.Version, out.ID, out.PromotionID)
		return nil
	}
	fmt.Printf("Applied %s/%s: %s (apply #%d of promotion #%d)\n", req.Project, req.Environment, out.Version, out.ID, out.PromotionID)
	fmt.Printf("  artifact sha256 %s — a nested hz pulls it on its next poll\n", out.ArtifactSHA256)
	return nil
}

func envHold(c *client, args []string, on bool) error {
	name := "env unhold"
	if on {
		name = "env hold"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envHoldUsage) }
	reason := fs.String("reason", "", "why the rung is held (required for hold)")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || (on && *reason == "") {
		return fmt.Errorf("%s", envHoldUsage)
	}
	path := "/api/v1/environments/unhold"
	if on {
		path = "/api/v1/environments/hold"
	}
	var out apitypes.HoldStateResp
	if err := c.do(http.MethodPost, path, apitypes.HoldReq{Project: pos[0], Environment: pos[1], Reason: *reason}, &out); err != nil {
		return err
	}
	if out.Hold == nil {
		fmt.Printf("%s/%s is not held\n", out.Project, out.Environment)
		return nil
	}
	fmt.Printf("%s/%s is HELD by %s at %s: %s\n", out.Project, out.Environment, out.Hold.By, out.Hold.At, out.Hold.Reason)
	return nil
}

func machineHZToken(c *client, args []string) error {
	fs := flag.NewFlagSet("machine hz-token", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, machineHZTokenUsage) }
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%s", machineHZTokenUsage)
	}
	var out apitypes.InstanceTokenResp
	if err := c.do(http.MethodPost, "/api/v1/machines/hz-token", apitypes.InstanceTokenReq{Machine: pos[0]}, &out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Instance token for %s — shown once; hz keeps only its sha256. Put it in the child's upstream.token_file (0600):\n", out.Machine)
	fmt.Println(out.Token)
	return nil
}
