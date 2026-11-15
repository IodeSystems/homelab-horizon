package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/wgkey"
)

func storeIn(t *testing.T) SegmentKeyStore {
	t.Helper()
	return SegmentKeyStore{Dir: filepath.Join(t.TempDir(), "keys")}
}

// A KEY PER SEGMENT, NOT PER MACHINE. This is the isolation property the whole
// segment model exists for: a box on wg-code and wg-redline holds two key
// pairs, so taking one tunnel does not hand over the other. One machine-level
// key would make the two segments one trust domain while still looking like
// two.
func TestEachSegmentGetsItsOwnKey(t *testing.T) {
	s := storeIn(t)
	code, minted, err := s.EnsureKey("seg:code")
	if err != nil || !minted {
		t.Fatalf("first key: minted=%v err=%v", minted, err)
	}
	redline, minted, err := s.EnsureKey("seg:redline")
	if err != nil || !minted {
		t.Fatalf("second key: minted=%v err=%v", minted, err)
	}
	if code == redline {
		t.Fatal("two segments were given one key — a compromise of either would hand over both")
	}
	if !wgkey.Valid(code) || !wgkey.Valid(redline) {
		t.Fatalf("what was minted is not a WireGuard key: %q %q", code, redline)
	}
	if s.Path("seg:code") == s.Path("seg:redline") {
		t.Fatal("two segments share one key file")
	}
	// Names that sanitise to the same readable string still get their own file.
	if s.Path("seg:code") == s.Path("seg/code") {
		t.Fatal("two segment names collided onto one key file")
	}
}

// A key is minted ONCE. Re-enrolment must report the key the box already has,
// or every re-run would be a rotation and every peer's config would be stale.
func TestAKeyIsMintedOnceAndThenReported(t *testing.T) {
	s := storeIn(t)
	first, minted, err := s.EnsureKey("seg:code")
	if err != nil || !minted {
		t.Fatalf("minted=%v err=%v", minted, err)
	}
	again, minted, err := s.EnsureKey("seg:code")
	if err != nil {
		t.Fatal(err)
	}
	if minted {
		t.Fatal("a second EnsureKey minted a new pair over a working one")
	}
	if again != first {
		t.Fatalf("the box reported %q and then %q for one segment", first, again)
	}
	if got := s.PublicKey("seg:code"); got != first {
		t.Fatalf("PublicKey says %q, EnsureKey said %q", got, first)
	}

	// Rotation is the deliberate replacement, and it really replaces.
	rotated, err := s.RotateKey("seg:code")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == first {
		t.Fatal("RotateKey kept the old pair")
	}
	if got := s.PublicKey("seg:code"); got != rotated {
		t.Fatalf("after a rotation the store reports %q, want %q", got, rotated)
	}
}

// THE PRIVATE HALF STAYS ON THE BOX, at 0600 in a 0700 directory — the same
// rule the agent credential lives under, and for the same reason: a key
// readable by every user on the machine is a tunnel every user on the machine
// can join.
func TestThePrivateKeyIsRootOnly(t *testing.T) {
	s := storeIn(t)
	// The directory already exists world-readable, which is the state the audit
	// VM was in. MkdirAll would have left it that way.
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pub, _, err := s.EnsureKey("seg:code")
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(s.Path("seg:code"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != SegmentKeyFileMode {
		t.Fatalf("the private key is mode %04o, want %04o", mode, SegmentKeyFileMode)
	}
	dir, err := os.Stat(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dir.Mode().Perm(); mode != SegmentKeyDirMode {
		t.Fatalf("the key directory is mode %04o, want %04o", mode, SegmentKeyDirMode)
	}

	// And what is on disk is the PRIVATE half: the public one is derived, so
	// the pair cannot drift apart. A store that wrote the public key would
	// report a key the box could not actually use.
	//
	// Both ROUTES to the public key are checked. EnsureKey returns what the
	// mint produced; PublicKey re-derives it from the file. They are different
	// code paths and a positive control proved it: breaking the read path alone
	// left this test green, because the first EnsureKey never takes it.
	if got := s.PublicKey("seg:code"); got != pub {
		t.Fatalf("reading the key back gives %q, minting gave %q", got, pub)
	}
	onDisk := readKeyFile(t, s.Path("seg:code"))
	if onDisk == pub {
		t.Fatal("the file holds the PUBLIC key — nothing on this box can bring the tunnel up with that")
	}
	derived, err := wgkey.Public(onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Fatalf("the stored private key derives %q and the store reported %q", derived, pub)
	}
}

// A key file that is not a key is replaced rather than reported. A truncated
// write or a half-copied directory leaves one, and the alternative is a box
// that can never enrol again without somebody deleting a file by hand.
func TestACorruptKeyFileIsReplaced(t *testing.T) {
	s := storeIn(t)
	if err := os.MkdirAll(s.Dir, SegmentKeyDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path("seg:code"), []byte("half a k"), SegmentKeyFileMode); err != nil {
		t.Fatal(err)
	}
	if got := s.PublicKey("seg:code"); got != "" {
		t.Fatalf("a corrupt file reported a key: %q", got)
	}
	pub, minted, err := s.EnsureKey("seg:code")
	if err != nil || !minted {
		t.Fatalf("minted=%v err=%v", minted, err)
	}
	if !wgkey.Valid(pub) {
		t.Fatalf("what replaced it is not a key: %q", pub)
	}
}

// Report is what enrolment puts on the wire: one entry per segment, sorted, no
// duplicates, every value a public key.
func TestReportCoversEverySegmentOnce(t *testing.T) {
	s := storeIn(t)
	keys, err := s.Report([]string{"seg:redline", "seg:code", "seg:code", "  ", ""}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("report = %+v, want one entry per distinct segment", keys)
	}
	if keys[0].Segment != "seg:code" || keys[1].Segment != "seg:redline" {
		t.Fatalf("report is not sorted by segment: %+v", keys)
	}
	for _, k := range keys {
		if !wgkey.Valid(k.PublicKey) {
			t.Fatalf("%s reported %q, which is not a key", k.Segment, k.PublicKey)
		}
	}

	// Reported again, it is the same two keys: reporting is not rotating.
	again, err := s.Report([]string{"seg:code", "seg:redline"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, again) {
		t.Fatalf("a second report changed the keys:\n%+v\n%+v", keys, again)
	}

	// With rotate, both change.
	rotated, err := s.Report([]string{"seg:code", "seg:redline"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rotated {
		if rotated[i].PublicKey == keys[i].PublicKey {
			t.Fatalf("a rotating report kept %s's key", rotated[i].Segment)
		}
	}
}

// THE ENROLMENT PAYLOAD CANNOT CARRY A PRIVATE KEY, and that is structural
// rather than a check on the bytes: a private key and a public key are the same
// 44 base64 characters, so nothing inspecting the value could refuse one. What
// can be guaranteed is that there is nowhere to put it — SegmentKey has one key
// field, named for the public half, and Report fills it from wgkey.Public.
func TestTheEnrolmentPayloadHasNowhereToPutAPrivateKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"SegmentKey", SegmentKey{}},
		{"EnrollRequest", EnrollRequest{}},
		{"SegmentKeyResult", SegmentKeyResult{}},
	} {
		typ := reflect.TypeOf(tc.v)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			tag := strings.ToLower(typ.Field(i).Tag.Get("json"))
			if strings.Contains(name, "priv") || strings.Contains(tag, "priv") {
				t.Errorf("%s has a field %q — a private key must never have a place on the wire",
					tc.name, typ.Field(i).Name)
			}
		}
	}
}

// And the value Report produces is never the private half, checked against the
// file rather than against the function that wrote it.
func TestReportSendsThePublicHalfAndNotThePrivateOne(t *testing.T) {
	s := storeIn(t)
	keys, err := s.Report([]string{"seg:code", "seg:redline"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		private := readKeyFile(t, s.Path(k.Segment))
		if k.PublicKey == private {
			t.Fatalf("%s: the report carries the PRIVATE key", k.Segment)
		}
		derived, err := wgkey.Public(private)
		if err != nil {
			t.Fatal(err)
		}
		if k.PublicKey != derived {
			t.Fatalf("%s: the report carries %q, which is not the public half of what is on disk", k.Segment, k.PublicKey)
		}
	}
}

func readKeyFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
