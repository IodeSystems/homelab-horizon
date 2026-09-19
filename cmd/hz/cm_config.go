package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	for _, v := range resp.Winner.Values {
		fmt.Printf("  %-28s %-10s %s\n", v.Key, v.Binding, v.Origin)
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
// Only invariant values carry. Environment-bound ones are bound in the target
// or the promotion is blocked, which is the whole reason promotion is a gate
// rather than a copy. Where the target already has values for them, they are
// carried forward BY CIPHERTEXT — same address, same key, so there is nothing
// to re-seal and nothing to open.
func cmPromote(c *client, args []string) error {
	fs := flag.NewFlagSet("cm promote", flag.ContinueOnError)
	to := fs.String("to", "", "target environment (required)")
	dryRun := fs.Bool("dry-run", false, "run the gate and the re-seal, but post nothing")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm promote <config-id> --to=<environment>")
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
	// nothing.
	gq := url.Values{apitypes.CMQueryConfigID: {src.ID}, apitypes.CMQueryTarget: {*to}}
	var gate apitypes.CMPromotionGateResp
	if err := c.do(http.MethodGet, cmAPI+"/promote/gate?"+gq.Encode(), nil, &gate); err != nil {
		return err
	}
	if !gate.OK {
		return fmt.Errorf("BLOCKED: %s has no value bound for %s\n"+
			"  promotion is a gate, not a copy: an environment-bound key is born in its own\n"+
			"  environment and cannot travel. Bind them there first, then promote",
			*to, strings.Join(gate.Blocked, ", "))
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}

	// Source side: open only what promotes.
	// The gate names what carries, and nothing outside that list does. hz can
	// only cause an OMISSION this way, never an injection — every carried value
	// is opened under the source key here and re-sealed under the target's — and
	// an omission is visible in the plan printed below and in the posted config.
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
	if len(carrying) == 0 {
		return fmt.Errorf("the gate names nothing to promote out of %s; a promotion that carries no value is a no-op, not a release step", src.ID)
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
	values := make([]apitypes.CMConfigValueReq, 0, len(opened)+len(gate.Blocked))
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

	// Carry the target's already-bound environment values forward untouched.
	// Same address and same key, so this is a ciphertext copy: no key is used
	// and no plaintext exists for them at any point here.
	bound, err := cmBoundEnvValues(c, dstAddr, src.MinVer)
	if err != nil {
		return err
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

	fmt.Printf("promote %s (%s) -> %s\n", src.ID, srcAddr, dstAddr)
	for _, v := range opened {
		fmt.Printf("  re-sealed  %-28s invariant, under %s\n", v.value.Key, dstInfo.ID)
	}
	for _, v := range bound {
		fmt.Printf("  carried    %-28s env, already bound in %s\n", v.Key, *to)
	}
	if *dryRun {
		fmt.Println("--dry-run, nothing posted.")
		return nil
	}

	var resp apitypes.CMConfigResp
	req := apitypes.CMCreateConfigReq{
		Environment: dstAddr.Environment,
		App:         dstAddr.App,
		Role:        dstAddr.Role,
		MinVer:      src.MinVer,
		MaxVer:      src.MaxVer,
		Values:      values,
	}
	if err := c.do(http.MethodPost, cmAPI+"/configs", req, &resp); err != nil {
		return err
	}
	fmt.Printf("blessed %s in %s (sequence %d).\n", resp.ID, *to, resp.Sequence)
	return nil
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
