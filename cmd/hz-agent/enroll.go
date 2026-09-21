package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// `hz-agent enroll` — ask hz for this machine's credential.
//
// WHAT CHANGED IN ITEM 13: THE ISSUER, AND ONLY THE ISSUER. This command used
// to mint a secret locally and write its own record into hz's credential store.
// That worked because hz and the agent were the same root on one box — the
// gateway is machine #1 — and it is the WRONG answer for machine #2, because a
// remote agent that can write hz's store can write itself in under any name.
//
// So hz mints. This command asks (internal/agent/enrolment.go), hz checks that
// it declares a Machine by that name, mints, records the HASH and answers with
// the secret exactly once. Unchanged at both ends: the store format, the
// Authorization header, the SHA-256 hashing, hz's verification
// (Server.agentCaller) and the poll (agent.HTTPSource). None of them know where
// a credential came from, which is why moving the issuer is a change to this
// file and to one handler rather than a redesign.
//
// WHAT AUTHORISES IT: an hz ADMIN credential, given at the box, used for this
// one request, never written to disk here and never reaching the unit. On the
// gateway it needs no argument at all — hz's own admin token file is on that
// box and root can read it. Anywhere else it is a deliberate act by somebody
// with authority, which is what "a machine hz does not know cannot enrol"
// means in practice.
//
// WHAT IT NEVER DOES: print the secret. A credential on a terminal is a
// credential in scrollback, in a screen-share and in a support paste. It goes
// to a 0600 file and is reported by path only. It is never passed on a command
// line either, in EITHER direction: the admin credential comes from a file or
// the environment, and `install` takes the same care with the unit
// (TestUnitPassesTheTokenByFileNotOnTheCommandLine).

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
	// Root, for the same reason `install` is: the file it writes is root-only,
	// the admin credential it reads on the gateway is root-only, and a
	// half-written enrolment is worse than none. Checked here rather than
	// inside enroll so the body is exercisable by a test — runInstall makes
	// the same check before it calls through.
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to read %s and write %s", f.adminTokenFile, f.tokenFile)
	}
	return enroll(&f, *rotate, os.Stdout)
}

// enroll is the body, seamed on its output so a test can prove the secret does
// not reach it, and on --hz so a test can point it at a real hz.
func enroll(f *agentFlags, rotate bool, out io.Writer) error {
	machine := f.machineName()
	if machine == "" {
		return errors.New("cannot determine this machine's name; pass --machine")
	}
	adminToken, err := f.resolveAdminToken()
	if err != nil {
		return err
	}

	// The hash of what this box already holds, never the secret. It lets hz
	// answer "you already have the current one" without a working credential
	// crossing the wire for a question a hash settles.
	currentHash := ""
	if existing := readTokenFile(f.tokenFile); existing != "" {
		currentHash = agent.HashSecret(existing)
	}

	enroller := &agent.Enroller{BaseURL: f.hzURL, AdminToken: adminToken}
	resp, err := enroller.Enroll(context.Background(), agent.EnrollRequest{
		Machine: machine, CurrentHash: currentHash, Rotate: rotate,
	})
	if err != nil {
		if errors.Is(err, agent.ErrMachineNotDeclared) {
			// The one refusal with a next step, so say it as one. hz issuing
			// only for machines it declares is the point of item 13, not an
			// obstacle to work around.
			return fmt.Errorf("hz will not enrol %s: %w.\nDeclare it first, from a machine with hz access:\n  hz machine add %s --segment <segment>",
				machine, err, machine)
		}
		return err
	}

	if resp.AlreadyEnrolled {
		_, _ = fmt.Fprintf(out, "%s is already enrolled with hz.\n", resp.Machine)
		_, _ = fmt.Fprintf(out, "  credential: %s\n", f.tokenFile)
		printMembership(out, resp)
		_, _ = fmt.Fprintln(out, "Nothing was written at either end. Pass --rotate to replace the credential.")
		return nil
	}

	// hz recorded the hash before answering, so a failure here leaves hz
	// accepting a credential nobody holds — which the next enrolment replaces.
	// The other order would leave this box holding one hz never recorded,
	// which is a 401 somebody has to debug at the box.
	if err := writeTokenFile(f.tokenFile, resp.Secret); err != nil {
		return fmt.Errorf("hz issued a credential but it could not be stored at %s: %w", f.tokenFile, err)
	}

	_, _ = fmt.Fprintf(out, "Enrolled %s with hz — hz issued the credential.\n", resp.Machine)
	_, _ = fmt.Fprintf(out, "  credential: %s (mode %04o, root only)\n", f.tokenFile, tokenFileMode)
	_, _ = fmt.Fprintf(out, "  hz keeps:   the hash, never the secret\n")
	printMembership(out, resp)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "The secret was not printed and is not on any command line.")
	_, _ = fmt.Fprintln(out, "hz re-reads its record per request, so no restart is needed. Check with:")
	_, _ = fmt.Fprintf(out, "  sudo %s diff --hz %s\n", execPath(), f.hzURL)
	return nil
}

// printMembership says what hz declares this machine to be part of, because a
// box that bridges segments should say so out loud at the moment it is
// enrolled — blast radius is the union of its segments.
func printMembership(out io.Writer, resp *agent.EnrollResponse) {
	if len(resp.Segments) == 0 {
		_, _ = fmt.Fprintln(out, "  segments:   none declared yet (`hz machine add` takes --segment)")
		return
	}
	_, _ = fmt.Fprintf(out, "  segments:   %s\n", strings.Join(resp.Segments, ", "))
	if len(resp.Segments) > 1 {
		_, _ = fmt.Fprintf(out, "  MULTI-HOMED: this machine bridges %d segments. Declared reason: %s\n",
			len(resp.Segments), resp.Note)
		_, _ = fmt.Fprintln(out, "  Its blast radius is the UNION of those segments. Forwarding between them")
		_, _ = fmt.Fprintln(out, "  stays denied by default.")
	}
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
