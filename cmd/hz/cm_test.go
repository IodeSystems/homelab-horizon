package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// --- a stub hz -------------------------------------------------------------

// cmStub serves the config-manager routes the CLI calls and records every body
// it was sent, so a test can assert what did and did not cross the wire. The
// assertion that matters most in this file is a negative one: no request body
// ever contains key material.
type cmStub struct {
	mu sync.Mutex

	registration apitypes.CMRegistrationResp
	publicKey    string
	configs      map[string]apitypes.CMConfigResp
	resolve      map[string]apitypes.CMResolveResp // "env/app/role" -> answer
	gate         apitypes.CMPromotionGateResp
	currentKeys  map[string]string // "env/app/role" -> key id
	// machines is keyed by BOTH name and id, so a test can drive the CLI's
	// reference either way.
	machines map[string]apitypes.CMMachineResp

	approved   []apitypes.CMApproveReq
	denied     []apitypes.CMDenyReq
	posted     []apitypes.CMCreateConfigReq
	removed    []string // machine names the stub actually deleted
	rawBodies  []string
	approveErr int
}

func newCMStub() *cmStub {
	return &cmStub{
		configs:     map[string]apitypes.CMConfigResp{},
		resolve:     map[string]apitypes.CMResolveResp{},
		currentKeys: map[string]string{},
		machines:    map[string]apitypes.CMMachineResp{},
	}
}

// addMachine registers a machine under its name and its id.
func (s *cmStub) addMachine(m apitypes.CMMachineResp) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines[m.Name] = m
	if m.ID != "" {
		s.machines[m.ID] = m
	}
}

func (s *cmStub) start(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		if len(raw) > 0 {
			s.rawBodies = append(s.rawBodies, string(raw))
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})

		case strings.HasSuffix(p, "/public-key"):
			_ = json.NewEncoder(w).Encode(cmPublicKeyResp{PublicKey: s.publicKey})

		case strings.HasSuffix(p, "/approve"):
			if s.approveErr != 0 {
				w.WriteHeader(s.approveErr)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
				return
			}
			var req apitypes.CMApproveReq
			_ = json.Unmarshal(raw, &req)
			s.mu.Lock()
			s.approved = append(s.approved, req)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))

		case strings.HasSuffix(p, "/deny"):
			var req apitypes.CMDenyReq
			_ = json.Unmarshal(raw, &req)
			s.mu.Lock()
			s.denied = append(s.denied, req)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))

		case strings.HasPrefix(p, "/api/v1/cm/registrations/"):
			_ = json.NewEncoder(w).Encode(s.registration)

		case p == "/api/v1/cm/registrations":
			_ = json.NewEncoder(w).Encode([]apitypes.CMRegistrationResp{s.registration})

		case p == "/api/v1/cm/current-key":
			q := r.URL.Query()
			k := q.Get(apitypes.CMQueryEnv) + "/" + q.Get(apitypes.CMQueryApp) + "/" + q.Get(apitypes.CMQueryRole)
			if r.Method == http.MethodPut {
				var req apitypes.CMCurrentKeyReq
				_ = json.Unmarshal(raw, &req)
				s.mu.Lock()
				s.currentKeys[k] = req.KeyID
				s.mu.Unlock()
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			s.mu.Lock()
			id := s.currentKeys[k]
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(apitypes.CMCurrentKeyResp{
				Environment: q.Get(apitypes.CMQueryEnv), App: q.Get(apitypes.CMQueryApp), Role: q.Get(apitypes.CMQueryRole), KeyID: id,
			})

		case p == "/api/v1/cm/configs" && r.Method == http.MethodPost:
			var req apitypes.CMCreateConfigReq
			_ = json.Unmarshal(raw, &req)
			s.mu.Lock()
			s.posted = append(s.posted, req)
			n := len(s.posted)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(apitypes.CMConfigResp{ID: "new-config", Sequence: int64(n)})

		case strings.HasPrefix(p, "/api/v1/cm/configs/"):
			id := strings.TrimPrefix(p, "/api/v1/cm/configs/")
			cfg, ok := s.configs[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no such config"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(cfg)

		// These stubs read the SAME constants the real handlers read. An
		// earlier version spelled them out, and spelled them the way the CLI
		// happened to send rather than the way hz reads — so the CLI's tests
		// passed against its own mistake for as long as it existed. A stub that
		// agrees with the code under test proves only that they agree.
		case p == apitypes.CMPathResolve:
			q := r.URL.Query()
			k := q.Get(apitypes.CMQueryEnv) + "/" + q.Get(apitypes.CMQueryApp) + "/" + q.Get(apitypes.CMQueryRole)
			_ = json.NewEncoder(w).Encode(s.resolve[k])

		case p == apitypes.CMPathPromoteGate:
			_ = json.NewEncoder(w).Encode(s.gate)

		case p == apitypes.CMPathMachines:
			s.mu.Lock()
			seen := map[string]bool{}
			rows := []apitypes.CMMachineResp{}
			for _, m := range s.machines {
				if !seen[m.Name] {
					seen[m.Name] = true
					rows = append(rows, m)
				}
			}
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(rows)

		// The removal guard is enforced HERE, the way hz enforces it, rather
		// than accepting whatever the CLI sends. A stub that took any confirm
		// value would pass against a CLI that echoed back the reference the
		// operator typed — which breaks the moment someone removes by id, and
		// is the exact shape of bug cm_routes_test.go exists for.
		case strings.HasPrefix(p, apitypes.CMPathMachines+"/"):
			ref := strings.TrimPrefix(p, apitypes.CMPathMachines+"/")
			s.mu.Lock()
			m, ok := s.machines[ref]
			s.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no such machine"}`))
				return
			}
			if r.Method != http.MethodDelete {
				_ = json.NewEncoder(w).Encode(m)
				return
			}
			if r.URL.Query().Get(apitypes.CMQueryConfirm) != m.Name {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"confirm does not name the machine"}`))
				return
			}
			s.mu.Lock()
			s.removed = append(s.removed, m.Name)
			delete(s.machines, m.Name)
			delete(s.machines, m.ID)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(apitypes.CMMachineRemovedResp{
				ID: m.ID, Name: m.Name,
				RegistrationsRemoved: len(m.Registrations),
				SecretsRemoved:       len(m.SecretKeys),
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unrouted: ` + p + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

// sentAnywhere reports whether any request body contained s. Used to assert the
// one invariant the whole design rests on.
func (s *cmStub) sentAnywhere(needle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.rawBodies {
		if strings.Contains(b, needle) {
			return true
		}
	}
	return false
}

// --- keystore fixtures -----------------------------------------------------

func testKeystore(t *testing.T) *configmgr.Keystore {
	t.Helper()
	root := t.TempDir()
	// t.TempDir honours the umask, which on most boxes leaves 0775 — and the
	// keystore refuses a group-writable directory, correctly, because a key
	// inside one can be unlinked and replaced whatever its own mode says.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configmgr.KeystoreRootEnv, root)
	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	return ks
}

func putKey(t *testing.T, ks *configmgr.Keystore, addr configmgr.EnvKeyAddr, label string, created time.Time) configmgr.EnvKey {
	t.Helper()
	k := configmgr.NewEnvKey()
	if err := ks.Put(keyAddr(addr), label, k, created); err != nil {
		t.Fatalf("put key: %v", err)
	}
	return k
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; _ = f.Close() })
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// --- address parsing -------------------------------------------------------

func TestParseCMAddr(t *testing.T) {
	good, err := parseCMAddr("staging/redline/app")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if good.Environment != "staging" || good.App != "redline" || good.Role != "app" {
		t.Fatalf("got %+v", good)
	}
	for _, bad := range []string{"staging/redline", "a/b/c/d", "/b/c", "a//c", "a/b/", ""} {
		if _, err := parseCMAddr(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// --- key minting and listing ----------------------------------------------

func TestKeyNewAndList(t *testing.T) {
	ks := testKeystore(t)
	s := newCMStub()
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmKeyNew(c, []string{"--label", "2026-01", "staging/redline/app"}); err != nil {
			t.Fatalf("key new: %v", err)
		}
	})

	held, err := ks.List(keyAddr(configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("want 1 key, got %d", len(held))
	}
	if !strings.Contains(out, held[0].ID.String()) {
		t.Errorf("mint output does not name the key id:\n%s", out)
	}

	// The material must not be in the output, and nothing must have been
	// posted: minting is a local act.
	key, err := ks.KeyFor(keyAddr(configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}), held[0].ID)
	if err != nil {
		t.Fatalf("key for: %v", err)
	}
	if strings.Contains(out, key.Text()) {
		t.Error("mint printed the key material")
	}
	if s.sentAnywhere(key.Text()) {
		t.Fatal("mint sent key material to hz")
	}

	listed := captureStdout(t, func() {
		if err := cmKeyList([]string{"--json"}); err != nil {
			t.Fatalf("key ls: %v", err)
		}
	})
	if !strings.Contains(listed, "staging/redline/app") || !strings.Contains(listed, held[0].ID.String()) {
		t.Errorf("ls did not report the key:\n%s", listed)
	}
	if strings.Contains(listed, key.Text()) {
		t.Error("ls printed the key material")
	}
}

func TestKeyNewSetCurrentSendsOnlyAnID(t *testing.T) {
	ks := testKeystore(t)
	s := newCMStub()
	c := s.start(t)

	captureStdout(t, func() {
		if err := cmKeyNew(c, []string{"--label", "2026-01", "--set-current", "staging/redline/app"}); err != nil {
			t.Fatalf("key new: %v", err)
		}
	})
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	held, _ := ks.List(keyAddr(addr))
	if got := s.currentKeys["staging/redline/app"]; got != held[0].ID.String() {
		t.Fatalf("pointer is %q, want %q", got, held[0].ID)
	}
	key, _ := ks.KeyFor(keyAddr(addr), held[0].ID)
	if s.sentAnywhere(key.Text()) {
		t.Fatal("--set-current sent key material")
	}
}

func TestKeyImportRoundTrip(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	original := configmgr.NewEnvKey()

	withStdin(t, "  "+original.Text()+"\n")
	captureStdout(t, func() {
		if err := cmKeyImport([]string{"--label", "2026-01", "--created-at", "2026-01-15T00:00:00Z", "prod/redline/app"}); err != nil {
			t.Fatalf("import: %v", err)
		}
	})

	got, err := ks.KeyFor(keyAddr(addr), original.ID())
	if err != nil {
		t.Fatalf("key for: %v", err)
	}
	if got != original {
		t.Fatal("imported key is not the key that was fed in")
	}
	held, _ := ks.List(keyAddr(addr))
	if !held[0].CreatedAt.Equal(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("created_at not preserved: %s", held[0].CreatedAt)
	}
}

func TestKeyImportRejectsGarbage(t *testing.T) {
	testKeystore(t)
	withStdin(t, "not-a-key\n")
	if err := cmKeyImport([]string{"prod/redline/app"}); err == nil {
		t.Fatal("importing garbage should fail")
	}
}

// TestKeyExportRefusesWithoutATTY is the point of the command's only guard: a
// redirect is how key material lands in a file with the wrong mode. Under `go
// test` stdout is a pipe, so the refusal is the natural path here.
func TestKeyExportRefusesWithoutATTY(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	err := cmKeyExport([]string{"prod/redline/app"})
	if err == nil {
		t.Fatal("export to a pipe should be refused")
	}
	if !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if strings.Contains(err.Error(), key.Text()) {
		t.Fatal("the refusal itself leaked the key")
	}
}

func TestSelectKeyNeedsAnIDWhenSeveralAreHeld(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	putKey(t, ks, addr, "2026-02", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	held, _ := ks.List(keyAddr(addr))

	if _, err := cmSelectKey(addr, held, ""); err == nil {
		t.Fatal("two keys and no --id should be refused, not guessed")
	}
	got, err := cmSelectKey(addr, held, held[1].ID.String())
	if err != nil || got != held[1].ID.String() {
		t.Fatalf("naming a key should work: %q %v", got, err)
	}
	if _, err := cmSelectKey(addr, held, "0011223344556677"); err == nil {
		t.Fatal("an id nothing holds should fail")
	}
	if _, err := cmSelectKey(addr, nil, ""); err == nil {
		t.Fatal("an empty address should fail")
	}
}

// --- the keystore's refusals, surfaced ------------------------------------

func TestSealRefusalIsActionable(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	old := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	putKey(t, ks, addr, "2026-02", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	s := newCMStub()
	// hz still calls the OLD key current, so the newest file on disk is one hz
	// has never heard of. That is exactly the drop-a-file-into-the-keystore
	// shape, and sealing must stop.
	s.currentKeys["staging/redline/app"] = old.ID().String()
	c := s.start(t)

	_, _, err := cmSealingKey(ks, c, addr)
	if err == nil {
		t.Fatal("sealing should be refused while the pointer disagrees")
	}
	for _, want := range []string{"refusing to seal", "hz cm key current", old.ID().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q:\n%v", want, err)
		}
	}
}

func TestSealRefusalWhenNothingIsHeld(t *testing.T) {
	ks := testKeystore(t)
	s := newCMStub()
	c := s.start(t)
	_, _, err := cmSealingKey(ks, c, configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"})
	if err == nil || !strings.Contains(err.Error(), "hz cm key import") {
		t.Fatalf("an empty address should say how to fill it: %v", err)
	}
}

func TestSealUsesTheOnlyKeyWhenHzHasNoPointer(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "dev", App: "redline", Role: "app"}
	only := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := newCMStub()
	c := s.start(t)

	k, info, err := cmSealingKey(ks, c, addr)
	if err != nil {
		t.Fatalf("a brand-new address has no pointer and one key; that must seal: %v", err)
	}
	if k != only || info.ID != only.ID() {
		t.Fatal("sealed under the wrong key")
	}
}

// --- the ceremony ----------------------------------------------------------

func TestApproveWrapsTheKeyAndSendsNoKeyMaterial(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	fp := configmgr.FingerprintOf(priv.PublicKey())

	s := newCMStub()
	s.publicKey = configmgr.MarshalMachinePublicKey(priv.PublicKey())
	s.registration = apitypes.CMRegistrationResp{
		ID: "reg-1", MachineID: "m-1", MachineName: "redline-01",
		Environment: "staging", App: "redline", Role: "app", Version: "1.3.0",
		State: configmgr.StatePending, Fingerprint: fp.String(),
	}
	s.currentKeys["staging/redline/app"] = key.ID().String()
	c := s.start(t)

	// The operator types the fingerprint the box printed.
	withStdin(t, fp.String()+"\n")
	out := captureStdout(t, func() {
		if err := cmApprove(c, []string{"reg-1"}); err != nil {
			t.Fatalf("approve: %v", err)
		}
	})
	if !strings.Contains(out, "matches") {
		t.Errorf("approve should confirm the match:\n%s", out)
	}

	if len(s.approved) != 1 {
		t.Fatalf("want one approval, got %d", len(s.approved))
	}
	got := s.approved[0]
	if got.WrapKeyID != key.ID().String() {
		t.Fatalf("wrap key id is %q, want %q", got.WrapKeyID, key.ID())
	}

	// The blob opens on the box, at the address taken from the registration,
	// and yields exactly the key the keystore holds.
	envelope, err := configmgr.DecodeEnvelope(got.WrappedEnvKey)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	unwrapped, err := configmgr.UnwrapEnvKey(priv, addr, envelope)
	if err != nil {
		t.Fatalf("the box cannot open its own grant: %v", err)
	}
	if unwrapped != key {
		t.Fatal("the box unwrapped a different key")
	}

	// And the grant is bound to this registration's address: relayed into
	// another slot on the same machine, it must not open.
	wrong := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	if _, err := configmgr.UnwrapEnvKey(priv, wrong, envelope); err == nil {
		t.Fatal("the grant opened at an address it was not made for")
	}

	// The invariant the whole feature rests on.
	if s.sentAnywhere(key.Text()) {
		t.Fatal("key material reached hz")
	}
}

func TestApproveRefusesAFingerprintMismatch(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// hz serves a public key whose private half hz holds — the substitution
	// attack — and reports its fingerprint consistently, which it would.
	attacker, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	// The box's real key. The operator reads THIS fingerprint off its console.
	boxKey, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}

	s := newCMStub()
	s.publicKey = configmgr.MarshalMachinePublicKey(attacker.PublicKey())
	s.registration = apitypes.CMRegistrationResp{
		ID: "reg-1", Environment: "staging", App: "redline", Role: "app",
		State: configmgr.StatePending, Fingerprint: configmgr.FingerprintOf(attacker.PublicKey()).String(),
	}
	s.currentKeys["staging/redline/app"] = key.ID().String()
	c := s.start(t)

	withStdin(t, configmgr.FingerprintOf(boxKey.PublicKey()).String()+"\n")
	err = captureStdoutErr(t, func() error { return cmApprove(c, []string{"reg-1"}) })
	if err == nil {
		t.Fatal("a fingerprint mismatch must refuse")
	}
	if !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.approved) != 0 {
		t.Fatal("something was sent despite the mismatch")
	}
	if s.sentAnywhere(key.Text()) {
		t.Fatal("key material reached hz")
	}
}

func TestApproveRefusesAnEmptyOrUnparseableFingerprint(t *testing.T) {
	for _, typed := range []string{"\n", "yes\n", "\n\n"} {
		ks := testKeystore(t)
		addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
		key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		priv, _ := configmgr.NewMachineKey()

		s := newCMStub()
		s.publicKey = configmgr.MarshalMachinePublicKey(priv.PublicKey())
		s.registration = apitypes.CMRegistrationResp{
			ID: "reg-1", Environment: "staging", App: "redline", Role: "app",
			State: configmgr.StatePending, Fingerprint: configmgr.FingerprintOf(priv.PublicKey()).String(),
		}
		s.currentKeys["staging/redline/app"] = key.ID().String()
		c := s.start(t)

		withStdin(t, typed)
		if err := captureStdoutErr(t, func() error { return cmApprove(c, []string{"reg-1"}) }); err == nil {
			t.Fatalf("typing %q should not approve anything", typed)
		}
		if len(s.approved) != 0 {
			t.Fatal("something was sent")
		}
	}
}

// TestApproveRefusesWhenHzDescribesOneKeyAndServesAnother catches an hz that is
// internally inconsistent, before a human is asked anything.
func TestApproveRefusesWhenHzContradictsItself(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	served, _ := configmgr.NewMachineKey()
	other, _ := configmgr.NewMachineKey()

	s := newCMStub()
	s.publicKey = configmgr.MarshalMachinePublicKey(served.PublicKey())
	s.registration = apitypes.CMRegistrationResp{
		ID: "reg-1", Environment: "staging", App: "redline", Role: "app",
		State: configmgr.StatePending, Fingerprint: configmgr.FingerprintOf(other.PublicKey()).String(),
	}
	c := s.start(t)

	withStdin(t, configmgr.FingerprintOf(served.PublicKey()).String()+"\n")
	err := captureStdoutErr(t, func() error { return cmApprove(c, []string{"reg-1"}) })
	if err == nil || !strings.Contains(err.Error(), "describing one key and serving another") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestApproveRefusesANonPendingRegistration(t *testing.T) {
	testKeystore(t)
	s := newCMStub()
	s.registration = apitypes.CMRegistrationResp{
		ID: "reg-1", Environment: "staging", App: "redline", Role: "app", State: configmgr.StateDenied,
	}
	c := s.start(t)
	if err := cmApprove(c, []string{"reg-1"}); err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestDenyRequiresAReason(t *testing.T) {
	s := newCMStub()
	c := s.start(t)
	if err := cmDeny(c, []string{"reg-1"}); err == nil {
		t.Fatal("a denial with no reason should be refused")
	}
	captureStdout(t, func() {
		if err := cmDeny(c, []string{"--reason", "unrecognised machine", "reg-1"}); err != nil {
			t.Fatalf("deny: %v", err)
		}
	})
	if len(s.denied) != 1 || s.denied[0].Reason != "unrecognised machine" {
		t.Fatalf("deny not recorded: %+v", s.denied)
	}
}

// captureStdoutErr runs fn with stdout captured and discarded, returning its
// error. Used where the prompt output is noise and only the refusal matters.
func captureStdoutErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = fn() })
	return err
}

// --- URL shapes ------------------------------------------------------------

func TestCurrentKeyQueryCarriesTheWholeAddress(t *testing.T) {
	s := newCMStub()
	s.currentKeys["prod/redline/ops"] = "00112233445566aa"
	c := s.start(t)
	cur, err := cmCurrentKey(c, configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if cur.String() != "00112233445566aa" {
		t.Fatalf("got %s", cur)
	}
	// An address with nothing set reads as unavailable, not as an error: that
	// is the ordinary state of a brand-new address.
	cur, err = cmCurrentKey(c, configmgr.EnvKeyAddr{Environment: "dev", App: "redline", Role: "app"})
	if err != nil || cur.String() != "unavailable" {
		t.Fatalf("got %s, %v", cur, err)
	}
}

func TestKeyAddressEnumerationIgnoresStrayFiles(t *testing.T) {
	ks := testKeystore(t)
	putKey(t, ks, configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}, "2026-01", time.Now())
	putKey(t, ks, configmgr.EnvKeyAddr{Environment: "dev", App: "redline", Role: "ops"}, "2026-01", time.Now())
	// A file where a directory would be must not become an address.
	if err := os.WriteFile(filepath.Join(ks.Root(), "secrets", "keys", "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	addrs, err := cmKeyAddresses(ks)
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 2 || addrs[0].String() != "dev/redline/ops" || addrs[1].String() != "prod/redline/app" {
		t.Fatalf("got %v", addrs)
	}
}

// --- argument order --------------------------------------------------------

// TestFlagsAfterThePositionalAreHonoured: `hz cm deny reg-1 --reason=x` is the
// natural order, and Go's flag package stops at the first non-flag token, so
// without splitCMPositional the reason would be silently dropped.
func TestFlagsAfterThePositionalAreHonoured(t *testing.T) {
	s := newCMStub()
	c := s.start(t)
	captureStdout(t, func() {
		if err := cmDeny(c, []string{"reg-1", "--reason=unrecognised machine"}); err != nil {
			t.Fatalf("deny: %v", err)
		}
	})
	if len(s.denied) != 1 || s.denied[0].Reason != "unrecognised machine" {
		t.Fatalf("the reason after the positional was dropped: %+v", s.denied)
	}

	testKeystore(t)
	s2 := newCMStub()
	s2.resolve["prod/redline/app"] = apitypes.CMResolveResp{Winner: &apitypes.CMConfigResp{ID: "cfg-1", MinVer: "1.0.0"}}
	c2 := s2.start(t)
	out := captureStdout(t, func() {
		if err := cmResolve(c2, []string{"prod/redline/app", "--version=1.4.2"}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	})
	if !strings.Contains(out, "cfg-1") {
		t.Fatalf("resolve with the address first did not run:\n%s", out)
	}
}

// TestKeyNewDoesNotMistakeAFlagValueForTheAddress is why splitCMPositional is
// stricter than the repo's splitNameArgs: "2026-01" is a legal-looking token in
// second position and must stay the label, not become the address.
func TestKeyNewDoesNotMistakeAFlagValueForTheAddress(t *testing.T) {
	ks := testKeystore(t)
	s := newCMStub()
	c := s.start(t)
	captureStdout(t, func() {
		if err := cmKeyNew(c, []string{"--label", "2026-01", "staging/redline/app"}); err != nil {
			t.Fatalf("key new: %v", err)
		}
	})
	held, err := ks.List(keyAddr(configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}))
	if err != nil || len(held) != 1 || held[0].Label != "2026-01" {
		t.Fatalf("got %+v, %v", held, err)
	}
}
