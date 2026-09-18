package configmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Push: sealing a role's values on this machine and blessing a config from them.
//
// This is a LIBRARY call rather than an `hz` subcommand, and that is the whole
// point of the file. Routing push through the generic CLI forced the app's key
// declaration to be a JSON file, so it existed twice — once as JSON for push and
// once as Go for pull — and two copies of one declaration drift. An app that
// links this instead declares its bindings once, in Go, beside the code. It also
// needs no separate binary on a laptop, in CI or on a build box: a linked
// library cannot be the one thing provisioning forgot to download.
//
// # What this package refuses to know
//
// Push takes values ALREADY ASSEMBLED, as names mapped to bytes. It does not
// read files, does not layer them, does not know that `.properties` is a
// format, and has never heard of local.properties. Where an app's values come
// from is the app's own business and its own format; teaching a generic library
// one app's file convention is how a generic library stops being one.
//
// A name is opaque. It may be one setting or a whole file, and **an extension
// means nothing** — nothing here strips, infers or special-cases anything from a
// name's shape. An app that stores `config.properties` as a single blob and one
// that stores `DB_PASSWORD` as a value are both using this correctly, and this
// package cannot tell them apart.
//
// Values are bytes, not strings, because a config value is not necessarily
// text: a certificate, a keystore and a DER blob are all valid contents.
//
// # What it still refuses to do
//
// Seal under an unvetted key. The keystore's SealingKey is the only way in, so a
// disagreement between the newest key on disk and hz's current-key pointer stops
// the push — see Keystore.SealingKey for why neither side is trusted to win. The
// refusal is surfaced with the two commands that resolve it rather than as a
// bare "refusing to seal", because an operator holding only the latter has no
// move.
//
// The plaintext exists in this process and nowhere else. hz receives base64
// envelopes it cannot open, key names, bindings, and a key id — which is a
// public name.

// The admin routes a bless runs over. They are hz's operator API, not the
// machine protocol in client.go, and they are reachable only with a credential:
// PushOptions.HTTP carries it.
const (
	adminConfigsPath    = "/api/v1/cm/configs"
	adminCurrentKeyPath = "/api/v1/cm/current-key"
)

// ErrSchema means the values being pushed and the app's binding declaration
// disagree — a value with no declared binding, or a declared key with no value.
var ErrSchema = errors.New("values do not match the declared schema")

// Bindings an app may declare. They are the promotion scope, and since the
// no-plaintext decision that is all they are.
var declarableBindings = map[string]bool{
	BindingInvariant: true,
	BindingEnv:       true,
}

// Schema is the app's binding declaration FOR PUSHING: every key it blesses and
// whether that key is invariant or environment-bound.
//
// It is not a contract this library enforces on the pull path — Load applies no
// schema at all and hands over whatever hz served. This exists because a bless
// has to tell hz each value's binding, and hz runs the promotion gate on those
// bindings. A value with no binding is not a value hz can store.
//
// Declare it in Go, beside the code that pushes:
//
//	var schema = configmgr.Schema{
//		"DB_PASSWORD":    configmgr.BindingEnv,
//		"RETENTION_DAYS": configmgr.BindingInvariant,
//	}
type Schema map[string]string

// Validate reports whether a schema is one a push can run against.
func (s Schema) Validate() error {
	if len(s) == 0 {
		return fmt.Errorf("%w: the schema is empty; there is nothing to push", ErrSchema)
	}
	for k, b := range s {
		if k == "" {
			return fmt.Errorf("%w: a schema key is empty", ErrSchema)
		}
		if !declarableBindings[b] {
			return fmt.Errorf("%w: key %q declares binding %q, which is not %s or %s", ErrSchema, k, b, BindingInvariant, BindingEnv)
		}
	}
	return nil
}

// Keys lists the declared keys, sorted, so a message about them reads the same
// way twice.
func (s Schema) Keys() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PushOptions is one bless: one address, one version range, one set of values.
type PushOptions struct {
	// BaseURL is hz, e.g. https://hz.internal. Required.
	BaseURL string

	// HTTP is the client every call runs over, and it is where the OPERATOR
	// CREDENTIAL lives: hz's admin API refuses an unauthenticated bless, and
	// this package deliberately holds no notion of a token. Supply a client
	// whose Transport adds the header your deployment uses. Defaults to a
	// 30s-timeout client, which only works against an endpoint that needs none.
	HTTP *http.Client

	// Keystore holds the environment keys. Required: sealing is the one thing
	// here that cannot be done without one. configmgr.DefaultKeystore() is the
	// usual answer.
	Keystore *Keystore

	// Schema declares each pushed key's binding. Required.
	Schema Schema

	// Environment, App and Role are the address being blessed. All required.
	Environment string
	App         string
	Role        string

	// MinVer is the lowest app version this config serves, and it is required:
	// ranges are immutable after blessing, so this is the only moment anybody
	// is present to get it right. MaxVer empty means open-ended.
	MinVer string
	MaxVer string

	// Values are the plaintexts to seal, keyed by NAME, already assembled by
	// the application.
	//
	// A name is an OPAQUE IDENTIFIER and nothing here interprets it. It may be
	// a single setting (`DB_PASSWORD`) or a whole file (`config.properties`) —
	// that is the application's choice, and this package cannot tell the
	// difference and must not try. In particular **extensions mean nothing
	// here**: nothing is stripped, inferred or special-cased from a name's
	// shape. The name is what gets bound into the AEAD, so whatever an
	// application asks for is what it must ask for again to open.
	//
	// []byte rather than string because a value is not necessarily text: a
	// certificate, a keystore, a DER blob and a UTF-8 properties file are all
	// just bytes, and typing them as string would invite a conversion that
	// corrupts the first three.
	//
	// Reading and layering whatever these came from is the app's job — see the
	// note at the top of this file.
	Values map[string][]byte

	// DryRun seals and reports, and posts nothing. The keystore is still
	// consulted and the pointer check still runs, so a dry run that succeeds
	// says the real one would too.
	DryRun bool
}

// PushResult describes what was sealed, and what hz called it.
//
// ConfigID and Sequence are empty and zero for a dry run. Report the sequence:
// resolution is computed and never stored, so a client's report of the sequence
// it applied is the only place the fact "this version ran this config" exists.
type PushResult struct {
	Addr     EnvKeyAddr
	Keys     []string // sealed key names, sorted
	KeyID    KeyID    // the environment key everything was sealed under
	KeyLabel string
	MinVer   string
	MaxVer   string
	DryRun   bool
	ConfigID string
	Sequence int64
}

// String names the push without disclosing a byte of it.
func (r *PushResult) String() string {
	what := fmt.Sprintf("%s: %d value(s) sealed under %s (%s), range %s",
		r.Addr, len(r.Keys), r.KeyID, r.KeyLabel, verRange(r.MinVer, r.MaxVer))
	if r.DryRun {
		return what + " — dry run, nothing posted"
	}
	return fmt.Sprintf("%s, blessed as config %s (sequence %d)", what, r.ConfigID, r.Sequence)
}

// Push seals every value and blesses a config from them, or fails having sent
// nothing.
//
// Every check runs before the first byte reaches hz. A bless is a unit: a
// half-blessed config is a box booting on a set of values nobody chose.
func Push(ctx context.Context, opts PushOptions) (*PushResult, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	addr := EnvKeyAddr{Environment: opts.Environment, App: opts.App, Role: opts.Role}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	// The schema, in both directions. A value with no declared binding cannot
	// be sent at all — there is no field to put — and a declared key with no
	// value would bless a config hz then serves short, which is what makes a
	// box fall back to a compiled default.
	var undeclared, missing []string
	for k := range opts.Values {
		if _, ok := opts.Schema[k]; !ok {
			undeclared = append(undeclared, k)
		}
	}
	for _, k := range opts.Schema.Keys() {
		if _, ok := opts.Values[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		return nil, fmt.Errorf("%w: %s %s declare no binding, and a value hz cannot label is a value it cannot store",
			ErrSchema, plural(len(undeclared), "key", "keys"), strings.Join(undeclared, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: declared but not supplied: %s\n"+
			"a config missing a declared key makes the box fall back to its compiled default,\n"+
			"which is the failure this system exists to prevent. Supply them or narrow the schema",
			ErrSchema, strings.Join(missing, ", "))
	}

	// The key. Sealing consults hz's pointer and may refuse; that refusal is
	// the point of the pointer, so it is surfaced with something to do.
	cur, err := currentKey(ctx, hc, opts.BaseURL, addr)
	if err != nil {
		return nil, err
	}
	key, info, err := opts.Keystore.SealingKey(Addr{Environment: addr.Environment, App: addr.App, Role: addr.Role}, cur)
	if err != nil {
		return nil, sealAdvice(addr, cur, err)
	}

	// Seal. The plaintext exists in this process and nowhere else.
	names := opts.Schema.Keys()
	values := make([]BlessValue, 0, len(names))
	for _, k := range names {
		values = append(values, BlessValue{
			Key:     k,
			Binding: opts.Schema[k],
			Sealed:  EncodeEnvelope(Seal(key, Addr{Environment: addr.Environment, App: addr.App, Role: addr.Role, Key: k}, opts.Values[k])),
			KeyID:   info.ID.String(),
		})
	}

	res := &PushResult{
		Addr:     addr,
		Keys:     names,
		KeyID:    info.ID,
		KeyLabel: info.Label,
		MinVer:   opts.MinVer,
		MaxVer:   opts.MaxVer,
		DryRun:   opts.DryRun,
	}
	if opts.DryRun {
		return res, nil
	}

	req := BlessRequest{
		Environment: addr.Environment,
		App:         addr.App,
		Role:        addr.Role,
		MinVer:      opts.MinVer,
		MaxVer:      opts.MaxVer,
		Values:      values,
	}
	var resp BlessResponse
	if err := jsonRPC(ctx, hc, http.MethodPost, joinURL(opts.BaseURL, adminConfigsPath), req, &resp, false); err != nil {
		return nil, fmt.Errorf("blessing a config at %s: %w", addr, err)
	}
	res.ConfigID = resp.ID
	res.Sequence = resp.Sequence
	return res, nil
}

func (o PushOptions) check() error {
	switch {
	case o.BaseURL == "":
		return errors.New("configmgr: no hz base URL to push to")
	case o.Keystore == nil:
		return errors.New("configmgr: no keystore; there is nothing to seal with")
	case o.Environment == "":
		return errors.New("configmgr: no environment")
	case o.App == "":
		return errors.New("configmgr: no app")
	case o.Role == "":
		return errors.New("configmgr: no role")
	case o.MinVer == "":
		return errors.New("configmgr: no minimum version; ranges are immutable after blessing, so this is the only moment to get it right")
	case len(o.Values) == 0:
		return errors.New("configmgr: no values; a config with nothing in it is not what anybody means")
	}
	return o.Schema.Validate()
}

// currentKey asks hz which key it calls current at an address.
//
// The answer is advisory in one direction only. The keystore refuses to SEAL
// when its newest key and this pointer disagree, and only warns on OPEN; see
// Keystore.SealingKey for why the asymmetry is the whole point. Nothing here
// obeys the pointer — obeying it would let a compromised hz pin the fleet to a
// key it had already stolen.
//
// A 404 and an empty key id both mean "no pointer set", which is the ordinary
// state of a brand-new address. Any other failure propagates: a push that
// cannot reach hz cannot post a config either, so swallowing the error would
// only move the failure later and make it less legible.
func currentKey(ctx context.Context, hc *http.Client, baseURL string, addr EnvKeyAddr) (CurrentKey, error) {
	q := url.Values{
		"environment": {addr.Environment},
		"app":         {addr.App},
		"role":        {addr.Role},
	}
	var resp CurrentKeyPointer
	if err := jsonRPC(ctx, hc, http.MethodGet, joinURL(baseURL, adminCurrentKeyPath)+"?"+q.Encode(), nil, &resp, false); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return CurrentKeyUnavailable(), nil
		}
		return CurrentKey{}, fmt.Errorf("asking hz which key is current for %s: %w", addr, err)
	}
	if resp.KeyID == "" {
		return CurrentKeyUnavailable(), nil
	}
	id, err := ParseKeyID(resp.KeyID)
	if err != nil {
		return CurrentKey{}, fmt.Errorf("hz names %q current for %s, which is not a key id: %w", resp.KeyID, addr, err)
	}
	return CurrentKeyIs(id), nil
}

// sealAdvice re-wraps the keystore's refusals with the moves that resolve them.
//
// The refusal is the interesting path, not the exception: it fires whenever the
// newest key on disk is not the one hz calls current, which is exactly what a
// dropped-in key file looks like.
func sealAdvice(addr EnvKeyAddr, cur CurrentKey, err error) error {
	switch {
	case errors.Is(err, ErrNoSuchKey):
		return fmt.Errorf("%w\n"+
			"  this machine holds no key for %s.\n"+
			"  import the one from your password manager:  hz cm key import %s\n"+
			"  or mint the first key for the address:       hz cm key new %s",
			err, addr, addr, addr)
	case errors.Is(err, ErrRefuseToSeal):
		return fmt.Errorf("%w\n"+
			"  the keystore refuses to seal while its newest key and hz's pointer disagree.\n"+
			"  that is deliberate: created_at lives in a file anyone who can write the\n"+
			"  keystore can set, and hz's pointer is the only thing that arbitrates it.\n"+
			"  hz calls %s current for %s.\n"+
			"  if that key is the right one:  hz cm key import %s\n"+
			"  if the key you hold is newer:  hz cm key current %s --set <keyid>",
			err, cur, addr, addr, addr)
	case errors.Is(err, ErrInsecureKey):
		return fmt.Errorf("%w\n  fix the mode and ownership (0600, owned by you, in a directory nobody else can write) and retry", err)
	}
	return err
}

func verRange(minVer, maxVer string) string {
	if maxVer == "" {
		return minVer + "–∞"
	}
	return minVer + "–" + maxVer
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
