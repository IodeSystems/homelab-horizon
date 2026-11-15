package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

const linesUsage = `usage: hz lines <project>

Release lines, per rung. A version's line is its MAJOR.MINOR.PATCH (1.9.0-1.2 is
on line 1.9.0). A rung SUPPORTS the line it last REPORTED RUNNING (current), the
newest different line it reported running before that (prior), and any line
the project pins. Not its declared version: a line exists on a rung once its
deploy reported running it. hz derives these on every read; nothing stores
them.

A promote into a rung is refused unless EVERY line it supports has a kept
backup and the promoted version passed a restore test against that backup.
Retired lines still have a kept backup but are supported nowhere — the app may
delete them. hz deletes nothing.
`

const lineUsage = `usage: hz line pin <project> <line> --reason "why"
       hz line unpin <project> <line>

A pin keeps a line supported on every rung of the project beyond current and
prior — so every promotion must restore-test it. --reason is REQUIRED.
`

// runLines is `hz lines <project>`.
func runLines(c *client, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("%s", linesUsage)
	}
	var out apitypes.ProjectLinesResp
	if err := c.do(http.MethodGet, "/api/v1/projects/lines?"+url.Values{"project": {args[0]}}.Encode(), nil, &out); err != nil {
		return err
	}
	printLines(out)
	return nil
}

func ago(seconds int64) string {
	return since(time.Now().Add(-time.Duration(seconds) * time.Second))
}

func printLines(out apitypes.ProjectLinesResp) {
	if len(out.Rungs) == 0 {
		fmt.Printf("%s declares no environment — no rung, so no supported line.\n", out.Project)
	}
	for _, r := range out.Rungs {
		declared := "declares nothing"
		if r.Declared != "" {
			declared = "declares " + r.Declared
		}
		from := ""
		if r.From != "" {
			from = ", from " + r.From
		}
		fmt.Printf("%s/%s (%s%s) %s\n", out.Project, r.Environment, r.Posture, from, declared)
		for _, g := range r.Gaps {
			fmt.Printf("  UNKNOWN: %s\n", g)
		}
		if r.NoneRequired != "" {
			fmt.Printf("  %s\n", r.NoneRequired)
		}
		for _, sl := range r.Supported {
			why := make([]string, 0, len(sl.Why))
			for _, w := range sl.Why {
				why = append(why, w.Kind+" ("+w.Detail+")")
			}
			fmt.Printf("  line %s — %s\n", sl.Line, strings.Join(why, "; "))
			if b := sl.KeptBackup; b != nil {
				fmt.Printf("    kept backup  %s at %s, taken by %s, %s\n", b.BackupSHA256[:12], b.Location, b.TakenByVersion, ago(b.AgeSeconds))
			} else {
				fmt.Printf("    kept backup  none\n")
			}
			fmt.Printf("    restore      %s: %s\n", sl.Restore.Status, sl.Restore.Sentence)
		}
	}
	if len(out.Pins) > 0 {
		fmt.Println("pins:")
		for _, p := range out.Pins {
			fmt.Printf("  %s — %s\n", p.Line, p.Reason)
		}
	}
	switch {
	case out.RetiredUnknown != "":
		fmt.Printf("retired: UNKNOWN — %s\n", out.RetiredUnknown)
	case len(out.Retired) == 0:
		fmt.Println("retired: none — every kept backup is of a supported line")
	default:
		fmt.Println("retired (a kept backup, supported on no rung — the app may delete it; hz deletes nothing):")
		for _, b := range out.Retired {
			fmt.Printf("  %s  %s at %s, %s\n", b.Line, b.BackupSHA256[:12], b.Location, ago(b.AgeSeconds))
		}
	}
}

// runLine is `hz line pin|unpin`. Flags may sit anywhere among the positionals.
func runLine(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", lineUsage)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("line "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, lineUsage) }
	reason := fs.String("reason", "", "why this line stays supported (required for pin)")
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
	if len(pos) != 2 {
		return fmt.Errorf("%s", lineUsage)
	}
	req := apitypes.LinePinReq{Project: pos[0], Line: pos[1]}
	var path string
	switch sub {
	case "pin":
		if strings.TrimSpace(*reason) == "" {
			return fmt.Errorf("a pin needs --reason: every supported line needs a kept backup and a restore test, so say why this one stays\n%s", lineUsage)
		}
		req.Reason = *reason
		path = "/api/v1/projects/lines/pin"
	case "unpin":
		path = "/api/v1/projects/lines/unpin"
	default:
		return fmt.Errorf("unknown line subcommand: %s (want pin or unpin)\n%s", sub, lineUsage)
	}
	var pins []apitypes.PinnedLineResp
	if err := c.do(http.MethodPost, path, req, &pins); err != nil {
		return err
	}
	if sub == "pin" {
		fmt.Printf("Pinned %s on %s: %s\n", req.Line, req.Project, req.Reason)
	} else {
		fmt.Printf("Unpinned %s on %s\n", req.Line, req.Project)
	}
	if len(pins) == 0 {
		fmt.Printf("  %s now pins no line\n", req.Project)
	}
	for _, p := range pins {
		fmt.Printf("  pinned %s — %s\n", p.Line, p.Reason)
	}
	return nil
}
