package configmgr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newState(t *testing.T) *State {
	t.Helper()
	s, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatalf("OpenState: %v", err)
	}
	return s
}

var stateAddr = EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}

func TestOpenStateRefusesARelativeRoot(t *testing.T) {
	// A cwd-relative state directory means a repository that ships a hostile
	// one gets consulted by whoever runs a binary inside it.
	if _, err := OpenState("state"); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}
	if _, err := OpenState(""); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}
}

func TestOpenStateCreates0700(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hz")
	if _, err := OpenState(root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("root is %#o, want 0700", fi.Mode().Perm())
	}
}

func TestMachineKeyIsGeneratedOnceAndKeptPrivate(t *testing.T) {
	s := newState(t)
	if _, err := s.MachineKey(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MachineKey on an empty tree = %v", err)
	}
	a, err := s.EnsureMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatal("EnsureMachineKey minted a second keypair")
	}

	path := filepath.Join(s.Root(), machineKeyName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s is %#o, want 0600", machineKeyName, fi.Mode().Perm())
	}
	// The file names itself, so a leaked private key is greppable.
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(blob), MachinePrivateKeyPrefix) {
		t.Errorf("machine.key does not carry the private-key prefix")
	}
}

func TestReadRefusesAFileOthersCanRead(t *testing.T) {
	s := newState(t)
	if _, err := s.EnsureMachineKey(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), machineKeyName)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MachineKey(); !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("err = %v, want ErrInsecureKey", err)
	}
}

func TestReadRefusesASymlink(t *testing.T) {
	s := newState(t)
	real := filepath.Join(t.TempDir(), "elsewhere.key")
	if err := os.WriteFile(real, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(s.Root(), machineKeyName)); err != nil {
		t.Fatal(err)
	}
	// O_NOFOLLOW: a symlinked component is an error, not a redirect to a file
	// that passes every other check.
	_, err := s.MachineKey()
	if err == nil {
		t.Fatal("a symlinked machine.key was followed")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the symlink refused", err)
	}
}

func TestMachineIDIsWrittenOnceAndNeverMoved(t *testing.T) {
	s := newState(t)
	if _, err := s.MachineID(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MachineID on an empty tree = %v", err)
	}
	if err := s.PutMachineID("m-0001"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MachineID(); err != nil || got != "m-0001" {
		t.Fatalf("MachineID = %q, %v", got, err)
	}
	// Idempotent.
	if err := s.PutMachineID("m-0001"); err != nil {
		t.Fatal(err)
	}
	// The id is half the authenticated address of every machine-scoped secret.
	// Letting a later answer move it would make the binding meaningless.
	if err := s.PutMachineID("m-9999"); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}
	if got, _ := s.MachineID(); got != "m-0001" {
		t.Errorf("MachineID moved to %q", got)
	}
}

func TestMachineIDValidation(t *testing.T) {
	s := newState(t)
	for _, id := range []string{"", strings.Repeat("x", maxMachineIDLen+1), "m-1\nm-2"} {
		if err := s.PutMachineID(id); !errors.Is(err, ErrBadState) {
			t.Errorf("PutMachineID(%q) = %v, want ErrBadState", id, err)
		}
	}
}

func TestEnvKeyIsPerAddressAndWriteOnce(t *testing.T) {
	s := newState(t)
	if _, err := s.EnvKey(stateAddr); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("EnvKey on an empty tree = %v", err)
	}
	k := NewEnvKey()
	if err := s.PutEnvKey(stateAddr, k); err != nil {
		t.Fatal(err)
	}
	got, err := s.EnvKey(stateAddr)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != k.ID() {
		t.Fatalf("EnvKey = %s, want %s", got.ID(), k.ID())
	}
	// Re-approval with the same key is a no-op; with a different one, refused.
	if err := s.PutEnvKey(stateAddr, k); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvKey(stateAddr, NewEnvKey()); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}
	if again, _ := s.EnvKey(stateAddr); again.ID() != k.ID() {
		t.Error("the key was replaced anyway")
	}

	// Another address on the same box is a different directory and a
	// different key.
	ops := EnvKeyAddr{Environment: "prod", App: "redline", Role: "ops"}
	if _, err := s.EnvKey(ops); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("EnvKey at %s = %v", ops, err)
	}

	fi, err := os.Stat(filepath.Join(s.Root(), "prod", "redline", "app", envKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config.key is %#o, want 0600", fi.Mode().Perm())
	}
}

func TestEnvKeyFileCarriesItsOwnChecksum(t *testing.T) {
	s := newState(t)
	if err := s.PutEnvKey(stateAddr, NewEnvKey()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "prod", "redline", "app", envKeyName)
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one character of the encoding. The stored form is EnvKey.Text(),
	// which carries a checksum, so this is a diagnosis rather than 32 wrong
	// bytes that look like a wrong-key problem.
	corrupt := []byte(strings.TrimSpace(string(blob)))
	if corrupt[len(corrupt)-1] == 'A' {
		corrupt[len(corrupt)-1] = 'B'
	} else {
		corrupt[len(corrupt)-1] = 'A'
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnvKey(stateAddr); !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("err = %v, want ErrMalformedKey", err)
	}
}

func TestAddressSegmentsAreValidatedNeverInterpolated(t *testing.T) {
	s := newState(t)
	bad := []EnvKeyAddr{
		{Environment: "..", App: "redline", Role: "app"},
		{Environment: "prod/../../etc", App: "redline", Role: "app"},
		{Environment: "prod", App: "", Role: "app"},
		{Environment: "prod", App: "redline", Role: ""},
		{Environment: "Prod", App: "redline", Role: "app"},
		{Environment: "prod", App: "red.line", Role: "app"},
		{Environment: strings.Repeat("a", maxNameLen+1), App: "redline", Role: "app"},
	}
	for _, addr := range bad {
		if _, err := s.addrDir(addr); !errors.Is(err, ErrBadName) {
			t.Errorf("addrDir(%+v) = %v, want ErrBadName", addr, err)
		}
		if err := s.PutEnvKey(addr, NewEnvKey()); !errors.Is(err, ErrBadName) {
			t.Errorf("PutEnvKey(%+v) = %v, want ErrBadName", addr, err)
		}
	}
	// Nothing was created outside the tree.
	entries, err := os.ReadDir(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused address left %d entries behind", len(entries))
	}
}

func TestCacheRoundTripAndFloors(t *testing.T) {
	s := newState(t)
	if _, err := s.cache(stateAddr); !errors.Is(err, ErrNoCache) {
		t.Fatalf("cache on an empty tree = %v", err)
	}
	if got := s.Floors(stateAddr); len(got) != 0 {
		t.Errorf("Floors on an empty tree = %v", got)
	}

	resp := ConfigResponse{ConfigID: "cfg", Sequence: 42, MinVer: "1.0.0", Entries: []ConfigEntry{{Key: "K", Binding: BindingEnv, Sealed: "AAA"}}}
	if err := s.PutCache(stateAddr, "1.2.3", resp, nil); err != nil {
		t.Fatal(err)
	}
	cf, err := s.cache(stateAddr)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Version != "1.2.3" || cf.Response.Sequence != 42 || cf.Floors["1.2.3"] != 42 {
		t.Fatalf("cache = %+v", cf)
	}
	if len(cf.Response.Entries) != 1 || cf.Response.Entries[0].Sealed != "AAA" {
		t.Errorf("entries did not round-trip: %+v", cf.Response.Entries)
	}

	// The floor is per version, and it only ever rises. A lower sequence
	// written at the same version does not lower it.
	resp.Sequence = 30
	if err := s.PutCache(stateAddr, "1.2.3", resp, s.Floors(stateAddr)); err != nil {
		t.Fatal(err)
	}
	if got := s.Floors(stateAddr)["1.2.3"]; got != 42 {
		t.Errorf("floor[1.2.3] = %d, want 42 — floors only rise", got)
	}
	// A different version carries its own floor, and the first survives.
	if err := s.PutCache(stateAddr, "1.2.2", resp, s.Floors(stateAddr)); err != nil {
		t.Fatal(err)
	}
	floors := s.Floors(stateAddr)
	if floors["1.2.2"] != 30 || floors["1.2.3"] != 42 {
		t.Errorf("floors = %v", floors)
	}
}

func TestCacheHoldsNoPlaintextAndNoClock(t *testing.T) {
	s := newState(t)
	resp := ConfigResponse{ConfigID: "cfg", Sequence: 1, MinVer: "1.0.0"}
	if err := s.PutCache(stateAddr, "1.2.3", resp, nil); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(s.Root(), "prod", "redline", "app", cacheName))
	if err != nil {
		t.Fatal(err)
	}
	// Rule 1: nothing in the boot path may depend on freshness, so there is
	// nothing time-shaped in the file to depend on.
	for _, forbidden := range []string{"expires", "ttl", "fetchedAt", "notAfter", "validUntil"} {
		if strings.Contains(string(blob), forbidden) {
			t.Errorf("last-known-good carries %q; the boot path must hold no clock", forbidden)
		}
	}
}

func TestCacheDecodeIsStrict(t *testing.T) {
	s := newState(t)
	if err := s.PutCache(stateAddr, "1.2.3", ConfigResponse{ConfigID: "cfg"}, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "prod", "redline", "app", cacheName)

	// A field this build does not know is an error, not a field quietly
	// defaulted — which is the bug class this project exists to kill.
	if err := os.WriteFile(path, []byte(`{"version":"1.2.3","response":{},"floors":{},"fromTheFuture":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cache(stateAddr); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}

	// Trailing bytes too: a clipped copy must not read as a shorter config.
	if err := os.WriteFile(path, []byte(`{"version":"1.2.3","response":{},"floors":{}}{"and":"more"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cache(stateAddr); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}

	// Floors survive as an empty map rather than nil, so a corrupt cache costs
	// the floor and nothing else.
	if got := s.Floors(stateAddr); got == nil || len(got) != 0 {
		t.Errorf("Floors on a corrupt cache = %v, want an empty map", got)
	}
}

func TestPutCacheIsAtomic(t *testing.T) {
	s := newState(t)
	if err := s.PutCache(stateAddr, "1.2.3", ConfigResponse{ConfigID: "first", Sequence: 1}, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Root(), "prod", "redline", "app")
	if err := s.PutCache(stateAddr, "1.2.3", ConfigResponse{ConfigID: "second", Sequence: 2}, s.Floors(stateAddr)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
	cf, err := s.cache(stateAddr)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Response.ConfigID != "second" {
		t.Errorf("ConfigID = %s", cf.Response.ConfigID)
	}
	fi, err := os.Stat(filepath.Join(dir, cacheName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("last-known-good is %#o, want 0600", fi.Mode().Perm())
	}
}

func TestAddressesListsWhatTheBoxHolds(t *testing.T) {
	s := newState(t)
	if got := s.Addresses(); len(got) != 0 {
		t.Fatalf("Addresses on an empty tree = %v", got)
	}
	want := []EnvKeyAddr{
		{Environment: "prod", App: "redline", Role: "app"},
		{Environment: "prod", App: "redline", Role: "ops"},
		{Environment: "staging", App: "redline", Role: "app"},
	}
	for _, a := range want {
		if err := s.PutEnvKey(a, NewEnvKey()); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with a cache but no key is not an address this box holds.
	if err := s.PutCache(EnvKeyAddr{Environment: "dev", App: "redline", Role: "app"}, "1.2.3", ConfigResponse{}, nil); err != nil {
		t.Fatal(err)
	}
	got := s.Addresses()
	if len(got) != len(want) {
		t.Fatalf("Addresses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Addresses[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCacheSizeIsCapped(t *testing.T) {
	s := newState(t)
	if err := s.PutCache(stateAddr, "1.2.3", ConfigResponse{ConfigID: "cfg"}, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "prod", "redline", "app", cacheName)
	if err := os.WriteFile(path, make([]byte, maxCacheFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cache(stateAddr); !errors.Is(err, ErrBadState) {
		t.Fatalf("err = %v, want ErrBadState", err)
	}
}
