package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz cm recovery` — key custody, and the reason the whole config manager
// needs it.
//
// configmgr/keystore.go:24 says it plainly: "hz never holds an environment key,
// so this tree is the only place one lives." That tree is ~/.hz on ONE machine.
// The moment an app's plaintext secrets are deleted in favour of the config
// manager, the sealed store stops being a convenience and becomes the system of
// record — and losing that one directory does not lock the secrets, it destroys
// them.
//
// A recovery key is a recipient that is always approved. There is NO new crypto
// here: `hz cm approve` already wraps an environment key to a machine's public
// key with configmgr.WrapEnvKey, and every command below does exactly that to a
// public key that lives in a password manager instead of on a box.
//
// Three rules this file exists to hold, and they are absolute:
//
//  1. A recovery PRIVATE key never reaches hz. Nothing here sends one, no
//     endpoint accepts one, and there is no field it could travel in.
//  2. A private key never appears in argv. /proc is world-readable and `ps` is
//     how it would leak, so `verify` reads it from stdin — the same rule that
//     gives `hz cm key import` no --key flag.
//  3. No environment key and no private key is ever printed, logged or put in
//     an error. Names, key ids and fingerprints only. `keygen` is the single
//     exception and it is deliberate: it exists to hand an operator a private
//     key once, to a terminal, refusing any other destination.

func runCMRecovery(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cm recovery subcommand required: ls | keygen | add | rm | backfill | verify")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "ls", "list":
		return cmRecoveryList(c, rest)
	case "keygen":
		return cmRecoveryKeygen(rest)
	case "add":
		return cmRecoveryAdd(c, rest)
	case "rm", "remove":
		return cmRecoveryRemove(c, rest)
	case "backfill":
		return cmRecoveryBackfill(c, rest)
	case "verify":
		return cmRecoveryVerify(c, rest)
	default:
		return fmt.Errorf("unknown cm recovery subcommand: %s", sub)
	}
}

// cmFetchRecovery reads the whole custody picture in one call.
func cmFetchRecovery(c *client) (apitypes.CMRecoveryResp, error) {
	var out apitypes.CMRecoveryResp
	err := c.do(http.MethodGet, apitypes.CMPathRecovery, nil, &out)
	return out, err
}

// --- keygen ----------------------------------------------------------------

// cmRecoveryKeygen mints a recovery keypair and prints it. It talks to hz not
// at all — that is the point. hz sees the public half only, and only when the
// operator chooses to add it.
//
// stdout must be a terminal, for the reason `hz cm key export` gives: a
// redirect is how key material lands in a file nobody set 0600 on, a shell
// history, a CI log, or a scrollback that gets pasted into a ticket. There is
// deliberately no --out flag. The private key is meant to go into a password
// manager by hand and never to exist as a file on this machine.
func cmRecoveryKeygen(args []string) error {
	fs := flag.NewFlagSet("cm recovery keygen", flag.ContinueOnError)
	name := fs.String("name", "", "the recipient name this key will be added under")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: hz cm recovery keygen [--name NAME]")
	}
	if !isTTY(os.Stdout) {
		return fmt.Errorf("refusing to print a recovery private key to something that is not a terminal.\n" +
			"  this key is the ONLY way back into every environment key wrapped to it, and a\n" +
			"  redirect is how it ends up in a file with the wrong mode or a CI log. Run this\n" +
			"  at a prompt and paste straight into your password manager")
	}

	priv, err := configmgr.NewMachineKey()
	if err != nil {
		return err
	}
	pub := priv.PublicKey()
	label := *name
	if label == "" {
		label = "<name>"
	}

	fmt.Fprintln(os.Stderr, "# The next line is a RECOVERY PRIVATE KEY. It is not written anywhere on this")
	fmt.Fprintln(os.Stderr, "# machine and cannot be regenerated. Put it in your password manager now.")
	fmt.Fprintln(os.Stderr, "# hz must never see it: there is no command that sends it and no endpoint that")
	fmt.Fprintln(os.Stderr, "# accepts it.")
	fmt.Println(configmgr.MarshalMachinePrivateKey(priv))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "# public key   %s\n", configmgr.MarshalMachinePublicKey(pub))
	fmt.Fprintf(os.Stderr, "# fingerprint  %s\n", configmgr.FingerprintOf(pub))
	fmt.Fprintln(os.Stderr, "#")
	fmt.Fprintln(os.Stderr, "# Register the PUBLIC half with hz, then wrap the keys you already hold to it:")
	fmt.Fprintf(os.Stderr, "#   hz cm recovery add %s --public-key %s\n", label, configmgr.MarshalMachinePublicKey(pub))
	fmt.Fprintln(os.Stderr, "#   hz cm recovery backfill")
	fmt.Fprintf(os.Stderr, "#   hz cm recovery verify <environment>/<app>/<role>   # prove it before you rely on it\n")
	return nil
}

// --- recipients ------------------------------------------------------------

// cmRecoveryAdd registers a recovery recipient's PUBLIC key.
//
// The public key may come from --public-key or from stdin. It is public, so
// argv is fine for it — the no-argv rule is about secrets, and treating a
// public key as one would only teach an operator that the rule is arbitrary.
func cmRecoveryAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("cm recovery add", flag.ContinueOnError)
	pubFlag := fs.String("public-key", "", "the recipient's public key (public: safe in argv). Omit to read it from stdin")
	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm recovery add <name> [--public-key K]")
	}

	raw := strings.TrimSpace(*pubFlag)
	if raw == "" {
		if isTTY(os.Stdin) {
			fmt.Fprintln(os.Stderr, "Paste the recipient's PUBLIC key, then press ctrl-D:")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
		if err != nil {
			return fmt.Errorf("reading the public key from stdin: %w", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return fmt.Errorf("no public key given; pass --public-key or pipe one in")
	}
	// Parsed here as well as by hz. Not defence in depth — hz's check is the
	// one that counts — but an operator who pasted half a key finds out before
	// a round trip, and the fingerprint printed below has to be derived from
	// the bytes being sent rather than read back from hz's answer.
	pub, err := configmgr.ParseMachinePublicKey(raw)
	if err != nil {
		return err
	}

	var resp apitypes.CMRecoveryResp
	req := apitypes.CMRecoveryRecipientReq{Name: name, PublicKey: configmgr.MarshalMachinePublicKey(pub)}
	if err := c.do(http.MethodPost, apitypes.CMPathRecoveryRecipients, req, &resp); err != nil {
		return err
	}

	fmt.Printf("Recovery recipient %q registered, fingerprint %s.\n", name, configmgr.FingerprintOf(pub))
	fmt.Println()
	fmt.Println("Every environment key minted from now on is wrapped to it automatically. The keys")
	fmt.Println("that already exist are NOT — the recipient list is fixed at the moment a key is")
	fmt.Println("wrapped, so they have to be re-wrapped explicitly:")
	fmt.Println("  hz cm recovery backfill")
	fmt.Println("  hz cm recovery verify <environment>/<app>/<role>   # then prove it opens")
	return nil
}

// cmRecoveryRemove drops a recipient. It says what removal does and does not
// do, every time, because the difference is the whole risk: the wraps already
// addressed to that key stay openable by whoever holds it.
func cmRecoveryRemove(c *client, args []string) error {
	fs := flag.NewFlagSet("cm recovery rm", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the confirmation")
	name, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm recovery rm <name> [--yes]")
	}

	if !*yes {
		fmt.Printf("Remove recovery recipient %q?\n\n", name)
		fmt.Println("This stops FUTURE environment keys being wrapped to it. It is NOT a revocation:")
		fmt.Println("every wrap already addressed to that key stays stored and stays openable by")
		fmt.Println("whoever holds its private half. Undoing a past grant means rotating the")
		fmt.Println("environment key itself.")
		fmt.Print("\ntype the recipient name to confirm: ")
		typed, err := readLine(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading the confirmation: %w", err)
		}
		if strings.TrimSpace(typed) != name {
			return fmt.Errorf("refusing: you typed %q, not %q. Nothing was changed", strings.TrimSpace(typed), name)
		}
	}

	var resp apitypes.CMRecoveryResp
	if err := c.do(http.MethodDelete, apitypes.CMPathRecoveryRecipients+"/"+url.PathEscape(name), nil, &resp); err != nil {
		return err
	}
	fmt.Printf("Removed %q from the recovery recipient list.\n", name)
	fmt.Println("Wraps already addressed to that key were kept, and remain openable by its holder.")
	return nil
}

// --- ls --------------------------------------------------------------------

// cmRecoveryCoverage is one (address, key id) row of the coverage matrix.
type cmRecoveryCoverage struct {
	Address string `json:"address"`
	KeyID   string `json:"keyId"`
	// Held is whether THIS machine's keystore holds the key. A gap on a key
	// this machine does not hold cannot be closed from here, and saying so is
	// the difference between a to-do and a thing to worry about.
	Held      bool     `json:"held"`
	WrappedTo []string `json:"wrappedTo"`
	Missing   []string `json:"missing"`
}

// cmRecoveryList is the report that makes the rot visible.
//
// The gap it exists to surface is "environment X has no wrap for recipient Y" —
// which is invisible everywhere else, silent, and only discovered during a
// recovery that then fails. So the matrix is the output, not the recipient
// list: a recipient with no wraps and an address with no recipients look
// identical in any listing that reports only one of the two.
func cmRecoveryList(c *client, args []string) error {
	fs := flag.NewFlagSet("cm recovery ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: hz cm recovery ls [--json]")
	}

	rec, err := cmFetchRecovery(c)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(rec.Recipients))
	for _, r := range rec.Recipients {
		names = append(names, r.Name)
	}

	// Rows come from two sources, unioned: what this machine HOLDS (the only
	// keys a backfill from here could wrap) and what hz already has a wrap for
	// (so a key held on another laptop still appears, marked not-held).
	rows := map[string]*cmRecoveryCoverage{}
	row := func(addr, keyID string) *cmRecoveryCoverage {
		k := addr + " " + keyID
		if rows[k] == nil {
			rows[k] = &cmRecoveryCoverage{Address: addr, KeyID: keyID}
		}
		return rows[k]
	}
	held, keystoreErr := cmHeldKeys()
	for _, h := range held {
		row(h.addr.String(), h.id).Held = true
	}
	for _, w := range rec.Wraps {
		r := row(w.Environment+"/"+w.App+"/"+w.Role, w.KeyID)
		r.WrappedTo = append(r.WrappedTo, w.Recipient)
	}
	out := make([]cmRecoveryCoverage, 0, len(rows))
	for _, r := range rows {
		sort.Strings(r.WrappedTo)
		for _, n := range names {
			if !slices.Contains(r.WrappedTo, n) {
				r.Missing = append(r.Missing, n)
			}
		}
		if r.WrappedTo == nil {
			r.WrappedTo = []string{}
		}
		if r.Missing == nil {
			r.Missing = []string{}
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Address != out[j].Address {
			return out[i].Address < out[j].Address
		}
		return out[i].KeyID < out[j].KeyID
	})

	if *asJSON {
		b, _ := json.MarshalIndent(struct {
			Recipients []apitypes.CMRecoveryRecipient `json:"recipients"`
			Coverage   []cmRecoveryCoverage           `json:"coverage"`
		}{rec.Recipients, out}, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if len(rec.Recipients) == 0 {
		fmt.Println("No recovery recipients.")
		fmt.Println()
		fmt.Println("Nothing is wrapped to anything, so every environment key exists in exactly one")
		fmt.Println("place: the ~/.hz tree of whichever machine minted it. Losing that directory")
		fmt.Println("destroys the secrets rather than locking them.")
		fmt.Println()
		fmt.Println("  hz cm recovery keygen --name <name>    # mint a recovery key, terminal only")
		fmt.Println("  hz cm recovery add <name> ...          # register its PUBLIC half")
		fmt.Println("  hz cm recovery backfill                # wrap the keys that already exist")
		return nil
	}

	fmt.Printf("%-20s  %-30s  %s\n", "RECIPIENT", "FINGERPRINT", "ADDED")
	for _, r := range rec.Recipients {
		fmt.Printf("%-20s  %-30s  %s\n", r.Name, orDash(r.Fingerprint), orDash(r.AddedAt))
	}

	fmt.Println()
	if len(out) == 0 {
		fmt.Println("No environment keys are known here and hz holds no wraps: nothing to report.")
		return nil
	}
	fmt.Printf("%-30s  %-16s  %-5s  %s\n", "ADDRESS", "KEY ID", "HELD", "COVERAGE")
	gaps, unreachable := 0, 0
	for _, r := range out {
		status := "all " + fmt.Sprint(len(names)) + " recipients"
		if len(r.Missing) > 0 {
			status = "MISSING: " + strings.Join(r.Missing, ", ")
			gaps++
			if !r.Held {
				unreachable++
			}
		}
		fmt.Printf("%-30s  %-16s  %-5s  %s\n", r.Address, r.KeyID, yesNo(r.Held), status)
	}

	// A wrap addressed to a name nobody lists any more is not an error, but it
	// is worth seeing: it is a past grant that is still openable.
	for _, w := range rec.Wraps {
		if !slices.Contains(names, w.Recipient) {
			fmt.Printf("\nnote: %s key %s is wrapped to %q, which is no longer a listed recipient.\n",
				w.Environment+"/"+w.App+"/"+w.Role, w.KeyID, w.Recipient)
			fmt.Println("      The wrap was kept deliberately — whoever holds that private key can still")
			fmt.Println("      open it. Removing a recipient never took that away.")
		}
	}

	if keystoreErr != nil {
		fmt.Printf("\nnote: this machine's keystore could not be read (%v), so the HELD column is\n", keystoreErr)
		fmt.Println("      blank for every row and a backfill from here would do nothing.")
	}
	if gaps > 0 {
		fmt.Printf("\n%d key(s) are not wrapped to every recipient.\n", gaps)
		fmt.Println("  hz cm recovery backfill       # closes every gap on a key THIS machine holds")
		if unreachable > 0 {
			fmt.Printf("  %d of them are on keys this machine does not hold: they can only be closed\n", unreachable)
			fmt.Println("  from a machine whose keystore has them, or after importing the key here.")
		}
	} else {
		fmt.Println("\nEvery known key is wrapped to every recipient. That a wrap EXISTS is not proof")
		fmt.Println("it opens — prove one: hz cm recovery verify <environment>/<app>/<role>")
	}
	return nil
}

// --- backfill --------------------------------------------------------------

// cmHeldKey is one key this machine's keystore holds.
type cmHeldKey struct {
	addr configmgr.EnvKeyAddr
	id   string
}

// cmHeldKeys enumerates every key in the local keystore.
//
// THIS is what decides which environments a backfill can reach, and the answer
// is the honest one: the keys this machine actually holds. There is no other
// candidate — hz cannot supply an environment key (it has never held one) and a
// wrap already stored can only be opened by a recovery private key, which by
// design is not on this box. So an address whose key lives on another laptop is
// reported, never guessed at.
//
// A keystore that does not exist is not an error: a machine that has never
// minted a key has nothing to back-fill and should say so, not fail.
func cmHeldKeys() ([]cmHeldKey, error) {
	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return nil, err
	}
	addrs, err := cmKeyAddresses(ks)
	if err != nil {
		return nil, err
	}
	var out []cmHeldKey
	for _, a := range addrs {
		keys, err := ks.List(keyAddr(a))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a, err)
		}
		for _, k := range keys {
			out = append(out, cmHeldKey{addr: a, id: k.ID.String()})
		}
	}
	return out, nil
}

// cmRecoveryBackfill wraps the keys that already exist.
//
// Without this the feature protects only keys minted from today, which is the
// less valuable half: the environment keys that matter are the ones already in
// use. It reads each key out of the local keystore and wraps it to every
// recipient that has no wrap for it — the same WrapEnvKey call an approval
// makes, to a different public key.
//
// It never prints key material, including in --dry-run, where it does not even
// load a key: a plan is a list of addresses, key ids and recipient names.
func cmRecoveryBackfill(c *client, args []string) error {
	fs := flag.NewFlagSet("cm recovery backfill", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be wrapped and send nothing")
	replace := fs.Bool("replace", false, "re-wrap even where a wrap is already stored")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: hz cm recovery backfill [--dry-run] [--replace]")
	}

	rec, err := cmFetchRecovery(c)
	if err != nil {
		return err
	}
	if len(rec.Recipients) == 0 {
		fmt.Println("No recovery recipients are registered, so there is nothing to wrap to.")
		fmt.Println("  hz cm recovery keygen --name <name>")
		fmt.Println("  hz cm recovery add <name> --public-key <key>")
		return nil
	}

	held, err := cmHeldKeys()
	if err != nil {
		return err
	}
	if len(held) == 0 {
		fmt.Println("This machine's keystore holds no environment keys, so a backfill from here")
		fmt.Println("would wrap nothing. Run it on the machine that minted the keys.")
		return nil
	}

	// Which (address, key id, recipient) tuples already have a wrap.
	have := map[string]bool{}
	for _, w := range rec.Wraps {
		have[w.Environment+"/"+w.App+"/"+w.Role+" "+w.KeyID+" "+w.Recipient] = true
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}

	wrapped, skipped, failed := 0, 0, 0
	for _, h := range held {
		// The key is loaded once per address+id, and only when something is
		// actually missing, so a fully-covered keystore never reads a key file
		// at all.
		var key configmgr.EnvKey
		loaded := false
		for _, r := range rec.Recipients {
			if !*replace && have[h.addr.String()+" "+h.id+" "+r.Name] {
				skipped++
				continue
			}
			if *dryRun {
				fmt.Printf("would wrap %s key %s to %s\n", h.addr, h.id, r.Name)
				wrapped++
				continue
			}
			pub, err := configmgr.ParseMachinePublicKey(r.PublicKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skipping recipient %s: its public key does not parse: %v\n", r.Name, err)
				failed++
				continue
			}
			if !loaded {
				if key, err = ks.KeyForID(keyAddr(h.addr), h.id); err != nil {
					// Reported once per address+id, then the whole address is
					// abandoned: every recipient would fail the same way.
					fmt.Fprintf(os.Stderr, "cannot read %s key %s: %v\n", h.addr, h.id, err)
					failed++
					break
				}
				loaded = true
			}
			blob, err := configmgr.WrapEnvKey(pub, h.addr, key)
			if err != nil {
				// No key material in this error path: WrapEnvKey reports the
				// recipient and the failure, never the plaintext.
				fmt.Fprintf(os.Stderr, "wrapping %s key %s to %s: %v\n", h.addr, h.id, r.Name, err)
				failed++
				continue
			}
			var resp apitypes.CMRecoveryWrapResp
			req := apitypes.CMRecoveryWrapReq{
				Environment: h.addr.Environment, App: h.addr.App, Role: h.addr.Role,
				KeyID: h.id, Recipient: r.Name,
				Wrapped: configmgr.EncodeEnvelope(blob), Replace: *replace,
			}
			if err := c.do(http.MethodPost, apitypes.CMPathRecoveryWraps, req, &resp); err != nil {
				fmt.Fprintf(os.Stderr, "storing the wrap for %s key %s to %s: %v\n", h.addr, h.id, r.Name, err)
				failed++
				continue
			}
			if resp.Stored {
				fmt.Printf("wrapped %s key %s to %s\n", h.addr, h.id, r.Name)
				wrapped++
			} else {
				skipped++
			}
		}
	}

	fmt.Println()
	if *dryRun {
		fmt.Printf("Dry run: %d wrap(s) would be written, %d already stored. Nothing was sent.\n", wrapped, skipped)
		return nil
	}
	fmt.Printf("%d wrap(s) written, %d already stored, %d failed.\n", wrapped, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d wrap(s) failed; the custody gap they leave is still open", failed)
	}
	fmt.Println("A stored wrap is not a proved one. Prove at least one before relying on it:")
	fmt.Println("  hz cm recovery verify <environment>/<app>/<role>")
	return nil
}

// --- verify ----------------------------------------------------------------

// cmRecoveryVerify is the command people skip, and the only one that proves
// anything.
//
// A backup nobody has ever restored from is not a backup. Everything else in
// this file produces a blob and reports that it produced one; this opens it.
//
// What it PROVES, given a recovery private key and an address:
//
//   - the stored blob is addressed to this key (its recipient fingerprint is
//     this key's fingerprint), and
//   - this key decrypts it — an AEAD check, so a single flipped bit fails —
//     with the address bound in as additional data, so a blob from a different
//     environment cannot pass for this one, and
//   - the environment key inside is the one the wrap CLAIMS, because a key id
//     is a hash of the key and is recomputed here from what came out.
//
// What it deliberately does NOT prove, and must never be read as proving:
//
//   - that this key is the key hz currently calls current for the address. A
//     rotation legitimately leaves older keys wrapped and verifiable.
//   - that any particular sealed config VALUE opens. That is a different
//     envelope with a different AAD; this proves custody of the key, which is
//     the thing that cannot be reconstructed.
//   - anything about addresses it was not asked about. Custody is per key.
//
// Side effects: none. One GET, then arithmetic. It writes nothing, changes
// nothing on hz, and prints no key material on any path — the answer is "yes"
// or "no" and the identifiers involved.
func cmRecoveryVerify(c *client, args []string) error {
	fs := flag.NewFlagSet("cm recovery verify", flag.ContinueOnError)
	id := fs.String("id", "", "verify only this key id (default: every wrapped key at the address)")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm recovery verify <environment>/<app>/<role> [--id KEYID] < recovery-key.txt")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	if *id != "" {
		if _, err := configmgr.ParseKeyID(*id); err != nil {
			return fmt.Errorf("--id must be a key id: %w", err)
		}
	}

	// stdin, never argv, and never a flag. The private key is the highest-value
	// secret this project has; a --key flag would publish it to every process
	// on the box through /proc for the lifetime of the command.
	if isTTY(os.Stdin) {
		fmt.Fprintln(os.Stderr, "Paste the recovery PRIVATE key, then press ctrl-D:")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
	if err != nil {
		return fmt.Errorf("reading the recovery key from stdin: %w", err)
	}
	priv, err := configmgr.ParseMachinePrivateKey(strings.TrimSpace(string(raw)))
	if err != nil {
		// ParseMachinePrivateKey's error names the prefix and the encoding, not
		// the bytes. Nothing here may echo what was typed.
		return fmt.Errorf("that is not a recovery private key: %w", err)
	}
	mine := configmgr.FingerprintOf(priv.PublicKey())

	rec, err := cmFetchRecovery(c)
	if err != nil {
		return err
	}

	fmt.Printf("Verifying %s against the key whose fingerprint is %s.\n\n", addr, mine)

	var addressed, opened, failures int
	for _, w := range rec.Wraps {
		if w.Environment != addr.Environment || w.App != addr.App || w.Role != addr.Role {
			continue
		}
		if *id != "" && w.KeyID != *id {
			continue
		}
		blob, err := configmgr.DecodeEnvelope(w.Wrapped)
		if err != nil {
			fmt.Printf("  no   key %s (%s): hz's stored blob does not decode: %v\n", w.KeyID, w.Recipient, err)
			failures++
			continue
		}
		header, err := configmgr.ParseEnvelopeHeader(blob)
		if err != nil {
			fmt.Printf("  no   key %s (%s): hz's stored blob is not an envelope: %v\n", w.KeyID, w.Recipient, err)
			failures++
			continue
		}
		// Only the blobs addressed to THIS key are the ones this key is
		// supposed to open. A wrap for another recipient failing would say
		// nothing, and counting it would make every answer "no".
		if header.Recipient != mine {
			continue
		}
		addressed++

		key, err := configmgr.UnwrapEnvKey(priv, addr, blob)
		if err != nil {
			fmt.Printf("  NO   key %s (%s): this key does not open it: %v\n", w.KeyID, w.Recipient, err)
			failures++
			continue
		}
		// The id is a hash of the key, so this catches the case the AEAD cannot:
		// a blob that decrypts perfectly because it was honestly wrapped to this
		// recipient, holding key material that is not the key it is filed under.
		if got := key.ID().String(); got != w.KeyID {
			fmt.Printf("  NO   key %s (%s): opens, but holds key %s — the blob is filed under the wrong id\n",
				w.KeyID, w.Recipient, got)
			failures++
			continue
		}
		fmt.Printf("  yes  key %s (%s): this key opens it.\n", w.KeyID, w.Recipient)
		opened++
	}

	fmt.Println()
	if addressed == 0 {
		fmt.Printf("NO. hz holds no wrap for %s addressed to this key.\n", addr)
		if *id != "" {
			fmt.Printf("Nothing is stored for key %s at that address, or it is wrapped to somebody else.\n", *id)
		}
		fmt.Println("Either this is the wrong recovery key, or the wrap was never written:")
		fmt.Println("  hz cm recovery ls          # which keys are covered, and by whom")
		fmt.Println("  hz cm recovery backfill    # from the machine that holds the environment key")
		return errors.New("no wrap at that address is addressed to this key")
	}
	if failures > 0 {
		fmt.Printf("NO. %d of %d wrap(s) addressed to this key did not verify.\n", failures, addressed)
		fmt.Println("Do not treat this address as recoverable. Re-wrap it from the machine that holds")
		fmt.Println("the environment key and verify again:  hz cm recovery backfill --replace")
		return fmt.Errorf("%d wrap(s) failed to verify", failures)
	}
	fmt.Printf("YES. This key opens %s: %d of %d wrap(s) addressed to it, all verified.\n", addr, opened, addressed)
	fmt.Println()
	fmt.Println("That is custody of the environment key proved, and nothing more: it does not say")
	fmt.Println("this key is the one hz calls current, and it does not test any sealed config")
	fmt.Println("value. Nothing was changed and no key material was printed.")
	return nil
}

// --- shared with `hz cm key new` -------------------------------------------

// cmWrapToRecovery wraps a freshly minted key to every recovery recipient.
//
// This is the automatic half, and it is what makes the scheme hold: a new
// environment is covered because nobody had to remember a step. It is called
// from the one place an environment key comes into existence on a client.
//
// The key is already on disk when this runs, so a failure here is never a lost
// key — it is a key with no custody, which is the state this feature exists to
// make impossible to sit in unnoticed. The caller reports it loudly and names
// the backfill that closes it.
func cmWrapToRecovery(c *client, addr configmgr.EnvKeyAddr, keyID string, key configmgr.EnvKey) (int, error) {
	rec, err := cmFetchRecovery(c)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rec.Recipients {
		pub, err := configmgr.ParseMachinePublicKey(r.PublicKey)
		if err != nil {
			return n, fmt.Errorf("recovery recipient %s has an unusable public key: %w", r.Name, err)
		}
		blob, err := configmgr.WrapEnvKey(pub, addr, key)
		if err != nil {
			return n, fmt.Errorf("wrapping to recovery recipient %s: %w", r.Name, err)
		}
		req := apitypes.CMRecoveryWrapReq{
			Environment: addr.Environment, App: addr.App, Role: addr.Role,
			KeyID: keyID, Recipient: r.Name, Wrapped: configmgr.EncodeEnvelope(blob),
		}
		if err := c.do(http.MethodPost, apitypes.CMPathRecoveryWraps, req, nil); err != nil {
			return n, fmt.Errorf("storing the wrap for recovery recipient %s: %w", r.Name, err)
		}
		n++
	}
	return n, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
