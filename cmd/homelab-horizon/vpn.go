package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/qr"
)

// `homelab-horizon vpn add` — the FIRST human VPN client of an hz nobody can
// reach yet, from the console (N4g).
//
// A nested hz bootstrapped with a human VPN has no client: its UI answers on
// loopback and on its wg0 address, and nothing reaches either from outside.
// This is the one-time way in. After it, clients are made in the hz's own UI,
// over its VPN, as on any hz.
//
// ONE WRITER. It asks the RUNNING hz, over loopback, to add the client
// (POST /api/v1/vpn/peers/add — the UI's own request, so the same wg0.conf
// edit, reload and chain rebuild), with the personal API token `user create`
// wrote (admin.token). Writing wg0.conf and config.json from this process
// would race the hz that holds them in memory and saves them.
//
// THE CLIENT'S PRIVATE KEY IS A TERMINAL-ONLY SECRET, like `hz config recovery
// keygen`'s: hz mints it, returns it once in the answer, keeps only the public
// half. This prints it — the config and a QR code — to stdout, and refuses
// before it asks hz for anything when stdout is not a terminal: a redirect is
// how a key lands in a file nobody set 0600 on or in a log. Nothing here logs
// it; stderr carries only what is not secret.
//
// ATTRIBUTED. The client is recorded as --user's device (the peer-owner row the
// UI writes for a signed-in user), and --user must be an account on this hz.

// stdoutIsTerminal reports whether stdout is a terminal; tests replace it.
var stdoutIsTerminal = func() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// renderQR draws the client config as a terminal QR code; tests replace it.
var renderQR = qr.Terminal

func newVPNCmd(opts *serveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vpn",
		Short: "Human VPN clients, from the console",
	}
	cmd.AddCommand(newVPNInviteCmd(opts))
	cmd.AddCommand(newVPNAddCmd(opts))
	return cmd
}

type vpnInviteArgs struct {
	req       apitypes.CreateInviteReq
	expires   time.Duration
	tokenFile string
}

// `homelab-horizon vpn invite` — the recommended first client (N4i). It asks
// the running hz over loopback for an invite and prints the LINK; the person
// opens it on the device, whose browser generates the WireGuard key. No
// private key is printed, made or held here, so unlike `vpn add` this needs no
// terminal.
func newVPNInviteCmd(opts *serveOpts) *cobra.Command {
	var a vpnInviteArgs
	cmd := &cobra.Command{
		Use:   "invite [--name N] [--profile P] [--user U] [--expires 24h]",
		Short: "Mint a single-use VPN invite link (console, root only)",
		Long: "Asks the hz running on this box, over loopback, for a single-use VPN invite\n" +
			"and prints its link (on a nested hz: https://<kiosk>/invite/<token>). Open it\n" +
			"on the device: the browser generates the WireGuard key and keeps the private\n" +
			"half; hz receives only the public key. The link is shown once — hz keeps only\n" +
			"its sha256 — and works once.\n\n" +
			"The way in to an hz nobody can reach yet (a nested hz's first client). The\n" +
			"kiosk must be reachable from where the device is: TCP 443 to the box.",
		Example: "  homelab-horizon vpn invite --name carl-1 --user carl",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVPNInvite(cmd.OutOrStdout(), cmd.ErrOrStderr(), opts.configPath, a)
		},
	}
	cmd.Flags().StringVar(&a.req.Name, "name", "", "the device name (default: the invitee chooses)")
	cmd.Flags().StringVar(&a.req.Profile, "profile", "", "the client's profile: "+config.ProfileVPNOnly+" (default), "+config.ProfileLanAccess+" or "+config.ProfileFullTunnel)
	cmd.Flags().StringVar(&a.req.User, "user", "", "the account the device is attributed to (must exist here)")
	cmd.Flags().DurationVar(&a.expires, "expires", 24*time.Hour, "how long the link lives (at most 168h)")
	cmd.Flags().StringVar(&a.tokenFile, "token-file", "", "file holding a personal API token (default: admin.token beside the config)")
	return cmd
}

func runVPNInvite(out, errOut io.Writer, configPath string, a vpnInviteArgs) error {
	if err := requireRoot("vpn invite"); err != nil {
		return err
	}
	if a.expires <= 0 {
		return fmt.Errorf("--expires must be positive")
	}
	a.req.ExpiresSeconds = int64(a.expires.Seconds())
	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	tokenFile := a.tokenFile
	if tokenFile == "" {
		tokenFile = filepath.Join(filepath.Dir(cfgPath), "admin.token")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return fmt.Errorf("read the API token from %s: %w (the bootstrap writes it; or: homelab-horizon user create <name> --admin --token-file %s)", tokenFile, err, tokenFile)
	}
	base, err := loopbackBase(cfg.EffectiveListenAddr())
	if err != nil {
		return err
	}
	var inv apitypes.CreateInviteResponse
	c := &http.Client{Timeout: 30 * time.Second}
	if err := consolePost(c, base, strings.TrimSpace(string(raw)), tokenFile, "/api/v1/vpn/invites/create", a.req, &inv); err != nil {
		return err
	}
	name, owner := inv.Name, inv.User
	if name == "" {
		name = "(the invitee names the device)"
	}
	if owner == "" {
		owner = "(none)"
	}
	say(errOut, "VPN invite %s: single use, shown once, expires %s. Device %s, profile %s, owner %s.\n",
		inv.ID, inv.ExpiresAt, name, inv.Profile, owner)
	if !strings.HasPrefix(inv.URL, "https://") {
		say(errOut, "WARNING: this hz has no kiosk_url, so the link is its loopback address and works only on this box.\n")
	}
	_, _ = fmt.Fprintln(out, inv.URL)
	return nil
}

type vpnAddArgs struct {
	name, profile, user, project, tokenFile string
	vpnAdmin                                bool
}

func newVPNAddCmd(opts *serveOpts) *cobra.Command {
	var a vpnAddArgs
	cmd := &cobra.Command{
		Use:   "add <name> --profile <profile> --user <account>",
		Short: "Create a VPN client and print its config and QR code (console, root only, terminal only)",
		Long: "Creates a human VPN client on the hz running on this box, over loopback, and\n" +
			"prints its WireGuard config and a QR code to this terminal ONCE. The client's\n" +
			"private key is in that config; hz keeps only the public half, so this is the\n" +
			"only copy. Refused when stdout is not a terminal.\n\n" +
			"The way in to an hz nobody can reach yet (a nested hz's first client). After\n" +
			"that, create clients from the hz's UI over its VPN.\n\n" +
			"--user names the account the device is attributed to; it must exist here\n" +
			"(homelab-horizon user create). --vpn-admin makes the client a VPN admin: its\n" +
			"address signs it in to this hz's UI (after the MFA kiosk, when MFA is on).\n" +
			"Authenticates with the personal API token in --token-file (default: admin.token\n" +
			"beside the config).",
		Example: "  homelab-horizon vpn add carl-1 --profile vpn-only --vpn-admin --user carl",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.name = strings.TrimSpace(args[0])
			return runVPNAdd(cmd.OutOrStdout(), cmd.ErrOrStderr(), opts.configPath, a)
		},
	}
	cmd.Flags().StringVar(&a.profile, "profile", "", "the client's profile: "+config.ProfileVPNOnly+", "+config.ProfileLanAccess+" or "+config.ProfileFullTunnel+" (required)")
	cmd.Flags().StringVar(&a.user, "user", "", "the account this device belongs to (required; must exist here)")
	cmd.Flags().StringVar(&a.project, "project", "", "the project the client is attributed to (default: global)")
	cmd.Flags().BoolVar(&a.vpnAdmin, "vpn-admin", false, "make the client a VPN admin of this hz")
	cmd.Flags().StringVar(&a.tokenFile, "token-file", "", "file holding a personal API token (default: admin.token beside the config)")
	_ = cmd.MarkFlagRequired("profile")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}

func runVPNAdd(out, errOut io.Writer, configPath string, a vpnAddArgs) error {
	// Every refusal comes before hz is asked for anything: a client that
	// exists and whose key was never shown is a client nobody can use.
	if err := requireRoot("vpn add"); err != nil {
		return err
	}
	if !stdoutIsTerminal() {
		return fmt.Errorf("refusing to print a VPN client's private key to something that is not a terminal: " +
			"a redirect is how it ends up in a file with the wrong mode or a log. Run this at a prompt; nothing was created")
	}
	switch a.profile {
	case config.ProfileVPNOnly, config.ProfileLanAccess, config.ProfileFullTunnel:
	case config.ProfileUpstream:
		return fmt.Errorf("--profile %s is a nested hz's link to this one, made by the bootstrap — not a person's device", a.profile)
	default:
		return fmt.Errorf("--profile %q is not one of %s, %s, %s", a.profile, config.ProfileVPNOnly, config.ProfileLanAccess, config.ProfileFullTunnel)
	}
	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	tokenFile := a.tokenFile
	if tokenFile == "" {
		tokenFile = filepath.Join(filepath.Dir(cfgPath), "admin.token")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return fmt.Errorf("read the API token from %s: %w (the bootstrap writes it; or: homelab-horizon user create <name> --admin --token-file %s)", tokenFile, err, tokenFile)
	}
	token := strings.TrimSpace(string(raw))
	base, err := loopbackBase(cfg.EffectiveListenAddr())
	if err != nil {
		return err
	}

	return withStore(configPath, func(ctx context.Context, store *db.DB) error {
		user, err := store.UserByUsername(ctx, a.user)
		if err != nil || !user.Enabled() {
			return fmt.Errorf("--user %q is not an enabled account on this hz, so the device cannot be attributed to it — create it first: homelab-horizon user create %s --admin --sign-in; nothing was created",
				a.user, a.user)
		}
		c := &http.Client{Timeout: 30 * time.Second}
		var added apitypes.AddPeerResponse
		if err := consolePost(c, base, token, tokenFile, "/api/v1/vpn/peers/add",
			apitypes.PeerAddReq{Name: a.name, Profile: a.profile, Project: a.project}, &added); err != nil {
			return err
		}
		var adminErr error
		if a.vpnAdmin {
			// SET, not flip: a flip run twice demotes (loadtest child, 2026-10-07).
			yes := true
			var got apitypes.ToggleAdminResponse
			adminErr = consolePost(c, base, token, tokenFile, "/api/v1/vpn/peers/toggle-admin",
				apitypes.ToggleAdminReq{Name: a.name, Admin: &yes}, &got)
			if adminErr == nil && !got.IsAdmin {
				adminErr = fmt.Errorf("hz answered isAdmin=false")
			}
		}
		ownerErr := store.SetPeerOwner(ctx, a.name, user.ID)

		// The one place the secret is written: stdout, a terminal.
		_, _ = fmt.Fprintf(out, "%s\n", added.Config)
		if q, err := renderQR(added.Config); err == nil {
			_, _ = fmt.Fprint(out, q)
		} else {
			say(errOut, "%v\n", err)
		}
		say(errOut, "VPN client %s created (%s%s), attributed to %s. The config above holds its private key and is shown once: hz kept only the public key %s.\n",
			a.name, a.profile, map[bool]string{true: ", VPN admin", false: ""}[a.vpnAdmin && adminErr == nil], user.Username, added.PublicKey)
		if adminErr != nil {
			say(errOut, "WARNING: the client works, but making it a VPN admin failed: %v — promote it from the UI\n", adminErr)
		}
		if ownerErr != nil {
			say(errOut, "WARNING: the client works, but recording %s as its owner failed: %v\n", user.Username, ownerErr)
		}
		return nil
	})
}

// consolePost is one admin request to the hz on this box, with a personal API
// token. The token is never printed.
func consolePost(c *http.Client, base, token, tokenFile, path string, body, v any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("hz is not answering at %s (%w) — is homelab-horizon running? systemctl status homelab-horizon", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("hz at %s refused the token in %s (401): revoked, expired, or not a personal API token", base, tokenFile)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			msg = []byte(e.Error)
		}
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
