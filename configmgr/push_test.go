package configmgr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The push tests run against a fake hz and a real keystore on a real directory
// tree. Nothing is stubbed on the crypto side: the envelopes the fake receives
// are opened again with the key the test planted, at the address the value was
// sealed at, so a test that passed while the binding was missing is not
// available here.

var pushAddr = EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}

var pushSchema = Schema{
	"DB_PASSWORD":    BindingEnv,
	"RETENTION_DAYS": BindingInvariant,
}

var pushValues = map[string]string{
	"DB_PASSWORD":    "hunter2",
	"RETENTION_DAYS": "30-days-retained",
}

// blessHZ is hz's admin API, reduced to the two endpoints a push touches.
type blessHZ struct {
	t   *testing.T
	mu  sync.Mutex
	srv *httptest.Server

	// pointers is what hz calls current, keyed "environment/app/role". A
	// missing entry answers with no key id, which means "no pointer set".
	pointers map[string]string

	// pointer404 makes the endpoint answer 404, the other way hz says the
	// same thing.
	pointer404 bool

	posted []apitypes.CMCreateConfigReq
	raw    []string // every request body, verbatim
}

func newBlessHZ(t *testing.T) *blessHZ {
	t.Helper()
	h := &blessHZ{t: t, pointers: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminCurrentKeyPath, h.handleCurrentKey)
	mux.HandleFunc("POST "+adminConfigsPath, h.handleBless)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *blessHZ) handleCurrentKey(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pointer404 {
		http.Error(w, "no pointer", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	addr := q.Get("environment") + "/" + q.Get("app") + "/" + q.Get("role")
	writeJSON(h.t, w, apitypes.CMCurrentKeyResp{
		Environment: q.Get("environment"), App: q.Get("app"), Role: q.Get("role"),
		KeyID: h.pointers[addr],
	})
}

func (h *blessHZ) handleBless(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.raw = append(h.raw, string(body))
	var req apitypes.CMCreateConfigReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.posted = append(h.posted, req)
	writeJSON(h.t, w, apitypes.CMConfigResp{
		ID: "cfg-1", Environment: req.Environment, App: req.App, Role: req.Role,
		MinVer: req.MinVer, MaxVer: req.MaxVer, Sequence: 7,
	})
}

// sentAnywhere reports whether a string appears in any request body. It is how
// the "no plaintext and no key material on the wire" assertions are made: they
// look at the bytes, not at the struct that produced them.
func (h *blessHZ) sentAnywhere(needle string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, b := range h.raw {
		if strings.Contains(b, needle) {
			return true
		}
	}
	return false
}

func pushOpts(h *blessHZ, ks *Keystore, mutate func(*PushOptions)) PushOptions {
	// Copies: a mutate func adds and deletes, and a shared map would carry that
	// into the next test.
	values := make(map[string]string, len(pushValues))
	for k, v := range pushValues {
		values[k] = v
	}
	schema := make(Schema, len(pushSchema))
	for k, v := range pushSchema {
		schema[k] = v
	}
	o := PushOptions{
		BaseURL:     h.srv.URL,
		Keystore:    ks,
		Schema:      schema,
		Environment: pushAddr.Environment,
		App:         pushAddr.App,
		Role:        pushAddr.Role,
		MinVer:      "1.0.0",
		Values:      values,
	}
	if mutate != nil {
		mutate(&o)
	}
	return o
}

// pushFixture plants one key at the address and points hz at it.
func pushFixture(t *testing.T) (*blessHZ, *Keystore, EnvKey) {
	t.Helper()
	ks := ksNew(t)
	key := ksPut(t, ks, keystoreAddr(pushAddr), "2026-01", ksAt("2026-01-01T00:00:00Z"))
	h := newBlessHZ(t)
	h.pointers[pushAddr.String()] = key.ID().String()
	return h, ks, key
}

func keystoreAddr(a EnvKeyAddr) Addr {
	return Addr{Environment: a.Environment, App: a.App, Role: a.Role}
}

func pushCtx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestPushSealsUnderTheCurrentKey is the whole happy path: hz names a key, the
// keystore agrees, every value is sealed under it at its own address, and what
// crosses the wire is envelopes and names.
func TestPushSealsUnderTheCurrentKey(t *testing.T) {
	h, ks, key := pushFixture(t)

	res, err := Push(pushCtx(t), pushOpts(h, ks, nil))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.ConfigID != "cfg-1" || res.Sequence != 7 {
		t.Errorf("result = %s", res)
	}
	if res.KeyID != key.ID() {
		t.Errorf("sealed under %s, want the key hz calls current, %s", res.KeyID, key.ID())
	}
	if len(h.posted) != 1 {
		t.Fatalf("posted %d configs, want 1", len(h.posted))
	}
	req := h.posted[0]
	if req.Environment != "staging" || req.App != "redline" || req.Role != "app" || req.MinVer != "1.0.0" || req.MaxVer != "" {
		t.Errorf("blessed the wrong address or range: %+v", req)
	}
	if len(req.Values) != 2 {
		t.Fatalf("posted %d values, want 2", len(req.Values))
	}

	for _, v := range req.Values {
		if v.Binding != pushSchema[v.Key] {
			t.Errorf("%s posted as %s, declared %s", v.Key, v.Binding, pushSchema[v.Key])
		}
		if v.KeyID != key.ID().String() {
			t.Errorf("%s names key %s, want %s", v.Key, v.KeyID, key.ID())
		}
		envelope, err := DecodeEnvelope(v.Sealed)
		if err != nil {
			t.Fatalf("%s: %v", v.Key, err)
		}
		pt, err := Open(key, Addr{Environment: "staging", App: "redline", Role: "app", Key: v.Key}, envelope)
		if err != nil {
			t.Fatalf("%s did not open at its own address: %v", v.Key, err)
		}
		if string(pt) != pushValues[v.Key] {
			t.Errorf("%s = %q, want %q", v.Key, pt, pushValues[v.Key])
		}
		// The key name is bound into the AEAD, so the same bytes must not open
		// under another key's address.
		if _, err := Open(key, Addr{Environment: "staging", App: "redline", Role: "app", Key: "SOMETHING_ELSE"}, envelope); err == nil {
			t.Errorf("%s opened at the wrong key name", v.Key)
		}
	}

	// Nothing readable crossed the wire: not the plaintexts, not the key.
	for _, plain := range []string{"hunter2", "30-days-retained"} {
		if h.sentAnywhere(plain) {
			t.Errorf("plaintext %q was sent to hz", plain)
		}
	}
	if h.sentAnywhere(key.Text()) {
		t.Error("key material was sent to hz")
	}
}

// TestPushRefusesAValueWithNoDeclaredBinding: a bless has to label every value
// invariant or env, and a value with no binding is not one hz can store.
func TestPushRefusesAValueWithNoDeclaredBinding(t *testing.T) {
	h, ks, _ := pushFixture(t)
	opts := pushOpts(h, ks, func(o *PushOptions) { o.Values["SURPRISE"] = "yes" })

	_, err := Push(pushCtx(t), opts)
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("Push err = %v, want ErrSchema", err)
	}
	if !strings.Contains(err.Error(), "SURPRISE") {
		t.Errorf("the error should name the key: %v", err)
	}
	if len(h.posted) != 0 {
		t.Error("something was posted")
	}
	if h.sentAnywhere("yes") {
		t.Error("the undeclared value reached hz")
	}
}

// TestPushRefusesADeclaredKeyWithNoValue: a config hz then serves short makes a
// box fall back to a compiled default, which is the founding bug.
func TestPushRefusesADeclaredKeyWithNoValue(t *testing.T) {
	h, ks, _ := pushFixture(t)
	opts := pushOpts(h, ks, func(o *PushOptions) { delete(o.Values, "RETENTION_DAYS") })

	_, err := Push(pushCtx(t), opts)
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("Push err = %v, want ErrSchema", err)
	}
	if !strings.Contains(err.Error(), "RETENTION_DAYS") {
		t.Errorf("the error should name the key: %v", err)
	}
	if len(h.posted) != 0 {
		t.Error("something was posted")
	}
}

// TestPushSurfacesTheSealRefusal: sealing goes through the keystore's
// current-key path, so the pointer disagreement stops the push — and the
// message has to leave an operator with a move.
func TestPushSurfacesTheSealRefusal(t *testing.T) {
	h, ks, _ := pushFixture(t)
	// A second, newer key on disk that hz has never heard of: exactly what a
	// dropped-in key file looks like.
	newer := ksPut(t, ks, keystoreAddr(pushAddr), "2026-09", ksAt("2026-09-01T00:00:00Z"))

	_, err := Push(pushCtx(t), pushOpts(h, ks, nil))
	if !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("Push err = %v, want ErrRefuseToSeal", err)
	}
	for _, want := range []string{"hz cm key import", "hz cm key current", newer.ID().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q:\n%v", want, err)
		}
	}
	if len(h.posted) != 0 {
		t.Error("something was posted after a refusal to seal")
	}
}

// TestPushRefusesWhenNothingIsHeld: no key at the address is not a reason to
// invent one.
func TestPushRefusesWhenNothingIsHeld(t *testing.T) {
	h := newBlessHZ(t)
	ks := ksNew(t)

	_, err := Push(pushCtx(t), pushOpts(h, ks, nil))
	if !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("Push err = %v, want ErrNoSuchKey", err)
	}
	if !strings.Contains(err.Error(), "hz cm key new") {
		t.Errorf("the refusal should say how to mint the first key: %v", err)
	}
	if len(h.posted) != 0 {
		t.Error("something was posted")
	}
}

// TestPushWithNoPointerUsesTheOnlyKey: a brand-new address has no pointer yet,
// and there is nothing to arbitrate when exactly one key is held. Both ways hz
// says "no pointer" — an empty id and a 404 — take this path.
func TestPushWithNoPointerUsesTheOnlyKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*blessHZ)
	}{
		{"an empty key id", func(h *blessHZ) { h.pointers = map[string]string{} }},
		{"a 404", func(h *blessHZ) { h.pointer404 = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ks, key := pushFixture(t)
			tc.setup(h)

			res, err := Push(pushCtx(t), pushOpts(h, ks, nil))
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			if res.KeyID != key.ID() {
				t.Errorf("sealed under %s, want the only key held, %s", res.KeyID, key.ID())
			}
		})
	}
}

// TestPushDryRunPostsNothing: it still consults the keystore and the pointer, so
// a dry run that succeeds says the real one would too — it just sends nothing.
func TestPushDryRunPostsNothing(t *testing.T) {
	h, ks, key := pushFixture(t)

	res, err := Push(pushCtx(t), pushOpts(h, ks, func(o *PushOptions) { o.DryRun = true }))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !res.DryRun || res.ConfigID != "" || res.Sequence != 0 {
		t.Errorf("a dry run blessed something: %+v", res)
	}
	if res.KeyID != key.ID() {
		t.Errorf("a dry run should still resolve the sealing key, got %s", res.KeyID)
	}
	if len(res.Keys) != 2 {
		t.Errorf("Keys = %v, want both declared keys", res.Keys)
	}
	if len(h.raw) != 0 {
		t.Fatalf("a dry run posted %d bodies: %v", len(h.raw), h.raw)
	}
}

// TestPushDryRunStillRefuses: the checks are not skipped on the way to sending
// nothing, or a dry run would say yes to a push that cannot happen.
func TestPushDryRunStillRefuses(t *testing.T) {
	h, ks, _ := pushFixture(t)
	opts := pushOpts(h, ks, func(o *PushOptions) {
		o.DryRun = true
		o.Values["SURPRISE"] = "yes"
	})
	if _, err := Push(pushCtx(t), opts); !errors.Is(err, ErrSchema) {
		t.Fatalf("Push err = %v, want ErrSchema", err)
	}
}

func TestPushValidatesItsOptions(t *testing.T) {
	h, ks, _ := pushFixture(t)
	cases := []struct {
		name   string
		mutate func(*PushOptions)
	}{
		{"no base url", func(o *PushOptions) { o.BaseURL = "" }},
		{"no keystore", func(o *PushOptions) { o.Keystore = nil }},
		{"no environment", func(o *PushOptions) { o.Environment = "" }},
		{"no app", func(o *PushOptions) { o.App = "" }},
		{"no role", func(o *PushOptions) { o.Role = "" }},
		{"no minimum version", func(o *PushOptions) { o.MinVer = "" }},
		{"no values", func(o *PushOptions) { o.Values = nil }},
		{"no schema", func(o *PushOptions) { o.Schema = nil }},
		{"a binding that is not declarable", func(o *PushOptions) { o.Schema = Schema{"DB_PASSWORD": "secret"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Push(pushCtx(t), pushOpts(h, ks, tc.mutate)); err == nil {
				t.Fatal("Push accepted it")
			}
			if len(h.posted) != 0 {
				t.Error("something was posted")
			}
		})
	}
}

// TestPushReportsWhatHzCalledIt: resolution is computed and never stored, so the
// sequence a push reports is the only place the fact exists.
func TestPushReportsWhatHzCalledIt(t *testing.T) {
	h, ks, key := pushFixture(t)

	res, err := Push(pushCtx(t), pushOpts(h, ks, func(o *PushOptions) { o.MaxVer = "2.0.0" }))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	s := res.String()
	for _, want := range []string{"staging/redline/app", "cfg-1", "sequence 7", "1.0.0–2.0.0", key.ID().String()} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, should mention %q", s, want)
		}
	}
	for _, plain := range []string{"hunter2", "30-days-retained"} {
		if strings.Contains(s, plain) {
			t.Errorf("String() disclosed a value: %q", s)
		}
	}
}
