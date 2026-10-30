package main

import (
	"fmt"
	"io"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runDoctor is `hz doctor [--deep]`: the same findings the System tab's
// Doctor card shows (GET /api/v1/doctor). Exit status 1 when any is failing,
// so a provisioning script can gate on it.
func runDoctor(c *client, args []string) error {
	deep := false
	for _, a := range args {
		switch a {
		case "--deep", "-deep":
			deep = true
		default:
			return fmt.Errorf("usage: hz doctor [--deep]")
		}
	}
	path := "/api/v1/doctor"
	if deep {
		path += "?deep=1"
	}
	var resp apitypes.DoctorResp
	if err := c.do("GET", path, nil, &resp); err != nil {
		return err
	}
	if failing := printDoctor(os.Stdout, resp); failing > 0 {
		return fmt.Errorf("%d check(s) failing", failing)
	}
	return nil
}

// printDoctor writes the findings, problems first, and returns how many are
// failing.
func printDoctor(w io.Writer, resp apitypes.DoctorResp) int {
	mark := map[string]string{"failing": "✗", "unknown": "?", "info": "i", "ok": "✓"}
	counts := map[string]int{}
	for _, status := range []string{"failing", "unknown", "info", "ok"} {
		for _, f := range resp.Findings {
			if f.Status != status {
				continue
			}
			counts[status]++
			_, _ = fmt.Fprintf(w, "%s %-14s %s\n", mark[status], f.Check, f.Subject)
			_, _ = fmt.Fprintf(w, "    %s\n", f.Problem)
			if f.Fix != "" {
				_, _ = fmt.Fprintf(w, "    fix: %s\n", f.Fix)
			}
			if f.Evidence != "" && status != "ok" {
				_, _ = fmt.Fprintf(w, "    how: %s\n", f.Evidence)
			}
		}
	}
	_, _ = fmt.Fprintf(w, "\n%d failing, %d unknown, %d info, %d ok", counts["failing"], counts["unknown"], counts["info"], counts["ok"])
	if !resp.Deep {
		_, _ = fmt.Fprint(w, " — DNS provider and public DNS checks need --deep")
	}
	_, _ = fmt.Fprintln(w)
	return counts["failing"]
}
