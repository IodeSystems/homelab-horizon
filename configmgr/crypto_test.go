package configmgr

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The addresses every test seals against unless it is testing addressing.
var (
	testAddr = Addr{Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}
	testMach = MachineAddr{Machine: "mch_01k9v2w3x4y5z6a7b8c9d0e1f2", Key: "NPM_TOKEN"}
)

func mustMachineKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	priv, err := NewMachineKey()
	if err != nil {
		t.Fatalf("NewMachineKey: %v", err)
	}
	return priv
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := NewEnvKey()

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("s3cr3t")},
		{"password with newline", []byte("hunter2\n")},
		{"binary", []byte{0x00, 0xff, 0x00, 0x80, 0x7f}},
		{"utf8", []byte("pässwörd — ünïcode")},
		{"long", bytes.Repeat([]byte("A"), 64*1024)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Seal(k, testAddr, tc.plaintext)
			if want := envMinLen + len(tc.plaintext); len(env) != want {
				t.Fatalf("envelope is %d bytes, want %d", len(env), want)
			}
			got, err := Open(k, testAddr, env)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("round trip changed the plaintext")
			}
			// The plaintext must not be sitting in the envelope in the clear.
			if len(tc.plaintext) > 4 && bytes.Contains(env, tc.plaintext) {
				t.Fatalf("plaintext appears verbatim in the envelope")
			}
		})
	}
}

func TestSealHeaderIsSelfDescribing(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("value"))

	h, err := ParseEnvelopeHeader(env)
	if err != nil {
		t.Fatalf("ParseEnvelopeHeader: %v", err)
	}
	if h.Version != EnvelopeVersion {
		t.Errorf("version = %d, want %d", h.Version, EnvelopeVersion)
	}
	if h.Kind != KindEnvSealed {
		t.Errorf("kind = %s, want %s", h.Kind, KindEnvSealed)
	}
	if h.KeyID != k.ID() {
		t.Errorf("key id = %s, want %s", h.KeyID, k.ID())
	}
}

func TestOpenWithWrongKey(t *testing.T) {
	right, wrong := NewEnvKey(), NewEnvKey()
	env := Seal(right, testAddr, []byte("prod gateway key"))

	pt, err := Open(wrong, testAddr, env)
	if err == nil {
		t.Fatal("Open with the wrong key succeeded")
	}
	if pt != nil {
		t.Fatalf("Open returned %d bytes of plaintext alongside an error", len(pt))
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
}

// A wrong key that CLAIMS to be the right one must fail at the tag, not decrypt
// to garbage. This is the path that matters: the key id is only a hint, the
// AEAD is what decides.
func TestOpenWithWrongKeyClaimingTheRightID(t *testing.T) {
	right, wrong := NewEnvKey(), NewEnvKey()
	plaintext := []byte("prod gateway key")
	env := Seal(right, testAddr, plaintext)

	// Restamp the header so the key-id check passes and the AEAD has to catch it.
	id := wrong.ID()
	copy(env[2:2+KeyIDSize], id[:])

	pt, err := Open(wrong, testAddr, env)
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err = %v, want ErrAuthentication", err)
	}
	if pt != nil {
		t.Fatalf("got %q back, want no plaintext at all", pt)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	k := NewEnvKey()
	plaintext := []byte("a value long enough to have a middle")

	cases := []struct {
		name    string
		corrupt func(env []byte)
		want    error
	}{
		{
			name:    "ciphertext bit flipped",
			corrupt: func(env []byte) { env[envMinLen-TagSize+2] ^= 0x01 },
			want:    ErrAuthentication,
		},
		{
			name:    "last ciphertext byte flipped",
			corrupt: func(env []byte) { env[len(env)-TagSize-1] ^= 0x80 },
			want:    ErrAuthentication,
		},
		{
			name:    "tag bit flipped",
			corrupt: func(env []byte) { env[len(env)-1] ^= 0x01 },
			want:    ErrAuthentication,
		},
		{
			name:    "nonce bit flipped",
			corrupt: func(env []byte) { env[envHeaderLen] ^= 0x01 },
			want:    ErrAuthentication,
		},
		{
			name:    "last nonce byte flipped",
			corrupt: func(env []byte) { env[envHeaderLen+NonceSize-1] ^= 0x40 },
			want:    ErrAuthentication,
		},
		{
			name:    "key id bit flipped",
			corrupt: func(env []byte) { env[2] ^= 0x01 },
			want:    ErrKeyMismatch,
		},
		{
			name:    "kind changed",
			corrupt: func(env []byte) { env[1] = byte(KindMachineSealed) },
			want:    ErrMalformedEnvelope,
		},
		{
			name:    "version changed",
			corrupt: func(env []byte) { env[0] = 0x02 },
			want:    ErrMalformedEnvelope,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Seal(k, testAddr, plaintext)
			tc.corrupt(env)
			pt, err := Open(k, testAddr, env)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if pt != nil {
				t.Fatalf("got plaintext back from a tampered envelope")
			}
		})
	}
}

// Every byte of the envelope is covered: nothing may be flipped anywhere
// without Open refusing it.
func TestOpenRejectsEverySingleBitFlip(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("covered"))

	for i := range env {
		for _, mask := range []byte{0x01, 0x80} {
			corrupt := bytes.Clone(env)
			corrupt[i] ^= mask
			if _, err := Open(k, testAddr, corrupt); err == nil {
				t.Fatalf("flipping byte %d with mask %#x was accepted", i, mask)
			}
		}
	}
}

func TestOpenRejectsTruncation(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("value"))

	for n := 0; n < len(env); n++ {
		if _, err := Open(k, testAddr, env[:n]); err == nil {
			t.Fatalf("a %d-byte prefix of a %d-byte envelope was accepted", n, len(env))
		}
	}
}

func TestNoncesAreUniqueAcrossSeals(t *testing.T) {
	k := NewEnvKey()
	plaintext := []byte("the same plaintext every time")

	const n = 2000
	nonces := make(map[string]struct{}, n)
	ciphertexts := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		env := Seal(k, testAddr, plaintext)
		nonce := string(env[envHeaderLen : envHeaderLen+NonceSize])
		if _, dup := nonces[nonce]; dup {
			t.Fatalf("nonce repeated after %d seals", i)
		}
		nonces[nonce] = struct{}{}

		ct := string(env[envHeaderLen+NonceSize:])
		if _, dup := ciphertexts[ct]; dup {
			t.Fatalf("ciphertext repeated after %d seals of the same plaintext", i)
		}
		ciphertexts[ct] = struct{}{}
	}
}

func TestKeyIDIsStableAndDistinct(t *testing.T) {
	k := NewEnvKey()
	first, second := k.ID(), k.ID()
	if first != second {
		t.Fatalf("ID is not deterministic: %q then %q", first, second)
	}
	// A key reconstructed from its text form names the same id.
	reparsed, err := ParseEnvKey(k.Text())
	if err != nil {
		t.Fatalf("ParseEnvKey: %v", err)
	}
	if reparsed.ID() != k.ID() {
		t.Fatal("ID changed across a text round trip")
	}

	const n = 1000
	seen := make(map[KeyID]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewEnvKey().ID()
		if _, dup := seen[id]; dup {
			t.Fatalf("two of %d fresh keys share an id", n)
		}
		seen[id] = struct{}{}
	}
}

// A one-bit difference in the key must not produce a related id.
func TestKeyIDDoesNotTrackTheKey(t *testing.T) {
	a := NewEnvKey()
	b := a
	b[0] ^= 0x01

	if a.ID() == b.ID() {
		t.Fatal("neighbouring keys share an id")
	}
	id := a.ID()
	if bytes.Contains(id[:], a[:4]) {
		t.Fatal("the id contains key material")
	}
}

func TestEnvKeyStringDoesNotLeakTheKey(t *testing.T) {
	k := NewEnvKey()
	s := k.String()
	if strings.Contains(s, k.Text()) {
		t.Fatal("String rendered the key itself")
	}
	if !strings.Contains(s, k.ID().String()) {
		t.Fatalf("String = %q, want it to name the key id", s)
	}
	if strings.Contains(s, hex.EncodeToString(k[:8])) {
		t.Fatal("String rendered key material")
	}
}

func TestEnvKeyTextRoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		k := NewEnvKey()
		text := k.Text()
		if !strings.HasPrefix(text, EnvKeyPrefix) {
			t.Fatalf("text %q lacks the %q prefix", text, EnvKeyPrefix)
		}
		got, err := ParseEnvKey(text)
		if err != nil {
			t.Fatalf("ParseEnvKey(%q): %v", text, err)
		}
		if got != k {
			t.Fatal("text round trip changed the key")
		}
	}
}

func TestParseEnvKeyAcceptsOperatorMangling(t *testing.T) {
	k := NewEnvKey()
	text := k.Text()
	body := strings.TrimPrefix(text, EnvKeyPrefix)

	cases := []struct {
		name  string
		input string
	}{
		{"canonical", text},
		{"lowercase", strings.ToLower(text)},
		{"surrounding whitespace", "  \n" + text + "\t\n"},
		{"grouped with dashes", EnvKeyPrefix + body[:8] + "-" + body[8:24] + "-" + body[24:]},
		{"spaces inside", EnvKeyPrefix + body[:10] + " " + body[10:]},
		{"O typed for zero", EnvKeyPrefix + strings.ReplaceAll(body, "0", "O")},
		{"I typed for one", EnvKeyPrefix + strings.ReplaceAll(body, "1", "I")},
		{"l typed for one", EnvKeyPrefix + strings.ReplaceAll(strings.ToLower(body), "1", "l")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseEnvKey(tc.input)
			if err != nil {
				t.Fatalf("ParseEnvKey: %v", err)
			}
			if got != k {
				t.Fatal("parsed to a different key")
			}
		})
	}
}

func TestParseEnvKeyRejects(t *testing.T) {
	k := NewEnvKey()
	text := k.Text()
	body := strings.TrimPrefix(text, EnvKeyPrefix)

	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"prefix only", EnvKeyPrefix},
		{"no prefix", body},
		{"wrong prefix", "hzkey_" + body},
		{"truncated by one", text[:len(text)-1]},
		{"one character too long", text + "0"},
		{"body replaced with zeros", EnvKeyPrefix + strings.Repeat("0", len(body))},
		{"letter outside the alphabet", EnvKeyPrefix + "U" + body[1:]},
		{"two characters transposed", EnvKeyPrefix + swap(body, 3, 4)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEnvKey(tc.input); !errors.Is(err, ErrMalformedKey) {
				t.Fatalf("err = %v, want ErrMalformedKey", err)
			}
		})
	}
}

// The checksum exists so that a mistyped key is an error rather than a decrypt
// that yields garbage. Every single-character substitution must be caught.
func TestParseEnvKeyRejectsEverySingleCharacterCorruption(t *testing.T) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	k := NewEnvKey()
	body := strings.TrimPrefix(k.Text(), EnvKeyPrefix)

	for i := 0; i < len(body); i++ {
		for _, c := range []byte(alphabet) {
			if c == body[i] {
				continue
			}
			corrupt := EnvKeyPrefix + body[:i] + string(c) + body[i+1:]
			got, err := ParseEnvKey(corrupt)
			if err == nil {
				t.Fatalf("corruption at %d (%c -> %c) was accepted as key %s", i, body[i], c, got.ID())
			}
			if !errors.Is(err, ErrMalformedKey) {
				t.Fatalf("corruption at %d: err = %v, want ErrMalformedKey", i, err)
			}
		}
	}
}

func TestWrapUnwrapEnvKey(t *testing.T) {
	machine := mustMachineKey(t)
	k := NewEnvKey()

	env, err := WrapEnvKey(machine.PublicKey(), k)
	if err != nil {
		t.Fatalf("WrapEnvKey: %v", err)
	}
	if want := ecMinLen + EnvKeySize; len(env) != want {
		t.Fatalf("wrapped key is %d bytes, want %d", len(env), want)
	}
	if bytes.Contains(env, k[:]) {
		t.Fatal("the environment key appears verbatim in the wrapped blob")
	}

	h, err := ParseEnvelopeHeader(env)
	if err != nil {
		t.Fatalf("ParseEnvelopeHeader: %v", err)
	}
	if h.Kind != KindWrappedEnvKey {
		t.Errorf("kind = %s, want %s", h.Kind, KindWrappedEnvKey)
	}
	if h.Recipient != FingerprintOf(machine.PublicKey()) {
		t.Error("recipient fingerprint does not name the machine")
	}

	got, err := UnwrapEnvKey(machine, env)
	if err != nil {
		t.Fatalf("UnwrapEnvKey: %v", err)
	}
	if got != k {
		t.Fatal("unwrapped a different environment key")
	}

	// And the recovered key opens what the original sealed, which is the whole
	// point of the grant.
	sealed := Seal(k, testAddr, []byte("db password"))
	pt, err := Open(got, testAddr, sealed)
	if err != nil {
		t.Fatalf("Open with the unwrapped key: %v", err)
	}
	if string(pt) != "db password" {
		t.Fatalf("got %q", pt)
	}
}

func TestWrapIsBoundToOneMachine(t *testing.T) {
	a, b := mustMachineKey(t), mustMachineKey(t)
	k := NewEnvKey()

	env, err := WrapEnvKey(a.PublicKey(), k)
	if err != nil {
		t.Fatalf("WrapEnvKey: %v", err)
	}

	if _, err := UnwrapEnvKey(b, env); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("machine B unwrapping A's envelope: err = %v, want ErrKeyMismatch", err)
	}

	// Restamp the fingerprint so the label check passes and only the
	// cryptography stands between B and the key.
	relabelled := bytes.Clone(env)
	fp := FingerprintOf(b.PublicKey())
	copy(relabelled[2:2+FingerprintSize], fp[:])

	got, err := UnwrapEnvKey(b, relabelled)
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("relabelled envelope: err = %v, want ErrAuthentication", err)
	}
	if got != (EnvKey{}) {
		t.Fatal("a failed unwrap returned key material")
	}

	// A is unaffected by the relabelling attempt.
	if _, err := UnwrapEnvKey(a, relabelled); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("A on a relabelled envelope: err = %v, want ErrKeyMismatch", err)
	}
}

func TestSealToMachineRoundTrip(t *testing.T) {
	machine := mustMachineKey(t)

	cases := [][]byte{
		{},
		[]byte("npm registry token"),
		bytes.Repeat([]byte{0xab}, 4096),
	}

	for _, plaintext := range cases {
		env, err := SealToMachine(machine.PublicKey(), testMach, plaintext)
		if err != nil {
			t.Fatalf("SealToMachine: %v", err)
		}
		got, err := OpenFromMachine(machine, testMach, env)
		if err != nil {
			t.Fatalf("OpenFromMachine: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatal("round trip changed the plaintext")
		}
	}
}

func TestMachineSealedIsBoundToOneMachine(t *testing.T) {
	a, b := mustMachineKey(t), mustMachineKey(t)

	env, err := SealToMachine(a.PublicKey(), testMach, []byte("registry token"))
	if err != nil {
		t.Fatalf("SealToMachine: %v", err)
	}

	if _, err := OpenFromMachine(b, testMach, env); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}

	relabelled := bytes.Clone(env)
	fp := FingerprintOf(b.PublicKey())
	copy(relabelled[2:2+FingerprintSize], fp[:])
	if _, err := OpenFromMachine(b, testMach, relabelled); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("relabelled: err = %v, want ErrAuthentication", err)
	}
}

// The purpose label in the HKDF info is what stops one construction's blob from
// being opened as the other's, beyond the kind byte claiming it.
func TestWrappedKeyAndMachineSecretDoNotInterchange(t *testing.T) {
	machine := mustMachineKey(t)
	k := NewEnvKey()

	wrapped, err := WrapEnvKey(machine.PublicKey(), k)
	if err != nil {
		t.Fatalf("WrapEnvKey: %v", err)
	}
	if _, err := OpenFromMachine(machine, testMach, wrapped); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("err = %v, want ErrMalformedEnvelope", err)
	}

	// Restamp the kind so only the derived key differs.
	relabelled := bytes.Clone(wrapped)
	relabelled[1] = byte(KindMachineSealed)
	if _, err := OpenFromMachine(machine, testMach, relabelled); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("relabelled kind: err = %v, want ErrAuthentication", err)
	}

	sealed, err := SealToMachine(machine.PublicKey(), testMach, k[:])
	if err != nil {
		t.Fatalf("SealToMachine: %v", err)
	}
	if _, err := UnwrapEnvKey(machine, sealed); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("err = %v, want ErrMalformedEnvelope", err)
	}
}

func TestMachineEnvelopeRejectsEverySingleBitFlip(t *testing.T) {
	machine := mustMachineKey(t)
	env, err := SealToMachine(machine.PublicKey(), testMach, []byte("covered"))
	if err != nil {
		t.Fatalf("SealToMachine: %v", err)
	}

	for i := range env {
		for _, mask := range []byte{0x01, 0x80} {
			corrupt := bytes.Clone(env)
			corrupt[i] ^= mask
			if _, err := OpenFromMachine(machine, testMach, corrupt); err == nil {
				t.Fatalf("flipping byte %d with mask %#x was accepted", i, mask)
			}
		}
	}
}

func TestMachineEnvelopesAreUniquePerSeal(t *testing.T) {
	machine := mustMachineKey(t)
	k := NewEnvKey()

	const n = 200
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		env, err := WrapEnvKey(machine.PublicKey(), k)
		if err != nil {
			t.Fatalf("WrapEnvKey: %v", err)
		}
		// Everything after the fingerprint is fresh per wrap: ephemeral key,
		// nonce and ciphertext.
		body := string(env[2+FingerprintSize:])
		if _, dup := seen[body]; dup {
			t.Fatalf("two wraps of the same key produced identical output after %d tries", i)
		}
		seen[body] = struct{}{}
	}
}

func TestMachineKeyMarshalling(t *testing.T) {
	priv := mustMachineKey(t)

	privText := MarshalMachinePrivateKey(priv)
	if !strings.HasPrefix(privText, MachinePrivateKeyPrefix) {
		t.Fatalf("private key text %q lacks its prefix", privText)
	}
	gotPriv, err := ParseMachinePrivateKey(privText + "\n")
	if err != nil {
		t.Fatalf("ParseMachinePrivateKey: %v", err)
	}
	if !gotPriv.Equal(priv) {
		t.Fatal("private key round trip changed the key")
	}

	pubText := MarshalMachinePublicKey(priv.PublicKey())
	if !strings.HasPrefix(pubText, MachinePublicKeyPrefix) {
		t.Fatalf("public key text %q lacks its prefix", pubText)
	}
	gotPub, err := ParseMachinePublicKey(pubText)
	if err != nil {
		t.Fatalf("ParseMachinePublicKey: %v", err)
	}
	if !gotPub.Equal(priv.PublicKey()) {
		t.Fatal("public key round trip changed the key")
	}
	if len(gotPub.Bytes()) != PublicKeySize {
		t.Fatalf("public key is %d bytes, want %d", len(gotPub.Bytes()), PublicKeySize)
	}
}

func TestParseMachineKeyRejects(t *testing.T) {
	priv := mustMachineKey(t)
	pubText := MarshalMachinePublicKey(priv.PublicKey())

	pubCases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no prefix", strings.TrimPrefix(pubText, MachinePublicKeyPrefix)},
		{"not base64", MachinePublicKeyPrefix + "!!!!"},
		{"truncated point", pubText[:len(pubText)-8]},
		// A point that is not on the curve is what an invalid-curve attack
		// looks like arriving at the approver's browser.
		{"off-curve point", MachinePublicKeyPrefix + base64Of(offCurvePoint())},
		{"point at infinity", MachinePublicKeyPrefix + base64Of(make([]byte, PublicKeySize))},
	}
	for _, tc := range pubCases {
		t.Run("public/"+tc.name, func(t *testing.T) {
			if _, err := ParseMachinePublicKey(tc.input); !errors.Is(err, ErrMalformedKey) {
				t.Fatalf("err = %v, want ErrMalformedKey", err)
			}
		})
	}

	privText := MarshalMachinePrivateKey(priv)
	privCases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no prefix", strings.TrimPrefix(privText, MachinePrivateKeyPrefix)},
		{"public key text", pubText},
		{"zero scalar", MachinePrivateKeyPrefix + base64Of(make([]byte, 32))},
		{"wrong length", MachinePrivateKeyPrefix + base64Of(make([]byte, 31))},
	}
	for _, tc := range privCases {
		t.Run("private/"+tc.name, func(t *testing.T) {
			if _, err := ParseMachinePrivateKey(tc.input); !errors.Is(err, ErrMalformedKey) {
				t.Fatalf("err = %v, want ErrMalformedKey", err)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	priv := mustMachineKey(t)
	fp := FingerprintOf(priv.PublicKey())

	s := fp.String()
	if want := 2*FingerprintSize + FingerprintSize/2 - 1; len(s) != want {
		t.Fatalf("fingerprint %q is %d characters, want %d", s, len(s), want)
	}
	if strings.Count(s, "-") != FingerprintSize/2-1 {
		t.Fatalf("fingerprint %q is not grouped for reading", s)
	}
	if s != strings.ToUpper(s) {
		t.Fatalf("fingerprint %q is not uppercase", s)
	}

	for _, input := range []string{s, strings.ToLower(s), strings.ReplaceAll(s, "-", "")} {
		got, err := ParseFingerprint(input)
		if err != nil {
			t.Fatalf("ParseFingerprint(%q): %v", input, err)
		}
		if got != fp {
			t.Fatalf("ParseFingerprint(%q) round trip changed the value", input)
		}
	}

	if _, err := ParseFingerprint(s[:len(s)-1]); !errors.Is(err, ErrMalformedKey) {
		t.Fatal("a truncated fingerprint was accepted")
	}

	other := mustMachineKey(t)
	if FingerprintOf(other.PublicKey()) == fp {
		t.Fatal("two machines share a fingerprint")
	}
	if fp != FingerprintOf(priv.PublicKey()) {
		t.Fatal("fingerprint is not deterministic")
	}
}

func TestParseEnvelopeHeaderRejects(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("value"))

	cases := []struct {
		name  string
		input []byte
	}{
		{"nil", nil},
		{"one byte", env[:1]},
		{"unknown kind", append([]byte{EnvelopeVersion, 0x7f}, env[2:]...)},
		{"future version", append([]byte{0x99}, env[1:]...)},
		{"env envelope one byte short", env[:envMinLen-1]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEnvelopeHeader(tc.input); !errors.Is(err, ErrMalformedEnvelope) {
				t.Fatalf("err = %v, want ErrMalformedEnvelope", err)
			}
		})
	}
}

func TestEnvelopeBase64RoundTrip(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("value"))

	got, err := DecodeEnvelope(EncodeEnvelope(env))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if !bytes.Equal(got, env) {
		t.Fatal("base64 round trip changed the envelope")
	}
	if _, err := DecodeEnvelope("not base64 !!!"); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatal("garbage base64 was accepted")
	}
}

// The end-to-end shape the plan describes: agent registers, approver wraps, hz
// relays, agent unwraps and reads its secrets on every later boot with no human
// in the path.
func TestApprovalGrantsExactlyOneCapability(t *testing.T) {
	approved := mustMachineKey(t)
	unapproved := mustMachineKey(t)

	envKey := NewEnvKey()
	secret := Seal(envKey, testAddr, []byte("prod database password"))

	// hz holds the sealed secret and the wrapped key and can read neither.
	wrapped, err := WrapEnvKey(approved.PublicKey(), envKey)
	if err != nil {
		t.Fatalf("WrapEnvKey: %v", err)
	}

	// The unapproved box has every blob and no key.
	if _, err := UnwrapEnvKey(unapproved, wrapped); err == nil {
		t.Fatal("an unapproved machine unwrapped the environment key")
	}
	if _, err := OpenFromMachine(unapproved, testMach, secret); err == nil {
		t.Fatal("an unapproved machine opened a sealed secret")
	}

	recovered, err := UnwrapEnvKey(approved, wrapped)
	if err != nil {
		t.Fatalf("UnwrapEnvKey: %v", err)
	}
	pt, err := Open(recovered, testAddr, secret)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(pt) != "prod database password" {
		t.Fatalf("got %q", pt)
	}
}

// browserContext is the address encoding rewritten from doc.go alone: a label
// and then each field as a 4-byte big-endian byte length followed by its UTF-8
// bytes. It stands in for what the approval page has to build with a DataView
// and a TextEncoder.
func browserContext(label string, fields ...string) []byte {
	var out []byte
	for _, f := range append([]string{label}, fields...) {
		b := []byte(f) // TextEncoder().encode(f)
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(b)))
		out = append(out, n[:]...)
		out = append(out, b...)
	}
	return out
}

func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// offCurvePoint is a well-formed SEC1 uncompressed encoding whose coordinates
// do not satisfy the curve equation.
func offCurvePoint() []byte {
	p := make([]byte, PublicKeySize)
	p[0] = 0x04
	p[32] = 0x01
	p[64] = 0x01
	return p
}

func swap(s string, i, j int) string {
	b := []byte(s)
	b[i], b[j] = b[j], b[i]
	return string(b)
}

// browserWrapEnvKey is an independent reimplementation of the wrapping
// construction, written only from the recipe in doc.go and from raw primitives
// — no package internals, HKDF spelled out as the two HMACs it is. It stands in
// for the WebCrypto implementation in the approval page. If the documented
// recipe ever stops describing what this package does, this test fails rather
// than the browser silently minting envelopes no machine can open.
func browserWrapEnvKey(t *testing.T, recipientSEC1 []byte, envKey []byte) []byte {
	t.Helper()

	recipient, err := ecdh.P256().NewPublicKey(recipientSEC1)
	if err != nil {
		t.Fatalf("import recipient: %v", err)
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral: %v", err)
	}
	ephSEC1 := eph.PublicKey().Bytes()
	if len(ephSEC1) != 65 {
		t.Fatalf("exported ephemeral key is %d bytes, the recipe says 65", len(ephSEC1))
	}

	shared, err := eph.ECDH(recipient)
	if err != nil {
		t.Fatalf("deriveBits: %v", err)
	}
	if len(shared) != 32 {
		t.Fatalf("shared secret is %d bytes, the recipe says deriveBits(..., 256)", len(shared))
	}

	// HKDF-SHA256, RFC 5869, as WebCrypto's deriveBits performs it.
	info := []byte("hz-config/v1 wrap-env-key")
	info = append(info, 0x00)
	info = append(info, ephSEC1...)
	info = append(info, recipientSEC1...)

	extract := hmac.New(sha256.New, []byte("hz-config/v1"))
	extract.Write(shared)
	prk := extract.Sum(nil)

	expand := hmac.New(sha256.New, prk)
	expand.Write(info)
	expand.Write([]byte{0x01})
	aesKey := expand.Sum(nil) // 32 bytes, so exactly one expansion block

	fpSum := sha256.Sum256(append([]byte("hz-config/v1 machine-fingerprint"), recipientSEC1...))

	header := []byte{0x01, 0x02}
	header = append(header, fpSum[:12]...)
	header = append(header, ephSEC1...)
	if len(header) != 79 {
		t.Fatalf("header is %d bytes, the recipe says the nonce starts at 79", len(header))
	}

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("getRandomValues: %v", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	if gcm.Overhead() != 16 {
		t.Fatalf("tag is %d bytes, the recipe says tagLength: 128", gcm.Overhead())
	}

	out := append([]byte{}, header...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, envKey, header)
}

func TestDocumentedBrowserRecipeInteroperates(t *testing.T) {
	machine := mustMachineKey(t)
	k := NewEnvKey()

	env := browserWrapEnvKey(t, machine.PublicKey().Bytes(), k[:])
	if len(env) != 139 {
		t.Fatalf("wrapped key is %d bytes, doc.go promises 139", len(env))
	}

	got, err := UnwrapEnvKey(machine, env)
	if err != nil {
		t.Fatalf("UnwrapEnvKey on a browser-built envelope: %v", err)
	}
	if got != k {
		t.Fatal("the browser recipe produced a different key")
	}
}

// The other direction: a page that seals a secret under a pasted environment
// key must produce something the agent on the box can open.
func TestDocumentedEnvSealRecipeInteroperates(t *testing.T) {
	k := NewEnvKey()
	plaintext := []byte("prod gateway key")

	idSum := sha256.Sum256(append([]byte("hz-config/v1 env-key-id"), k[:]...))
	header := []byte{0x01, 0x01}
	header = append(header, idSum[:8]...)
	if len(header) != 10 {
		t.Fatalf("header is %d bytes, the recipe says the nonce starts at 10", len(header))
	}

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("getRandomValues: %v", err)
	}
	block, err := aes.NewCipher(k[:]) // no KDF on this path
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	env := append([]byte{}, header...)
	env = append(env, nonce...)
	env = gcm.Seal(env, nonce, plaintext, append(append([]byte{}, header...),
		browserContext("hz-config/v1 addr",
			testAddr.Environment, testAddr.App, testAddr.Role, testAddr.Key)...))

	got, err := Open(k, testAddr, env)
	if err != nil {
		t.Fatalf("Open on a browser-built envelope: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("the browser recipe produced a different plaintext")
	}
}

// The checksum and text encoding the approval page has to reimplement to accept
// a pasted key.
func TestDocumentedEnvKeyTextRecipe(t *testing.T) {
	k := NewEnvKey()

	sum := sha256.Sum256(append([]byte("hz-config/v1 env-key-checksum"), k[:]...))
	body := append(append([]byte{}, k[:]...), sum[:4]...)
	text := "hzenv_" + base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").
		WithPadding(base32.NoPadding).EncodeToString(body)

	if text != k.Text() {
		t.Fatalf("recipe produced %q, package produced %q", text, k.Text())
	}
	if n := len(strings.TrimPrefix(text, "hzenv_")); n != 58 {
		t.Fatalf("encoded body is %d characters, doc.go promises 58", n)
	}
}

func TestParseKeyID(t *testing.T) {
	k := NewEnvKey()
	id := k.ID()

	got, err := ParseKeyID("  " + id.String() + "\n")
	if err != nil {
		t.Fatalf("ParseKeyID: %v", err)
	}
	if got != id {
		t.Fatal("round trip changed the key id")
	}

	for _, bad := range []string{"", "zz", id.String()[:15], id.String() + "00", "gg" + id.String()[2:]} {
		if _, err := ParseKeyID(bad); !errors.Is(err, ErrMalformedKey) {
			t.Fatalf("ParseKeyID(%q): err = %v, want ErrMalformedKey", bad, err)
		}
	}
}

// A wrapped-key envelope whose plaintext is not 32 bytes authenticates but is
// still not an environment key. It must be refused rather than padded.
func TestUnwrapRejectsWrongSizedPayload(t *testing.T) {
	machine := mustMachineKey(t)

	env := browserWrapEnvKey(t, machine.PublicKey().Bytes(), []byte("only sixteen byt"))
	if _, err := UnwrapEnvKey(machine, env); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("err = %v, want ErrMalformedEnvelope", err)
	}
}

// The substitution the address binding exists to refuse: hz serving a blob that
// authenticates perfectly but belongs to a different environment, app, role or
// key name.
func TestOpenRejectsEveryAlteredAddressField(t *testing.T) {
	k := NewEnvKey()
	env := Seal(k, testAddr, []byte("prod database password"))

	cases := []struct {
		name string
		addr Addr
	}{
		{"environment", Addr{"staging", testAddr.App, testAddr.Role, testAddr.Key}},
		{"app", Addr{testAddr.Environment, "billing", testAddr.Role, testAddr.Key}},
		{"role", Addr{testAddr.Environment, testAddr.App, "ops", testAddr.Key}},
		{"key name", Addr{testAddr.Environment, testAddr.App, testAddr.Role, "API_TOKEN"}},
		{"environment emptied", Addr{"", testAddr.App, testAddr.Role, testAddr.Key}},
		{"key name emptied", Addr{testAddr.Environment, testAddr.App, testAddr.Role, ""}},
		{"everything empty", Addr{}},
		{"key name with a trailing space", Addr{testAddr.Environment, testAddr.App, testAddr.Role, testAddr.Key + " "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pt, err := Open(k, tc.addr, env)
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("Open at %s: err = %v, want ErrAuthentication", tc.addr, err)
			}
			if pt != nil {
				t.Fatal("a blob served under the wrong address yielded plaintext")
			}
		})
	}

	// The right address still opens it.
	if _, err := Open(k, testAddr, env); err != nil {
		t.Fatalf("Open at the sealed address: %v", err)
	}
}

func TestOpenFromMachineRejectsEveryAlteredAddressField(t *testing.T) {
	machine := mustMachineKey(t)
	env, err := SealToMachine(machine.PublicKey(), testMach, []byte("registry token"))
	if err != nil {
		t.Fatalf("SealToMachine: %v", err)
	}

	cases := []struct {
		name string
		addr MachineAddr
	}{
		{"machine id", MachineAddr{"mch_01k9v2w3x4y5z6a7b8c9d0e1ff", testMach.Key}},
		{"key name", MachineAddr{testMach.Machine, "DOCKER_TOKEN"}},
		{"machine emptied", MachineAddr{"", testMach.Key}},
		{"key emptied", MachineAddr{testMach.Machine, ""}},
		{"both empty", MachineAddr{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := OpenFromMachine(machine, tc.addr, env); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("OpenFromMachine at %s: err = %v, want ErrAuthentication", tc.addr, err)
			}
		})
	}

	if _, err := OpenFromMachine(machine, testMach, env); err != nil {
		t.Fatalf("OpenFromMachine at the sealed address: %v", err)
	}
}

// Tuples that a delimiter-joined encoding would collapse into the same string
// must produce different contexts, and must not open each other's blobs.
func TestAddressEncodingIsUnambiguous(t *testing.T) {
	envCases := [][2]Addr{
		{
			{Environment: "a", App: "b/c", Role: "r", Key: "k"},
			{Environment: "a/b", App: "c", Role: "r", Key: "k"},
		},
		{
			{Environment: "", App: "ab", Role: "r", Key: "k"},
			{Environment: "a", App: "b", Role: "r", Key: "k"},
		},
		{
			{Environment: "prod", App: "redline", Role: "app", Key: "A#B"},
			{Environment: "prod", App: "redline", Role: "app#A", Key: "B"},
		},
		{
			{Environment: "x", App: "", Role: "", Key: "y"},
			{Environment: "x", App: "y", Role: "", Key: ""},
		},
	}

	k := NewEnvKey()
	for _, pair := range envCases {
		a, b := pair[0], pair[1]
		if bytes.Equal(a.context(), b.context()) {
			t.Fatalf("%s and %s encode identically", a, b)
		}
		env := Seal(k, a, []byte("value"))
		if _, err := Open(k, b, env); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("a blob for %s opened at %s: err = %v", a, b, err)
		}
	}

	machineCases := [][2]MachineAddr{
		{{Machine: "mch_1", Key: "2TOKEN"}, {Machine: "mch_12", Key: "TOKEN"}},
		{{Machine: "", Key: "ab"}, {Machine: "a", Key: "b"}},
		{{Machine: "mch_1#X", Key: "Y"}, {Machine: "mch_1", Key: "X#Y"}},
	}

	machine := mustMachineKey(t)
	for _, pair := range machineCases {
		a, b := pair[0], pair[1]
		if bytes.Equal(a.context(), b.context()) {
			t.Fatalf("%s and %s encode identically", a, b)
		}
		env, err := SealToMachine(machine.PublicKey(), a, []byte("value"))
		if err != nil {
			t.Fatalf("SealToMachine: %v", err)
		}
		if _, err := OpenFromMachine(machine, b, env); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("a blob for %s opened at %s: err = %v", a, b, err)
		}
	}

	// An environment-scoped and a machine-scoped address never collide either,
	// even given field contents chosen to try.
	env := Addr{Environment: "mch_1", App: "TOKEN", Role: "", Key: ""}
	mach := MachineAddr{Machine: "mch_1", Key: "TOKEN"}
	if bytes.Equal(env.context(), mach.context()) {
		t.Fatal("an Addr and a MachineAddr encode identically")
	}
}

// Non-ASCII fields must be prefixed with their BYTE length, not their character
// count, or the encoding stops being unambiguous the moment anyone names a key
// in something other than ASCII.
func TestAddressEncodingCountsBytesNotRunes(t *testing.T) {
	a := Addr{Environment: "pröd", App: "x", Role: "r", Key: "k"}
	ctx := a.context()

	want := browserContext("hz-config/v1 addr", a.Environment, a.App, a.Role, a.Key)
	if !bytes.Equal(ctx, want) {
		t.Fatal("context does not match the documented encoding for a non-ASCII field")
	}
	// "pröd" is 4 runes, 5 bytes.
	if !bytes.Contains(ctx, []byte{0, 0, 0, 5}) {
		t.Fatal("the length prefix is not the byte length")
	}
}

// The context bytes, reimplemented from doc.go alone, must be what the package
// authenticates — the same guarantee the other browser-recipe tests give.
func TestDocumentedAddressRecipeInteroperates(t *testing.T) {
	if got, want := testAddr.context(), browserContext("hz-config/v1 addr",
		testAddr.Environment, testAddr.App, testAddr.Role, testAddr.Key); !bytes.Equal(got, want) {
		t.Fatalf("Addr context = %x, recipe produced %x", got, want)
	}
	if got, want := testMach.context(), browserContext("hz-config/v1 machine-addr",
		testMach.Machine, testMach.Key); !bytes.Equal(got, want) {
		t.Fatalf("MachineAddr context = %x, recipe produced %x", got, want)
	}

	// And a machine-scoped secret built entirely from the recipe opens.
	machine := mustMachineKey(t)
	env := browserSealToMachine(t, machine.PublicKey().Bytes(), testMach, []byte("registry token"))
	got, err := OpenFromMachine(machine, testMach, env)
	if err != nil {
		t.Fatalf("OpenFromMachine on a browser-built envelope: %v", err)
	}
	if string(got) != "registry token" {
		t.Fatalf("got %q", got)
	}
}

// browserSealToMachine is the kind 0x03 construction written from doc.go alone,
// including the address in the additional data.
func browserSealToMachine(t *testing.T, recipientSEC1 []byte, addr MachineAddr, plaintext []byte) []byte {
	t.Helper()

	recipient, err := ecdh.P256().NewPublicKey(recipientSEC1)
	if err != nil {
		t.Fatalf("import recipient: %v", err)
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral: %v", err)
	}
	ephSEC1 := eph.PublicKey().Bytes()
	shared, err := eph.ECDH(recipient)
	if err != nil {
		t.Fatalf("deriveBits: %v", err)
	}

	info := []byte("hz-config/v1 machine-secret")
	info = append(info, 0x00)
	info = append(info, ephSEC1...)
	info = append(info, recipientSEC1...)

	extract := hmac.New(sha256.New, []byte("hz-config/v1"))
	extract.Write(shared)
	expand := hmac.New(sha256.New, extract.Sum(nil))
	expand.Write(info)
	expand.Write([]byte{0x01})
	aesKey := expand.Sum(nil)

	fpSum := sha256.Sum256(append([]byte("hz-config/v1 machine-fingerprint"), recipientSEC1...))
	header := []byte{0x01, 0x03}
	header = append(header, fpSum[:12]...)
	header = append(header, ephSEC1...)

	additional := append(append([]byte{}, header...),
		browserContext("hz-config/v1 machine-addr", addr.Machine, addr.Key)...)

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("getRandomValues: %v", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	out := append([]byte{}, header...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, additional)
}

// The address the agent passes to Open comes from the request it made, not from
// hz's answer. That is the property that makes authenticating it worth
// anything.
func TestConfigRequestAddr(t *testing.T) {
	req := ConfigRequest{Environment: "prod", App: "redline", Role: "app", Version: "1.2.5"}
	got := req.Addr("DB_PASSWORD")
	want := Addr{Environment: "prod", App: "redline", Role: "app", Key: "DB_PASSWORD"}
	if got != want {
		t.Fatalf("Addr = %s, want %s", got, want)
	}

	k := NewEnvKey()
	env := Seal(k, want, []byte("value"))
	if _, err := Open(k, req.Addr("DB_PASSWORD"), env); err != nil {
		t.Fatalf("Open at the request's own address: %v", err)
	}
	if _, err := Open(k, req.Addr("OTHER"), env); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a different key name opened the blob")
	}
}
