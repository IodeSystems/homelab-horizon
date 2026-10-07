package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

const vpnUsage = `usage:
  hz vpn invite [--name N] [--profile P] [--user U] [--expires 24h]
      Mint a single-use VPN invite and print its link (shown once; hz keeps
      only its sha256). The invitee opens the link; their browser generates
      the WireGuard key, and hz never sees the private half.
        --name     the device name (default: the invitee chooses)
        --profile  vpn-only (default), lan-access or full-tunnel
        --user     the account the device is attributed to (must exist)
        --expires  how long the link lives: default 24h, at most 168h (7 days)
`

func runVPN(c *client, args []string) error {
	if len(args) == 0 || args[0] != "invite" {
		return fmt.Errorf("%s", vpnUsage)
	}
	req, err := parseVPNInvite(args[1:])
	if err != nil {
		return err
	}
	var out apitypes.CreateInviteResponse
	if err := c.do(http.MethodPost, "/api/v1/vpn/invites/create", req, &out); err != nil {
		return err
	}
	printInvite(os.Stdout, os.Stderr, out)
	return nil
}

// parseVPNInvite reads the flags into the create request. Shared shape with
// the console's `homelab-horizon vpn invite`.
func parseVPNInvite(args []string) (apitypes.CreateInviteReq, error) {
	fs := flag.NewFlagSet("vpn invite", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, vpnUsage) }
	name := fs.String("name", "", "device name")
	profile := fs.String("profile", "", "vpn-only, lan-access or full-tunnel")
	user := fs.String("user", "", "owning account")
	expires := fs.Duration("expires", 24*time.Hour, "link lifetime (max 168h)")
	pos, err := positional(fs, args)
	if err != nil {
		return apitypes.CreateInviteReq{}, err
	}
	if len(pos) != 0 {
		return apitypes.CreateInviteReq{}, fmt.Errorf("%s", vpnUsage)
	}
	if *expires <= 0 {
		return apitypes.CreateInviteReq{}, fmt.Errorf("--expires must be positive")
	}
	return apitypes.CreateInviteReq{Name: *name, Profile: *profile, User: *user,
		ExpiresSeconds: int64(expires.Seconds())}, nil
}

// printInvite puts the link alone on stdout (so it can be piped) and what it
// is on stderr.
func printInvite(out, errOut io.Writer, inv apitypes.CreateInviteResponse) {
	name := inv.Name
	if name == "" {
		name = "(the invitee names the device)"
	}
	owner := inv.User
	if owner == "" {
		owner = "(none)"
	}
	_, _ = fmt.Fprintf(errOut, "VPN invite %s — single use, shown once, expires %s.\n  device: %s\n  profile: %s\n  owner: %s\n",
		inv.ID, inv.ExpiresAt, name, inv.Profile, owner)
	_, _ = fmt.Fprintln(out, inv.URL)
}
