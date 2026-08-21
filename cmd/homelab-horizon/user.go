package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/db"
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
	cmd.AddCommand(newUserListCmd(opts), newSetPasswordCmd(opts))
	return cmd
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
