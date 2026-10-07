package main

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `vpn invite`: root only, asks the running hz with the local admin token,
// prints the link alone on stdout. No terminal is needed: no key is involved.
func TestVPNInvitePrintsTheLinkAndNeedsRoot(t *testing.T) {
	asRoot(t, 0)
	asTerminal(t, false)
	c, f, _ := newVPNCLI(t)
	stdout, stderr, err := c.run("", "vpn", "invite", "--name", "carl-1", "--user", "carl", "--expires", "48h")
	if err != nil {
		t.Fatalf("vpn invite: %v\n%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != "https://kiosk.example.com/invite/tok" {
		t.Fatalf("stdout = %q, want the link only", stdout)
	}
	if f.invited != (apitypes.CreateInviteReq{Name: "carl-1", User: "carl", ExpiresSeconds: 48 * 3600}) {
		t.Fatalf("request = %+v", f.invited)
	}
	if len(f.auth) != 1 || f.auth[0] != "Bearer hzu_local-admin-token" {
		t.Fatalf("authorization: %v", f.auth)
	}

	asRoot(t, 1000)
	c2, f2, _ := newVPNCLI(t)
	if _, _, err := c2.run("", "vpn", "invite"); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("non-root: %v", err)
	}
	if len(f2.calls) != 0 {
		t.Fatalf("a non-root invite asked hz: %v", f2.calls)
	}
}
