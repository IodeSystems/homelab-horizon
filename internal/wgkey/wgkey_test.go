package wgkey

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// THE INTEROP CLAIM, CHECKED RATHER THAN ASSERTED IN A COMMENT. Generate mints
// in Go; every box that consumes the result runs `wg`, which mints and derives
// with Curve25519's base point. If those two ever disagreed, hz would record a
// key no tunnel could use and the failure would surface as a tunnel that never
// handshakes — the least debuggable shape there is.
//
// RFC 7748 §6.1 is the vector both implementations answer to: Alice's private
// scalar and the public key X25519 derives from it. `wg pubkey` is that
// operation over base64, which is what Public does.
func TestPublicMatchesTheCurve25519Vector(t *testing.T) {
	privHex := "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	pubHex := "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"

	privB, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatal(err)
	}
	pubB, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	priv := base64.StdEncoding.EncodeToString(privB)
	want := base64.StdEncoding.EncodeToString(pubB)

	got, err := Public(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Public derived %q, and Curve25519 says %q — a key minted here would not be the key wg computes", got, want)
	}
}

// A minted pair is self-consistent: the public half this package hands to hz is
// the one `wg pubkey` would derive from the private half it leaves on the box.
func TestAMintedPairAgreesWithItself(t *testing.T) {
	priv, pub, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(priv) || !Valid(pub) {
		t.Fatalf("Generate produced something that is not a key: priv=%q pub=%q", priv, pub)
	}
	if priv == pub {
		t.Fatal("the private and public halves are the same string")
	}
	derived, err := Public(priv)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Fatalf("Generate said %q and Public derives %q from the same private key", pub, derived)
	}
}

// Two mints are two keys. A key store that handed every segment the same key
// would collapse the per-interface isolation the segment model exists for, and
// the failure would look exactly like it working.
func TestGenerateDoesNotRepeatItself(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		_, pub, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if seen[pub] {
			t.Fatalf("Generate returned %q twice in %d mints", pub, i+1)
		}
		seen[pub] = true
	}
}

// A KEY IS VALIDATED AS A KEY, not accepted as any string. Each case below is a
// string somebody has actually put in a config field at some point.
func TestParseRefusesWhatIsNotAKey(t *testing.T) {
	_, realPub, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	// The last character of a canonical 32-byte key carries four real bits and
	// two structurally-zero ones. Advancing it by one sets a trailing bit: the
	// string still decodes to the same 32 bytes and is not the same string —
	// a second spelling of one peer.
	nonCanonical := realPub[:42] + string(nudge(realPub[42])) + "="
	if nonCanonical == realPub {
		t.Fatal("failed to build a non-canonical spelling")
	}

	for name, s := range map[string]string{
		"empty":            "",
		"a placeholder":    "key-app-1",
		"the old fixture":  "abc+/def=",
		"a hostname":       "gw-1.example.com",
		"right length, no": strings.Repeat("!", EncodedLen),
		"32 hex chars":     "0123456789abcdef0123456789abcdef",
		"unpadded":         strings.TrimSuffix(realPub, "="),
		"too long":         realPub + "A",
		"non-canonical":    nonCanonical,
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%s: %q was accepted as a WireGuard key", name, s)
		}
		if Valid(s) {
			t.Errorf("%s: Valid(%q) says yes", name, s)
		}
	}

	if _, err := Parse(realPub); err != nil {
		t.Fatalf("a real key was refused: %v", err)
	}
}

// nudge flips a bit in a base64 character so the result is still a base64
// character. Used only to build a non-canonical spelling above.
func nudge(c byte) byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	i := strings.IndexByte(alphabet, c)
	if i < 0 {
		return 'A'
	}
	return alphabet[(i+1)%len(alphabet)]
}

// Encode is Parse's inverse and refuses anything that is not 32 bytes.
func TestEncodeRoundTrips(t *testing.T) {
	_, pub, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(pub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != pub {
		t.Fatalf("round trip changed the key: %q → %q", pub, got)
	}
	if _, err := Encode(b[:31]); err == nil {
		t.Fatal("Encode accepted 31 bytes")
	}
}

// THE LIMIT, STATED AS A TEST so nobody later mistakes validation for a
// guarantee. A private key has exactly the same shape as a public one, so this
// package cannot refuse one and no caller may pretend it does. What keeps hz
// from holding a private key is the agent sending only the public half and the
// record having only one field to put it in — see
// TestEnrolmentNeverPutsAPrivateKeyOnTheWire.
func TestAPrivateKeyIsIndistinguishableFromAPublicOne(t *testing.T) {
	priv, _, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(priv) {
		t.Fatal("a private key failed the shape check — if this ever becomes true, " +
			"say so where it matters: validation would then be a real defence and the comments here are wrong")
	}
}
