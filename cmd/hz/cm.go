package main

import (
	"bufio"
	"crypto/ecdh"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The config-manager operator surface, and the reason it lives in a CLI at all.
//
// An adversarial review of the browser ceremony (plan/config-manager.md, hole
// 9) settled this: a page served by hz cannot defend against hz. Compromise hz,
// edit one line of the bundle, and the approval page POSTs the pasted key
// before wrapping it. Every mitigation the design named — a mandatory
// fingerprint compare, an explicit lock, no localStorage — would run inside the
// attacker's own code. `hz` is a locally installed binary, updated
// deliberately, outside hz's control at the moment it runs.
//
// So the invariant this file exists to hold is exactly one sentence:
//
//	NO environment key, in any form, is ever sent to hz.
//
// hz receives wrapped blobs it cannot open, sealed values it cannot read, and
// key IDs, which are public names. If a request body built anywhere under this
// prefix carries key material, the feature is broken, not merely buggy.
//
// A second rule, from the same document: a key never appears in argv, because
// /proc is world-readable and `ps` is how it leaks. Keys come from the local
// keystore, or from stdin. There is no --key flag and there must never be one.

// cmAPI is the admin route prefix. Every path below is assembled from it.
//
// Note on where these shapes come from: internal/apitypes fixes the request and
// response bodies, and this file treats them as given. The paths under this
// prefix are still spelled out here rather than shared, which is the same shape
// that made five endpoints unreachable — see cm_routes_test.go. The ones that
// were fixed moved to configmgr constants (apitypes.CMPath*); anything added
// under this prefix should be declared there too, not assembled from cmAPI.
const cmAPI = "/api/v1/cm"

func runCM(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cm subcommand required: key | recovery | machines | pending | approve | deny | remove | promote | show | resolve")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "key":
		return runCMKey(c, rest)
	case "recovery":
		return runCMRecovery(c, rest)
	case "machines":
		return cmMachines(c, rest)
	case "pending":
		return cmPending(c, rest)
	case "approve":
		return cmApprove(c, rest)
	case "deny":
		return cmDeny(c, rest)
	case "remove":
		return cmRemove(c, rest)
	case "promote":
		return cmPromote(c, rest)
	case "show":
		return cmShow(c, rest)
	case "resolve":
		return cmResolve(c, rest)
	default:
		return fmt.Errorf("unknown cm subcommand: %s", sub)
	}
}

// --- addresses -------------------------------------------------------------

// parseCMAddr reads the <environment>/<app>/<role> form every cm command takes.
//
// Only the shape is checked here. The charset is the keystore's business
// (configmgr.ErrBadName), and it is checked there because that is where a bad
// segment would become a path — validating it twice would let the two
// definitions drift and the weaker one win.
func parseCMAddr(s string) (configmgr.EnvKeyAddr, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return configmgr.EnvKeyAddr{}, fmt.Errorf("address %q must be <environment>/<app>/<role>, e.g. staging/redline/app", s)
	}
	for i, what := range []string{"environment", "app", "role"} {
		if parts[i] == "" {
			return configmgr.EnvKeyAddr{}, fmt.Errorf("address %q has an empty %s", s, what)
		}
	}
	return configmgr.EnvKeyAddr{Environment: parts[0], App: parts[1], Role: parts[2]}, nil
}

// splitCMPositional pulls a leading positional off the argument list.
//
// Go's flag package stops at the first non-flag token, so `hz cm deny reg-1
// --reason=x` would silently drop --reason. The repo's splitNameArgs solves
// that by taking the first non-flag token anywhere, which misreads the value of
// a space-separated flag (`--label 2026-01 prod/redline/app` would take
// "2026-01" as the address). These commands take exactly one positional and it
// is a required address or id, so the stricter rule is better: only a token in
// FIRST position is the positional. A flags-first invocation still works, with
// the positional falling out of fs.Args().
func splitCMPositional(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// keyAddr projects a grant address onto the keystore's address type. The
// keystore ignores Addr.Key — the key tree is (environment, app, role) and the
// config key name is bound into the AEAD, not into a path.
func keyAddr(a configmgr.EnvKeyAddr) configmgr.Addr {
	return configmgr.Addr{Environment: a.Environment, App: a.App, Role: a.Role}
}

// valueAddr is the address one config VALUE is sealed at.
func valueAddr(a configmgr.EnvKeyAddr, key string) configmgr.Addr {
	return configmgr.Addr{Environment: a.Environment, App: a.App, Role: a.Role, Key: key}
}

func addrOfConfig(cfg apitypes.CMConfigResp) configmgr.EnvKeyAddr {
	return configmgr.EnvKeyAddr{Environment: cfg.Environment, App: cfg.App, Role: cfg.Role}
}

// --- the current-key pointer ----------------------------------------------

// cmCurrentKey asks hz which key it calls current at an address.
//
// The answer is advisory in one direction only. The keystore refuses to SEAL
// when its newest key and this pointer disagree, and only warns on OPEN; see
// configmgr.Keystore.SealingKey for why the asymmetry is the whole point.
// Nothing here obeys the pointer — obeying it would let a compromised hz pin
// the fleet to a key it had already stolen.
//
// A 404 and an empty KeyID both mean "no pointer set", which is the ordinary
// state of a brand-new address. Any other failure propagates: a promotion that
// cannot reach hz cannot post a config either, so swallowing the error would
// only move the failure later and make it less legible.
func cmCurrentKey(c *client, addr configmgr.EnvKeyAddr) (configmgr.CurrentKey, error) {
	q := url.Values{
		apitypes.CMQueryEnv:  {addr.Environment},
		apitypes.CMQueryApp:  {addr.App},
		apitypes.CMQueryRole: {addr.Role},
	}
	var resp apitypes.CMCurrentKeyResp
	if err := c.do(http.MethodGet, cmAPI+"/current-key?"+q.Encode(), nil, &resp); err != nil {
		var se *apiStatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return configmgr.CurrentKeyUnavailable(), nil
		}
		return configmgr.CurrentKey{}, err
	}
	if resp.KeyID == "" {
		return configmgr.CurrentKeyUnavailable(), nil
	}
	id, err := configmgr.ParseKeyID(resp.KeyID)
	if err != nil {
		return configmgr.CurrentKey{}, fmt.Errorf("hz names %q current for %s, which is not a key id: %w", resp.KeyID, addr, err)
	}
	return configmgr.CurrentKeyIs(id), nil
}

// cmSealingKey resolves the key to seal with, consulting hz's pointer first.
//
// The keystore's refusal is the interesting path, not the exception: it fires
// whenever the newest key on disk is not the one hz calls current, which is
// exactly what a dropped-in key file looks like. Surfacing it as a bare
// "refusing to seal" would leave an operator with no move, so the refusal is
// re-wrapped with the two things that resolve it.
func cmSealingKey(ks *configmgr.Keystore, c *client, addr configmgr.EnvKeyAddr) (configmgr.EnvKey, configmgr.KeyInfo, error) {
	cur, err := cmCurrentKey(c, addr)
	if err != nil {
		return configmgr.EnvKey{}, configmgr.KeyInfo{}, err
	}
	k, info, err := ks.SealingKey(keyAddr(addr), cur)
	if err != nil {
		return configmgr.EnvKey{}, configmgr.KeyInfo{}, cmSealAdvice(addr, cur, err)
	}
	return k, info, nil
}

func cmSealAdvice(addr configmgr.EnvKeyAddr, cur configmgr.CurrentKey, err error) error {
	switch {
	case errors.Is(err, configmgr.ErrNoSuchKey):
		return fmt.Errorf("%w\n"+
			"  this machine holds no key for %s.\n"+
			"  import the one from your password manager:  hz cm key import %s\n"+
			"  or mint the first key for the address:       hz cm key new %s",
			err, addr, addr, addr)
	case errors.Is(err, configmgr.ErrRefuseToSeal):
		return fmt.Errorf("%w\n"+
			"  the keystore refuses to seal while its newest key and hz's pointer disagree.\n"+
			"  that is deliberate: created_at lives in a file anyone who can write the\n"+
			"  keystore can set, and hz's pointer is the only thing that arbitrates it.\n"+
			"  hz calls %s current for %s.\n"+
			"  if that key is the right one:  hz cm key import %s\n"+
			"  if the key you hold is newer:  hz cm key current %s --set <keyid>",
			err, cur, addr, addr, addr)
	case errors.Is(err, configmgr.ErrInsecureKey):
		return fmt.Errorf("%w\n  fix the mode and ownership (0600, owned by you, in a directory nobody else can write) and retry", err)
	}
	return err
}

// --- the approval queue ----------------------------------------------------

func cmPending(c *client, args []string) error {
	fs := flag.NewFlagSet("cm pending", flag.ContinueOnError)
	all := fs.Bool("all", false, "list every registration, not only pending ones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := cmAPI + "/registrations?" + apitypes.CMQueryState + "=" + configmgr.StatePending
	if *all {
		path = cmAPI + "/registrations"
	}
	var rows []apitypes.CMRegistrationResp
	if err := c.do(http.MethodGet, path, nil, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No registrations waiting.")
		return nil
	}
	fmt.Printf("%-38s  %-20s  %-30s  %-10s  %s\n", "ID", "MACHINE", "ADDRESS", "STATE", "VERSION")
	for _, r := range rows {
		addr := configmgr.EnvKeyAddr{Environment: r.Environment, App: r.App, Role: r.Role}
		fmt.Printf("%-38s  %-20s  %-30s  %-10s  %s\n", r.ID, r.MachineName, addr, r.State, r.Version)
		if r.EnrolledEnvironment != "" && r.EnrolledEnvironment != r.Environment {
			fmt.Printf("%-38s  ! enrolled as %q, now asking for %q\n", "", r.EnrolledEnvironment, r.Environment)
		}
	}
	// The fingerprint is deliberately not a column. See cmApprove.
	fmt.Println("\nApprove one with 'hz cm approve <id>'. You will need the fingerprint the box printed.")
	return nil
}

func cmFetchRegistration(c *client, id string) (apitypes.CMRegistrationResp, error) {
	var r apitypes.CMRegistrationResp
	err := c.do(http.MethodGet, cmAPI+"/registrations/"+url.PathEscape(id), nil, &r)
	return r, err
}

// cmPublicKeyResp is the machine public key the wrap runs against.
//
// It is NOT in internal/apitypes: CMRegistrationResp carries the fingerprint
// and no key. The ceremony needs the key itself, so this client fetches it from
// its own endpoint and derives the fingerprint from the bytes it is about to
// wrap to, rather than trusting a fingerprint field computed by hz. That is
// strictly better than a field would have been — a fingerprint that hz computes
// over a key hz chose proves nothing, whereas one derived here at least names
// the key this process actually used.
type cmPublicKeyResp struct {
	PublicKey string `json:"publicKey"`
}

func cmFetchPublicKey(c *client, id string) (*ecdh.PublicKey, error) {
	var resp cmPublicKeyResp
	if err := c.do(http.MethodGet, cmAPI+"/registrations/"+url.PathEscape(id)+"/public-key", nil, &resp); err != nil {
		return nil, err
	}
	if resp.PublicKey == "" {
		return nil, fmt.Errorf("hz returned no public key for registration %s", id)
	}
	return configmgr.ParseMachinePublicKey(resp.PublicKey)
}

// --- the ceremony ----------------------------------------------------------

// cmApprove wraps an environment key to one machine's public key.
//
// The steps are fixed and the order matters:
//
//  1. Fetch the registration and the public key hz holds for it.
//  2. Make the operator TYPE the fingerprint the BOX printed, and compare it
//     against the fingerprint derived from those key bytes. This is a blocking
//     step with no default and no --yes, because it is the only thing standing
//     between an approval and hz substituting a public key whose private half
//     it holds (hole 1). Until the enrollment-token HMAC lands (Phase 2.9) it
//     is the entire defence.
//  3. Load the environment key from the LOCAL keystore, for the address taken
//     from the registration.
//  4. Wrap it to that public key at EnvKeyAddr{environment, app, role}.
//  5. POST the wrapped blob and nothing else.
//
// What is deliberately NOT printed before the prompt: hz's fingerprint. Showing
// the expected answer next to the question turns typing it into transcription,
// and a human transcribing reliably checks the first group and the last. The
// operator reads the fingerprint off the box's own console. It is shown
// afterwards — on a match, to confirm, and on a mismatch, so the difference is
// visible.
func cmApprove(c *client, args []string) error {
	fs := flag.NewFlagSet("cm approve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: hz cm approve <registration-id>\n\n"+
			"Wraps this machine's environment key to the box's public key and sends the\n"+
			"wrapped blob. The key itself never leaves this process.\n\n"+
			"You will be asked to type the fingerprint the BOX printed. Read it from the\n"+
			"box's console, not from hz.\n")
	}
	id, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" || fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("exactly one registration id is required")
	}

	reg, err := cmFetchRegistration(c, id)
	if err != nil {
		return err
	}
	if reg.State != configmgr.StatePending {
		return fmt.Errorf("registration %s is %s, not pending; approving it again would grant a key to a machine somebody already decided about", id, reg.State)
	}
	addr := configmgr.EnvKeyAddr{Environment: reg.Environment, App: reg.App, Role: reg.Role}

	pub, err := cmFetchPublicKey(c, id)
	if err != nil {
		return err
	}
	derived := configmgr.FingerprintOf(pub)

	// hz's own fingerprint field must agree with the key it served. It is not a
	// security check — hz controls both sides — but a disagreement means hz is
	// internally inconsistent, and wrapping to a key its own queue does not
	// describe is not something to do quietly.
	if reg.Fingerprint != "" {
		claimed, err := configmgr.ParseFingerprint(reg.Fingerprint)
		if err != nil {
			return fmt.Errorf("registration %s carries an unreadable fingerprint %q: %w", id, reg.Fingerprint, err)
		}
		if claimed != derived {
			return fmt.Errorf("refusing: hz's queue says registration %s has fingerprint %s, but the public key it served fingerprints as %s — hz is describing one key and serving another", id, claimed, derived)
		}
	}

	fmt.Printf("Registration %s\n", reg.ID)
	fmt.Printf("  machine      %s (%s)\n", orDash(reg.MachineName), orDash(reg.MachineID))
	fmt.Printf("  address      %s\n", addr)
	fmt.Printf("  version      %s\n", orDash(reg.Version))
	if reg.EnrolledEnvironment != "" && reg.EnrolledEnvironment != reg.Environment {
		fmt.Printf("  ! enrolled as %q but asking for %q\n", reg.EnrolledEnvironment, reg.Environment)
	}
	fmt.Printf("  first seen   %s\n", orDash(reg.CreatedAt))
	fmt.Println()
	fmt.Println("The box printed a fingerprint when it generated its key. Read it off THAT")
	fmt.Println("console and type it here. Do not copy it from hz: hz substituting its own")
	fmt.Println("public key is the attack this step exists to catch, and it would have")
	fmt.Println("substituted the fingerprint too.")
	fmt.Print("\nfingerprint: ")

	typed, err := readLine(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading the fingerprint: %w", err)
	}
	if strings.TrimSpace(typed) == "" {
		return fmt.Errorf("refusing: no fingerprint typed. Nothing was sent")
	}
	got, err := configmgr.ParseFingerprint(typed)
	if err != nil {
		// One shot, no retry loop. A retry loop invites typing until something
		// passes, which is the shape of the mistake this step is guarding.
		return fmt.Errorf("refusing: %q is not a fingerprint (%v). Nothing was sent", strings.TrimSpace(typed), err)
	}
	if got != derived {
		return fmt.Errorf("REFUSED: the fingerprint you typed does not match the public key hz served.\n"+
			"  you typed  %s\n"+
			"  hz served  %s\n"+
			"Nothing was sent. Do not retry until you know why they differ: this is either a\n"+
			"typo or hz handing you a public key whose private half it holds", got, derived)
	}
	fmt.Printf("fingerprint %s matches the key hz served.\n\n", derived)

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	key, info, err := cmSealingKey(ks, c, addr)
	if err != nil {
		return err
	}

	wrapped, err := configmgr.WrapEnvKey(pub, addr, key)
	if err != nil {
		return fmt.Errorf("wrapping the key for %s: %w", addr, err)
	}

	// The only thing crossing the wire: an envelope only that box can open, and
	// the id of the key inside it. The key itself has not left this process and
	// there is no field here that could carry it.
	req := apitypes.CMApproveReq{
		WrappedEnvKey: configmgr.EncodeEnvelope(wrapped),
		WrapKeyID:     info.ID.String(),
	}
	if err := c.do(http.MethodPost, cmAPI+"/registrations/"+url.PathEscape(id)+"/approve", req, nil); err != nil {
		return err
	}
	fmt.Printf("Approved %s: key %s (%s) wrapped to %s at %s.\n", reg.ID, info.ID, info.Label, derived, addr)
	return nil
}

func cmDeny(c *client, args []string) error {
	fs := flag.NewFlagSet("cm deny", flag.ContinueOnError)
	reason := fs.String("reason", "", "why this registration is refused (required)")
	id, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm deny <registration-id> --reason=\"...\"")
	}
	if strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("--reason is required: a denial with no stated cause is indistinguishable from a mistake six months later")
	}
	if err := c.do(http.MethodPost, cmAPI+"/registrations/"+url.PathEscape(id)+"/deny", apitypes.CMDenyReq{Reason: *reason}, nil); err != nil {
		return err
	}
	fmt.Printf("Denied %s.\n", id)
	// Stated because the UI must not claim otherwise either: hz dropping its
	// copy of a wrapped key takes nothing off a box that already unwrapped one.
	fmt.Println("This clears hz's copy of any wrapped key. It is NOT a revocation: a box that was")
	fmt.Println("ever approved already holds the key unwrapped on its own disk.")
	return nil
}

// --- small shared helpers --------------------------------------------------

// isTTY reports whether f is a terminal.
//
// A character device is the whole test, and it is done with the standard
// library rather than golang.org/x/term so cmd/hz keeps building with nothing
// new in go.mod. It over-reports for /dev/null, which is harmless: the checks
// that use it are about not spraying secrets into a pipe or a log, and nobody
// redirects a key export to /dev/null on purpose.
func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readLine reads one line, without echo suppression. Everything read this way
// is public — a fingerprint, a key id — and the one genuinely secret thing read
// from stdin (hz cm key import) is a whole-stream read, not a prompt.
func readLine(r *os.File) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
