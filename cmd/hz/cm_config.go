package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Reading and moving configs: show (decrypt locally), resolve (what a box would
// get, and what it shadowed), and promote (open under the source key, re-seal
// under the target's).
//
// All three decrypt on this machine. hz holds no key and never sees a
// plaintext, which is also why the admin UI cannot show a value: it would have
// to be given a key, and the decision was that it never is.

// verRange renders a config's version range for a human. An empty max is
// open-ended rather than unbounded-by-omission.
func verRange(minVer, maxVer string) string {
	if maxVer == "" {
		return minVer + "–∞"
	}
	return minVer + "–" + maxVer
}

// zero wipes a decrypted value. Everything read here is plaintext that exists
// in this process and nowhere else, and it stops existing as soon as it has
// been printed.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// fromConfig renders " (from cfg-xyz)" or nothing, so a provenance clause reads
// as a sentence whether or not the id is there.
func fromConfig(id string) string {
	if id == "" {
		return ""
	}
	return " of " + id
}

func cmFetchConfig(c *client, id string) (apitypes.CMConfigResp, error) {
	var cfg apitypes.CMConfigResp
	err := c.do(http.MethodGet, cmAPI+"/configs/"+url.PathEscape(id), nil, &cfg)
	return cfg, err
}

// --- show ------------------------------------------------------------------

// cmShow decrypts a config and prints it.
//
// A partial decrypt fails the whole config. Printing the values that opened and
// a note about the ones that did not would hand an operator a config that looks
// complete and is not, and "which keys did I actually see" is not a question
// anybody asks of output they have already read.
func cmShow(c *client, args []string) error {
	fs := flag.NewFlagSet("cm show", flag.ContinueOnError)
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm show <config-id>")
	}
	cfg, err := cmFetchConfig(c, pos)
	if err != nil {
		return err
	}
	addr := addrOfConfig(cfg)

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	// Advisory only, on the OPEN side. Old ciphertext under an old key is the
	// normal state during a rotation; refusing it would make a rotation break
	// every read that had not yet been re-sealed.
	cur, err := cmCurrentKey(c, addr)
	if err != nil {
		return err
	}

	opened, err := cmOpenValues(ks, addr, cfg.Values, cur)
	if err != nil {
		return err
	}
	defer func() {
		for i := range opened {
			zero(opened[i].plain)
		}
	}()

	if !isTTY(os.Stdout) {
		fmt.Fprintf(os.Stderr, "# writing decrypted values to something that is not a terminal\n")
	}
	fmt.Printf("# config %s  %s  %s  sequence %d  blessed %s by %s\n",
		cfg.ID, addr, verRange(cfg.MinVer, cfg.MaxVer), cfg.Sequence, orDash(cfg.CreatedAt), orDash(cfg.CreatedBy))
	for _, v := range opened {
		origin := v.value.Origin
		if v.value.SourceConfigID != "" {
			origin += " from " + v.value.SourceConfigID
		}
		fmt.Printf("# %s, %s\n%s=%s\n", v.value.Binding, origin, v.value.Key, v.plain)
	}
	return nil
}

type openedValue struct {
	value apitypes.CMConfigValueResp
	plain []byte
}

// cmOpenValues decrypts every value of a config, or returns the first failure.
//
// The address handed to Open comes from the config's own (environment, app,
// role) and the value's key name, which is the address the AEAD authenticates.
// A blob hz filed under the wrong key name, or promoted from another
// environment without a re-seal, fails here rather than opening into the wrong
// slot.
func cmOpenValues(ks *configmgr.Keystore, addr configmgr.EnvKeyAddr, values []apitypes.CMConfigValueResp, cur configmgr.CurrentKey) ([]openedValue, error) {
	out := make([]openedValue, 0, len(values))
	fail := func(err error) ([]openedValue, error) {
		for i := range out {
			zero(out[i].plain)
		}
		return nil, err
	}
	for _, v := range values {
		// Two different reasons a key has no bytes, and telling an operator the
		// wrong one sends them to the wrong fix. Awaiting means nobody has
		// supplied a value yet; tombstoned means one existed and was destroyed.
		if v.Origin == configmgr.OriginAwaiting {
			return fail(fmt.Errorf("%s is declared in %s and awaiting a value; it was blanked by a promotion%s and nobody has answered it",
				v.Key, addr.Environment, fromConfig(v.SourceConfigID)))
		}
		if v.TombstonedAt != "" || v.Sealed == "" {
			return fail(fmt.Errorf("%s was tombstoned at %s by %s; its bytes are gone and the config cannot be read whole",
				v.Key, orDash(v.TombstonedAt), orDash(v.TombstonedBy)))
		}
		key, err := ks.KeyForID(keyAddr(addr), v.KeyID)
		if err != nil {
			return fail(fmt.Errorf("%s (sealed under key %s): %w", v.Key, v.KeyID, err))
		}
		if id, perr := configmgr.ParseKeyID(v.KeyID); perr == nil {
			if stale := configmgr.StaleKey(id, cur); stale != nil {
				fmt.Fprintf(os.Stderr, "# %s: %v\n", v.Key, stale)
			}
		}
		envelope, err := configmgr.DecodeEnvelope(v.Sealed)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", v.Key, err))
		}
		pt, err := configmgr.Open(key, valueAddr(addr, v.Key), envelope)
		if err != nil {
			return fail(fmt.Errorf("%s: %w\n  the blob does not authenticate at %s. Either the key is wrong, or hz served a value that belongs somewhere else",
				v.Key, err, valueAddr(addr, v.Key)))
		}
		out = append(out, openedValue{value: v, plain: pt})
	}
	return out, nil
}

// --- resolve ---------------------------------------------------------------

// cmResolve answers what a box at a version would get — and what it would not.
//
// Shadowed is the half that matters. Several open-ended configs at one address
// is the normal shape of supersession, so resolution always has candidates it
// passed over; silent resolution is fine only when you can ask what it resolved
// to.
func cmResolve(c *client, args []string) error {
	fs := flag.NewFlagSet("cm resolve", flag.ContinueOnError)
	version := fs.String("version", "", "the app version to resolve for (required)")
	asJSON := fs.Bool("json", false, "output raw JSON")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm resolve <environment>/<app>/<role> --version=<v>")
	}
	if *version == "" {
		return fmt.Errorf("--version is required: resolution is a range containment test, and there is no default version to test")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	q := url.Values{
		apitypes.CMQueryEnv:     {addr.Environment},
		apitypes.CMQueryApp:     {addr.App},
		apitypes.CMQueryRole:    {addr.Role},
		apitypes.CMQueryVersion: {*version},
	}
	var resp apitypes.CMResolveResp
	if err := c.do(http.MethodGet, cmAPI+"/resolve?"+q.Encode(), nil, &resp); err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if resp.Error != "" {
		// A named failure, never a hang and never a wait-for-approval: nothing
		// satisfying the range is a different thing from nothing being blessed.
		return fmt.Errorf("%s at %s: %s", addr, *version, resp.Error)
	}
	if resp.Winner == nil {
		return fmt.Errorf("no config satisfies %s for %s", *version, addr)
	}
	fmt.Printf("WINNER  %s  %s  sequence %d  blessed %s by %s\n",
		resp.Winner.ID, verRange(resp.Winner.MinVer, resp.Winner.MaxVer), resp.Winner.Sequence,
		orDash(resp.Winner.CreatedAt), orDash(resp.Winner.CreatedBy))
	awaiting := 0
	for _, v := range resp.Winner.Values {
		note := v.Origin
		if v.Origin == configmgr.OriginAwaiting {
			// Said out loud on the line that resolves, because this config is
			// what a box would GET and it is not servable. "awaiting" as a bare
			// origin column reads like a state; "AWAITING A VALUE" reads like the
			// blocker it is.
			note = "AWAITING A VALUE" + fromConfig(v.SourceConfigID)
			awaiting++
		}
		fmt.Printf("  %-28s %-10s %s\n", v.Key, v.Binding, note)
	}
	if awaiting > 0 {
		fmt.Printf("\n%d key(s) are declared here with no value. A box at this address will be\n", awaiting)
		fmt.Printf("refused this config by name until each one is answered in %s.\n", addr.Environment)
	}
	if len(resp.Shadowed) == 0 {
		fmt.Println("\nNothing shadowed.")
		return nil
	}
	fmt.Printf("\nSHADOWED (%d)\n", len(resp.Shadowed))
	for _, s := range resp.Shadowed {
		fmt.Printf("  %s  %s  sequence %d\n", s.ID, verRange(s.MinVer, s.MaxVer), s.Sequence)
	}
	return nil
}

// --- promote ---------------------------------------------------------------

// cmPromote opens a config under the source environment's key and re-seals it
// under the target's.
//
// A value cannot be copied across environments: the target's copy must be
// readable by the target's key, and the source's ciphertext is not. So the
// cycle is decrypt-then-encrypt, both keys come from the LOCAL keystore, and
// the address bound into the AAD changes with the environment. The plaintext
// exists in this process, momentarily, and nowhere else — hz sees a read of one
// ciphertext and a write of another, both endpoints it has anyway.
//
// Three things happen to a key, and exactly one of them happens to each:
//
//	copied   invariant — opened under the source key, re-sealed under the target's
//	carried  environment-bound and ALREADY answered in the target — ciphertext,
//	         untouched: same address, same key, nothing to open
//	blanked  environment-bound and NOT answered in the target — declared with no
//	         value, so the target's config says "this key exists here and is
//	         waiting", and the pull refuses to serve it until someone answers
//
// Blanked is not absent, and that distinction is the whole reason it is a row
// rather than an omission. A promoted config that simply left PUBLIC_URL out
// would leave prod holding a config in which the key had never been heard of —
// indistinguishable from one nobody declared, and the app falls back to its
// compiled default. That is the founding bug (plan/architecture.md, goal
// property 6), and promotion is exactly where it would be reintroduced.
//
// DRY RUN IS THE DEFAULT. This is an irreversible, cross-environment operation
// on production credentials; the default has to be to show, not to do. The full
// cycle still runs — the gate, the decrypt, the re-seal — because a dry run that
// skipped them would tell you nothing about whether the real one would work.
// Only the POST is withheld.
func cmPromote(c *client, args []string) error {
	fs := flag.NewFlagSet("cm promote", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: hz cm promote <config-id> --to=<environment> [--execute]\n\n"+
			"Copies the invariants, carries the environment-bound keys the target has\n"+
			"already answered, and blanks the ones it has not. Prints the plan and posts\n"+
			"NOTHING unless --execute is given.\n\n"+
			"  --to        target environment (required)\n"+
			"  --project   narrow the target when two projects declare that name\n"+
			"  --blank     acknowledge that environment-bound keys will be left unanswered\n"+
			"  --force     promote against the ladder (a lateral or downward rung)\n"+
			"  --execute   actually post the promoted config\n")
	}
	to := fs.String("to", "", "target environment (required)")
	project := fs.String("project", "", "project declaring the target environment, when the name is ambiguous")
	allowBlank := fs.Bool("blank", false, "leave environment-bound keys the target has not answered declared-but-blank")
	force := fs.Bool("force", false, "promote even though the target is not above the source by posture")
	execute := fs.Bool("execute", false, "post the promoted config; without it this is a dry run")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm promote <config-id> --to=<environment> [--execute]")
	}
	if *to == "" {
		return fmt.Errorf("--to is required")
	}
	src, err := cmFetchConfig(c, pos)
	if err != nil {
		return err
	}
	srcAddr := addrOfConfig(src)
	if srcAddr.Environment == *to {
		return fmt.Errorf("%s is already in %s", src.ID, *to)
	}
	dstAddr := configmgr.EnvKeyAddr{Environment: *to, App: src.App, Role: src.Role}

	// The gate, first and separately. It asks only whether a key is BOUND in
	// the target, never what it holds, which is why it survives hz reading
	// nothing — and it answers the declared promotion edge, which needs no
	// values either.
	gq := url.Values{apitypes.CMQueryConfigID: {src.ID}, apitypes.CMQueryTarget: {*to}}
	if *project != "" {
		gq.Set(apitypes.CMQueryProject, *project)
	}
	var gate apitypes.CMPromotionGateResp
	if err := c.do(http.MethodGet, cmAPI+"/promote/gate?"+gq.Encode(), nil, &gate); err != nil {
		return err
	}
	if err := cmCheckEdge(gate.Edge, srcAddr.Environment, *to, *force); err != nil {
		return err
	}

	// What the promotion would do, decided entirely from NAMES. None of this
	// needs a key, so the plan is computed and printed — and can be refused —
	// before a single value is opened. A promotion that will not happen must not
	// decrypt anything on the way to saying so.
	//
	// The gate names what carries, and nothing outside that list does. hz can
	// only cause an OMISSION this way, never an injection — every carried value
	// is opened under the source key below and re-sealed under the target's —
	// and an omission is visible in this plan and in the posted config.
	promotes := map[string]bool{}
	for _, k := range gate.Promotes {
		promotes[k] = true
	}
	var carrying []apitypes.CMConfigValueResp
	for _, v := range src.Values {
		if v.Binding == configmgr.BindingInvariant && promotes[v.Key] {
			carrying = append(carrying, v)
		}
	}

	// Blanks: environment-bound in the source, unanswered in the target. They are
	// declared with no ciphertext and no sealing key — there is nothing to seal,
	// because nobody has supplied anything to seal. The source config is their
	// provenance: it is the promotion that declared them.
	blanks := append([]string(nil), gate.Blocked...)
	sort.Strings(blanks)

	// The target's already-answered environment values, carried forward
	// untouched. Same address and same key, so this is a ciphertext copy: no key
	// is used and no plaintext exists for them at any point here.
	bound, err := cmBoundEnvValues(c, dstAddr, src.MinVer)
	if err != nil {
		return err
	}

	// The plan. Names only — no value from either environment appears here, and a
	// dry run is the thing an operator is most likely to paste into a ticket.
	fmt.Printf("promote %s (%s) -> %s\n", src.ID, srcAddr, dstAddr)
	fmt.Printf("  versions   %s, carried unchanged from the source\n", verRange(src.MinVer, src.MaxVer))
	for _, v := range carrying {
		fmt.Printf("  copied     %-28s invariant, re-sealed under %s's key\n", v.Key, *to)
	}
	for _, v := range bound {
		fmt.Printf("  carried    %-28s env, already answered in %s\n", v.Key, *to)
	}
	for _, key := range blanks {
		fmt.Printf("  BLANKED    %-28s env, declared in %s and awaiting a value\n", key, *to)
	}

	if len(blanks) > 0 {
		fmt.Printf("\n%d key(s) must be answered in %s before any box there can boot.\n", len(blanks), *to)
		fmt.Println("They are DECLARED, not dropped: hz records the name and refuses to serve the")
		fmt.Println("config until each one has a value, rather than letting the app default it.")
		if !*allowBlank {
			return fmt.Errorf("BLOCKED: %s has no value bound for %s\n"+
				"  promotion is a gate, not a copy: an environment-bound key is born in its own\n"+
				"  environment and cannot travel. Bind them there first, then promote — or pass\n"+
				"  --blank to promote now and leave them declared-but-unanswered",
				*to, strings.Join(blanks, ", "))
		}
	}
	if len(carrying) == 0 {
		return fmt.Errorf("the gate names nothing to promote out of %s; a promotion that carries no value is a no-op, not a release step", src.ID)
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	srcCur, err := cmCurrentKey(c, srcAddr)
	if err != nil {
		return err
	}
	opened, err := cmOpenValues(ks, srcAddr, carrying, srcCur)
	if err != nil {
		return fmt.Errorf("opening %s under %s's key: %w", src.ID, srcAddr.Environment, err)
	}
	defer func() {
		for i := range opened {
			zero(opened[i].plain)
		}
	}()

	// Target side: seal under the target's current key.
	dstKey, dstInfo, err := cmSealingKey(ks, c, dstAddr)
	if err != nil {
		return fmt.Errorf("sealing for %s: %w", dstAddr, err)
	}
	values := make([]apitypes.CMConfigValueReq, 0, len(opened)+len(bound)+len(blanks))
	for _, v := range opened {
		values = append(values, apitypes.CMConfigValueReq{
			Key:     v.value.Key,
			Binding: configmgr.BindingInvariant,
			// The key name is not a parameter of promote, but it IS bound into
			// each value's AAD by this re-seal. Without it hz could serve one
			// key's blob in another's slot of the same config and it would
			// authenticate.
			Sealed:         configmgr.EncodeEnvelope(configmgr.Seal(dstKey, valueAddr(dstAddr, v.value.Key), v.plain)),
			KeyID:          dstInfo.ID.String(),
			SourceConfigID: src.ID,
		})
	}
	for _, v := range bound {
		values = append(values, apitypes.CMConfigValueReq{
			Key:            v.Key,
			Binding:        configmgr.BindingEnv,
			Sealed:         v.Sealed,
			KeyID:          v.KeyID,
			SourceConfigID: v.SourceConfigID,
		})
	}
	for _, key := range blanks {
		values = append(values, apitypes.CMConfigValueReq{
			Key:            key,
			Binding:        configmgr.BindingEnv,
			Origin:         configmgr.OriginAwaiting,
			SourceConfigID: src.ID,
		})
	}
	fmt.Printf("\n%d invariant(s) opened under %s's key and re-sealed under %s.\n",
		len(opened), srcAddr.Environment, dstInfo.ID)

	if !*execute {
		fmt.Println("\nDry run: nothing was posted. Re-run with --execute to promote.")
		return nil
	}

	var resp apitypes.CMConfigResp
	req := apitypes.CMCreateConfigReq{
		Environment: dstAddr.Environment,
		App:         dstAddr.App,
		Role:        dstAddr.Role,
		// The range travels VERBATIM, and that is a decision rather than a
		// default. A range says which APP VERSIONS a config is valid for, which
		// is a fact about the app and not about the environment it runs in, so a
		// promotion has no new information with which to change it. Widening it
		// (dropping a closed max_ver) would assert coverage nobody blessed;
		// narrowing it would leave versions at the target resolving to nothing,
		// which fails at some box's next restart rather than here. Ranges are
		// immutable after blessing, so a different range is a different bless.
		MinVer: src.MinVer,
		MaxVer: src.MaxVer,
		Values: values,
	}
	if err := c.do(http.MethodPost, cmAPI+"/configs", req, &resp); err != nil {
		return err
	}
	fmt.Printf("blessed %s in %s (sequence %d).\n", resp.ID, *to, resp.Sequence)
	if len(blanks) > 0 {
		fmt.Printf("%s is NOT usable yet: %s await values.\n", *to, strings.Join(blanks, ", "))
	}
	return nil
}

// cmCheckEdge refuses a promotion the declared environments do not allow.
//
// A NIL edge is a refusal, not a pass. hz answering the gate without answering
// the edge means the check never ran, and treating silence as permission on an
// irreversible operation against production credentials is the one failure mode
// worth being rude about. hz can omit things; it must not be able to omit a
// refusal.
//
// --force reaches exactly one refusal, the one hz marks Forcible: a promotion
// that does not climb the ladder. A missing or mismatched edge is a declaration
// that does not exist, and no flag invents a declaration.
func cmCheckEdge(edge *apitypes.CMPromotionEdgeResp, from, to string, force bool) error {
	if edge == nil {
		return fmt.Errorf("refusing: hz answered the promotion gate without answering the edge from %s to %s.\n"+
			"  the edge is what says this promotion is one somebody declared. An answer that\n"+
			"  omits it is not permission", from, to)
	}
	if edge.OK {
		return nil
	}
	if edge.Forcible && force {
		fmt.Fprintf(os.Stderr, "! forcing a promotion the ladder refuses: %s\n", edge.Error)
		return nil
	}
	msg := edge.Error
	if msg == "" {
		msg = fmt.Sprintf("hz refused the edge from %s to %s without saying why", from, to)
	}
	if edge.Forcible {
		return fmt.Errorf("REFUSED: %s\n"+
			"  a promotion earns its evidence by climbing. Pass --force if this rung is\n"+
			"  deliberately lateral — a disposable box borrowing a posture", msg)
	}
	return fmt.Errorf("REFUSED: %s\n"+
		"  promotion runs along a declared edge and there is none here. Declare `from` on\n"+
		"  the target environment; --force cannot invent an edge nobody wrote down", msg)
}

// cmBoundEnvValues reads the environment-bound values the target already holds,
// so a promoted config is complete rather than a set of invariants a box cannot
// boot on. They come from what the target currently resolves to at the same
// minimum version — the config a box there is running now.
func cmBoundEnvValues(c *client, addr configmgr.EnvKeyAddr, version string) ([]apitypes.CMConfigValueResp, error) {
	q := url.Values{
		apitypes.CMQueryEnv:  {addr.Environment},
		apitypes.CMQueryApp:  {addr.App},
		apitypes.CMQueryRole: {addr.Role},
		"version":            {version},
	}
	var resp apitypes.CMResolveResp
	if err := c.do(http.MethodGet, cmAPI+"/resolve?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Winner == nil {
		return nil, nil
	}
	var out []apitypes.CMConfigValueResp
	for _, v := range resp.Winner.Values {
		if v.Binding == configmgr.BindingEnv && v.Sealed != "" {
			out = append(out, v)
		}
	}
	return out, nil
}
