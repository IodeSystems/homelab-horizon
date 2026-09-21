// Command hz-agent is homelab-horizon's privileged on-box component: the
// thing that writes /etc and reloads services, so hz's web surface does not
// have to (plan/architecture.md, "hz-agent de-roots the hz web surface").
//
// It polls; hz never initiates. That is what lets a machine behind NAT be
// managed with no inbound credential and no ssh key on hz, and it makes the
// gateway machine #1 rather than a special case — the gateway's agent polls
// localhost exactly the way a remote agent polls hz.
//
// # It is INERT until somebody turns it on, deliberately
//
// hz still runs as root and still applies its own config. Item 12 is the flip;
// a window where hz and the agent both reconcile haproxy is how the gateway
// breaks. So, three independent things must all be true before this binary
// changes anything on a machine:
//
//  1. the unit must be enabled     install writes it and stops there
//  2. the unit must be started     install does not start it
//  3. --apply must be passed       the unit's ExecStart does not carry it,
//     and `run` without it only reports
//
// Installing this on the live gateway today therefore changes nothing: a unit
// file appears on disk and nothing runs. Even a `systemctl enable --now
// hz-agent` after that would only log a diff.
//
// The diff is the point in the meantime. `hz-agent diff` says what the agent
// WOULD change, which is the data behind the drift screen and the way item 12
// gets verified before it flips.
package main

import (
	"fmt"
	"os"
	"strings"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

const usage = `hz-agent - the privileged on-box half of homelab-horizon

USAGE
  hz-agent <command> [flags]

COMMANDS
  diff              Print what the agent would change. Changes nothing.
  run               Poll hz and reconcile. Reports only, unless --apply.
  enroll            Give this machine a credential of its own. Needs root.
  install           Enroll, then write the systemd unit. Does not enable it,
                    does not start it.
  show-systemd      Print the unit that install would write
  version           Print version

IT DOES NOTHING BY DEFAULT, ON PURPOSE
  hz is still root and still applies its own config. Until that changes
  (plan/architecture.md phase 4 item 12), two processes reconciling the same
  haproxy is a broken gateway. So:

    install    writes the unit and stops. No enable, no start.
    run        computes the diff and logs it. It applies nothing.
    run --apply  is the only thing that writes, and it needs root.

  Turning it on is three explicit steps, and each one is somebody's decision:
    sudo systemctl enable --now hz-agent      # still only reports
    <edit the unit to add --apply>            # now it applies

PRIVILEGE
  Computing and reporting a diff needs NO privilege — run 'diff' as anybody.
  Applying needs root, because it writes /etc and reloads services. Those are
  different privileges and keeping them apart is the point of this binary.

  A diff run unprivileged says which targets it could not read rather than
  reporting them as drifted.

CREDENTIAL
  The agent authenticates with a PER-MACHINE credential of its own, not an hz
  admin token — hz refuses an admin token presented here. 'enroll' mints one,
  writes it to --token-file (0600, root only) and records its HASH with hz;
  the secret is never printed and never reaches a command line. hz re-reads
  its record per request, so enrolling needs no hz restart.

  'install' enrolls for you. Re-running either is safe: an enrolment that
  already matches is left alone. 'enroll --rotate' replaces it.

  Today the agent mints locally because hz is on the same box and both halves
  are root. A remote machine will be issued its credential BY hz, through the
  Machine record (plan/architecture.md phase 4 item 13) — that changes who
  issues it, not what it is or how it is presented.

FLAGS
  --hz URL          hz base URL to poll (default http://127.0.0.1:8080)
  --token TOK       credential for the poll; prefer --token-file
  --token-file PATH file holding it (default /etc/hz-agent/token)
  --hz-credentials PATH
                    hz's enrolled-agent store, written by 'enroll'
                    (default /etc/homelab-horizon/config.json.agents)
  --rotate          'enroll' replaces an existing credential
  --from PATH       read the desired state from a local JSON file instead of
                    polling. For inspecting a payload offline.
  --machine NAME    refuse a payload addressed to another machine
                    (default: this host's name)
  --interval D      poll interval for 'run' (default 5s)
  --once            'run' does one pass and exits
  --apply           'run' may write. Without it, nothing is written. Needs root.
  --json            'diff' prints the plan as JSON
  --report          'run' tells hz what it found (default true)
  --report-interval D
                    how often to report when nothing has changed (default 60s)

REPORTING IS NOT APPLYING
  'run' reports what this machine looks like to hz, so hz can show desired
  minus observed. That writes NOTHING on this box: it is a POST, with the
  same credential the poll uses, and the agent stays as inert as it was.
  '--report=false' turns it off, at the cost of hz rendering this machine as
  silent.

  WHAT CROSSES IS THE PLAN, NEVER THE OBSERVED STATE. Observed holds this
  machine's raw file contents; the plan is the redacted summary — a secret
  file is described by its size, and every line is pattern-redacted again
  before it leaves. The live firewall rule set crosses as well, because after
  item 12 hz cannot read it itself, and a rule carries nothing to redact.

SECRETS
  Nothing here prints file contents that carry key material, and every line it
  does print is redacted a second time by pattern. A diff is safe to paste,
  and so is a report.
`

func main() {
	args := os.Args[1:]

	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd == "" {
		// No default command, unlike hz-probe. A binary whose bare invocation
		// started reconciling would be one keystroke from the thing this one
		// must not do yet.
		fmt.Print(usage)
		os.Exit(2)
	}

	var err error
	switch cmd {
	case "diff":
		err = runDiff(args)
	case "run":
		err = runAgent(args)
	case "enroll":
		err = runEnroll(args)
	case "install":
		err = runInstall(args)
	case "show-systemd":
		err = runShowSystemd(args)
	case "version", "--version":
		fmt.Printf("hz-agent %s (built %s)\n", Version, BuildTime)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "hz-agent: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "hz-agent:", err)
		os.Exit(1)
	}
}
