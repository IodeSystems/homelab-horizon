package server

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// cmRecoveryServer is cmServer with a config PATH, because every write here
// goes through Server.updateConfig and therefore through config.Save — which is
// also what makes these tests assert the thing that matters: that the wrap
// lands in the file the backup zip carries.
func cmRecoveryServer(t *testing.T) (*Server, *http.Cookie, string) {
	t.Helper()
	s, admin := cmServer(t)
	path := filepath.Join(t.TempDir(), "config.json")
	s.configPath = path
	return s, admin, path
}

// recoveryFixtureKey mints an obviously generated throwaway keypair. This
// repository is public: no key in it may be real.
func recoveryFixtureKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("fixture key: %v", err)
	}
	return priv
}

func addRecoveryRecipient(t *testing.T, s *Server, admin *http.Cookie, name string, priv *ecdh.PrivateKey) {
	t.Helper()
	w := cmAdminCall(t, admin, s.handleAPICMRecoveryRecipients, http.MethodPost, apitypes.CMPathRecoveryRecipients,
		apitypes.CMRecoveryRecipientReq{Name: name, PublicKey: configmgr.MarshalMachinePublicKey(priv.PublicKey())})
	if w.Code != http.StatusOK {
		t.Fatalf("add recipient %s: status %d: %s", name, w.Code, w.Body.String())
	}
}

// wrapFor produces the blob a client would send: the same WrapEnvKey call an
// approval makes, to a recovery recipient's public key instead of a box's.
func wrapFor(t *testing.T, pub *ecdh.PublicKey, addr configmgr.EnvKeyAddr, key configmgr.EnvKey) string {
	t.Helper()
	blob, err := configmgr.WrapEnvKey(pub, addr, key)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	return configmgr.EncodeEnvelope(blob)
}

func TestRecoveryStartsEmptyAndStaysOptional(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	w := cmAdminCall(t, admin, s.handleAPICMRecovery, http.MethodGet, apitypes.CMPathRecovery, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.CMRecoveryResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Arrays, not nulls: a client that iterates must not have to nil-check, and
	// "no recipients" is a legal, silent, default state.
	if len(resp.Recipients) != 0 || len(resp.Wraps) != 0 {
		t.Fatalf("a fresh config reported custody it does not have: %+v", resp)
	}
}

func TestRecoveryWrapIsStoredInTheConfigTheBackupCarries(t *testing.T) {
	s, admin, path := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)

	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	req := apitypes.CMRecoveryWrapReq{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops",
		Wrapped: wrapFor(t, ops.PublicKey(), addr, key),
	}
	w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.CMRecoveryWrapResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Stored {
		t.Fatal("the first wrap reported that it stored nothing")
	}

	// On DISK, not merely in memory. handlers_backup.go's export zip carries
	// config.json and does NOT carry hz.db, so this file is the only store in
	// hz whose contents survive the loss of the box — which is the entire
	// reason the wraps live here.
	saved, err := config.Load(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	stored, ok := saved.FindRecoveryWrap(addr.Environment, addr.App, addr.Role, key.ID().String(), "ops")
	if !ok {
		t.Fatalf("the wrap is not in the saved config: %+v", saved.RecoveryWraps)
	}

	// And it really is the key: unwrap it with the private half, the way a
	// recovery would. This is the round trip the whole feature is for.
	blob, err := configmgr.DecodeEnvelope(stored.Wrapped)
	if err != nil {
		t.Fatalf("decode stored blob: %v", err)
	}
	got, err := configmgr.UnwrapEnvKey(ops, addr, blob)
	if err != nil {
		t.Fatalf("the stored wrap does not open with the recipient's private key: %v", err)
	}
	if got.ID() != key.ID() {
		t.Fatalf("the stored wrap holds key %s, want %s", got.ID(), key.ID())
	}
}

func TestRecoveryWrapsToEveryRecipientIndependently(t *testing.T) {
	// The list, not a pair. Two recipients, two wraps of the same key, each
	// opening with its own private key and neither with the other's — which is
	// what makes a successor a successor rather than a shared credential.
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	successor := recoveryFixtureKey(t)
	stranger := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)
	addRecoveryRecipient(t, s, admin, "successor", successor)

	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	for name, priv := range map[string]*ecdh.PrivateKey{"ops": ops, "successor": successor} {
		req := apitypes.CMRecoveryWrapReq{
			Environment: addr.Environment, App: addr.App, Role: addr.Role,
			KeyID: key.ID().String(), Recipient: name,
			Wrapped: wrapFor(t, priv.PublicKey(), addr, key),
		}
		if w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req); w.Code != http.StatusOK {
			t.Fatalf("store %s: %d: %s", name, w.Code, w.Body.String())
		}
	}

	cfg := s.cfg()
	if len(cfg.RecoveryWraps) != 2 {
		t.Fatalf("want one wrap per recipient, got %d", len(cfg.RecoveryWraps))
	}
	for _, tc := range []struct {
		name string
		priv *ecdh.PrivateKey
		open bool
	}{
		{"ops", ops, true},
		{"successor", successor, true},
		{"an unrelated key", stranger, false},
	} {
		opened := 0
		for _, stored := range cfg.RecoveryWraps {
			blob, err := configmgr.DecodeEnvelope(stored.Wrapped)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := configmgr.UnwrapEnvKey(tc.priv, addr, blob); err == nil {
				opened++
			}
		}
		if tc.open && opened != 1 {
			t.Fatalf("%s opened %d wraps, want exactly its own", tc.name, opened)
		}
		if !tc.open && opened != 0 {
			t.Fatalf("%s opened %d wraps and is not a recipient at all", tc.name, opened)
		}
	}
}

func TestRecoveryWrapRefusesABlobAddressedElsewhere(t *testing.T) {
	// The check hz CAN make without a private key, and the reason to make it: a
	// blob wrapped to the wrong key stores cleanly and is discovered unopenable
	// at the only moment it is ever needed. Same rule cmApprove enforces for a
	// machine's grant.
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	someoneElse := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)

	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	req := apitypes.CMRecoveryWrapReq{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops",
		Wrapped: wrapFor(t, someoneElse.PublicKey(), addr, key),
	}
	w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "addressed to") {
		t.Fatalf("the refusal does not say why: %s", w.Body.String())
	}
	if len(s.cfg().RecoveryWraps) != 0 {
		t.Fatal("a blob nobody can open was stored anyway")
	}
}

func TestRecoveryWrapRefusesTheWrongKindOfEnvelope(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)

	// A machine-sealed envelope: correctly addressed to this recipient, but not
	// a wrapped environment key. Storing it would file a payload that cannot be
	// a key under a key id.
	sealed, err := configmgr.SealToMachine(ops.PublicKey(), configmgr.MachineAddr{Machine: "box-1", Key: "TOKEN"}, []byte("not a key"))
	if err != nil {
		t.Fatal(err)
	}
	key := configmgr.NewEnvKey()
	req := apitypes.CMRecoveryWrapReq{
		Environment: "prod", App: "redline", Role: "app",
		KeyID: key.ID().String(), Recipient: "ops",
		Wrapped: configmgr.EncodeEnvelope(sealed),
	}
	w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "wrapped environment key") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestRecoveryWrapRefusesAnUnknownRecipientAndABadKeyID(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	good := wrapFor(t, ops.PublicKey(), addr, key)

	t.Run("unknown recipient", func(t *testing.T) {
		req := apitypes.CMRecoveryWrapReq{Environment: "prod", App: "redline", Role: "app",
			KeyID: key.ID().String(), Recipient: "nobody", Wrapped: good}
		w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
		}
	})
	t.Run("key id is not canonical", func(t *testing.T) {
		// The id becomes a keystore filename on every client, so "*" must never
		// reach a directory scan. Same reason cmApprove validates it.
		req := apitypes.CMRecoveryWrapReq{Environment: "prod", App: "redline", Role: "app",
			KeyID: "*", Recipient: "ops", Wrapped: good}
		w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
		}
	})
	t.Run("no address", func(t *testing.T) {
		req := apitypes.CMRecoveryWrapReq{KeyID: key.ID().String(), Recipient: "ops", Wrapped: good}
		w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
		}
	})
}

func TestRecoveryWrapIsIdempotentAndReplaceable(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()

	post := func(replace bool) apitypes.CMRecoveryWrapResp {
		t.Helper()
		req := apitypes.CMRecoveryWrapReq{
			Environment: addr.Environment, App: addr.App, Role: addr.Role,
			KeyID: key.ID().String(), Recipient: "ops",
			Wrapped: wrapFor(t, ops.PublicKey(), addr, key), Replace: replace,
		}
		w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var resp apitypes.CMRecoveryWrapResp
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	first := post(false)
	if !first.Stored {
		t.Fatal("first wrap was not stored")
	}
	second := post(false)
	if second.Stored {
		t.Fatal("a re-post replaced a stored wrap without being asked to")
	}
	if second.Wrap.Wrapped != first.Wrap.Wrapped {
		t.Fatal("the response did not echo the wrap that is actually held")
	}
	third := post(true)
	if !third.Stored || third.Wrap.Wrapped == first.Wrap.Wrapped {
		t.Fatal("replace did not replace")
	}
	if len(s.cfg().RecoveryWraps) != 1 {
		t.Fatalf("idempotency appended rows: %d", len(s.cfg().RecoveryWraps))
	}
}

func TestRecoveryRecipientRemovalKeepsTheWraps(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	ops := recoveryFixtureKey(t)
	addRecoveryRecipient(t, s, admin, "ops", ops)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	req := apitypes.CMRecoveryWrapReq{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops", Wrapped: wrapFor(t, ops.PublicKey(), addr, key),
	}
	if w := cmAdminCall(t, admin, s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps, req); w.Code != http.StatusOK {
		t.Fatalf("store: %d: %s", w.Code, w.Body.String())
	}

	w := cmAdminCall(t, admin, s.handleAPICMRecoveryRecipients, http.MethodDelete, apitypes.CMPathRecoveryRecipients+"/ops", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d: %s", w.Code, w.Body.String())
	}
	cfg := s.cfg()
	if len(cfg.RecoveryRecipients) != 0 {
		t.Fatal("the recipient survived removal")
	}
	// Removal is not revocation: the holder of that private key can still open
	// what was already wrapped, so hz keeps the blob rather than pretending.
	if len(cfg.RecoveryWraps) != 1 {
		t.Fatal("removing a recipient deleted its wraps")
	}

	again := cmAdminCall(t, admin, s.handleAPICMRecoveryRecipients, http.MethodDelete, apitypes.CMPathRecoveryRecipients+"/ops", nil)
	if again.Code != http.StatusNotFound {
		t.Fatalf("removing an absent recipient: status %d, want 404", again.Code)
	}
}

func TestRecoveryRecipientRefusesAnUnusablePublicKey(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	for _, tc := range []struct{ name, key string }{
		{"empty", ""},
		{"not a key", "hzpub-nope"},
		{"not on the curve", "hzpub-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := cmAdminCall(t, admin, s.handleAPICMRecoveryRecipients, http.MethodPost, apitypes.CMPathRecoveryRecipients,
				apitypes.CMRecoveryRecipientReq{Name: "ops", PublicKey: tc.key})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
			}
			if len(s.cfg().RecoveryRecipients) != 0 {
				t.Fatal("a recipient nothing can wrap to was stored")
			}
		})
	}
}

func TestRecoveryRefusesANonAdmin(t *testing.T) {
	// No cookie at all. Every path on this surface serves or accepts blobs, and
	// while none of them discloses a key, none of them is public either.
	s, _, _ := cmRecoveryServer(t)
	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		method string
		path   string
	}{
		{"read", s.handleAPICMRecovery, http.MethodGet, apitypes.CMPathRecovery},
		{"add recipient", s.handleAPICMRecoveryRecipients, http.MethodPost, apitypes.CMPathRecoveryRecipients},
		{"store wrap", s.handleAPICMRecoveryWraps, http.MethodPost, apitypes.CMPathRecoveryWraps},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := cmAdminCall(t, &http.Cookie{Name: userSessionCookie, Value: "not-a-session"}, tc.h, tc.method, tc.path, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestCMRecoveryRoutesAreReachableAsBuilt drives the REAL mux, because the
// table in cm_routes_test.go can only reach GET — and the two routes that
// matter most here are a POST to a collection and a DELETE on a subtree, which
// is exactly the pair that was unrouted for machines. An unrouted wrap endpoint
// would mean `hz cm key new` silently cannot establish custody, discovered
// during a recovery rather than now.
func TestCMRecoveryRoutesAreReachableAsBuilt(t *testing.T) {
	s, admin, _ := cmRecoveryServer(t)
	mux := s.setupRoutes()
	ops := recoveryFixtureKey(t)

	call := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			raw, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		}
		r.AddCookie(admin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		// The mux's own 404 is plain text; a handler's is JSON and means the
		// request was understood.
		if w.Code == http.StatusNotFound && !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s %s is not registered: %s", method, path, strings.TrimSpace(w.Body.String()))
		}
		return w
	}

	if w := call(t, http.MethodPost, apitypes.CMPathRecoveryRecipients,
		apitypes.CMRecoveryRecipientReq{Name: "ops", PublicKey: configmgr.MarshalMachinePublicKey(ops.PublicKey())}); w.Code != http.StatusOK {
		t.Fatalf("add recipient through the mux: %d: %s", w.Code, w.Body.String())
	}

	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := configmgr.NewEnvKey()
	if w := call(t, http.MethodPost, apitypes.CMPathRecoveryWraps, apitypes.CMRecoveryWrapReq{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops", Wrapped: wrapFor(t, ops.PublicKey(), addr, key),
	}); w.Code != http.StatusOK {
		t.Fatalf("store wrap through the mux: %d: %s", w.Code, w.Body.String())
	}

	if w := call(t, http.MethodDelete, apitypes.CMPathRecoveryRecipients+"/ops", nil); w.Code != http.StatusOK {
		t.Fatalf("remove recipient through the mux: %d: %s", w.Code, w.Body.String())
	}
}

// TestRecoveryWireShapesCannotCarryAPrivateKey is a structural check, not a
// behavioural one: the invariant is that hz has no field a private key could
// arrive in, and the way that breaks is somebody adding one. A name containing
// "private" or "secret" on a request type here is the shape of that mistake.
func TestRecoveryWireShapesCannotCarryAPrivateKey(t *testing.T) {
	raw, err := json.Marshal(apitypes.CMRecoveryRecipientReq{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	rawWrap, err := json.Marshal(apitypes.CMRecoveryWrapReq{})
	if err != nil {
		t.Fatal(err)
	}
	var wrapFields map[string]any
	if err := json.Unmarshal(rawWrap, &wrapFields); err != nil {
		t.Fatal(err)
	}
	for _, m := range []map[string]any{fields, wrapFields} {
		for name := range m {
			lower := strings.ToLower(name)
			if strings.Contains(lower, "private") || strings.Contains(lower, "secret") {
				t.Fatalf("field %q on a recovery request body: hz must never receive a private key", name)
			}
		}
	}
	if _, ok := fields["publicKey"]; !ok {
		t.Fatal("the recipient request lost its publicKey field")
	}
}
