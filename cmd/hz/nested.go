package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz nested bootstrap` — the CLI half of the one-command nested-hz bootstrap
// (internal/server/handlers_nested_bootstrap.go).

const nestedBootstrapUsage = `usage: hz nested bootstrap <project> <instance> --rung <name> --from <rung>
                          --kiosk <hostname> | --human-vpn=false
                          [--human-vpn-range CIDR]
                          [--posture prod] [--platform linux-amd64|linux-arm64] [--url U]

The child gets its OWN human VPN by default: a wg0 on a range this hz
proposes (the next free 10.<n>.0.0/24 after its own; --human-vpn-range to
choose), its server key minted on the box, the endpoint <kiosk>:51820, and
the MFA kiosk at https://<kiosk>. --kiosk is added to the instance's domains
(this hz issues its certificate) and DNS grant (the child asks for its A
record). --human-vpn=false bootstraps without one (ssh tunnel only).

Declares <instance> as an hz instance (a nested hz — not a machine) owned by
<project>, declares the rung
<project>/<name> placed in it (promoted into from --from; posture prod by
default), and prints ONE command. Run it as root on a fresh Ubuntu box (amd64
or arm64): it generates the box's WireGuard key there, enrols with this hz,
joins its VPN, installs hz and connects it to this one.

The command is single use and expires in an hour. Running this again for the
same instance revokes an unused earlier command; an existing instance and
rung are reused (re-provisioning a wiped box rotates its VPN key and its
token). The command goes to stdout alone, so it can be piped.

<instance> may also be given as --instance N; --machine N is its pre-B3
spelling, accepted one release.
`

func runNested(c *client, args []string) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return fmt.Errorf("%s", nestedBootstrapUsage)
	}
	fs := flag.NewFlagSet("nested bootstrap", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, nestedBootstrapUsage) }
	rung := fs.String("rung", "", "the rung's name in the project (required)")
	from := fs.String("from", "", "the existing rung that promotes into it (required)")
	posture := fs.String("posture", "", "the rung's posture (default prod)")
	platform := fs.String("platform", "", "the box's platform, when known: refuse unless its binary is uploaded")
	hzURL := fs.String("url", "", "where the child's hz will answer (default: its VPN address, set at enrolment)")
	humanVPN := fs.Bool("human-vpn", true, "give the child its own human VPN and MFA kiosk (--human-vpn=false: ssh tunnel only)")
	kiosk := fs.String("kiosk", "", "the kiosk's hostname: its MFA portal and VPN endpoint (required with the human VPN)")
	humanRange := fs.String("human-vpn-range", "", "the child's wg0 range (default: proposed by the parent, the next free 10.<n>.0.0/24)")
	instFlag := fs.String("instance", "", "the hz instance's name (instead of the second positional)")
	machineFlag := fs.String("machine", "", "DEPRECATED spelling of --instance, one release")
	pos, err := positional(fs, args[1:])
	if err != nil {
		return err
	}
	if *machineFlag != "" {
		fmt.Fprintln(os.Stderr, "hz nested bootstrap --machine is deprecated and goes in the next release: use --instance (an hz instance is not a machine)")
	}
	names := map[string]bool{}
	for _, n := range []string{*instFlag, *machineFlag} {
		if n != "" {
			names[n] = true
		}
	}
	if len(pos) == 2 {
		names[pos[1]] = true
	}
	if len(pos) < 1 || len(pos) > 2 || len(names) != 1 || *rung == "" || *from == "" {
		return fmt.Errorf("%s", nestedBootstrapUsage)
	}
	instance := ""
	for n := range names {
		instance = n
	}
	req := apitypes.NestedBootstrapReq{Project: pos[0], Instance: instance, Rung: *rung, From: *from,
		Posture: *posture, Platform: *platform, URL: *hzURL, Kiosk: *kiosk, HumanVPNRange: *humanRange}
	if !*humanVPN {
		off := false
		req.HumanVPN = &off // nil is on: the parent's default
	}
	var out apitypes.NestedBootstrapResp
	if err := c.do(http.MethodPost, apitypes.NestedBootstrapPath, req, &out); err != nil {
		return err
	}
	if out.Warning != "" {
		fmt.Fprintln(os.Stderr, "WARNING: "+out.Warning)
	}
	if out.Revoked {
		fmt.Fprintln(os.Stderr, "The earlier unused command for "+out.Instance+" no longer works.")
	}
	for _, r := range out.Reused {
		fmt.Fprintf(os.Stderr, "Reused the existing %s.\n", r)
	}
	if h := out.HumanVPN; h != nil {
		how := "given"
		if h.Proposed {
			how = "proposed by this hz"
		}
		fmt.Fprintf(os.Stderr, "Human VPN: wg0 on %s (%s), the child at %s; kiosk %s, endpoint %s — open UDP %s inbound on the box.\n",
			h.Range, how, h.ServerAddress, h.KioskURL, h.Endpoint, endpointPort(h.Endpoint))
		if h.DomainsChanged {
			fmt.Fprintf(os.Stderr, "%s is now one of %s's domains and inside its DNS grant: this hz issues its certificate and publishes its A record when the child asks.\n", h.Kiosk, out.Instance)
		}
	} else {
		fmt.Fprintln(os.Stderr, "No human VPN: the child is reached by ssh tunnel only.")
	}
	fmt.Fprintf(os.Stderr, "Nested hz %s for %s — run this as root on the box. Single use, expires %s:\n",
		out.Instance, out.Rung, time.Unix(out.ExpiresAt, 0).Format(time.RFC3339))
	fmt.Println(out.Command)
	return nil
}

// endpointPort is the port of a host:port endpoint, or the whole string.
func endpointPort(ep string) string {
	if _, p, err := net.SplitHostPort(ep); err == nil {
		return p
	}
	return ep
}
