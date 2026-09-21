package main

import (
	"flag"
	"fmt"
	"os"
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
	// hzCredentials is hz's enrolled set, which `enroll` writes this
	// machine's hash into. Only meaningful when hz is on this box, which is
	// the only arrangement that exists today; item 13 makes hz the issuer and
	// this flag goes away with the local mint.
	hzCredentials string
	from          string
	machine       string
	interval      time.Duration
	once          bool
	apply         bool
	asJSON        bool
}

const (
	defaultHZURL     = "http://127.0.0.1:8080"
	defaultTokenFile = "/etc/hz-agent/token"

	// defaultInterval is the poll cadence, and it is the whole latency cost of
	// moving the apply out of hz: a service change is rendered immediately and
	// applied within one interval. Seconds, because an operator who just
	// clicked something is watching; not sub-second, because every machine in
	// the fleet does this forever and a 304 is still a request.
	defaultInterval = 5 * time.Second
)

func (f *agentFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.hzURL, "hz", defaultHZURL, "hz base URL to poll")
	fs.StringVar(&f.token, "token", "", "credential for the poll (prefer --token-file)")
	fs.StringVar(&f.tokenFile, "token-file", defaultTokenFile, "file holding the credential")
	fs.StringVar(&f.hzCredentials, "hz-credentials", agent.DefaultCredentialsPath, "hz's enrolled-agent store (enroll/install only)")
	fs.StringVar(&f.from, "from", "", "read the desired state from a local JSON file instead of polling")
	fs.StringVar(&f.machine, "machine", "", "refuse a payload addressed to another machine (default: this host)")
	fs.DurationVar(&f.interval, "interval", defaultInterval, "poll interval")
	fs.BoolVar(&f.once, "once", false, "one pass, then exit")
	fs.BoolVar(&f.apply, "apply", false, "allow writing. Without it nothing is applied. Needs root")
	fs.BoolVar(&f.asJSON, "json", false, "print the plan as JSON")
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
