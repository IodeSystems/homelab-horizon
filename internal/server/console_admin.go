package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// ConsoleAdminTokenName is the name the console gives the token it mints with
// a new account, as the audit log and `token list` show it.
const ConsoleAdminTokenName = "console-user-create"

// CreateConsoleAdmin is `homelab-horizon user create`: an admin account (hz has
// one role) plus a personal API token, written to tokenFile (0600) and never
// returned, printed or logged.
//
// Exported so the nested bootstrap's end-to-end test drives the same function
// the console does — a box seeded by the script has its first account made
// here, which is what closes bootstrapAllowed's unauthenticated door.
//
// Refuses when the account exists (re-running must not mint a second
// credential for it) and when tokenFile exists (a credential file is never
// overwritten). The token file is created exclusively BEFORE the account, so a
// refusal on it changes nothing in the store.
func CreateConsoleAdmin(ctx context.Context, store *db.DB, username, tokenFile string) (*db.User, error) {
	username = db.NormalizeUsername(username)
	if username == "" {
		return nil, errors.New("a username is required")
	}
	if strings.TrimSpace(tokenFile) == "" {
		return nil, errors.New("--token-file is required: the token is written there, never printed")
	}
	if _, err := store.UserByUsername(ctx, username); err == nil {
		return nil, fmt.Errorf("refusing: account %q already exists; nothing was changed", username)
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("look up %q: %w", username, err)
	}

	f, err := os.OpenFile(tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("refusing: %s already exists, and a credential file is never overwritten; nothing was changed", tokenFile)
		}
		return nil, fmt.Errorf("create %s: %w", tokenFile, err)
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tokenFile)
		}
	}()
	// The umask can only narrow 0600; this states it regardless.
	if err := f.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", tokenFile, err)
	}

	user, err := store.CreateUser(ctx, username, "", db.RoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("create account %q: %w", username, err)
	}
	token, _, err := store.CreateAPIToken(ctx, user.ID, ConsoleAdminTokenName, 0, false)
	if err != nil {
		return nil, fmt.Errorf("account %q was created but its token was not (%w); mint one with: homelab-horizon token create --user %s --name <what-it-is-for>",
			username, err, username)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		return nil, fmt.Errorf("account %q was created but its token could not be written to %s (%w); mint one with: homelab-horizon token create --user %s --name <what-it-is-for>",
			username, tokenFile, err, username)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("account %q was created but %s could not be synced (%w)", username, tokenFile, err)
	}
	keep = true
	return user, nil
}
