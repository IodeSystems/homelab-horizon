package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// runAgent is the daemon: poll, plan, report, and — only when explicitly told
// to — apply.
//
// THE DEFAULT IS REPORT-ONLY, and that is the property the whole item turns
// on. hz is still root and still applies its own config; two processes
// reconciling one haproxy is a broken gateway. So `run` without --apply
// computes the plan, logs it and does nothing, which makes it safe to enable
// on the live box while item 12 is still being prepared — and makes its log a
// continuous record of how far hz and the agent agree.
func runAgent(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Applying writes /etc and reloads services. Refuse up front rather than
	// discovering it halfway through a write.
	if f.apply && os.Geteuid() != 0 {
		return fmt.Errorf("--apply needs root; without it this command reports and changes nothing")
	}

	mode := "REPORT-ONLY — nothing will be written"
	if f.apply {
		mode = "APPLY — this process will write files and reload services"
	}
	slog.Info("hz-agent starting", "mode", mode, "interval", f.interval, "machine", f.machineName())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	src := f.source()
	obs := agent.NewSystemObserver()

	var etag string
	for {
		etag = onePass(ctx, &f, src, obs, etag)
		if f.once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(f.interval):
		}
	}
}

// onePass is one poll-plan-(apply) cycle. It returns the ETag to send next
// time; an error leaves it untouched, so a failed poll re-asks for the whole
// payload rather than silently keeping a generation it may not hold.
func onePass(ctx context.Context, f *agentFlags, src agent.Source, obs agent.Observer, etag string) string {
	d, newETag, changed, err := src.Fetch(ctx, etag)
	if err != nil {
		// Not fatal. hz restarting, a reload in progress and a network blip
		// all look like this, and an agent that exited on one would need a
		// human on a box that is probably remote.
		slog.Warn("poll failed", "err", err)
		return etag
	}
	if !changed {
		return newETag
	}
	if err := f.checkAddressed(d); err != nil {
		slog.Error("refusing the payload", "err", err)
		return etag
	}

	plan := agent.Compute(d, obs.Observe(d))

	if !plan.Changed() {
		slog.Info("in sync", "generation", short(plan.Generation), "unknown", len(plan.Unknown()))
		return newETag
	}

	if !f.apply {
		// The diff, at info level, every time it changes. This is the record
		// item 12 gets verified against: a gateway where this stays empty is
		// a gateway where flipping ownership is a no-op.
		slog.Info("changes NOT applied (report-only)",
			"generation", short(plan.Generation), "pending", len(plan.Pending()))
		fmt.Print(agent.Report(plan))
		return newETag
	}

	res, err := agent.Apply(d, plan, obs.Observe(d), agent.SystemReloader{})
	if err != nil {
		slog.Error("apply failed", "err", err, "errors", res.Errors)
		// Do NOT advance the ETag: the next pass must re-plan against what is
		// actually on disk now, which a partial apply has changed.
		return etag
	}
	slog.Info("applied", "generation", short(res.Generation),
		"wrote", res.Wrote, "reloaded", res.Reloaded)
	return newETag
}

// short trims a generation for a log line. The whole hash goes in the diff
// report; a log wants something a human can compare at a glance.
func short(gen string) string {
	if len(gen) > 12 {
		return gen[:12]
	}
	return gen
}
