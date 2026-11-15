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

// enrollOpts are the two deliberate replacements enrolment can perform. They
// are separate because they replace different things with different blast
// radii: --rotate retires this box's CREDENTIAL, which affects this box alone,
// while --rotate-keys replaces its WireGuard KEYS, which makes every peer's
// rendered config stale until hz re-serves it. One flag for both would make the
// second a side effect of the first.
type enrollOpts struct {
	// Rotate mints a new agent credential even when the one this box holds is
	// still the one hz accepts.
	Rotate bool

	// RotateKeys mints a fresh WireGuard key pair per segment and tells hz the
	// change is a rotation. Without it hz REFUSES a key that differs from the
	// one it holds, because a differing key is equally the signature of another
	// box claiming this one's peering — see recordSegmentKeys in
	// internal/server. This is the flag an operator reaches for after rebuilding
	// a box, or when a key is believed leaked.
	RotateKeys bool
}

func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	var f agentFlags
	f.register(fs)
	var opts enrollOpts
	fs.BoolVar(&opts.Rotate, "rotate", false, "mint a new secret even if this machine is already enrolled")
	fs.BoolVar(&opts.RotateKeys, "rotate-keys", false,
		"mint fresh WireGuard keys for this machine's segments and have hz REPLACE the ones it holds. Without this a changed key is refused, because hz cannot tell a rotation from another box claiming this peering")
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
	return enroll(&f, opts, os.Stdout)
}

// enroll is the body, seamed on its output so a test can prove the secret does
// not reach it, and on --hz so a test can point it at a real hz.
//
// IT IS TWO REQUESTS, AND THE SECOND ONE IS THE KEYS. The box cannot report a
// key for a segment it does not know it is in: membership is declared on hz
// (`hz machine add --segment`) and reaches the box in the enrolment RESPONSE.
// So the first request establishes the credential and learns the segments, and
// the second reports a public key per segment. The credential half is
// idempotent — the second request carries the hash of what this box now holds,
// so hz answers "already enrolled", writes no credential and mints nothing —
// which is what makes a second round trip safe rather than a second enrolment.
//
// The alternative was for the agent to remember its own segment list between
// runs, which is a second copy of a fact hz owns, free to go stale exactly when
// a machine joins a segment.
func enroll(f *agentFlags, opts enrollOpts, out io.Writer) error {
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
	ctx := context.Background()
	resp, err := enroller.Enroll(ctx, agent.EnrollRequest{
		Machine: machine, CurrentHash: currentHash, Rotate: opts.Rotate,
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
	} else {
		// hz recorded the hash before answering, so a failure here leaves hz
		// accepting a credential nobody holds — which the next enrolment
		// replaces. The other order would leave this box holding one hz never
		// recorded, which is a 401 somebody has to debug at the box.
		if err := writeTokenFile(f.tokenFile, resp.Secret); err != nil {
			return fmt.Errorf("hz issued a credential but it could not be stored at %s: %w", f.tokenFile, err)
		}
		currentHash = agent.HashSecret(resp.Secret)

		_, _ = fmt.Fprintf(out, "Enrolled %s with hz — hz issued the credential.\n", resp.Machine)
		_, _ = fmt.Fprintf(out, "  credential: %s (mode %04o, root only)\n", f.tokenFile, tokenFileMode)
		_, _ = fmt.Fprintf(out, "  hz keeps:   the hash, never the secret\n")
		printMembership(out, resp)
	}

	keyErr := reportSegmentKeys(ctx, f, enroller, machine, currentHash, resp, opts, out)

	_, _ = fmt.Fprintln(out)
	if resp.AlreadyEnrolled {
		_, _ = fmt.Fprintln(out, "The credential was left alone. Pass --rotate to replace it.")
	} else {
		_, _ = fmt.Fprintln(out, "The secret was not printed and is not on any command line.")
	}
	_, _ = fmt.Fprintln(out, "hz re-reads its record per request, so no restart is needed. Check with:")
	_, _ = fmt.Fprintf(out, "  sudo %s diff --hz %s\n", execPath(), f.hzURL)
	return keyErr
}

// reportSegmentKeys is the second half: mint a WireGuard key per segment hz
// says this machine is in, and report the PUBLIC halves.
//
// A failure here is returned, not swallowed, but it is returned AFTER the
// credential has been written and reported — a box that is enrolled and
// unkeyed is a real, recoverable state (hz can address it and cannot peer with
// it, which is exactly what the projection says about it), and throwing away a
// working enrolment over it would be worse than saying so.
func reportSegmentKeys(ctx context.Context, f *agentFlags, enroller *agent.Enroller,
	machine, currentHash string, first *agent.EnrollResponse, opts enrollOpts, out io.Writer) error {
	if len(first.Segments) == 0 {
		// Nothing to key. Not a gap: a machine in no segment peers with nothing
		// and needs no interface, which is a true statement about it.
		return nil
	}

	keys, err := f.segmentKeys().Report(first.Segments, opts.RotateKeys)
	if err != nil {
		return fmt.Errorf("minting this machine's WireGuard keys: %w", err)
	}

	resp, err := enroller.Enroll(ctx, agent.EnrollRequest{
		Machine: machine, CurrentHash: currentHash,
		SegmentKeys: keys, RotateKeys: opts.RotateKeys,
	})
	if err != nil {
		return fmt.Errorf("reporting this machine's WireGuard public keys to hz: %w", err)
	}
	return printKeyResults(out, f, resp)
}

// printKeyResults says per segment what hz did, and turns a refusal into a
// non-zero exit.
//
// EVERY OUTCOME GETS A LINE, including the boring ones. An operator who has
// just enrolled a box needs to be able to read off which segments can now carry
// a tunnel and which cannot, and silence on the ones that worked would make the
// list unreadable as an answer to that.
func printKeyResults(out io.Writer, f *agentFlags, resp *agent.EnrollResponse) error {
	if len(resp.SegmentKeys) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "WireGuard keys (this box mints them; hz gets the public half only):\n")
	_, _ = fmt.Fprintf(out, "  private keys: %s (mode %04o, root only) — never sent, never printed\n",
		f.segmentKeys().Dir, agent.SegmentKeyFileMode)
	for _, k := range resp.SegmentKeys {
		switch k.Status {
		case agent.SegmentKeyRecorded:
			_, _ = fmt.Fprintf(out, "  %-24s recorded with hz — peers on this segment can be configured now\n", k.Segment)
		case agent.SegmentKeyUnchanged:
			_, _ = fmt.Fprintf(out, "  %-24s unchanged — hz already holds this box's key\n", k.Segment)
		default:
			_, _ = fmt.Fprintf(out, "  %-24s %s\n", k.Segment, strings.ToUpper(k.Status))
		}
		if k.Detail != "" {
			_, _ = fmt.Fprintf(out, "      %s\n", k.Detail)
		}
	}

	conflicts := resp.Conflicts()
	if len(conflicts) == 0 {
		return nil
	}
	names := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		names = append(names, c.Segment)
	}
	// LOUD, because the silent version of this is a peer takeover. hz kept what
	// it had, so nothing is broken yet; what must not happen is this scrolling
	// past as though enrolment succeeded outright.
	return fmt.Errorf("hz holds a DIFFERENT WireGuard key for this machine on %s and refused to replace it.\n"+
		"That is either a key rotation nobody declared, or another box claiming this machine's peering — hz cannot tell them apart.\n"+
		"The credential above was issued; only the keys were refused, and hz still holds the old ones.\n"+
		"If this box was rebuilt or its keys were rotated on purpose, say so:\n"+
		"  sudo %s enroll --hz %s --rotate-keys\n"+
		"If it was not, find out which box is presenting a new key before doing that",
		strings.Join(names, ", "), execPath(), f.hzURL)
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
