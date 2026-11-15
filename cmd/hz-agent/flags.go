package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// agentFlags is the shared flag set. One struct so `run`, `diff`, `install`
// and `show-systemd` cannot disagree about a default — which matters most for
// the unit: show-systemd has to print the exact text install writes.
type agentFlags struct {
	hzURL string
	token string
	// tokenFile holds THIS machine's agent credential (internal/agent's
	// credential.go). Not an hz admin token — hz refuses one presented here.
	tokenFile string
	// adminTokenFile is where `enroll` reads the OPERATOR's hz credential
	// from, for the length of one request. hz is the issuer since item 13, so
	// enrolling is an admin act: this authorises the ask and is never stored
	// on this box, never reaches the unit, and the running agent never holds
	// it. It replaced --hz-credentials, which pointed at hz's store back when
	// the agent wrote its own record into it.
	//
	// Its default is hz's own admin token file, which exists only on the box
	// hz runs on — so the gateway enrols with no argument, and any other
	// machine has to be given a credential by somebody who has one.
	adminTokenFile string
	from           string
	// statePath is the applied-generation record: which sealed config this
	// agent last restarted each unit for (internal/agent/generations.go). The
	// one thing the agent remembers across a restart, and it has to be, or
	// every boot would re-adopt and the config-restart trigger would never
	// fire.
	statePath string
	machine   string
	interval  time.Duration
	once      bool
	apply     bool
	asJSON    bool

	// report turns the report-back off. On by default: hz cannot show drift
	// for a machine that does not speak, and a screen with no data is the
	// failure this channel exists to fix. The flag is here for an operator
	// who wants a purely read-only agent on a box for a while.
	report bool

	// reportEvery is the cadence of the report, independent of the poll.
	//
	// They are different jobs on different clocks. The poll is cheap — an
	// ETag and a 304 — so it runs at --interval to keep the apply latency
	// low. A report has to re-read the machine, which means iptables-save,
	// so it runs slower and would otherwise run only when hz changed
	// something: a steady fleet would report once and then look silent
	// forever.
	reportEvery time.Duration

	// carried is the daemon's loop state, deliberately not a flag.
	//
	// An unchanged poll answers 304 and returns no payload, so a heartbeat
	// report has nothing to plan against unless the last one is kept. It
	// lives here because onePass is a function of (flags, source, observer,
	// etag) and the four inertness tests in install_test.go pin that
	// signature — a fifth parameter would edit tests that exist to be
	// unedited.
	carried    *agent.Desired
	lastReport time.Time

	// hold is the apply backoff, and it lives here for the same reason
	// carried does: onePass's signature is pinned by the inertness tests, so
	// loop state that has to survive a pass goes on the flags rather than
	// into a parameter.
	hold applyHold

	// testReloader replaces the systemd reloader. Nil everywhere but in a
	// test — a backoff whose only proof was that the code reads correctly is
	// not proven, and SystemReloader was hardcoded at the call site until
	// this seam.
	testReloader agent.Reloader

	// testLinks replaces the interface lookup. Nil everywhere but in a test:
	// the boot tests need a box where an interface exists without root.
	testLinks func(names []string) map[string]agent.LinkState
}

const (
	defaultHZURL     = "http://127.0.0.1:8080"
	defaultTokenFile = "/etc/hz-agent/token"

	// defaultAdminTokenFile is hz's own admin token, beside hz's config. It is
	// present only on the machine hz runs on, which is exactly the bootstrap
	// the gateway needs and exactly the one a remote box must not have.
	defaultAdminTokenFile = "/etc/homelab-horizon/config.json.token"

	// adminTokenEnv carries the operator's hz credential when enrolling a box
	// hz does not run on. The environment rather than a flag, for the reason
	// the unit passes the agent credential by file: a credential in argv is a
	// credential in every `ps` on the machine.
	adminTokenEnv = "HZ_ADMIN_TOKEN"

	// defaultInterval is the poll cadence, and it is the whole latency cost of
	// moving the apply out of hz: a service change is rendered immediately and
	// applied within one interval. Seconds, because an operator who just
	// clicked something is watching; not sub-second, because every machine in
	// the fleet does this forever and a 304 is still a request.
	defaultInterval = 5 * time.Second

	// defaultReportEvery is how often the agent tells hz what it found when
	// nothing has changed. A minute, matching hz's own 60s iptables
	// reconcile: it is the cadence that read already ran at, and hz's
	// staleness threshold is derived from whatever the agent declares here
	// rather than assumed.
	defaultReportEvery = 60 * time.Second
)

func (f *agentFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.hzURL, "hz", defaultHZURL, "hz base URL to poll")
	fs.StringVar(&f.token, "token", "", "credential for the poll (prefer --token-file)")
	fs.StringVar(&f.tokenFile, "token-file", defaultTokenFile, "file holding the credential")
	fs.StringVar(&f.adminTokenFile, "admin-token-file", defaultAdminTokenFile, "file holding an hz ADMIN credential, read once to authorise enrolment (enroll/install only)")
	fs.StringVar(&f.from, "from", "", "read the desired state from a local JSON file instead of polling")
	fs.StringVar(&f.statePath, "state", agent.DefaultGenerationsPath, "record of which sealed config each unit was last restarted for. Read always, written only when applying")
	fs.StringVar(&f.machine, "machine", "", "refuse a payload addressed to another machine (default: this host)")
	fs.DurationVar(&f.interval, "interval", defaultInterval, "poll interval")
	fs.BoolVar(&f.once, "once", false, "one pass, then exit")
	fs.BoolVar(&f.apply, "apply", false, "allow writing. Without it nothing is applied. Needs root")
	fs.BoolVar(&f.asJSON, "json", false, "print the plan as JSON")
	fs.BoolVar(&f.report, "report", true, "tell hz what this machine looks like. Reports the PLAN, never file contents")
	fs.DurationVar(&f.reportEvery, "report-interval", defaultReportEvery, "how often to report when nothing has changed")
}

// generations is the applied-generation record, or nil when the operator
// asked for none.
//
// Nil is not a soft failure: an agent with nowhere to write what it applied
// restarts nothing at all, because a restart it could not record would be
// performed again on the next pass and every pass after it. The report says so
// rather than the agent quietly never firing.
func (f *agentFlags) generations() agent.GenerationStore {
	if strings.TrimSpace(f.statePath) == "" {
		return nil
	}
	return agent.FileGenerationStore{Path: f.statePath}
}

// tunnelStore is the segment tunnels' last-known-good (internal/agent's
// tunnel_state.go), or nil when --state is empty.
//
// DERIVED FROM --state, beside the generations record, for the reason
// segmentKeys is derived from the credential path: both are the agent's own
// state in the agent's own 0700 directory, and a second flag would be a
// second place to get wrong.
func (f *agentFlags) tunnelStore() agent.TunnelStore {
	if strings.TrimSpace(f.statePath) == "" {
		return nil
	}
	return agent.FileTunnelStore{Path: filepath.Join(filepath.Dir(f.statePath), agent.TunnelStateFile)}
}

// segmentKeys is where this box keeps its per-segment WireGuard private keys.
//
// DERIVED FROM THE CREDENTIAL PATH rather than given its own flag, and that is
// the point: these files live under the same root-only directory as the agent
// credential, which is the directory whose permissions are already asserted and
// already reasoned about. A separate --keys-dir would be a second place to get
// wrong, and an operator who moved one and not the other would have a box whose
// secrets are half in a directory nobody audits.
func (f *agentFlags) segmentKeys() agent.SegmentKeyStore {
	return agent.SegmentKeyStore{Dir: filepath.Join(filepath.Dir(f.tokenFile), "keys")}
}

// observer reads the machine, including this agent's own record of what it
// last applied.
func (f *agentFlags) observer() *agent.SystemObserver {
	o := agent.NewSystemObserver().WithGenerations(f.generations()).WithSegmentKeys(f.segmentKeys()).
		WithTunnelRecord(f.tunnelStore())
	if f.testLinks != nil {
		o = o.WithLinks(f.testLinks)
	}
	return o
}

// reloader is the privileged half that reloads services and restarts units.
func (f *agentFlags) reloader() agent.Reloader {
	if f.testReloader != nil {
		return f.testReloader
	}
	return agent.SystemReloader{SegmentKeys: f.segmentKeys()}
}

// reporter is where this agent tells hz what it found, or nil.
//
// Only the HTTP source has an hz to report to. `--from` reads a payload off
// disk for an offline diff and has nobody to tell; a FileSource that quietly
// grew a network call would be the opposite of what --from is for.
func (f *agentFlags) reporter(src agent.Source) agent.StateReporter {
	if !f.report {
		return nil
	}
	rep, ok := src.(agent.StateReporter)
	if !ok {
		return nil
	}
	return rep
}

// reportDue reports whether the heartbeat is owed at now.
func (f *agentFlags) reportDue(now time.Time) bool {
	every := f.reportEvery
	if every <= 0 {
		every = defaultReportEvery
	}
	return f.lastReport.IsZero() || now.Sub(f.lastReport) >= every
}

// source builds where the desired state comes from.
func (f *agentFlags) source() agent.Source {
	if f.from != "" {
		return agent.FileSource{Path: f.from}
	}
	return &agent.HTTPSource{BaseURL: f.hzURL, Token: f.resolveToken()}
}

// resolveToken reads the credential, preferring the file so it stays off the
// process list.
func (f *agentFlags) resolveToken() string {
	if b, err := os.ReadFile(f.tokenFile); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok
		}
	}
	if tok := strings.TrimSpace(os.Getenv("HZ_AGENT_TOKEN")); tok != "" {
		return tok
	}
	return strings.TrimSpace(f.token)
}

// resolveAdminToken is the operator credential that authorises an enrolment.
//
// File first, environment second, and NO FLAG AT ALL — the same discipline the
// agent's own credential gets. A credential on a command line is in `ps`, in
// the shell history and in the systemd journal if anything ever logs the
// invocation; there is no version of enrolment that is worth that.
//
// Empty is an error rather than an attempt, because hz would answer 401 and the
// operator would debug the credential they never supplied.
func (f *agentFlags) resolveAdminToken() (string, error) {
	if b, err := os.ReadFile(f.adminTokenFile); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	if tok := strings.TrimSpace(os.Getenv(adminTokenEnv)); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("no hz admin credential to enrol with. hz issues the agent credential now, so enrolling is an admin act:\n"+
		"  on the box hz runs on, %s holds one and root can read it\n"+
		"  anywhere else, pass it in the environment: %s=<token> hz-agent enroll --hz <url>\n"+
		"It is used for this one request, never stored here, and never reaches the unit",
		f.adminTokenFile, adminTokenEnv)
}

// machineName is who this agent believes it is.
func (f *agentFlags) machineName() string {
	if f.machine != "" {
		return f.machine
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// checkAddressed refuses a payload computed for a different machine.
//
// With one machine this is theatre. With two it is the thing that stops a
// copied token applying the wrong box's network config, and it costs one
// comparison.
func (f *agentFlags) checkAddressed(d *agent.Desired) error {
	want := f.machineName()
	if want == "" || d.Machine == "" || d.Machine == want {
		return nil
	}
	return fmt.Errorf("hz sent a payload for %q and this machine is %q — refusing it", d.Machine, want)
}
