package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/server"
)

// Console account recovery.
//
// The web UI cannot help when nobody can sign in — a forgotten password on the
// only account, a second factor on a lost phone, an install being automated
// before anyone has a browser session. This is the same console-only escape
// hatch as --enable-admin-token, and it requires root on the box, which is
// already full control of everything hz holds.

func newUserCmd(opts *serveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage accounts from the console",
	}
	cmd.AddCommand(newUserListCmd(opts), newSetPasswordCmd(opts), newUserCreateCmd(opts))
	return cmd
}

// geteuid is os.Geteuid; tests replace it to drive the root-only commands.
var geteuid = os.Geteuid

// requireRoot refuses a root-only console command for anyone else.
func requireRoot(what string) error {
	if geteuid() != 0 {
		return fmt.Errorf("%s must run as root (sudo): it acts with the full authority of this hz", what)
	}
	return nil
}

// newUserCreateCmd creates an admin account and its first personal API token.
//
// The console path for a box nobody can sign in to yet — above all a nested hz
// seeded by the bootstrap script, which has no account and so keeps
// bootstrapAllowed's unauthenticated first-account door open until one
// exists. Like --enable-admin-token it is console-only and needs root; it is
// never exposed over HTTP.
func newUserCreateCmd(opts *serveOpts) *cobra.Command {
	var admin, signIn bool
	var tokenFile string

	cmd := &cobra.Command{
		Use:   "create <name> --admin (--sign-in | --token-file F)",
		Short: "Create an admin account: for a person (--sign-in) or a service (--token-file) (console, root only)",
		Long: "Creates an admin account. Say who it is for:\n\n" +
			"  --sign-in      a PERSON. A one-time password is generated and printed to\n" +
			"                 this terminal, once (refused when stdout is not a terminal;\n" +
			"                 nothing logs it). The person must replace it at the first\n" +
			"                 sign-in, after any second factor — the path\n" +
			"                 'user set-password' uses.\n" +
			"  --token-file   a SERVICE (or a script). A personal API token is written to\n" +
			"                 the file (0600) and never printed. The account has no\n" +
			"                 password and cannot sign in to the UI.\n\n" +
			"Both may be given. One is required: an account with neither is one nobody\n" +
			"can use, and nothing would say so.\n\n" +
			"hz has exactly one role, admin, and --admin says so on purpose: this is\n" +
			"full control of this hz.\n\n" +
			"Refuses when the account already exists or the token file is already there.\n\n" +
			"A console path like --enable-admin-token: it requires root on the box and is\n" +
			"never reachable over HTTP. Creating the first account also closes the\n" +
			"unauthenticated first-account sign-up a fresh hz has.",
		Example: "  homelab-horizon user create carl --admin --sign-in\n" +
			"  homelab-horizon user create hz-local-admin --admin --token-file /etc/homelab-horizon/admin.token",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot("user create"); err != nil {
				return err
			}
			if !admin {
				return fmt.Errorf("pass --admin: hz has exactly one role, admin, and this account will hold it")
			}
			if !signIn && tokenFile == "" {
				return fmt.Errorf("say who the account is for: --sign-in (a person: a one-time password, printed here) or --token-file F (a service: an API token in F). With neither nobody could use it; nothing was created")
			}
			if signIn && !stdoutIsTerminal() {
				return fmt.Errorf("refusing to print a one-time password to something that is not a terminal: a redirect is how it ends up in a file or a log. Run this at a prompt; nothing was created")
			}
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				var user *db.User
				var err error
				if tokenFile != "" {
					user, err = server.CreateConsoleAdmin(ctx, store, args[0], tokenFile)
				} else {
					user, err = server.CreateConsolePerson(ctx, store, args[0])
				}
				if err != nil {
					return err
				}
				say(cmd.ErrOrStderr(), "Created admin account %s.\n", user.Username)
				if tokenFile != "" {
					// Never the token: only where it is.
					say(cmd.ErrOrStderr(), "Its API token %q is in %s (0600); it is not shown.\n", server.ConsoleAdminTokenName, tokenFile)
					say(cmd.ErrOrStderr(), "Take it back with: homelab-horizon token revoke --user %s --name %s\n",
						user.Username, server.ConsoleAdminTokenName)
				}
				if signIn {
					pw, err := setOneTimePassword(ctx, store, user)
					if err != nil {
						return fmt.Errorf("account %s was created, but its one-time password was not: %w — set one with: homelab-horizon user set-password --user %s", user.Username, err, user.Username)
					}
					say(cmd.ErrOrStderr(), "One-time password for %s below, shown once. It must be replaced at the first sign-in.\n", user.Username)
					// stdout, alone, the one place it is written.
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), pw)
				} else {
					say(cmd.ErrOrStderr(), "It has no password, so it cannot sign in to the UI (add --sign-in for a person).\n")
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&admin, "admin", false, "required: the account is an admin (hz's only role)")
	cmd.Flags().BoolVar(&signIn, "sign-in", false, "a person: print a one-time password (terminal only) to replace at first sign-in")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "a service: write the account's API token here, 0600 (must not exist)")
	return cmd
}

// setOneTimePassword gives the account a generated password it must replace
// at the next sign-in, and returns it: what `user set-password` does with no
// --password and no --keep. Used by `user create --sign-in`.
func setOneTimePassword(ctx context.Context, store *db.DB, user *db.User) (string, error) {
	// Generated rather than prompted: this runs over ssh and in scripts, and
	// a prompt there either hangs or gets a password echoed into a log.
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	pw := base64.RawURLEncoding.EncodeToString(raw)
	if err := store.SetPassword(ctx, user.ID, pw); err != nil {
		return "", err
	}
	if err := store.RequirePasswordChange(ctx, user.ID); err != nil {
		return "", err
	}
	return pw, nil
}

func newUserListCmd(opts *serveOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List accounts",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				users, err := store.ListUsers(ctx)
				if err != nil {
					return err
				}
				for _, u := range users {
					state := "enabled"
					if !u.Enabled() {
						state = "disabled"
					}
					must, _ := store.PasswordMustChange(ctx, u.ID)
					if must {
						state += ", must change password"
					}
					fmt.Printf("%s\t%s\t%s\n", u.Username, u.Role, state)
				}
				return nil
			})
		},
	}
}

func newSetPasswordCmd(opts *serveOpts) *cobra.Command {
	var username, password string
	var keep bool

	cmd := &cobra.Command{
		Use:   "set-password",
		Short: "Set a user's password, requiring a change at next sign-in",
		Long: "Sets a password from the console. By default the account must replace it\n" +
			"on the next sign-in: a credential handed over out of band should not\n" +
			"survive first use, and unlike age-based expiry this applies even to\n" +
			"accounts holding a second factor.\n\n" +
			"The change is demanded AFTER any second factor, so the temporary password\n" +
			"alone never gets far enough to set a new one.",
		Example: "  homelab-horizon user set-password --user carl\n" +
			"  homelab-horizon user set-password --user carl --password 's3cret' --keep",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				user, err := store.UserByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("no such user %q: %w", username, err)
				}

				generated := false
				if password == "" {
					// Generated rather than prompted: this runs over ssh and in
					// scripts, and a prompt there either hangs or gets a
					// password echoed into a terminal log.
					raw := make([]byte, 18)
					if _, err := rand.Read(raw); err != nil {
						return fmt.Errorf("generate password: %w", err)
					}
					password = base64.RawURLEncoding.EncodeToString(raw)
					generated = true
				}

				// History is not enforced here on purpose: a locked-out
				// operator being told their reset password was used two years
				// ago helps nobody, and the change they are about to be forced
				// into goes through the normal path that does enforce it.
				if err := store.SetPassword(ctx, user.ID, password); err != nil {
					return err
				}
				if !keep {
					if err := store.RequirePasswordChange(ctx, user.ID); err != nil {
						return err
					}
				}

				fmt.Fprintf(os.Stderr, "Password set for %s.\n", user.Username)
				if keep {
					fmt.Fprintf(os.Stderr, "It will NOT be forced to change at next sign-in (--keep).\n")
				} else {
					fmt.Fprintf(os.Stderr, "%s must choose a new one at the next sign-in.\n", user.Username)
				}
				if generated {
					// stdout, alone, so it composes into a variable.
					fmt.Println(password)
				}
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account to set a password for (required)")
	cmd.Flags().StringVar(&password, "password", "", "the password to set (generated and printed if omitted)")
	cmd.Flags().BoolVar(&keep, "keep", false, "do not require a change at next sign-in")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}
