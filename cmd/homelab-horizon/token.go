package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	cmd.AddCommand(newTokenCreateCmd(opts), newTokenListCmd(opts), newTokenRevokeCmd(opts))
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
	var requireOTP bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a personal API token for a user",
		Example: "  homelab-horizon token create --user carl --name ci-deploy\n" +
			"  homelab-horizon token create --user carl --name laptop --days 90",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				user, err := store.UserByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("no such user %q: %w", username, err)
				}
				if !user.Enabled() {
					return fmt.Errorf("%s is disabled; enable the account before giving it a credential", username)
				}

				token, meta, err := store.CreateAPIToken(ctx, user.ID, name,
					time.Duration(days)*24*time.Hour, requireOTP)
				if err != nil {
					return err
				}

				// stdout is the token alone, so this composes:
				//   TOKEN=$(homelab-horizon token create --user x --name y)
				// Everything else goes to stderr.
				errOut := cmd.ErrOrStderr()
				say(errOut, "Created %q for %s", meta.Name, user.Username)
				if meta.ExpiresAt != nil {
					say(errOut, ", expires %s", meta.ExpiresAt.Format(time.DateOnly))
				}
				if requireOTP {
					say(errOut, "\nThis token also needs a one-time code on every "+
						"request: pass OTP=<code> to the hz scripts, or send it as %s.",
						"X-HZ-OTP")
				}
				say(errOut, ".\nShown once — hz stores only a hash.\n")
				say(errOut, "Take it back with: homelab-horizon token revoke --user %s --name %s\n",
					user.Username, meta.Name)
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), token)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account the token belongs to (required)")
	cmd.Flags().StringVar(&name, "name", "", "what the token is for, shown in the audit log (required)")
	cmd.Flags().IntVar(&days, "days", 0, "expire after this many days (0 = never)")
	cmd.Flags().BoolVar(&requireOTP, "require-otp", false,
		"also demand a one-time code on every request (default: no, so the token "+
			"works unattended)")
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
		RunE: func(cmd *cobra.Command, _ []string) error {
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
					say(cmd.ErrOrStderr(), "%s has no tokens.\n", user.Username)
					return nil
				}

				// ID is shown because `token revoke --id` needs it, and a name
				// is not unique: when two tokens share one, the id is the only
				// thing that tells them apart. A list you cannot act on is the
				// same half-a-feature as minting without revoking.
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "ID\tNAME\tCREATED\tEXPIRES\tLAST USED")
				for _, t := range tokens {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
						t.ID, t.Name, t.CreatedAt.Format(time.DateOnly),
						tokenExpiry(t), tokenLastUse(t))
				}
				return w.Flush()
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account whose tokens to list (required)")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}

// newTokenRevokeCmd takes a token back.
//
// The other half of `token create`, and the half whose absence cost something:
// a console that can mint a credential but not withdraw one is only useful
// while nothing has gone wrong. The case this subcommand family exists for —
// the web UI is not reachable — is precisely the case where a credential most
// needs taking back, and until now the answer was "you cannot".
//
// Revoke, not delete: the store marks revoked_at and keeps the row, which is
// what the web UI's revoke button does, and what an audit trail requires (a
// token whose rows vanish takes the record of what it did with it).
func newTokenRevokeCmd(opts *serveOpts) *cobra.Command {
	var username, name, id string
	var yes bool

	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a personal API token",
		Long: "Revokes one token, so every request presenting it stops being\n" +
			"authenticated from this moment. The record of the token stays, because\n" +
			"the audit log points at it.\n\n" +
			"Name it with --name, or with --id when two tokens share a name\n" +
			"('homelab-horizon token list --user <user>' shows both).",
		Example: "  homelab-horizon token revoke --user carl --name ci-deploy\n" +
			"  homelab-horizon token revoke --user carl --id pat_0123456789 --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (name == "") == (id == "") {
				return fmt.Errorf("name exactly one token: pass --name or --id, not both and not neither")
			}
			return withStore(opts.configPath, func(ctx context.Context, store *db.DB) error {
				// Every returned error ends up in exitWith's one-line slog
				// record, so guidance that needs more than a line is written
				// here as prose and the error stays a sentence. A JSON field
				// full of \n is not something an operator reads at 3am.
				out := cmd.ErrOrStderr()

				user, err := store.UserByUsername(ctx, username)
				if err != nil {
					return fmt.Errorf("no such user %q: %w", username, err)
				}

				token, err := resolveToken(ctx, store, user, name, id, out)
				if err != nil {
					return err
				}

				// Printed before anything is asked or done, so the operator
				// confirms against the token in front of them. "last used
				// today from <ip>" is the line that says this one is live.
				say(out, "Token %q belonging to %s\n", token.Name, user.Username)
				say(out, "  id         %s\n", token.ID)
				say(out, "  created    %s\n", token.CreatedAt.Format(time.DateOnly))
				say(out, "  expires    %s\n", tokenExpiry(*token))
				say(out, "  last used  %s\n", tokenLastUse(*token))
				say(out, "\nRevoking stops every request that presents it. It cannot be un-revoked;\n"+
					"replacing it means minting a new one, which is a different secret.\n")

				// Printed before the decision and regardless of --yes: whether
				// this is someone's last way in is exactly the fact that would
				// change the answer, and learning it afterwards is too late.
				lastWay, err := describeRemainingAccess(ctx, store, user, token.ID)
				if err != nil {
					return err
				}
				if lastWay != "" {
					say(out, "\n%s", lastWay)
				}

				if !yes {
					say(out, "\nRevoke it? Type y to confirm: ")
					typed, err := readConfirmation(cmd.InOrStdin())
					if err != nil {
						// EOF here is a cron job or a pipeline reaching the
						// prompt, not an operator. Saying so beats hanging or
						// guessing yes.
						say(out, "\nPass --yes to revoke without a prompt.\n")
						return fmt.Errorf("no confirmation could be read from stdin (%w); nothing was revoked", err)
					}
					if a := strings.ToLower(strings.TrimSpace(typed)); a != "y" && a != "yes" {
						return fmt.Errorf("refusing: you answered %q. Nothing was revoked",
							strings.TrimSpace(typed))
					}
				}

				// The store's own revoke, scoped by user id, is the single
				// implementation — the same call the web UI's revoke button
				// makes. A second delete path here would be a second thing to
				// keep correct.
				if err := store.RevokeAPIToken(ctx, user.ID, token.ID); err != nil {
					if errors.Is(err, db.ErrNotFound) {
						// Someone revoked it between the read and the write.
						return fmt.Errorf("token %s is already revoked; nothing to do", token.ID)
					}
					return fmt.Errorf("revoke token %s: %w", token.ID, err)
				}

				// Never the token value, here or anywhere: hz holds only a hash,
				// and an operator's terminal log is not a secret store.
				say(out, "\nRevoked %q (%s). Requests presenting it are no longer authenticated.\n",
					token.Name, token.ID)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&username, "user", "", "the account the token belongs to (required)")
	cmd.Flags().StringVar(&name, "name", "", "the token's name, as shown by 'token list'")
	cmd.Flags().StringVar(&id, "id", "", "the token's id, when a name matches more than one")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (for scripts)")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}

// tokenIDPrefix is what CreateAPIToken's ashid.New("pat") stamps on every token
// id. Checked rather than assumed so that "this is an id, not a secret" is a
// property the code can test before it prints anything back.
const tokenIDPrefix = "pat_"

// resolveToken turns what the operator typed into exactly one live token.
//
// By id it is a direct hit or nothing. By name it may be several, because the
// schema does not make (user, name) unique and creation never checked: two
// tokens called "ci-deploy" are a normal state after a rotation that minted the
// replacement before retiring the original. Revoking "one of them" would be a
// coin flip over which script keeps working, so ambiguity is refused and the
// operator is shown the ids to choose between.
// out carries the part that does not fit in an error: exitWith logs a returned
// error as one slog field, so what an operator needs to READ is written here
// and the error stays a sentence.
func resolveToken(ctx context.Context, store *db.DB, user *db.User, name, id string, out io.Writer) (*db.APIToken, error) {
	listHint := func() {
		say(out, "  'homelab-horizon token list --user %s' shows the ones %s has.\n",
			user.Username, user.Username)
	}

	// A name is an operator's own label and gets quoted back freely. An id is
	// not: the plausible slip is pasting the TOKEN into --id (a script has the
	// value to hand and not the id), and an error that quotes it writes a live
	// credential into a shell history, a CI log or a pasted ticket. So the id
	// is quoted only once it looks like an id, and a value is named for what it
	// is instead of being repeated.
	if id != "" && !strings.HasPrefix(id, tokenIDPrefix) {
		say(out, "--id takes a token id, which begins %q and is in the ID column of the list.\n"+
			"  What was passed is not one, and is not repeated here in case it was the\n"+
			"  token itself — hz keeps only a hash, so a token cannot be looked up by value.\n",
			tokenIDPrefix)
		listHint()
		return nil, errors.New("--id was not given a token id; nothing was revoked")
	}
	if strings.HasPrefix(name, db.APITokenPrefix) {
		say(out, "--name takes the token's name, not the token itself. hz keeps only a hash,\n"+
			"  so it cannot find a token by its value.\n")
		listHint()
		return nil, errors.New("--name was given a token value; nothing was revoked")
	}

	if id != "" {
		tokens, err := store.ListAPITokens(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		for i := range tokens {
			if tokens[i].ID == id {
				return &tokens[i], nil
			}
		}
		// Searched within this user's tokens on purpose: an id belonging to
		// someone else must not be distinguishable from one that does not
		// exist, which is the same scoping RevokeAPIToken does in SQL.
		listHint()
		return nil, fmt.Errorf("%s has no live token with id %q", user.Username, id)
	}

	matches, err := store.APITokensByName(ctx, user.ID, name)
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		listHint()
		return nil, fmt.Errorf("%s has no live token named %q", user.Username, name)
	case 1:
		return &matches[0], nil
	default:
		say(out, "%s has %d live tokens named %q, so the name does not say which one:\n",
			user.Username, len(matches), name)
		for _, t := range matches {
			say(out, "  %s  created %s, last used %s\n",
				t.ID, t.CreatedAt.Format(time.DateOnly), tokenLastUse(t))
		}
		say(out, "  Re-run with --id <id> to say which.\n")
		return nil, fmt.Errorf("%q names %d of %s's tokens; name one with --id. Nothing was revoked",
			name, len(matches), user.Username)
	}
}

// describeRemainingAccess reports what this user would have left, in words, if
// the named token went — or "" when they plainly keep other ways in.
//
// This warns rather than refuses, and that is deliberate. Refusing would put
// the tool back in the position this subcommand exists to fix: the operator
// whose one token leaked needs it gone NOW, and a guard saying "not your last
// one" would fail the single case that motivated the command. There is also no
// lockout to prevent — running this at all means root on the box, and root can
// mint a replacement with 'token create' in the same shell. What is worth
// preventing is the surprise, so the cost is stated before the prompt, every
// time, including under --yes.
func describeRemainingAccess(ctx context.Context, store *db.DB, user *db.User, revokingID string) (string, error) {
	tokens, err := store.ListAPITokens(ctx, user.ID)
	if err != nil {
		return "", err
	}
	others := 0
	for _, t := range tokens {
		if t.ID != revokingID {
			others++
		}
	}
	if others > 0 {
		return "", nil
	}

	passwords, err := store.CredentialsFor(ctx, user.ID, db.KindPassword)
	if err != nil {
		return "", err
	}
	if len(passwords) > 0 {
		return fmt.Sprintf("This is %s's last API token. They can still sign in with their password;\n"+
			"any script or pipeline holding this token cannot, and starts getting 401\n"+
			"immediately.\n", user.Username), nil
	}
	return fmt.Sprintf("WARNING: this is %s's last API token AND that account has no password, so\n"+
		"afterwards it has no way in at all — only this console does.\n"+
		"  Give it one first if that is not what you want:\n"+
		"    homelab-horizon user set-password --user %s\n"+
		"    homelab-horizon token create --user %s --name <what-it-is-for>\n",
		user.Username, user.Username, user.Username), nil
}

// tokenExpiry and tokenLastUse render the two nullable columns the same way in
// the list and in the revoke summary, so the line an operator reads before
// revoking is the line they picked the token from.
func tokenExpiry(t db.APIToken) string {
	if t.ExpiresAt == nil {
		return "never"
	}
	return t.ExpiresAt.Format(time.DateOnly)
}

func tokenLastUse(t db.APIToken) string {
	if t.LastUsedAt == nil {
		return "never"
	}
	s := t.LastUsedAt.Format(time.DateOnly)
	if t.LastUsedIP != "" {
		s += " from " + t.LastUsedIP
	}
	return s
}

// say writes one piece of operator prose.
//
// The write error is dropped on purpose, and named here rather than left as a
// bare `_, _ =` at every call site: a closed or full stderr must not turn into
// a failure to revoke. Losing the narration is survivable; refusing to withdraw
// a credential because the narration could not be printed is not.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// readConfirmation reads one line. One shot, no retry loop: a loop invites
// typing until something passes, which is the shape of the mistake the prompt
// is there to catch.
func readConfirmation(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}
