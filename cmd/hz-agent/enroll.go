package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// `hz-agent enroll` — give this machine a credential of its own.
//
// It mints a secret, writes it where only root can read it, and records its
// HASH with hz. hz never holds the secret; the agent never holds an admin
// token. That is the whole of plan/privilege-audit.md §3 item 1.
//
// WHY THE AGENT MINTS IT, TODAY. hz and the agent are the same box: the
// gateway is machine #1 (plan/architecture.md), and both halves run as root on
// it. A local mint is therefore a root process writing two root-only files,
// and it needs no bootstrap credential — which matters, because the only
// credential available to bootstrap with would be the admin token, and handing
// the agent an admin token is the thing being fixed.
//
// It is also the WRONG answer for machine #2, and deliberately so. A remote
// agent must not be able to write itself into hz's store. Item 13's Machine
// record makes hz the issuer: hz mints the secret at enrolment and hands it
// down the same approval flow `configmgr` already uses (register → approve →
// resolve). When that lands, THIS FILE is what changes — `enroll` stops
// writing a record and starts asking for one. The store format, the header,
// the hash, hz's verification and the poll are untouched, because none of them
// know where a credential came from.
//
// WHAT IT NEVER DOES: print the secret. A credential on a terminal is a
// credential in scrollback, in a screen-share and in a support paste. It goes
// to a 0600 file and is reported by path only. `install` takes the same care
// with argv (TestUnitPassesTheTokenByFileNotOnTheCommandLine).

const tokenFileMode = 0o600

// tokenDirMode: the directory is root-only too. A 0755 directory holding a
// 0600 file still tells every user on the box that this machine is enrolled.
const tokenDirMode = 0o700

func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	rotate := fs.Bool("rotate", false, "mint a new secret even if this machine is already enrolled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Root, for the same reason `install` is: both files it touches are
	// root-only, and a half-written enrolment is worse than none. Checked
	// here rather than inside enroll so the body is exercisable by a test —
	// runInstall makes the same check before it calls through.
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to write %s and %s", f.tokenFile, f.hzCredentials)
	}
	return enroll(&f, *rotate, os.Stdout)
}

// enroll is the body, seamed on its output so a test can prove the secret does
// not reach it.
func enroll(f *agentFlags, rotate bool, out io.Writer) error {
	machine := f.machineName()
	if machine == "" {
		return errors.New("cannot determine this machine's name; pass --machine")
	}
	store := agent.CredentialStore{Path: f.hzCredentials}
	if store.Path == "" {
		return errors.New("no hz credential store; pass --hz-credentials")
	}

	// Already enrolled and still matching? Say so and change nothing.
	// Enrolment has to be safe to re-run — `install` calls it every time.
	if existing := readTokenFile(f.tokenFile); existing != "" && !rotate {
		if store.Enrolled(machine, existing) {
			_, _ = fmt.Fprintf(out, "%s is already enrolled with hz.\n", machine)
			_, _ = fmt.Fprintf(out, "  credential: %s\n", f.tokenFile)
			_, _ = fmt.Fprintf(out, "  hz's record: %s\n", store.Path)
			_, _ = fmt.Fprintln(out, "Pass --rotate to replace it.")
			return nil
		}
	}

	secret, err := agent.NewSecret()
	if err != nil {
		return err
	}

	// The secret lands first. If recording the hash then fails, the agent
	// holds a credential hz does not accept — a 401, which is where it
	// already was. The other order would have hz accepting a credential
	// nobody holds.
	if err := writeTokenFile(f.tokenFile, secret); err != nil {
		return err
	}
	if err := store.Enroll(machine, secret); err != nil {
		return fmt.Errorf("recording the credential with hz (%s): %w", store.Path, err)
	}

	_, _ = fmt.Fprintf(out, "Enrolled %s with hz.\n", machine)
	_, _ = fmt.Fprintf(out, "  credential: %s (mode %04o, root only)\n", f.tokenFile, tokenFileMode)
	_, _ = fmt.Fprintf(out, "  hz's record: %s — the hash, never the secret\n", store.Path)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "The secret was not printed and is not on any command line.")
	_, _ = fmt.Fprintln(out, "hz re-reads its record per request, so no restart is needed. Check with:")
	_, _ = fmt.Fprintf(out, "  sudo %s diff --hz %s\n", execPath(), f.hzURL)
	return nil
}

// readTokenFile returns the stored secret, or "" for anything unreadable.
func readTokenFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeTokenFile writes the secret 0600 into a 0700 directory.
//
// Through a temp file in the same directory, so a reader that catches the
// write mid-flight gets the old secret or the new one, never half of one. The
// mode is set before the content is written — a world-readable window of even
// a few microseconds is a window.
func writeTokenFile(path, secret string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, tokenDirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	// MkdirAll leaves an EXISTING directory's mode alone, and hz-agent's
	// install already creates /etc/hz-agent at 0755 — measured on the audit
	// VM, where the first enrolment landed a 0600 token inside a
	// world-readable directory. The file was safe and the claim above was
	// not, so the mode is asserted rather than assumed.
	if err := os.Chmod(dir, tokenDirMode); err != nil {
		return fmt.Errorf("securing %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(tokenFileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(secret + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
