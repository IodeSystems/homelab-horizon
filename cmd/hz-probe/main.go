// Command hz-probe is homelab-horizon's outside-in vantage point. It runs on
// a host outside the homelab — a cheap VPS is the usual choice — probes the
// public names hz serves, and hands its results to hz when hz asks for them.
//
// The direction is the design. hz-probe never dials hz: it has no hz address
// and no hz credential, only a token hz must present. So hz needs no inbound
// reachability, no port forward and no stable public address, and the only
// host that has to be accessible is this one. Nothing about the private
// network crosses the wire — hz sends the hostnames it already publishes and
// the public IP those names are supposed to resolve to.
//
// It probes on its own schedule and buffers what it saw, so the poll after an
// hz outage returns the outage rather than a gap in the history.
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

const usage = `hz-probe - outside-in vantage agent for homelab-horizon

USAGE
  hz-probe [command] [flags]

  With no command, hz-probe serves. Every other command is setup you run once.

COMMANDS
  serve             Probe on a timer and answer hz's polls (default)
  install           Write and enable the systemd unit, minting what it needs
  show-systemd      Print the systemd unit that install would write
  gen-cert          Write a self-signed certificate and key for --tls-cert/--tls-key
  fingerprint       Print the SHA-256 of a certificate, for hz's pin_sha256
  version           Print version

FIRST RUN, on the outside host
  hz-probe gen-cert --host 198.51.100.7        # skip if you have a real cert
  sudo hz-probe install --listen :8443
  sudo hz-probe fingerprint                    # paste into hz's pin_sha256

SERVE FLAGS
  --listen ADDR         address to listen on (default :8443)
  --vantage NAME        name for this vantage point (default: hostname)
  --token-file PATH     file holding the shared token (default /etc/hz-probe/token)
  --token TOK           the token inline; prefer --token-file or HZ_PROBE_TOKEN
  --state PATH          target cache, so a restart keeps probing while hz is down
  --tls-cert PATH       TLS certificate (default /etc/hz-probe/cert.pem)
  --tls-key PATH        TLS key (default /etc/hz-probe/key.pem)

INSTALL FLAGS
  the serve flags, plus:
  --dry-run             print what install would do, change nothing
  --no-start            write and enable the unit, but do not start it

GEN-CERT FLAGS
  --host IP-OR-NAME     what hz will connect to; repeatable (default: this host)
  --days N              validity in days (default 3650)
  --force               overwrite an existing certificate

THE TOKEN
  Resolved in order: --token-file, HZ_PROBE_TOKEN, --token. A file and the
  environment both keep it off the process list. 'install' mints one if the
  file does not exist.

SERVE TLS
  The token crosses the public internet on every poll. With a domain, use a
  real certificate. On a bare IP, use 'gen-cert' and pin the result in hz.
`

func main() {
	args := os.Args[1:]

	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	// The two flags that predate the subcommands, and which people type by
	// habit on any binary.
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			fmt.Print(usage)
			return
		case "--version":
			cmd = "version"
		}
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "install":
		err = runInstall(args)
	case "show-systemd":
		err = runShowSystemd(args)
	case "gen-cert":
		err = runGenCert(args)
	case "fingerprint":
		err = runFingerprint(args)
	case "version":
		fmt.Printf("hz-probe %s (built %s)\n", Version, BuildTime)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "hz-probe: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "hz-probe:", err)
		os.Exit(1)
	}
}
