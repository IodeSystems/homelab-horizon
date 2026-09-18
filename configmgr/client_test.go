package configmgr

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake hz, built out of httptest.Server and the real envelope format. Nothing
// here is mocked: the server wraps a real environment key to the public key the
// client actually sent, and seals real values that the client actually opens.
// A test that stubbed the crypto would pass while the binding it is supposed to
// prove was missing.

const (
	testMachineID = "m-0001"
	testVersion   = "1.2.3"
)

type hz struct {
	t  *testing.T
	mu sync.Mutex

	srv *httptest.Server

	// envKeys is the key held per grant address, minted on demand.
	envKeys map[EnvKeyAddr]EnvKey

	// state is what the next registration answer says.
	state string

	// machineID is what hz calls the box.
	machineID string

	// wrapFor, when set, is the address hz wraps the environment key AT,
	// regardless of the address the client registered for. That is the relay
	// attack: hz files a grant under a registration the approver did not mean.
	wrapFor *EnvKeyAddr

	// fingerprint, when set, overrides the echoed fingerprint.
	fingerprint string

	// omitMachineID drops the machine id from an approval.
	omitMachineID bool

	// config is the resolved config hz serves, built from the request.
	config func(req ConfigRequest, k EnvKey) (ConfigResponse, error)

	// configStatus, when non-zero, is returned instead of a config.
	configStatus int

	// configBody, when set, is returned verbatim instead of a config.
	configBody string

	registers int
	polls     int

	pubs map[string]string // registration id -> public key
}

func newHZ(t *testing.T) *hz {
	t.Helper()
	h := &hz{
		t:         t,
		envKeys:   map[EnvKeyAddr]EnvKey{},
		state:     StateApproved,
		machineID: testMachineID,
		pubs:      map[string]string{},
	}
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		return ConfigResponse{
			ConfigID: "cfg-1",
			Sequence: 10,
			MinVer:   "1.0.0",
			Entries: []ConfigEntry{
				sealEntry(t, k, req.Addr("DB_PASSWORD"), BindingEnv, "hunter2"),
				sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
			},
		}, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+registerPath, h.handleRegister)
	mux.HandleFunc("GET "+registerPath+"/{id}", h.handlePoll)
	mux.HandleFunc("POST "+configPath, h.handleConfig)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

// set mutates the fake under its lock. Tests change hz's behaviour between
// calls, and a handler goroutine may still be unwinding from the previous one.
func (h *hz) set(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f()
}

// key mints (or returns) the environment key for one grant address.
func (h *hz) key(addr EnvKeyAddr) EnvKey {
	if k, ok := h.envKeys[addr]; ok {
		return k
	}
	k := NewEnvKey()
	h.envKeys[addr] = k
	return k
}

func (h *hz) handleRegister(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.registers++

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := "reg-1"
	h.pubs[id] = req.PublicKey
	h.answer(w, id, req)
}

func (h *hz) handlePoll(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.polls++

	id := r.PathValue("id")
	pub, ok := h.pubs[id]
	if !ok {
		http.Error(w, "no such registration", http.StatusNotFound)
		return
	}
	// hz knows the registration's own address; the test only ever runs one.
	h.answer(w, id, RegisterRequest{PublicKey: pub, Environment: "prod", App: "redline", Role: "app"})
}

func (h *hz) answer(w http.ResponseWriter, id string, req RegisterRequest) {
	resp := RegisterResponse{ID: id, State: h.state}
	if h.state != StateApproved {
		writeJSON(h.t, w, resp)
		return
	}
	pub, err := ParseMachinePublicKey(req.PublicKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := req.EnvKeyAddr()
	if h.wrapFor != nil {
		addr = *h.wrapFor
	}
	blob, err := WrapEnvKey(pub, addr, h.key(req.EnvKeyAddr()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.WrappedEnvKey = EncodeEnvelope(blob)
	resp.Fingerprint = FingerprintOf(pub).String()
	if h.fingerprint != "" {
		resp.Fingerprint = h.fingerprint
	}
	if !h.omitMachineID {
		resp.MachineID = h.machineID
	}
	writeJSON(h.t, w, resp)
}

func (h *hz) handleConfig(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.configStatus != 0 {
		http.Error(w, "hz is having a day", h.configStatus)
		return
	}
	if h.configBody != "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(h.configBody))
		return
	}
	var req ConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := h.config(req, h.key(EnvKeyAddr{Environment: req.Environment, App: req.App, Role: req.Role}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(h.t, w, resp)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding response: %v", err)
	}
}

func sealEntry(t *testing.T, k EnvKey, addr Addr, binding, value string) ConfigEntry {
	t.Helper()
	return ConfigEntry{Key: addr.Key, Binding: binding, Sealed: EncodeEnvelope(Seal(k, addr, []byte(value)))}
}

// newClient builds a client against a fake hz, with a fresh state tree and a
// poll interval short enough that a test is not a nap.
func newClient(t *testing.T, h *hz, mutate func(*Options)) *Client {
	t.Helper()
	opts := Options{
		BaseURL:      h.srv.URL,
		StateDir:     t.TempDir(),
		Machine:      "box-1",
		Environment:  "prod",
		App:          "redline",
		Role:         "app",
		Version:      testVersion,
		Build:        "v1.2.3-4-gdeadbee",
		PollInterval: time.Millisecond,
		Logf:         func(string, ...any) {},
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestEnrolApproveResolve is the whole happy path: generate a keypair, register,
// be approved, unwrap the grant, fetch, open every value, cache it.
func TestEnrolApproveResolve(t *testing.T) {
	h := newHZ(t)
	c := newClient(t, h, nil)

	cfg, err := c.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source != SourceServer {
		t.Errorf("Source = %s, want %s", cfg.Source, SourceServer)
	}
	if cfg.Degraded != nil {
		t.Errorf("Degraded = %v, want nil", cfg.Degraded)
	}
	if got := cfg.Get("DB_PASSWORD"); got != "hunter2" {
		t.Errorf("DB_PASSWORD = %q", got)
	}
	if got := cfg.Get("LOG_LEVEL"); got != "debug" {
		t.Errorf("LOG_LEVEL = %q", got)
	}
	if cfg.ConfigID != "cfg-1" || cfg.Sequence != 10 {
		t.Errorf("applied %s seq %d", cfg.ConfigID, cfg.Sequence)
	}

	// The private half never left: the state tree holds it at 0600 and the
	// registration carried only the public half.
	st := statOrFail(t, filepath.Join(c.State().Root(), machineKeyName))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("machine.key is %#o, want 0600", st.Mode().Perm())
	}
	// The machine id is persisted beside it.
	if id, err := c.State().MachineID(); err != nil || id != testMachineID {
		t.Errorf("MachineID = %q, %v", id, err)
	}
	// The address is the path.
	for _, name := range []string{envKeyName, cacheName} {
		p := filepath.Join(c.State().Root(), "prod", "redline", "app", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}

	// A second Load re-registers nothing: approval is of the registration, not
	// of the boot.
	before := h.registers
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if h.registers != before {
		t.Errorf("second Load registered again: %d -> %d", before, h.registers)
	}
}

// TestEnrolPollsUntilApproved holds the registration pending for a few polls,
// which is the shape of a human walking to a terminal.
func TestEnrolPollsUntilApproved(t *testing.T) {
	h := newHZ(t)
	h.state = StatePending
	c := newClient(t, h, nil)

	go func() {
		for {
			h.mu.Lock()
			polls := h.polls
			if polls >= 3 {
				h.state = StateApproved
				h.mu.Unlock()
				return
			}
			h.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	if err := c.Enrol(ctx(t)); err != nil {
		t.Fatalf("Enrol: %v", err)
	}
	if _, err := c.State().EnvKey(c.Addr()); err != nil {
		t.Fatalf("no key after approval: %v", err)
	}
}

// TestGrantRelayedToTheWrongAddressFails is the reason the wrapped key is
// authenticated at the address the client ASKED for. hz wraps a real key to the
// box's real public key, for a different one of that box's registrations.
// Recipient, ECDH and kind are all correct; only the address is not.
func TestGrantRelayedToTheWrongAddressFails(t *testing.T) {
	h := newHZ(t)
	h.wrapFor = &EnvKeyAddr{Environment: "prod", App: "redline", Role: "ops"}
	c := newClient(t, h, nil)

	err := c.Enrol(ctx(t))
	if !errors.Is(err, ErrUnsettled) {
		t.Fatalf("Enrol err = %v, want ErrUnsettled", err)
	}
	if !strings.Contains(err.Error(), "did not unwrap") {
		t.Errorf("error should name the unwrap: %v", err)
	}
	if _, err := c.State().EnvKey(c.Addr()); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("a refused grant must leave no key: %v", err)
	}
}

// TestApprovalWithMismatchedFingerprintFails: hz echoing a fingerprint that is
// not this box's means hz recorded somebody else's public key here.
func TestApprovalWithMismatchedFingerprintFails(t *testing.T) {
	other, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	h := newHZ(t)
	h.fingerprint = FingerprintOf(other.PublicKey()).String()
	c := newClient(t, h, nil)

	if err := c.Enrol(ctx(t)); !errors.Is(err, ErrUnsettled) {
		t.Fatalf("Enrol err = %v, want ErrUnsettled", err)
	}
}

// TestApprovalWithoutMachineIDFails: the id is half the address of every
// machine-scoped secret, so an approval that does not name one is not settled.
func TestApprovalWithoutMachineIDFails(t *testing.T) {
	h := newHZ(t)
	h.omitMachineID = true
	c := newClient(t, h, nil)

	if err := c.Enrol(ctx(t)); !errors.Is(err, ErrBadState) {
		t.Fatalf("Enrol err = %v, want ErrBadState", err)
	}
}

// TestThreeStates is the matrix. The middle row is the one that matters:
// anything that is not a positive denial boots last-known-good.
func TestThreeStates(t *testing.T) {
	t.Run("denied refuses", func(t *testing.T) {
		h := newHZ(t)
		h.state = StateDenied
		c := newClient(t, h, nil)

		if _, err := c.Load(ctx(t)); !errors.Is(err, ErrDenied) {
			t.Fatalf("Load err = %v, want ErrDenied", err)
		}
	})

	// Every remaining case starts from an enrolled box with a good cache,
	// which is the fleet's normal state.
	enrolled := func(t *testing.T) (*hz, *Client) {
		t.Helper()
		h := newHZ(t)
		c := newClient(t, h, nil)
		if _, err := c.Load(ctx(t)); err != nil {
			t.Fatalf("priming Load: %v", err)
		}
		return h, c
	}

	cases := []struct {
		name   string
		break_ func(*hz)
	}{
		{"unreachable", func(h *hz) { h.srv.Close() }},
		{"unknown — hz restored from backup", func(h *hz) { h.set(func() { h.configStatus = http.StatusNotFound }) }},
		{"error", func(h *hz) { h.set(func() { h.configStatus = http.StatusInternalServerError }) }},
		{"a body that does not parse", func(h *hz) { h.set(func() { h.configBody = `{"nonsense": true}` }) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := enrolled(t)
			tc.break_(h)

			cfg, err := c.Load(ctx(t))
			if err != nil {
				t.Fatalf("Load must boot the cache, not fail: %v", err)
			}
			if cfg.Source != SourceCache {
				t.Errorf("Source = %s, want %s", cfg.Source, SourceCache)
			}
			if cfg.Degraded == nil {
				t.Error("a cached boot must say why")
			}
			if got := cfg.Get("DB_PASSWORD"); got != "hunter2" {
				t.Errorf("cached DB_PASSWORD = %q", got)
			}
		})
	}
}

// TestCachedBootIsLoud: rule 2 is worth nothing if the fallback is silent — the
// fleet then looks healthy while hz has been gone for a week.
func TestCachedBootIsLoud(t *testing.T) {
	h := newHZ(t)
	var lines []string
	c := newClient(t, h, func(o *Options) {
		o.Logf = func(f string, a ...any) { lines = append(lines, f) }
	})
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatal(err)
	}
	lines = nil
	h.set(func() { h.configStatus = http.StatusInternalServerError })
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatal(err)
	}
	var loud int
	for _, l := range lines {
		if strings.Contains(l, "LOUD") {
			loud++
		}
	}
	if loud == 0 {
		t.Fatalf("a cached boot logged nothing loud: %q", lines)
	}
}

// TestNoCacheAndNoAnswer: a box that has never booted has nothing to fall back
// to, and says so rather than inventing one.
func TestNoCacheAndNoAnswer(t *testing.T) {
	h := newHZ(t)
	c := newClient(t, h, nil)
	if err := c.Enrol(ctx(t)); err != nil {
		t.Fatal(err)
	}
	h.set(func() { h.configStatus = http.StatusInternalServerError })

	_, err := c.Load(ctx(t))
	if !errors.Is(err, ErrNoCache) {
		t.Fatalf("Load err = %v, want ErrNoCache", err)
	}
}

// TestSequenceFloor is rule 3. hz serving an older blessed config
// authenticates perfectly: same address, same key name, same key id. Only the
// floor knows better.
func TestSequenceFloor(t *testing.T) {
	h := newHZ(t)
	c := newClient(t, h, nil)

	serve := func(seq int64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
			return ConfigResponse{
				ConfigID: "cfg",
				Sequence: seq,
				MinVer:   "1.0.0",
				Entries: []ConfigEntry{
					sealEntry(t, k, req.Addr("DB_PASSWORD"), BindingEnv, "seq"+req.Version),
					sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
				},
			}, nil
		}
	}

	serve(42)
	cfg, err := c.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sequence != 42 {
		t.Fatalf("Sequence = %d", cfg.Sequence)
	}

	// hz now serves an older blessed config at the same version. Refused, and
	// the boot falls back to the cache — which holds the newer one.
	serve(30)
	cfg, err = c.Load(ctx(t))
	if err != nil {
		t.Fatalf("a rollback must fall back, not fail: %v", err)
	}
	if cfg.Source != SourceCache {
		t.Errorf("Source = %s, want %s", cfg.Source, SourceCache)
	}
	if !errors.Is(cfg.Degraded, ErrSequenceRollback) {
		t.Errorf("Degraded = %v, want ErrSequenceRollback", cfg.Degraded)
	}
	if cfg.Sequence != 42 {
		t.Errorf("cached Sequence = %d, want the 42 already applied", cfg.Sequence)
	}

	// The floor is keyed by VERSION, so rolling the BINARY back still
	// legitimately selects a lower sequence. Same state tree, older version.
	older := newClient(t, h, func(o *Options) {
		o.StateDir = c.State().Root()
		o.Version = "1.2.2"
	})
	cfg, err = older.Load(ctx(t))
	if err != nil {
		t.Fatalf("a version rollback is legitimate: %v", err)
	}
	if cfg.Source != SourceServer {
		t.Errorf("Source = %s, want %s — 30 is not below 1.2.2's floor", cfg.Source, SourceServer)
	}
	if cfg.Sequence != 30 {
		t.Errorf("Sequence = %d, want 30", cfg.Sequence)
	}

	// And the floor for the NEWER version survived the rollback, so going back
	// to it still refuses the older sequence.
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatal(err)
	}
	floors := c.State().Floors(c.Addr())
	if floors[testVersion] != 42 {
		t.Errorf("floor[%s] = %d, want 42", testVersion, floors[testVersion])
	}
	if floors["1.2.2"] != 30 {
		t.Errorf("floor[1.2.2] = %d, want 30", floors["1.2.2"])
	}
}

// TestPartialDecryptFailsWhole: mid-rotation some entries open and some do not.
// A config with half its keys is not a config.
func TestPartialDecryptFailsWhole(t *testing.T) {
	h := newHZ(t)
	stale := NewEnvKey()
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		return ConfigResponse{
			ConfigID: "cfg",
			Sequence: 1,
			MinVer:   "1.0.0",
			Entries: []ConfigEntry{
				sealEntry(t, k, req.Addr("DB_PASSWORD"), BindingEnv, "hunter2"),
				// Sealed under a key this box does not hold, as if LOG_LEVEL
				// had already been re-sealed by a rotation.
				sealEntry(t, stale, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
			},
		}, nil
	}
	c := newClient(t, h, nil)

	_, err := c.Load(ctx(t))
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Load err = %v, want ErrDecrypt", err)
	}
	if !errors.Is(err, ErrNoCache) {
		t.Errorf("the failure should have fallen back and found no cache: %v", err)
	}
	// Nothing was applied, so nothing was cached.
	if _, err := os.Stat(filepath.Join(c.State().Root(), "prod", "redline", "app", cacheName)); !os.IsNotExist(err) {
		t.Errorf("a failed config must not be cached: %v", err)
	}
}

// TestValueSealedForAnotherAddressFails: hz relaying staging's ciphertext as
// prod's. Right key id would not help; the address is authenticated.
func TestValueSealedForAnotherAddressFails(t *testing.T) {
	h := newHZ(t)
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		elsewhere := Addr{Environment: "staging", App: req.App, Role: req.Role, Key: "DB_PASSWORD"}
		return ConfigResponse{
			ConfigID: "cfg",
			Sequence: 1,
			MinVer:   "1.0.0",
			Entries: []ConfigEntry{
				{Key: "DB_PASSWORD", Binding: BindingEnv, Sealed: EncodeEnvelope(Seal(k, elsewhere, []byte("staging's")))},
				sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
			},
		}, nil
	}
	c := newClient(t, h, nil)

	if _, err := c.Load(ctx(t)); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Load err = %v, want ErrDecrypt", err)
	}
}

// TestCacheIsNotReadableAtAnotherAddress: the cache holds sealed envelopes, so
// copying the file into another address's directory is not enough. Even handing
// that address the SAME key bytes does not help — the role is in the AAD.
func TestCacheIsNotReadableAtAnotherAddress(t *testing.T) {
	h := newHZ(t)
	c := newClient(t, h, nil)
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatal(err)
	}
	root := c.State().Root()

	// A second registration on the same box, at a different role, holding the
	// very same key material.
	ops := EnvKeyAddr{Environment: "prod", App: "redline", Role: "ops"}
	key, err := c.State().EnvKey(c.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.State().PutEnvKey(ops, key); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(root, "prod", "redline", "app", cacheName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prod", "redline", "ops", cacheName), blob, 0o600); err != nil {
		t.Fatal(err)
	}

	h.srv.Close() // force the cached path
	opsClient := newClient(t, h, func(o *Options) {
		o.StateDir = root
		o.Role = "ops"
	})
	_, err = opsClient.Load(ctx(t))
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("a cache from another address must not open: %v", err)
	}

	// And a different ENVIRONMENT does not even find a directory to look in.
	staging := newClient(t, h, func(o *Options) {
		o.StateDir = root
		o.Environment = "staging"
	})
	if _, err := staging.Load(ctx(t)); !errors.Is(err, ErrUnsettled) {
		t.Fatalf("a different environment must not be enrolled: %v", err)
	}
	if _, err := staging.State().EnvKey(staging.Addr()); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("EnvKey at staging = %v, want ErrNotEnrolled", err)
	}
}

// TestCacheNeverGoesStale: rule 1. No TTL, no expiry, no age check anywhere —
// a cache written long ago is valid config, and so is one written by a binary
// that has since been rolled back.
func TestCacheNeverGoesStale(t *testing.T) {
	h := newHZ(t)
	c := newClient(t, h, nil)
	if _, err := c.Load(ctx(t)); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(c.State().Root(), "prod", "redline", "app", cacheName)
	long := time.Now().Add(-3 * 365 * 24 * time.Hour)
	if err := os.Chtimes(cachePath, long, long); err != nil {
		t.Fatal(err)
	}
	h.srv.Close()

	// Three years old, and read by a binary at a different version.
	rolled := newClient(t, h, func(o *Options) {
		o.StateDir = c.State().Root()
		o.Version = "1.2.2"
	})
	cfg, err := rolled.Load(ctx(t))
	if err != nil {
		t.Fatalf("a three-year-old cache is valid config: %v", err)
	}
	if cfg.Get("DB_PASSWORD") != "hunter2" {
		t.Errorf("DB_PASSWORD = %q", cfg.Get("DB_PASSWORD"))
	}
}

// TestMachineScopedSecret opens a kind 0x03 entry, which is addressed by the
// machine id persisted at approval rather than one read out of the response.
func TestMachineScopedSecret(t *testing.T) {
	h := newHZ(t)
	var pub *ecdh.PublicKey
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		blob, err := SealToMachine(pub, MachineAddr{Machine: testMachineID, Key: "DB_PASSWORD"}, []byte("device-only"))
		if err != nil {
			return ConfigResponse{}, err
		}
		return ConfigResponse{
			ConfigID: "cfg",
			Sequence: 1,
			MinVer:   "1.0.0",
			Entries: []ConfigEntry{
				{Key: "DB_PASSWORD", Binding: BindingEnv, Sealed: EncodeEnvelope(blob)},
				sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
			},
		}, nil
	}
	c := newClient(t, h, nil)
	if err := c.Enrol(ctx(t)); err != nil {
		t.Fatal(err)
	}
	priv, err := c.State().MachineKey()
	if err != nil {
		t.Fatal(err)
	}
	pub = priv.PublicKey()

	cfg, err := c.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Get("DB_PASSWORD"); got != "device-only" {
		t.Errorf("DB_PASSWORD = %q", got)
	}

	// Sealed to a machine id that is not the one persisted at approval: hz
	// naming a different box, or renaming this one, changes nothing.
	h.set(func() {
		h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
			blob, err := SealToMachine(pub, MachineAddr{Machine: "m-9999", Key: "DB_PASSWORD"}, []byte("somebody else's"))
			if err != nil {
				return ConfigResponse{}, err
			}
			return ConfigResponse{
				ConfigID: "cfg",
				Sequence: 2,
				MinVer:   "1.0.0",
				Entries: []ConfigEntry{
					{Key: "DB_PASSWORD", Binding: BindingEnv, Sealed: EncodeEnvelope(blob)},
					sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
				},
			}, nil
		}
	})
	cfg, err = c.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Source != SourceCache || !errors.Is(cfg.Degraded, ErrDecrypt) {
		t.Errorf("Source = %s, Degraded = %v", cfg.Source, cfg.Degraded)
	}
}

// TestWrappedEnvKeyIsNotAConfigValue: a kind 0x02 envelope served as a config
// entry is refused by kind, not merely by a failed decrypt.
func TestWrappedEnvKeyIsNotAConfigValue(t *testing.T) {
	h := newHZ(t)
	var pub *ecdh.PublicKey
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		blob, err := WrapEnvKey(pub, req.Addr("DB_PASSWORD").EnvKeyAddr(), k)
		if err != nil {
			return ConfigResponse{}, err
		}
		return ConfigResponse{
			ConfigID: "cfg",
			Sequence: 1,
			MinVer:   "1.0.0",
			Entries: []ConfigEntry{
				{Key: "DB_PASSWORD", Binding: BindingEnv, Sealed: EncodeEnvelope(blob)},
				sealEntry(t, k, req.Addr("LOG_LEVEL"), BindingInvariant, "debug"),
			},
		}, nil
	}
	c := newClient(t, h, nil)
	if err := c.Enrol(ctx(t)); err != nil {
		t.Fatal(err)
	}
	priv, err := c.State().MachineKey()
	if err != nil {
		t.Fatal(err)
	}
	pub = priv.PublicKey()

	_, err = c.Load(ctx(t))
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Load err = %v, want ErrDecrypt", err)
	}
	if !strings.Contains(err.Error(), "cannot be a config value") {
		t.Errorf("error should refuse by kind: %v", err)
	}
}

// TestAMissingKeyIsTheApplicationsToNotice: the library enforces no schema, so
// a key hz did not serve arrives as Lookup's false and nothing else. An app that
// ignores that boolean reintroduces the compiled default this project exists to
// prevent — which is now its bug to avoid, not the library's to refuse.
func TestAMissingKeyIsTheApplicationsToNotice(t *testing.T) {
	h := newHZ(t)
	h.config = func(req ConfigRequest, k EnvKey) (ConfigResponse, error) {
		return ConfigResponse{
			ConfigID: "cfg", Sequence: 1, MinVer: "1.0.0",
			Entries: []ConfigEntry{sealEntry(t, k, req.Addr("DB_PASSWORD"), BindingEnv, "hunter2")},
		}, nil
	}
	c := newClient(t, h, nil)
	cfg, err := c.Load(ctx(t))
	if err != nil {
		t.Fatalf("a config hz served short is still a config: %v", err)
	}
	if v, ok := cfg.Lookup("DB_PASSWORD"); !ok || v != "hunter2" {
		t.Errorf("Lookup(DB_PASSWORD) = %q, %v", v, ok)
	}
	if v, ok := cfg.Lookup("LOG_LEVEL"); ok {
		t.Errorf("Lookup on a key hz did not serve = %q, %v; want \"\", false", v, ok)
	}
	// And no panic: there is no declaration for a read to be undeclared against.
	if got := cfg.Get("NEVER_SERVED"); got != "" {
		t.Errorf("Get on an absent key = %q, want the empty string", got)
	}
	if got := cfg.Bytes("NEVER_SERVED"); got != nil {
		t.Errorf("Bytes on an absent key = %v, want nil", got)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	dir := t.TempDir()
	base := Options{BaseURL: "http://hz", StateDir: dir, Machine: "b", Environment: "prod", App: "redline", Role: "app", Version: testVersion}

	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no base url", func(o *Options) { o.BaseURL = "" }},
		{"no version", func(o *Options) { o.Version = "" }},
		{"no role", func(o *Options) { o.Role = "" }},
		{"an environment that walks out of the tree", func(o *Options) { o.Environment = ".." }},
		{"an uppercase app", func(o *Options) { o.App = "Redline" }},
		{"a relative state dir", func(o *Options) { o.StateDir = "state" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mutate(&o)
			if _, err := New(o); err == nil {
				t.Fatal("New accepted it")
			}
		})
	}
}

func TestPollTimeoutIsNotADenial(t *testing.T) {
	h := newHZ(t)
	h.state = StatePending
	c := newClient(t, h, nil)

	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := c.Enrol(cctx)
	if errors.Is(err, ErrDenied) {
		t.Fatal("waiting is not a denial")
	}
	if !errors.Is(err, ErrUnsettled) {
		t.Fatalf("Enrol err = %v, want ErrUnsettled", err)
	}
}

func statOrFail(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi
}
