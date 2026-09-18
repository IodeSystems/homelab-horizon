package configmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// The client library: the half of the config manager that runs on a managed
// box. An APPLICATION imports it — `redline --serviceName=failover web start`
// registers and resolves for its own address. hz ships no agent, so there is no
// daemon, no socket and nothing to keep running between boots.
//
// The whole surface is two calls. Enrol gets the box a key, once, with a human
// in the path exactly once. Load is the boot path.
//
// # Three rules, and each one is a refusal to do the obvious thing
//
//  1. NOTHING IN THE BOOT PATH DEPENDS ON FRESHNESS. There is no TTL here, no
//     expiry, no staleness check and no "last successful fetch" anywhere a boot
//     reads. A three-year-old cache is valid config. Services here run
//     unattended for years, and a box must not break because something lapsed
//     while nobody was watching.
//
//  2. THREE STATES, NOT TWO. hz unreachable boots the cache, loudly. hz
//     reachable but answering anything that is not a positive denial — unknown,
//     pending, a 500, a malformed body — ALSO boots the cache, loudly. Only an
//     explicit denied refuses. The middle row is the one that matters:
//     collapsing it into "refuse" means an hz restored from backup or failed
//     over bricks every box in the fleet at its next restart, while each of them
//     holds a valid key, a valid cache, and an approval a human genuinely
//     granted.
//
//  3. A MONOTONIC SEQUENCE FLOOR. The cache records version → the highest
//     sequence ever applied at that version, and a response carrying a lower one
//     is refused. hz serving an older blessed config authenticates perfectly —
//     identical address, identical key name, identical key id, so the AEAD
//     cannot tell — and this cache is the only thing in the system that knows
//     better. Keyed by version, so rolling a binary back still legitimately
//     selects a lower sequence. There is no clock in it, which is why it
//     survives rule 1.
//
// # The library declares nothing about keys
//
// It hands over what hz served, decrypted, and says nothing about which keys
// that is. There is no compiled schema on this path and no presence check: an
// application knows its own config in a way a library cannot, so deciding
// whether a key it needs is missing is ITS job, through Config.Lookup.
//
// An app that ignores Lookup's boolean falls back to its own compiled default —
// a value nobody set, nobody reviewed and nobody can see in the config manager.
// That is the founding bug this project exists to prevent, and it is now the
// implementor's to avoid rather than the library's to refuse.
//
// # Partial decrypt fails the whole config
//
// Mid-rotation some entries open and some do not. A config with half its keys
// is not a config: any decrypt failure fails all of it and the boot falls back
// to the cache, which is internally consistent.
//
// # Every address is the one the client asked for
//
// The wrapped environment key is authenticated at RegisterRequest.EnvKeyAddr()
// and every value at ConfigRequest.Addr(key) — both built from the app's own
// launch arguments, never read back out of hz's answer. An address hz supplied
// would authenticate nothing at all, and neither would a machine id re-read
// from each response, which is why it is persisted at approval instead.

// Wire paths. The machine protocol is three endpoints and no more.
const (
	registerPath = "/api/v1/cm/register"
	configPath   = "/api/v1/cm/config"
)

// defaultPollInterval is how often an unapproved box asks whether a human has
// looked at it yet. It is a courtesy interval, not a deadline: the caller's
// context decides how long to wait.
const defaultPollInterval = 5 * time.Second

// maxBodySize caps what is read from hz. A resolved config is key names and
// base64 envelopes.
const maxBodySize = 8 << 20

// Errors a caller is expected to branch on.
var (
	// ErrDenied is the ONLY refusal that comes from hz. Everything else hz can
	// say or fail to say takes the cached-boot path. See rule 2.
	ErrDenied = errors.New("registration denied")

	// ErrSequenceRollback means hz served a config older than one already
	// applied at this version. It authenticates perfectly; only the floor
	// knows better.
	ErrSequenceRollback = errors.New("config sequence went backwards")

	// ErrDecrypt means at least one entry did not open, which fails the whole
	// config. A config with half its keys is not a config.
	ErrDecrypt = errors.New("config entry did not decrypt")

	// ErrUnsettled means hz answered, and its answer was not a positive
	// denial: pending, unknown, an error status, a body that did not parse.
	// A caller holding a cache boots it; a caller holding none has nothing.
	ErrUnsettled = errors.New("hz gave no settled answer")
)

// Source says where a Config's bytes came from.
type Source string

const (
	// SourceServer means hz answered and the answer was applied.
	SourceServer Source = "server"

	// SourceCache means the boot fell back to last-known-good. Config.Degraded
	// says why.
	SourceCache Source = "cache"
)

// Options configure a Client. The address fields come from the app's own
// launch arguments and compiled constants, which is what makes authenticating
// them worth anything.
type Options struct {
	// BaseURL is hz, e.g. https://hz.internal. Required.
	BaseURL string

	// StateDir is the root of the client state tree. Absolute. Required.
	StateDir string

	// Machine is the box's name. Defaults to os.Hostname.
	Machine string

	// Environment is a launch flag (--env), App is compiled in, Role is the
	// process's own launch flag (redline's --serviceName). All three are
	// required, and an empty Role is refused rather than defaulted: the
	// unprefixed default maps to a NAMED default role, because an empty role
	// breaks the state path even though the AEAD encodes an empty field
	// happily.
	Environment string
	App         string
	Role        string

	// Version is the clean semver hz runs its range test against. Required:
	// it is also the key of the sequence floor.
	Version string

	// Build is the full `git describe` string, for provenance only. It is not
	// well ordered, so it never takes part in range containment.
	Build string

	// HTTP is the client used for every call. Defaults to one with a 30s
	// timeout. Supply your own to add a transport, a CA or a proxy.
	HTTP *http.Client

	// Logf is where this library is LOUD. Rule 2 is worth nothing if a box
	// boots its cache silently — the fleet then looks healthy while hz has
	// been gone for a week. Defaults to the standard logger.
	Logf func(format string, args ...any)

	// PollInterval is how often an unapproved box re-asks. Defaults to 5s.
	PollInterval time.Duration
}

// Client is one application's view of the config manager, for one address.
type Client struct {
	opts      Options
	state     *State
	http      *http.Client
	logf      func(string, ...any)
	poll      time.Duration
	machineID string
}

// New validates the options and roots the state tree. It touches the network
// and reads no keys.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("configmgr: no hz base URL")
	}
	if _, err := url.Parse(opts.BaseURL); err != nil {
		return nil, fmt.Errorf("configmgr: base URL: %w", err)
	}
	if opts.Version == "" {
		return nil, errors.New("configmgr: no version; the sequence floor is keyed by it")
	}
	if opts.Machine == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("configmgr: no machine name and no hostname: %w", err)
		}
		opts.Machine = h
	}
	state, err := OpenState(opts.StateDir)
	if err != nil {
		return nil, err
	}
	// Validates environment, app and role as path segments, before anything
	// else has a chance to use them.
	if _, err := state.addrDir(EnvKeyAddr{Environment: opts.Environment, App: opts.App, Role: opts.Role}); err != nil {
		return nil, err
	}
	c := &Client{
		opts:  opts,
		state: state,
		http:  opts.HTTP,
		logf:  opts.Logf,
		poll:  opts.PollInterval,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.logf == nil {
		c.logf = func(format string, args ...any) { log.Printf("configmgr: "+format, args...) }
	}
	if c.poll <= 0 {
		c.poll = defaultPollInterval
	}
	return c, nil
}

// Addr is this client's grant address, built from its own launch arguments.
func (c *Client) Addr() EnvKeyAddr {
	return EnvKeyAddr{Environment: c.opts.Environment, App: c.opts.App, Role: c.opts.Role}
}

// State is the tree this client reads and writes.
func (c *Client) State() *State { return c.state }

// Enrol makes sure this box holds the environment key for its own address.
//
// It is idempotent and, on every boot after the first, it is a file read: an
// already-enrolled box does NOT register again. That is not an optimisation —
// registering on every boot would put a pending state in the boot path of a box
// that is already approved, so an hz restored from backup would hold the whole
// fleet at the door. Approve the REGISTRATION, not the BOOT.
//
// On a box that is not yet enrolled it registers, polls until the state
// settles, and on approval unwraps the environment key AT THE ADDRESS IT ASKED
// FOR — never one read back out of the response. hz files a relayed blob under
// whichever registration it likes, and a box may hold several; a grant of
// staging's key relayed into the prod slot unwraps perfectly on the same box
// otherwise, and is noticed only later as a key-id mismatch.
func (c *Client) Enrol(ctx context.Context) error {
	priv, err := c.state.EnsureMachineKey()
	if err != nil {
		return err
	}
	c.warnOnForeignState()

	switch _, err := c.state.EnvKey(c.Addr()); {
	case err == nil:
		id, idErr := c.state.MachineID()
		if idErr != nil {
			// Not fatal. The id is needed only to open a machine-scoped
			// secret, and failing the boot of a box that holds a perfectly
			// good environment key would be rule 2 in miniature.
			c.logf("LOUD: enrolled at %s but the machine id is unreadable (%v); machine-scoped secrets will not open", c.Addr(), idErr)
		}
		c.machineID = id
		return nil
	case !errors.Is(err, ErrNotEnrolled):
		return err
	}

	req := RegisterRequest{
		Machine:     c.opts.Machine,
		Environment: c.opts.Environment,
		App:         c.opts.App,
		Role:        c.opts.Role,
		Version:     c.opts.Version,
		PublicKey:   MarshalMachinePublicKey(priv.PublicKey()),
	}
	resp, err := c.registerAndWait(ctx, req)
	if err != nil {
		return err
	}

	switch resp.State {
	case StateApproved:
	case StateDenied:
		return fmt.Errorf("%w: %s", ErrDenied, c.Addr())
	default:
		return fmt.Errorf("%w: registration %s is %q at %s", ErrUnsettled, resp.ID, resp.State, c.Addr())
	}

	// hz echoes the fingerprint so the box can confirm hz recorded the public
	// key it actually sent. A mismatch is not a denial: it is hz holding
	// somebody else's identity under this registration.
	if resp.Fingerprint != "" {
		want := FingerprintOf(priv.PublicKey())
		got, err := ParseFingerprint(resp.Fingerprint)
		if err != nil {
			return fmt.Errorf("%w: hz echoed an unreadable fingerprint: %v", ErrUnsettled, err)
		}
		if got != want {
			return fmt.Errorf("%w: hz recorded fingerprint %s, this box is %s", ErrUnsettled, got, want)
		}
	}

	envelope, err := DecodeEnvelope(resp.WrappedEnvKey)
	if err != nil {
		return fmt.Errorf("%w: approved with an unreadable wrapped key: %v", ErrUnsettled, err)
	}
	// req.EnvKeyAddr(), not anything from resp. The point of authenticating an
	// address is that the opener knows it independently of the party serving
	// the blob.
	key, err := UnwrapEnvKey(priv, req.EnvKeyAddr(), envelope)
	if err != nil {
		return fmt.Errorf("%w: the grant at %s did not unwrap: %v", ErrUnsettled, req.EnvKeyAddr(), err)
	}

	// The machine id first: it is half the authenticated address of every
	// machine-scoped secret, and writing it before config.key means a box that
	// holds a key holds an id too.
	if err := c.state.PutMachineID(resp.MachineID); err != nil {
		return err
	}
	if err := c.state.PutEnvKey(req.EnvKeyAddr(), key); err != nil {
		return err
	}
	c.machineID = resp.MachineID
	c.logf("enrolled at %s as machine %s, key %s", req.EnvKeyAddr(), resp.MachineID, key.ID())
	return nil
}

// Load is the boot path: enrol if needed, fetch, verify, apply and cache —
// falling back to last-known-good, loudly, on anything that is not a positive
// denial.
//
// It returns an error in exactly two situations: the registration was explicitly
// denied, or there is nothing to boot at all — no key, or no cache and hz did
// not answer usefully. Everything else returns a Config, with Source saying
// where it came from and Degraded saying what went wrong on the way.
func (c *Client) Load(ctx context.Context) (*Config, error) {
	if err := c.Enrol(ctx); err != nil {
		return nil, err
	}
	key, err := c.state.EnvKey(c.Addr())
	if err != nil {
		return nil, err
	}
	floors := c.state.Floors(c.Addr())

	resp, err := c.fetch(ctx)
	if err == nil {
		err = c.checkFloor(floors, resp)
	}
	var cfg *Config
	if err == nil {
		cfg, err = c.build(key, resp, SourceServer)
	}
	if err == nil {
		if perr := c.state.PutCache(c.Addr(), c.opts.Version, *resp, floors); perr != nil {
			// Applied but not cached. The config in hand is good; the next
			// boot loses a floor and a fallback, which is worth a loud line
			// and not worth refusing to start over.
			c.logf("LOUD: applied config %s at %s but could not write last-known-good: %v", resp.ConfigID, c.Addr(), perr)
		}
		return cfg, nil
	}

	if errors.Is(err, ErrDenied) {
		return nil, err
	}
	c.logf("LOUD: hz gave no usable config for %s (%v); booting last-known-good", c.Addr(), err)
	cached, cerr := c.loadCache(key)
	if cerr != nil {
		// Both causes are wrapped, so a caller can branch on either: what hz
		// said and what the cache could not do are two different operational
		// problems and an operator needs to see both.
		return nil, fmt.Errorf("no config for %s: hz: %w; cache: %w", c.Addr(), err, cerr)
	}
	cached.Degraded = err
	return cached, nil
}

// loadCache boots the last-known-good record.
//
// It re-OPENS the cached envelopes rather than storing plaintext, so the cache
// is authenticated against this client's own address on every boot. A cache
// file copied into another address's directory fails here, not silently.
//
// No age check. The cached version is compared against the running one only to
// say so out loud: refusing a cache written by another version would brick a
// legitimate binary rollback on a box that cannot reach hz.
func (c *Client) loadCache(key EnvKey) (*Config, error) {
	cf, err := c.state.cache(c.Addr())
	if err != nil {
		return nil, err
	}
	if cf.Version != c.opts.Version {
		c.logf("LOUD: last-known-good at %s was resolved for version %s, this binary is %s", c.Addr(), cf.Version, c.opts.Version)
	}
	cfg, err := c.build(key, &cf.Response, SourceCache)
	if err != nil {
		return nil, err
	}
	c.logf("LOUD: booted last-known-good at %s: config %s, sequence %d", c.Addr(), cfg.ConfigID, cfg.Sequence)
	return cfg, nil
}

// build opens every entry and only then produces a Config. Nothing is applied
// until all of it opens: a partial decrypt fails the whole config, because a
// config with half its keys is not a config.
//
// Nothing here inspects WHICH keys arrived. Two entries for one key is the only
// shape the map cannot represent, and the later one wins — an application that
// cares reads Config.Keys and says so itself.
func (c *Client) build(key EnvKey, resp *ConfigResponse, src Source) (*Config, error) {
	values := make(map[string][]byte, len(resp.Entries))
	for _, e := range resp.Entries {
		pt, err := c.open(key, e)
		if err != nil {
			return nil, fmt.Errorf("%w: %s at %s: %v", ErrDecrypt, e.Key, c.Addr(), err)
		}
		values[e.Key] = pt
	}
	return &Config{
		ConfigID: resp.ConfigID,
		Sequence: resp.Sequence,
		MinVer:   resp.MinVer,
		MaxVer:   resp.MaxVer,
		Source:   src,
		values:   values,
	}, nil
}

// checkFloor is rule 3. hz serving an older blessed config authenticates
// perfectly — same address, same key name, same key id — so the AEAD cannot
// tell and this floor is the only thing that can.
func (c *Client) checkFloor(floors map[string]int64, resp *ConfigResponse) error {
	floor, ok := floors[c.opts.Version]
	if ok && resp.Sequence < floor {
		return fmt.Errorf("%w: hz served sequence %d for version %s; %d has already been applied here", ErrSequenceRollback, resp.Sequence, c.opts.Version, floor)
	}
	return nil
}

// open decrypts one entry. Which key opens it is the envelope's own kind byte,
// which is covered by the AEAD — there is no field on the wire saying so,
// because a second copy could only ever disagree with it.
func (c *Client) open(key EnvKey, e ConfigEntry) ([]byte, error) {
	envelope, err := DecodeEnvelope(e.Sealed)
	if err != nil {
		return nil, err
	}
	h, err := ParseEnvelopeHeader(envelope)
	if err != nil {
		return nil, err
	}
	switch h.Kind {
	case KindEnvSealed:
		// ConfigRequest.Addr, built from this client's own launch arguments.
		return Open(key, c.configRequest().Addr(e.Key), envelope)
	case KindMachineSealed:
		if c.machineID == "" {
			return nil, fmt.Errorf("a machine-scoped secret needs the machine id learned at approval, and none is held")
		}
		priv, err := c.state.MachineKey()
		if err != nil {
			return nil, err
		}
		// The PERSISTED machine id, not one read out of this response.
		return OpenFromMachine(priv, MachineAddr{Machine: c.machineID, Key: e.Key}, envelope)
	default:
		return nil, fmt.Errorf("%w: a %s envelope cannot be a config value", ErrMalformedEnvelope, h.Kind)
	}
}

func (c *Client) configRequest() ConfigRequest {
	return ConfigRequest{
		Machine:     c.opts.Machine,
		Environment: c.opts.Environment,
		App:         c.opts.App,
		Role:        c.opts.Role,
		Version:     c.opts.Version,
		Build:       c.opts.Build,
	}
}

// warnOnForeignState says, loudly, that this box holds state for an address it
// was not started with. Fail-closed on a mistyped --env is correct and still a
// bad ten minutes if nobody can see why.
func (c *Client) warnOnForeignState() {
	mine := c.Addr()
	for _, a := range c.state.Addresses() {
		if a != mine {
			c.logf("LOUD: %s also holds state for %s; this process was started for %s", c.state.Root(), a, mine)
		}
	}
}

// registerAndWait posts the registration and polls until the state settles.
//
// A poll that fails is retried rather than returned: hz being briefly
// unreachable during an enrolment is not an answer, and certainly not a denial.
// The caller's context is the only thing that ends the wait.
func (c *Client) registerAndWait(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.post(ctx, registerPath, req, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsettled, err)
	}
	for {
		switch resp.State {
		case StateApproved, StateDenied:
			return &resp, nil
		}
		if resp.ID == "" {
			return nil, fmt.Errorf("%w: hz answered %q with no registration id to poll", ErrUnsettled, resp.State)
		}
		c.logf("registration %s at %s is %q; waiting for an operator", resp.ID, c.Addr(), resp.State)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: waiting for approval at %s: %w", ErrUnsettled, c.Addr(), ctx.Err())
		case <-time.After(c.poll):
		}

		var polled RegisterResponse
		if err := c.get(ctx, registerPath+"/"+url.PathEscape(resp.ID), &polled); err != nil {
			// Not an answer. An hz restored from backup answers 404 here, and
			// treating that as a denial is the failure rule 2 forbids.
			c.logf("LOUD: polling registration %s: %v", resp.ID, err)
			continue
		}
		resp = polled
	}
}

// fetch asks for the config that resolves for this process.
func (c *Client) fetch(ctx context.Context) (*ConfigResponse, error) {
	var resp ConfigResponse
	if err := c.post(ctx, configPath, c.configRequest(), &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsettled, err)
	}
	return &resp, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	// strict: the machine protocol is fixed and this box is the party that
	// cannot afford a surprise field. A response carrying one is hz saying
	// something this binary does not understand, which rule 2 turns into a
	// cached boot rather than a guess.
	return jsonRPC(ctx, c.http, http.MethodPost, joinURL(c.opts.BaseURL, path), in, out, true)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return jsonRPC(ctx, c.http, http.MethodGet, joinURL(c.opts.BaseURL, path), nil, out, true)
}

// statusError is a non-2xx answer, carrying the code so a caller can branch on
// it. Push needs that: a 404 from the current-key endpoint means "no pointer is
// set", which is the ordinary state of a brand-new address and not a failure.
type statusError struct {
	Method string
	Path   string
	Code   int
	Status string
	Body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// jsonRPC is the one HTTP round trip in this package: the machine protocol uses
// it and so does Push. in may be nil for a body-less request; out may be nil to
// discard the answer.
//
// strict rejects a response field the caller's type does not name. The machine
// protocol wants that; the admin API Push talks to does not, because hz gaining
// a field there must not break a push.
func jsonRPC(ctx context.Context, hc *http.Client, method, u string, in, out any, strict bool) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Declare the wire contract this build compiled against. A library is
	// pinned at build time, so this is the only thing that can tell a server
	// it is talking to something months old.
	hzapi.SetRequest(req.Header)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return fmt.Errorf("%s %s: reading the body: %w", req.Method, req.URL.Path, err)
	}
	if len(raw) > maxBodySize {
		return fmt.Errorf("%s %s: body is larger than %d bytes", req.Method, req.URL.Path, maxBodySize)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Method: req.Method, Path: req.URL.Path, Code: resp.StatusCode, Status: resp.Status, Body: snippet(raw)}
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

func joinURL(base, path string) string {
	return strings.TrimSuffix(base, "/") + path
}

// snippet bounds an error body so a misrouted request that returns a whole HTML
// page does not become a whole HTML page in the log.
func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// Config is one resolved, decrypted config — the whole of it, or nothing.
//
// It carries exactly what hz served for this address and no opinion about it.
// Which keys an app needs is the app's own statement to make, so the presence
// test lives on this type rather than in a schema the library enforces: see
// Lookup.
type Config struct {
	// ConfigID and Sequence name the config that was applied. Report them:
	// resolution is computed and never stored, so this is the only place the
	// fact "this version ran this config" exists.
	ConfigID string
	Sequence int64

	// MinVer and MaxVer are the range that selected it. MaxVer empty means
	// open-ended.
	MinVer string
	MaxVer string

	// Source says whether this came from hz or from last-known-good.
	Source Source

	// Degraded is why the boot fell back, and is nil only for SourceServer.
	// Surface it: a fleet that boots its cache silently looks healthy while hz
	// has been gone for a week.
	Degraded error

	values map[string][]byte
}

// Get returns a key's value, or "" when the config does not carry it.
//
// "" is indistinguishable from a value that IS empty, so Get is for reads that
// genuinely do not care. Use Lookup for everything else.
func (c *Config) Get(key string) string {
	return string(c.values[key])
}

// Lookup returns a key's value and whether the config carried it.
//
// FALSE MEANS HZ SERVED NO SUCH KEY, and this boolean is how an application
// detects it. There is nothing else: the library enforces no schema and refuses
// nothing on the app's behalf, so a missing key arrives here and nowhere else.
//
// An app that ignores the boolean falls back to whatever default is compiled
// beside the read — a value nobody set, nobody reviewed and nobody can see in
// the config manager. That is the founding bug this whole project exists to
// prevent; since the library no longer decides which keys matter, avoiding it
// is the implementor's job. Branch on this boolean and fail loudly.
func (c *Config) Lookup(key string) (string, bool) {
	v, ok := c.values[key]
	if !ok {
		return "", false
	}
	return string(v), true
}

// Bytes is Lookup's value half for config that is not text. It returns nil for
// a key the config does not carry, which an empty value is indistinguishable
// from; Lookup is the presence test.
func (c *Config) Bytes(key string) []byte {
	return bytes.Clone(c.values[key])
}

// Keys lists what the config holds, sorted. Values are never logged; names are
// not secret and are what an operator needs to see.
func (c *Config) Keys() []string {
	out := make([]string, 0, len(c.values))
	for k := range c.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String names the config without disclosing a byte of it.
func (c *Config) String() string {
	return fmt.Sprintf("config %s seq %d from %s (%d keys)", c.ConfigID, c.Sequence, c.Source, len(c.values))
}
