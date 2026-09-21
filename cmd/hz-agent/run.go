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
	reporting := "OFF — hz cannot show drift for this machine"
	if f.report {
		reporting = "on, every " + f.reportInterval().String()
	}
	slog.Info("hz-agent starting", "mode", mode, "interval", f.interval,
		"machine", f.machineName(), "reporting", reporting)

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
		// The payload is unchanged, but hz's record of this machine goes
		// stale if the agent stops speaking — and "silent" is not a state a
		// healthy box should sit in. So the heartbeat re-observes and reports
		// on its own, slower clock.
		f.heartbeat(ctx, src, obs)
		return newETag
	}
	if err := f.checkAddressed(d); err != nil {
		slog.Error("refusing the payload", "err", err)
		return etag
	}
	f.carried = d

	observed := obs.Observe(d)
	plan := agent.Compute(d, observed)

	// Report BEFORE applying, always. What the drift screen is for is
	// "desired minus observed, per machine, BEFORE anything is applied"
	// (plan/architecture.md); a report taken after a write would show hz the
	// result rather than the disagreement.
	f.sendReport(ctx, f.reporter(src), d, plan, observed)

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

// heartbeat re-observes the machine and reports, when one is due.
//
// It plans against the CARRIED payload: an unchanged poll answers 304 with no
// body, so there is nothing to compare against unless the last one is kept.
// Nothing happens before the first successful poll, which is right — an agent
// that has never heard from hz has no desired state to report drift against,
// and hz already renders it as silent.
func (f *agentFlags) heartbeat(ctx context.Context, src agent.Source, obs agent.Observer) {
	rep := f.reporter(src)
	if rep == nil || f.carried == nil || !f.reportDue(time.Now()) {
		return
	}
	observed := obs.Observe(f.carried)
	f.sendReport(ctx, rep, f.carried, agent.Compute(f.carried, observed), observed)
}

// sendReport tells hz what this machine looks like.
//
// WHAT CROSSES IS THE PLAN, NEVER THE OBSERVED. Observed holds the machine's
// raw file contents — wg0.conf's private key among them — and no amount of
// care makes shipping that safe. The plan is the redacted product: a secret
// file is already collapsed to its size by File.Secret, and every line is put
// through pattern redaction again by NewStateReport. The live firewall rules
// cross raw because they carry nothing to redact and hz classifies them with
// its own classifier.
//
// A failed report is a warning, not a fault. hz restarting and a network blip
// both look like this, and the machine's own reconcile does not depend on hz
// having heard: the next heartbeat carries the same facts.
func (f *agentFlags) sendReport(
	ctx context.Context,
	rep agent.StateReporter,
	d *agent.Desired,
	p agent.Plan,
	observed agent.Observed,
) {
	if rep == nil {
		return
	}
	r := agent.NewStateReport(d, p, observed)
	r.AgentVersion = Version
	r.Applying = f.apply
	r.IntervalSeconds = int(f.reportInterval() / time.Second)

	if err := rep.ReportState(ctx, r); err != nil {
		slog.Warn("reporting to hz failed", "err", err)
		return
	}
	f.lastReport = time.Now()
	slog.Debug("reported", "generation", short(r.Generation),
		"pending", r.Pending(), "unknown", r.Unknown())
}

// reportInterval is the cadence this agent declares to hz, which hz derives
// its staleness threshold from.
func (f *agentFlags) reportInterval() time.Duration {
	if f.reportEvery > 0 {
		return f.reportEvery
	}
	return defaultReportEvery
}

// short trims a generation for a log line. The whole hash goes in the diff
// report; a log wants something a human can compare at a glance.
func short(gen string) string {
	if len(gen) > 12 {
		return gen[:12]
	}
	return gen
}
