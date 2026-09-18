package configmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The keystore tests use a real directory tree under t.TempDir() with real
// modes. Nothing here mocks the filesystem: every property being tested —
// O_NOFOLLOW, fstat on the open descriptor, a parent directory's mode — is a
// property of the filesystem, and a fake one would assert only that the fake
// agrees with the code.

var ksAddr = Addr{Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}

func ksNew(t *testing.T) *Keystore {
	t.Helper()
	root := t.TempDir()
	// t.TempDir honours the umask, which on a lot of boxes leaves 0775 — and a
	// group-writable keystore root is exactly what checkDir refuses. Tighten it
	// so the tests exercise the checks rather than tripping over the harness.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	ks, err := NewKeystore(root)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	return ks
}

func ksPut(t *testing.T, ks *Keystore, addr Addr, label string, createdAt time.Time) EnvKey {
	t.Helper()
	k := NewEnvKey()
	if err := ks.Put(addr, label, k, createdAt); err != nil {
		t.Fatalf("Put(%s, %s): %v", addr, label, err)
	}
	return k
}

// ksRoleDir is the on-disk directory for an address. Tests build it directly
// because they have to plant hostile files the API refuses to write.
func ksRoleDir(ks *Keystore, addr Addr) string {
	return filepath.Join(ks.Root(), "secrets", "keys", addr.Environment, addr.App, addr.Role)
}

func ksWriteRaw(t *testing.T, ks *Keystore, addr Addr, name, content string) string {
	t.Helper()
	dir := ksRoleDir(ks, addr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func ksAt(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

// --- resolution by key id -------------------------------------------------

func TestKeyForResolvesByKeyID(t *testing.T) {
	ks := ksNew(t)
	old := ksPut(t, ks, ksAddr, "2026-01", ksAt("2026-01-01T00:00:00Z"))
	cur := ksPut(t, ks, ksAddr, "2026-09", ksAt("2026-09-01T00:00:00Z"))

	for _, want := range []EnvKey{old, cur} {
		got, err := ks.KeyFor(ksAddr, want.ID())
		if err != nil {
			t.Fatalf("KeyFor(%s): %v", want.ID(), err)
		}
		if got != want {
			t.Fatalf("KeyFor(%s) returned %s", want.ID(), got.ID())
		}
	}

	// A key that is simply not there is ErrNoSuchKey, not a security failure:
	// an address a client was never approved for has no file.
	absent := NewEnvKey()
	if _, err := ks.KeyFor(ksAddr, absent.ID()); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("absent key: want ErrNoSuchKey, got %v", err)
	}
	// So is an address with no directory at all.
	other := Addr{Environment: "staging", App: "redline", Role: "app"}
	if _, err := ks.KeyFor(other, cur.ID()); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("absent address: want ErrNoSuchKey, got %v", err)
	}
}

func TestKeyForRefusesTwoFilesClaimingOneKeyID(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "real", ksAt("2026-01-01T00:00:00Z"))
	// The same id under a second label: what a swapped key file looks like.
	if err := ks.Put(ksAddr, "also-real", k, ksAt("2026-02-01T00:00:00Z")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
}

// --- current-key selection ------------------------------------------------

// The whole point of created_at living inside the file: filename order and
// mtime both say the opposite here, and neither is allowed to win.
func TestCurrentKeyIsCreatedAtNotNameOrMtime(t *testing.T) {
	ks := ksNew(t)

	// "a" sorts first by name but is the NEWEST by created_at.
	newest := ksPut(t, ks, ksAddr, "a", ksAt("2027-05-01T00:00:00Z"))
	oldest := ksPut(t, ks, ksAddr, "z", ksAt("2026-01-01T00:00:00Z"))

	// And mtime says "z" is newest, because mtime lies after any copy.
	zPath := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("z", oldest.ID()))
	aPath := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("a", newest.ID()))
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(zPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(aPath, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, info, err := ks.SealingKey(ksAddr, CurrentKeyIs(newest.ID()))
	if err != nil {
		t.Fatalf("SealingKey: %v", err)
	}
	if got != newest {
		t.Fatalf("SealingKey chose %s, want %s (the greatest created_at)", got.ID(), newest.ID())
	}
	if info.Label != "a" || !info.CreatedAt.Equal(ksAt("2027-05-01T00:00:00Z")) {
		t.Fatalf("KeyInfo = %+v", info)
	}

	// The other direction: hz naming the name-sorted-last / mtime-newest file
	// is a disagreement, and a disagreement is a stop.
	if _, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(oldest.ID())); !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("want ErrRefuseToSeal, got %v", err)
	}
}

func TestListIsOldestFirstAndCarriesNoMaterial(t *testing.T) {
	ks := ksNew(t)
	mid := ksPut(t, ks, ksAddr, "mid", ksAt("2026-05-01T00:00:00Z"))
	old := ksPut(t, ks, ksAddr, "old", ksAt("2026-01-01T00:00:00Z"))
	new1 := ksPut(t, ks, ksAddr, "new", ksAt("2026-09-01T00:00:00Z"))

	got, err := ks.List(ksAddr)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []KeyID{old.ID(), mid.ID(), new1.ID()}
	if len(got) != len(want) {
		t.Fatalf("List returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("List[%d] = %s, want %s", i, got[i].ID, want[i])
		}
	}

	// A file at the address that does not verify fails the whole listing: the
	// seal path must not quietly skip a key file it could not read.
	ksWriteRaw(t, ks, ksAddr, "broken.0123456789abcdef.key", "{")
	if _, err := ks.List(ksAddr); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
	// Opening is unaffected: it loads only the file the envelope named.
	if _, err := ks.KeyFor(ksAddr, old.ID()); err != nil {
		t.Fatalf("KeyFor with a broken sibling: %v", err)
	}
	// A non-.key file is not a claim to be a key.
	ksWriteRaw(t, ks, ksAddr, "README", "notes")
	if _, err := ks.KeyFor(ksAddr, old.ID()); err != nil {
		t.Fatalf("KeyFor with a non-key file present: %v", err)
	}
}

// --- the pointer asymmetry ------------------------------------------------

func TestSealRefusesOnPointerDisagreement(t *testing.T) {
	ks := ksNew(t)
	old := ksPut(t, ks, ksAddr, "2026-01", ksAt("2026-01-01T00:00:00Z"))

	// The attack: a key file dropped into the tree with created_at next year.
	planted := ksPut(t, ks, ksAddr, "helpful", ksAt("2099-01-01T00:00:00Z"))

	// hz still points at the real key, so the planted one does not become the
	// sealing key — and the refusal does NOT fall back to hz's choice either.
	_, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(old.ID()))
	if !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("want ErrRefuseToSeal, got %v", err)
	}
	if !strings.Contains(err.Error(), planted.ID().String()) || !strings.Contains(err.Error(), old.ID().String()) {
		t.Fatalf("refusal should name both keys, got %v", err)
	}

	// A pointer naming a key that is not held at all is also a refusal.
	stranger := NewEnvKey()
	if _, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(stranger.ID())); !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("want ErrRefuseToSeal, got %v", err)
	}

	// Agreement seals.
	got, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(planted.ID()))
	if err != nil || got != planted {
		t.Fatalf("SealingKey on agreement: %v", err)
	}
}

// The zero CurrentKey is what a caller who never asked hz holds. Sealing with
// it must be impossible, which is the reason CurrentKey is a type at all.
func TestSealRefusesWhenThePointerWasNeverConsulted(t *testing.T) {
	ks := ksNew(t)
	ksPut(t, ks, ksAddr, "only", ksAt("2026-01-01T00:00:00Z"))

	_, _, err := ks.SealingKey(ksAddr, CurrentKey{})
	if !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("want ErrRefuseToSeal, got %v", err)
	}
	if !strings.Contains(err.Error(), "not consulted") {
		t.Fatalf("refusal should say the pointer was not consulted, got %v", err)
	}
}

func TestOpenOnlyWarnsOnPointerDisagreement(t *testing.T) {
	ks := ksNew(t)
	old := ksPut(t, ks, ksAddr, "2026-01", ksAt("2026-01-01T00:00:00Z"))
	cur := ksPut(t, ks, ksAddr, "2026-09", ksAt("2026-09-01T00:00:00Z"))

	// Opening the superseded key still works — that is the whole of a gradual
	// rotation, and refusing would break every boot not yet re-sealed.
	got, err := ks.KeyFor(ksAddr, old.ID())
	if err != nil {
		t.Fatalf("KeyFor on the superseded key: %v", err)
	}
	if got != old {
		t.Fatal("KeyFor returned the wrong key")
	}

	// The disagreement surfaces as an advisory, not as a resolve failure.
	warn := StaleKey(old.ID(), CurrentKeyIs(cur.ID()))
	if !errors.Is(warn, ErrStaleKey) {
		t.Fatalf("want ErrStaleKey, got %v", warn)
	}
	if !strings.Contains(warn.Error(), cur.ID().String()) {
		t.Fatalf("warning should name the current key, got %v", warn)
	}

	// Nothing to warn about in the other three cases.
	for name, current := range map[string]CurrentKey{
		"agreement":     CurrentKeyIs(old.ID()),
		"unavailable":   CurrentKeyUnavailable(),
		"not consulted": {},
	} {
		if err := StaleKey(old.ID(), current); err != nil {
			t.Fatalf("StaleKey(%s) = %v, want nil", name, err)
		}
	}
}

// An absent pointer must never brick anything: hz may be unreachable, or the
// address may be brand new. It also must not become a way to skip the check.
func TestAbsentPointer(t *testing.T) {
	t.Run("no keys held", func(t *testing.T) {
		ks := ksNew(t)
		if _, _, err := ks.SealingKey(ksAddr, CurrentKeyUnavailable()); !errors.Is(err, ErrNoSuchKey) {
			t.Fatalf("want ErrNoSuchKey, got %v", err)
		}
	})

	t.Run("exactly one key seals", func(t *testing.T) {
		// Nothing to arbitrate: this is the first push at a brand-new address.
		ks := ksNew(t)
		only := ksPut(t, ks, ksAddr, "first", ksAt("2026-01-01T00:00:00Z"))
		got, _, err := ks.SealingKey(ksAddr, CurrentKeyUnavailable())
		if err != nil || got != only {
			t.Fatalf("SealingKey: %v", err)
		}
	})

	t.Run("two keys refuse", func(t *testing.T) {
		// The drop-a-file case with the arbiter missing. created_at is whatever
		// the newest file says, so there is nothing to check it against.
		ks := ksNew(t)
		ksPut(t, ks, ksAddr, "real", ksAt("2026-01-01T00:00:00Z"))
		ksPut(t, ks, ksAddr, "helpful", ksAt("2099-01-01T00:00:00Z"))
		if _, _, err := ks.SealingKey(ksAddr, CurrentKeyUnavailable()); !errors.Is(err, ErrRefuseToSeal) {
			t.Fatalf("want ErrRefuseToSeal, got %v", err)
		}
	})

	t.Run("opening never needs the pointer", func(t *testing.T) {
		// The boot path: hz unreachable, cached ciphertext, still opens.
		ks := ksNew(t)
		a := ksPut(t, ks, ksAddr, "a", ksAt("2026-01-01T00:00:00Z"))
		ksPut(t, ks, ksAddr, "b", ksAt("2026-09-01T00:00:00Z"))
		if _, err := ks.KeyFor(ksAddr, a.ID()); err != nil {
			t.Fatalf("KeyFor with no pointer available: %v", err)
		}
	})
}

func TestSealRefusesOnACreatedAtTie(t *testing.T) {
	ks := ksNew(t)
	same := ksAt("2026-01-01T00:00:00Z")
	a := ksPut(t, ks, ksAddr, "a", same)
	ksPut(t, ks, ksAddr, "b", same)
	if _, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(a.ID())); !errors.Is(err, ErrRefuseToSeal) {
		t.Fatalf("want ErrRefuseToSeal, got %v", err)
	}
}

// --- path traversal -------------------------------------------------------

// Every segment is free text an operator chose. These are the values that walk
// out of the tree if any of them is ever interpolated into a path.
var hostileNames = []string{
	"../../..",
	"..",
	".",
	"2026-09/../..",
	"a/b",
	"/etc",
	"",
	"prod/../../../etc",
	"..\x00",
	"a\x00b",
	"a b",
	"PROD",
	"-leading-dash",
	"_leading-underscore",
	".hidden",
	strings.Repeat("a", maxNameLen+1),
}

func TestTraversalInAddressFieldsRefused(t *testing.T) {
	ks := ksNew(t)
	good := ksPut(t, ks, ksAddr, "ok", ksAt("2026-01-01T00:00:00Z"))
	before := ksTreeSnapshot(t, ks.Root())

	for _, hostile := range hostileNames {
		for _, field := range []string{"environment", "app", "role"} {
			addr := ksAddr
			switch field {
			case "environment":
				addr.Environment = hostile
			case "app":
				addr.App = hostile
			case "role":
				addr.Role = hostile
			}

			if _, err := ks.KeyFor(addr, good.ID()); !errors.Is(err, ErrBadName) {
				t.Errorf("KeyFor %s=%q: want ErrBadName, got %v", field, hostile, err)
			}
			if _, _, err := ks.SealingKey(addr, CurrentKeyIs(good.ID())); !errors.Is(err, ErrBadName) {
				t.Errorf("SealingKey %s=%q: want ErrBadName, got %v", field, hostile, err)
			}
			if _, err := ks.List(addr); !errors.Is(err, ErrBadName) {
				t.Errorf("List %s=%q: want ErrBadName, got %v", field, hostile, err)
			}
			if err := ks.Put(addr, "ok2", NewEnvKey(), ksAt("2026-01-01T00:00:00Z")); !errors.Is(err, ErrBadName) {
				t.Errorf("Put %s=%q: want ErrBadName, got %v", field, hostile, err)
			}
		}
	}

	if after := ksTreeSnapshot(t, ks.Root()); after != before {
		t.Fatalf("the tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestTraversalInTheLabelRefused(t *testing.T) {
	ks := ksNew(t)
	ksPut(t, ks, ksAddr, "ok", ksAt("2026-01-01T00:00:00Z"))
	before := ksTreeSnapshot(t, ks.Root())

	for _, hostile := range hostileNames {
		if err := ks.Put(ksAddr, hostile, NewEnvKey(), ksAt("2026-01-01T00:00:00Z")); !errors.Is(err, ErrBadName) {
			t.Errorf("Put label=%q: want ErrBadName, got %v", hostile, err)
		}
	}

	if after := ksTreeSnapshot(t, ks.Root()); after != before {
		t.Fatalf("the tree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A label planted directly on disk, bypassing Put, is refused on the way back
// in — the charset is enforced on read as well as on write.
func TestHostileLabelOnDiskRefusedOnRead(t *testing.T) {
	ks := ksNew(t)
	k := NewEnvKey()
	body, err := json.Marshal(keyFile{Key: k.Text(), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "../../../../etc/shadow"})
	if err != nil {
		t.Fatal(err)
	}
	ksWriteRaw(t, ks, ksAddr, keyFileName("innocent", k.ID()), string(body))

	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
}

func ksTreeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v\n", rel, info.Mode())
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}

// --- the key id, before it becomes a glob ---------------------------------

// The id arrives from the envelope, which arrives from hz. Pasted into a
// pattern unchecked, "*" matches an arbitrary key file.
func TestMalformedKeyIDRejectedBeforeGlobbing(t *testing.T) {
	ks := ksNew(t)
	planted := ksPut(t, ks, ksAddr, "victim", ksAt("2026-01-01T00:00:00Z"))

	for _, bad := range []string{
		"*",
		"*.key",
		"?",
		"[a-f]*",
		"..",
		"../../x",
		"victim",
		"",
		"0123456789abcde",   // 15 hex characters
		"0123456789abcdef0", // 17
		"0123456789abcdeg",  // not hex
		"0123456789abcdef ", // trailing byte that is not hex
	} {
		_, err := ks.KeyForID(ksAddr, bad)
		if !errors.Is(err, ErrMalformedKey) {
			t.Errorf("KeyForID(%q): want ErrMalformedKey, got %v", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), planted.ID().String()) {
			t.Errorf("KeyForID(%q) reached the planted key", bad)
		}
	}

	// ParseKeyID is lenient for a human pasting an id; a filename cannot be, or
	// two spellings denote one key.
	for _, noncanonical := range []string{
		strings.ToUpper(planted.ID().String()),
		" " + planted.ID().String(),
		planted.ID().String() + "\n",
	} {
		if _, err := ks.KeyForID(ksAddr, noncanonical); !errors.Is(err, ErrMalformedKey) {
			t.Errorf("KeyForID(%q): want ErrMalformedKey, got %v", noncanonical, err)
		}
	}

	// The rejection happens before the filesystem is touched at all: an address
	// with no directory still fails on the id, not on the missing directory.
	empty := Addr{Environment: "staging", App: "redline", Role: "app"}
	if _, err := ks.KeyForID(empty, "*"); !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("want ErrMalformedKey before any lookup, got %v", err)
	}

	// The canonical form still resolves.
	got, err := ks.KeyForID(ksAddr, planted.ID().String())
	if err != nil || got != planted {
		t.Fatalf("KeyForID on the real id: %v", err)
	}
}

func TestKeyFilenameMustCarryACanonicalKeyID(t *testing.T) {
	ks := ksNew(t)
	k := NewEnvKey()
	body, err := json.Marshal(keyFile{Key: k.Text(), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "label"})
	if err != nil {
		t.Fatal(err)
	}
	upper := "label." + strings.ToUpper(k.ID().String()) + ".key"
	ksWriteRaw(t, ks, ksAddr, upper, string(body))

	// Uppercase hex decodes to the same bytes, so tolerating it would mean two
	// filenames denote one key.
	if _, err := ks.List(ksAddr); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
}

// --- the filename is a claim; the material is the fact --------------------

func TestEmbeddedKeyIDMustMatchTheFilename(t *testing.T) {
	ks := ksNew(t)
	real1 := NewEnvKey()
	impostor := NewEnvKey()

	body, err := json.Marshal(keyFile{Key: real1.Text(), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	// The file holds real1 but the name claims impostor: a renamed file, or a
	// swap that kept a familiar-looking name.
	ksWriteRaw(t, ks, ksAddr, keyFileName("renamed", impostor.ID()), string(body))

	_, err = ks.KeyFor(ksAddr, impostor.ID())
	if !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
	if !strings.Contains(err.Error(), real1.ID().String()) {
		t.Fatalf("error should name the key actually held, got %v", err)
	}
	// And it is not reachable under its real id either, because the filename is
	// what the lookup matches on.
	if _, err := ks.KeyFor(ksAddr, real1.ID()); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("want ErrNoSuchKey, got %v", err)
	}
}

// --- the serialization ----------------------------------------------------

func TestKeyFileSerializationIsStrict(t *testing.T) {
	k := NewEnvKey()
	good := fmt.Sprintf(`{"key":%q,"created_at":"2026-01-01T00:00:00Z","label":"jan"}`, k.Text())

	for name, body := range map[string]string{
		"empty":                "",
		"truncated object":     good[:len(good)-1],
		"truncated mid-string": good[:len(good)/2],
		"not json":             "hzenv_" + strings.Repeat("A", 58),
		"unknown field":        `{"key":"x","created_at":"2026-01-01T00:00:00Z","label":"jan","algorithm":"rot13"}`,
		"two objects":          good + good,
		"trailing garbage":     good + "garbage",
		"no key":               `{"created_at":"2026-01-01T00:00:00Z","label":"jan"}`,
		"no created_at":        fmt.Sprintf(`{"key":%q,"label":"jan"}`, k.Text()),
		"no label":             fmt.Sprintf(`{"key":%q,"created_at":"2026-01-01T00:00:00Z"}`, k.Text()),
		"array":                `[]`,
		"null":                 `null`,
	} {
		if _, err := readKeyFile(strings.NewReader(body), "test"); !errors.Is(err, ErrMalformedKeyFile) {
			t.Errorf("%s: want ErrMalformedKeyFile, got %v", name, err)
		}
	}

	if _, err := readKeyFile(strings.NewReader(good), "test"); err != nil {
		t.Fatalf("well-formed key file: %v", err)
	}
	// Oversized: a client must not read an arbitrary file into memory because
	// somebody put it where a key belongs.
	if _, err := readKeyFile(strings.NewReader(strings.Repeat(" ", maxKeyFileSize+1)+good), "test"); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatal("an oversized key file should be refused")
	}
}

// A corrupted key fails with a diagnosis rather than yielding 32 wrong bytes,
// which is what the Crockford checksum inside EnvKey.Text buys here.
func TestCorruptedKeyMaterialIsCaughtByTheChecksum(t *testing.T) {
	ks := ksNew(t)
	k := NewEnvKey()
	text := []byte(k.Text())
	if text[len(text)-2] == 'A' {
		text[len(text)-2] = 'B'
	} else {
		text[len(text)-2] = 'A'
	}
	body, err := json.Marshal(keyFile{Key: string(text), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "corrupt"})
	if err != nil {
		t.Fatal(err)
	}
	ksWriteRaw(t, ks, ksAddr, keyFileName("corrupt", k.ID()), string(body))

	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrMalformedKeyFile) {
		t.Fatalf("want ErrMalformedKeyFile, got %v", err)
	}
}

func TestPutRoundTripsAndRefusesToOverwrite(t *testing.T) {
	ks := ksNew(t)
	created := ksAt("2026-09-01T12:34:56Z")
	k := ksPut(t, ks, ksAddr, "2026-09", created)

	info, err := ks.List(ksAddr)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(info) != 1 || info[0].ID != k.ID() || info[0].Label != "2026-09" || !info[0].CreatedAt.Equal(created) {
		t.Fatalf("List = %+v", info)
	}

	// The file is 0600 and the tree above it is 0700.
	path := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("2026-09", k.ID()))
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode is %04o, want 0600", st.Mode().Perm())
	}

	// Write-once: silently replacing material under a familiar name is exactly
	// the swap the id check exists to catch, so it is not allowed at all.
	if err := ks.Put(ksAddr, "2026-09", k, created); err == nil {
		t.Fatal("Put overwrote an existing key file")
	}
	// A zero created_at has nothing for current-key resolution to turn on.
	if err := ks.Put(ksAddr, "zero", NewEnvKey(), time.Time{}); err == nil {
		t.Fatal("Put accepted a zero created_at")
	}
}

// --- permissions, ownership, symlinks -------------------------------------

func TestGroupOrWorldAccessibleKeyRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666, 0o700 | 0o060, 0o601} {
		ks := ksNew(t)
		k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))
		path := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("key", k.ID()))
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrInsecureKey) {
			t.Errorf("mode %04o: want ErrInsecureKey, got %v", mode, err)
		}
		if _, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(k.ID())); !errors.Is(err, ErrInsecureKey) {
			t.Errorf("mode %04o on the seal path: want ErrInsecureKey, got %v", mode, err)
		}
	}
}

// A 0600 key in a 0777 directory is not protected: anyone can unlink it and put
// their own there, whatever mode the file itself carries.
func TestGroupOrWorldWritableParentDirectoryRefused(t *testing.T) {
	// Every directory from the root down, because any one of them is enough.
	dirs := []func(ks *Keystore) string{
		func(ks *Keystore) string { return ks.Root() },
		func(ks *Keystore) string { return filepath.Join(ks.Root(), "secrets") },
		func(ks *Keystore) string { return filepath.Join(ks.Root(), "secrets", "keys") },
		func(ks *Keystore) string { return filepath.Join(ks.Root(), "secrets", "keys", "prod") },
		func(ks *Keystore) string { return filepath.Join(ks.Root(), "secrets", "keys", "prod", "redline") },
		func(ks *Keystore) string { return ksRoleDir(ks, ksAddr) },
	}
	for i, pick := range dirs {
		for _, mode := range []os.FileMode{0o777, 0o722, 0o702, 0o770} {
			ks := ksNew(t)
			k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))
			dir := pick(ks)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			_, err := ks.KeyFor(ksAddr, k.ID())
			if !errors.Is(err, ErrInsecureKey) {
				t.Errorf("dir %d mode %04o: want ErrInsecureKey, got %v", i, mode, err)
			}
			// Leave it traversable so t.TempDir can clean up.
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatalf("chmod back: %v", err)
			}
		}
	}
}

// Group- and world-READ on a directory is tolerated on purpose: it discloses
// labels and key ids, which are public names, and requiring 0700 all the way up
// would refuse an ordinary 0755 home directory.
func TestWorldReadableDirectoryIsTolerated(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))
	if err := os.Chmod(ksRoleDir(ks, ksAddr), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := ks.KeyFor(ksAddr, k.ID()); err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
}

func TestSymlinkedKeyFileRefused(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "real", ksAt("2026-01-01T00:00:00Z"))
	real1 := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("real", k.ID()))

	// A second name for the same key, this time a symlink. Everything the
	// target says about itself is fine; the link is the problem.
	other := NewEnvKey()
	body, err := json.Marshal(keyFile{Key: other.Text(), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "link"})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(outside, body, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("link", other.ID()))
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}

	if _, err := ks.KeyFor(ksAddr, other.ID()); !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want ErrInsecureKey, got %v", err)
	}
	// A symlink pointing back at a legitimate key in the same tree is refused
	// just the same: the check is "is this a symlink", not "where does it go".
	selfLink := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("alias", k.ID()))
	_ = os.Remove(selfLink)
	if err := os.Symlink(real1, selfLink); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrMalformedKeyFile) && !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestSymlinkedDirectoryComponentRefused(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "real", ksAt("2026-01-01T00:00:00Z"))

	// Move the role directory aside and leave a symlink in its place.
	role := ksRoleDir(ks, ksAddr)
	moved := role + "-moved"
	if err := os.Rename(role, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, role); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}

	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want ErrInsecureKey, got %v", err)
	}
}

// Ownership is checked, not only mode: 0600 owned by somebody else is still
// somebody else's file.
//
// The real form of this test — chown a 0600 key file to another uid and watch
// it be refused — needs CAP_CHOWN, so it runs only as root and is skipped
// otherwise. What runs everywhere is the same check driven from the other side:
// the keystore's recorded uid is changed instead of the file's owner, which
// exercises the identical branch on a real file with a real fstat.
func TestKeyOwnedBySomebodyElseRefused(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))

	ks.uid = ks.uid + 1
	_, err := ks.KeyFor(ksAddr, k.ID())
	if !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want ErrInsecureKey, got %v", err)
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("error should name the owner, got %v", err)
	}
}

func TestKeyChownedToAnotherUserRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("UNTESTED as a non-root user: chown(2) to another uid needs CAP_CHOWN, " +
			"so the real 'a 0600 file owned by somebody else' case cannot be built here. " +
			"TestKeyOwnedBySomebodyElseRefused exercises the same fstat branch by changing " +
			"the keystore's recorded uid instead of the file's owner.")
	}
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))
	path := filepath.Join(ksRoleDir(ks, ksAddr), keyFileName("key", k.ID()))
	const nobody = 65534
	if err := os.Chown(path, nobody, nobody); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if _, err := ks.KeyFor(ksAddr, k.ID()); !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want ErrInsecureKey, got %v", err)
	}
}

func TestDirectoryOwnedBySomebodyElseRefused(t *testing.T) {
	ks := ksNew(t)
	k := ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))
	ks.uid = ks.uid + 1
	// The root is the first descriptor checked, so this fails before the file
	// is ever opened.
	_, err := ks.KeyFor(ksAddr, k.ID())
	if !errors.Is(err, ErrInsecureKey) || !strings.Contains(err.Error(), ks.Root()) {
		t.Fatalf("want the root refused by ownership, got %v", err)
	}
}

// A fifo where a key belongs must not block the client forever on open, and
// must not be read as a key.
func TestNonRegularFileRefused(t *testing.T) {
	ks := ksNew(t)
	k := NewEnvKey()
	dir := ksRoleDir(ks, ksAddr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, keyFileName("fifo", k.ID()))
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("UNTESTED: this filesystem or platform cannot create a fifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ks.KeyFor(ksAddr, k.ID())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInsecureKey) {
			t.Fatalf("want ErrInsecureKey, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("KeyFor blocked on a fifo")
	}
}

// --- the root -------------------------------------------------------------

func TestRootMustBeAbsolute(t *testing.T) {
	// A cwd-relative root means a repository carrying a hostile .hz/ gets
	// consulted by anyone who runs the CLI inside it.
	for _, root := range []string{".hz", "./.hz", "../.hz", "", "secrets/keys"} {
		if _, err := NewKeystore(root); err == nil {
			t.Errorf("NewKeystore(%q) was accepted", root)
		}
	}
	if _, err := NewKeystore("/home/someone/.hz"); err != nil {
		t.Fatalf("NewKeystore on an absolute path: %v", err)
	}
}

func TestDefaultKeystoreHonoursTheEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(KeystoreRootEnv, dir)
	ks, err := DefaultKeystore()
	if err != nil {
		t.Fatalf("DefaultKeystore: %v", err)
	}
	if ks.Root() != dir {
		t.Fatalf("root = %q, want %q", ks.Root(), dir)
	}

	// And refuses a relative override rather than resolving it against cwd.
	t.Setenv(KeystoreRootEnv, ".hz")
	if _, err := DefaultKeystore(); err == nil {
		t.Fatal("DefaultKeystore accepted a relative override")
	}

	t.Setenv(KeystoreRootEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UNTESTED: no home directory in this environment: %v", err)
	}
	ks, err = DefaultKeystore()
	if err != nil {
		t.Fatalf("DefaultKeystore: %v", err)
	}
	if ks.Root() != filepath.Join(home, ".hz") {
		t.Fatalf("default root = %q, want %q", ks.Root(), filepath.Join(home, ".hz"))
	}
}

// --- the keystore against the crypto it serves ----------------------------

// The end-to-end shape: seal with the key the keystore chose, then open with
// the key the envelope names.
func TestSealAndOpenThroughTheKeystore(t *testing.T) {
	ks := ksNew(t)
	ksPut(t, ks, ksAddr, "2026-01", ksAt("2026-01-01T00:00:00Z"))
	cur := ksPut(t, ks, ksAddr, "2026-09", ksAt("2026-09-01T00:00:00Z"))

	sealing, _, err := ks.SealingKey(ksAddr, CurrentKeyIs(cur.ID()))
	if err != nil {
		t.Fatalf("SealingKey: %v", err)
	}
	envelope := Seal(sealing, ksAddr, []byte("hunter2"))

	h, err := ParseEnvelopeHeader(envelope)
	if err != nil {
		t.Fatalf("ParseEnvelopeHeader: %v", err)
	}
	opening, err := ks.KeyFor(ksAddr, h.KeyID)
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	pt, err := Open(opening, ksAddr, envelope)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(pt) != "hunter2" {
		t.Fatalf("plaintext = %q", pt)
	}
}

// --- filenames and odd inodes ---------------------------------------------

func TestMalformedKeyFilenamesRefused(t *testing.T) {
	k := NewEnvKey()
	body, err := json.Marshal(keyFile{Key: k.Text(), CreatedAt: ksAt("2026-01-01T00:00:00Z"), Label: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"nodots.key",
		"label..key",
		"label.nothex.key",
		".0123456789abcdef.key",
		"bad label.0123456789abcdef.key",
	} {
		ks := ksNew(t)
		ksWriteRaw(t, ks, ksAddr, name, string(body))
		if _, err := ks.List(ksAddr); !errors.Is(err, ErrMalformedKeyFile) {
			t.Errorf("%q: want ErrMalformedKeyFile, got %v", name, err)
		}
	}
}

// A plain file where a directory belongs is a refusal, not a crash.
func TestNonDirectoryComponentRefused(t *testing.T) {
	ks := ksNew(t)
	ksPut(t, ks, ksAddr, "key", ksAt("2026-01-01T00:00:00Z"))

	role := ksRoleDir(ks, ksAddr)
	if err := os.RemoveAll(role); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(role, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.List(ksAddr); !errors.Is(err, ErrInsecureKey) {
		t.Fatalf("want ErrInsecureKey, got %v", err)
	}
}

func TestStringsDoNotLeakMaterial(t *testing.T) {
	k := NewEnvKey()
	info := KeyInfo{ID: k.ID(), Label: "2026-09", CreatedAt: ksAt("2026-09-01T00:00:00Z")}
	s := info.String()
	if !strings.Contains(s, k.ID().String()) || !strings.Contains(s, "2026-09-01T00:00:00Z") {
		t.Fatalf("KeyInfo.String = %q", s)
	}
	if strings.Contains(s, k.Text()) {
		t.Fatal("KeyInfo.String leaked the key")
	}

	if got := CurrentKeyIs(k.ID()).String(); got != k.ID().String() {
		t.Fatalf("CurrentKeyIs.String = %q", got)
	}
	if got := CurrentKeyUnavailable().String(); got != "unavailable" {
		t.Fatalf("CurrentKeyUnavailable.String = %q", got)
	}
	if got := (CurrentKey{}).String(); got != "not consulted" {
		t.Fatalf("zero CurrentKey.String = %q", got)
	}
}
