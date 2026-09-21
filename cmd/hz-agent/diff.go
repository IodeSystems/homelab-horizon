package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// runDiff prints what the agent WOULD change, and changes nothing.
//
// No Geteuid check, deliberately: computing and reporting a diff is not a
// privileged operation and the binary must not pretend it is. Where a target
// genuinely cannot be read without root, the report says so for that target
// instead of refusing the whole command or guessing.
//
// This is also how item 12 gets verified before it flips: run it on the
// gateway, and a clean report is evidence that the agent would write exactly
// what hz already wrote.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	d, _, _, err := f.source().Fetch(context.Background(), "")
	if err != nil {
		return err
	}
	if d == nil {
		return errors.New("hz returned nothing to compare against")
	}
	if err := f.checkAddressed(d); err != nil {
		return err
	}

	plan := agent.Compute(d, agent.NewSystemObserver().Observe(d))

	if f.asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}

	fmt.Print(agent.Report(plan))
	if !plan.Changed() {
		return nil
	}
	fmt.Println()
	fmt.Println("Nothing above has been applied. hz still owns this box.")
	return nil
}
