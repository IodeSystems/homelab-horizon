package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Console management of personal API tokens.
//
// The counterpart to `--enable-admin-token`: something has to work when nobody
// can sign in through the web UI — a fresh install automating its own setup, or
// an operator who has disabled the shared token and needs a credential for CI
// before they have a browser session.
//
// Being able to mint a token as any user is equivalent to root on this box,
// which is what running this command already requires. It is a local console
// operation on purpose and is never exposed over HTTP.

func newTokenCmd(opts *serveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage personal API tokens from the console",
		Long: "Personal API tokens authenticate scripts as a named user, so their\n" +
			"actions are attributable (PCI DSS 8.2.1). This subcommand exists for\n" +
			"the cases where the web UI is not reachable — the same reason\n" +
			"--enable-admin-token exists.",
	}
	cmd.AddCommand(newTokenCreateCmd(opts), newTokenListCmd(opts))
	return cmd
}

// withStore opens the identity store the running server uses.
//
// Safe alongside a live hz: SQLite is in WAL mode, so a second connection reads
// and writes without stopping the service.
func withStore(configPath string, fn func(context.Context, *db.DB) error) error {
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := db.Open(cfg.UsersDBPath())
	if err != nil {
		return fmt.Errorf("open identity store at %s: %w", cfg.UsersDBPath(), err)
	}
	defer func() { _ = store.Close() }()
	return fn(context.Background(), store)
}

func newTokenCreateCmd(opts *serveOpts) *cobra.Command {
	var username, name string
	var days int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a personal API token for a user",
		Example: "  homelab-horizon token create --user carl --name ci-deploy\n" +
			"  homelab-horizon token create --user carl --name laptop --days 90",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				user, err := store.UserByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("no such user %q: %w", username, err)
				}
				if !user.Enabled() {
					return fmt.Errorf("%s is disabled; enable the account before giving it a credential", username)
				}

				token, meta, err := store.CreateAPIToken(ctx, user.ID, name,
					time.Duration(days)*24*time.Hour)
				if err != nil {
					return err
				}

				// stdout is the token alone, so this composes:
				//   TOKEN=$(homelab-horizon token create --user x --name y)
				// Everything else goes to stderr.
				fmt.Fprintf(os.Stderr, "Created %q for %s", meta.Name, user.Username)
				if meta.ExpiresAt != nil {
					fmt.Fprintf(os.Stderr, ", expires %s", meta.ExpiresAt.Format(time.DateOnly))
				}
				fmt.Fprintf(os.Stderr, ".\nShown once — hz stores only a hash.\n")
				fmt.Println(token)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account the token belongs to (required)")
	cmd.Flags().StringVar(&name, "name", "", "what the token is for, shown in the audit log (required)")
	cmd.Flags().IntVar(&days, "days", 0, "expire after this many days (0 = never)")
	_ = cmd.MarkFlagRequired("user")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newTokenListCmd(opts *serveOpts) *cobra.Command {
	var username string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a user's tokens",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				user, err := store.UserByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("no such user %q: %w", username, err)
				}
				tokens, err := store.ListAPITokens(ctx, user.ID)
				if err != nil {
					return err
				}
				if len(tokens) == 0 {
					fmt.Fprintf(os.Stderr, "%s has no tokens.\n", user.Username)
					return nil
				}

				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "NAME\tCREATED\tEXPIRES\tLAST USED")
				for _, t := range tokens {
					expires, lastUsed := "never", "never"
					if t.ExpiresAt != nil {
						expires = t.ExpiresAt.Format(time.DateOnly)
					}
					if t.LastUsedAt != nil {
						lastUsed = t.LastUsedAt.Format(time.DateOnly)
						if t.LastUsedIP != "" {
							lastUsed += " from " + t.LastUsedIP
						}
					}
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						t.Name, t.CreatedAt.Format(time.DateOnly), expires, lastUsed)
				}
				return w.Flush()
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account whose tokens to list (required)")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}
