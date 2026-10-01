package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

const envPromoteUsage = `usage: hz env promote <project> <from> <to> --version X [--allow-downgrade]

Moves version X up one promotion edge: sets <to>'s declared version to X and
records who promoted which artifact, when.

Refused unless ALL hold, each with its own reason:
  - <to> declares ` + "`from: <from>`" + `, in the same project, at a higher posture;
  - <from>'s NEWEST deploy report is X (POST /api/v1/deploys/report) — the
    evidence that X is what ran there;
  - X is not lower than <to>'s declared version, unless --allow-downgrade;
  - for EVERY line <to> supports now (` + "`hz lines <project>`" + `), a kept backup
    exists and X's newest restore test on <from> against it passed. A target
    with no supported line (the first release) requires none, and says so.

The artifact sha256 <from> reported for X is pinned on the promotion, and
GET /api/v1/deploys/check refuses any other bundle of X for <to>.

  --version X         a semver tag, no leading v ("1.4.0", "1.0.0-rc.1.1414")
  --allow-downgrade   a rollback: X may be lower; recorded as a downgrade
  --skip-restore-tests  skip the restore-test gate — only for the exact build
                      <to> already ran (a rollback); recorded on the promotion
`

// envPromote is `hz env promote`. Flags may sit before, between or after the
// three positionals, so the flag set is re-parsed after each one.
func envPromote(c *client, args []string) error {
	fs := flag.NewFlagSet("env promote", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, envPromoteUsage) }
	version := fs.String("version", "", "the version to promote")
	allowDowngrade := fs.Bool("allow-downgrade", false, "allow a version lower than the target's")
	skipRestore := fs.Bool("skip-restore-tests", false, "skip the restore-test gate for a build the target already ran")

	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 3 || *version == "" {
		return fmt.Errorf("%s", envPromoteUsage)
	}

	req := apitypes.PromoteReq{
		Project: pos[0], From: pos[1], To: pos[2],
		Version: *version, AllowDowngrade: *allowDowngrade, SkipRestoreTests: *skipRestore,
	}
	var out apitypes.PromoteResp
	if err := c.do(http.MethodPost, "/api/v1/environments/promote", req, &out); err != nil {
		return err
	}
	kind := "Promoted"
	if out.Downgrade {
		kind = "Rolled back (recorded as a downgrade)"
	}
	fmt.Printf("%s %s/%s -> %s: %s\n", kind, req.Project, req.From, req.To, out.Version)
	fmt.Printf("  artifact sha256 %s (pinned; the deploy check refuses any other build of %s)\n", out.ArtifactSHA256, out.Version)
	if out.BuildURL != "" {
		fmt.Printf("  build %s\n", out.BuildURL)
	}
	fmt.Printf("  restore tests: %s\n", out.RestoreTests)
	return nil
}
