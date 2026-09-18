package configmgr

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Sizes, in bytes, of everything that appears on the wire.
const (
	EnvKeySize      = 32 // AES-256
	KeyIDSize       = 8
	FingerprintSize = 12
	NonceSize       = 12 // GCM's native nonce length; any other size costs the browser an extra GHASH step
	TagSize         = 16 // GCM tag, 128 bits, the only length WebCrypto agrees with Go on by default
	PublicKeySize   = 65 // SEC1 uncompressed P-256 point: 0x04 || X(32) || Y(32)

	aesKeySize   = 32
	checksumSize = 4
)

// EnvelopeVersion is byte 0 of every envelope. It exists so a future format
// change is a rejection with a version number in it rather than an
// authentication failure nobody can explain.
const EnvelopeVersion byte = 0x01

// Prefixes. Every encoded artifact names itself, so a value found in a log, a
// config file or a paste buffer can be identified without context, and so a
// leaked private key is greppable across a fleet.
const (
	EnvKeyPrefix            = "hzenv_"
	MachinePublicKeyPrefix  = "hzmpub_"
	MachinePrivateKeyPrefix = "hzmprv_"
)

// Domain separation labels. Every hash and every derived key is bound to one of
// these, so a value computed for one purpose can never be reused as another.
// No label is a prefix of any other, which is what makes label||input
// unambiguous without an explicit length field.
const (
	hkdfSalt = "hz-config/v1"

	purposeWrapEnvKey  = "hz-config/v1 wrap-env-key"
	purposeMachineSeal = "hz-config/v1 machine-secret"

	labelAddr        = "hz-config/v1 addr"
	labelMachineAddr = "hz-config/v1 machine-addr"
	labelKeyID       = "hz-config/v1 env-key-id"
	labelEnvKeySum   = "hz-config/v1 env-key-checksum"
	labelFingerprint = "hz-config/v1 machine-fingerprint"
)

// Errors callers are expected to branch on.
//
// There are only four, deliberately. A caller that can distinguish "wrong key"
// from "tampered blob" from "truncated blob" learns nothing useful and gains
// three branches to get wrong; what it needs to know is whether the plaintext
// is trustworthy, and it never is unless err is nil.
var (
	ErrMalformedKey      = errors.New("malformed key")
	ErrMalformedEnvelope = errors.New("malformed envelope")
	ErrKeyMismatch       = errors.New("envelope is addressed to a different key")
	ErrAuthentication    = errors.New("envelope failed authentication")
)

// Envelope offsets. Both layouts are header || nonce || ciphertext||tag, where
// the header is every byte before the nonce. The AEAD authenticates the header
// as stored, followed by the address the caller supplies — which is not stored
// anywhere, so serving a blob under the wrong address fails the tag.
const (
	envHeaderLen = 2 + KeyIDSize // version, kind, key id
	envMinLen    = envHeaderLen + NonceSize + TagSize
	ecHeaderLen  = 2 + FingerprintSize + PublicKeySize // version, kind, recipient fingerprint, ephemeral public key
	ecMinLen     = ecHeaderLen + NonceSize + TagSize
)

// EnvelopeKind is byte 1 of an envelope. It says which key opens the envelope,
// which is why the client library needs no parallel field on the wire telling
// it the same thing: the blob is the single source of truth, and the kind byte
// is covered by the AEAD's additional data so it cannot be changed in flight.
type EnvelopeKind byte

const (
	KindEnvSealed     EnvelopeKind = 0x01 // opens with the environment key
	KindWrappedEnvKey EnvelopeKind = 0x02 // an environment key, wrapped to a machine
	KindMachineSealed EnvelopeKind = 0x03 // a value sealed directly to a machine
)

func (k EnvelopeKind) String() string {
	switch k {
	case KindEnvSealed:
		return "env-sealed"
	case KindWrappedEnvKey:
		return "wrapped-env-key"
	case KindMachineSealed:
		return "machine-sealed"
	default:
		return fmt.Sprintf("unknown(0x%02x)", byte(k))
	}
}

// EnvKey is one environment's symmetric key: the credential the whole scheme
// reduces to. It is a value type rather than a slice so that passing one around
// cannot alias a buffer some other goroutine is about to reuse.
//
// No attempt is made to zeroize it. Go copies values freely and moves them
// during a stack growth, so wiping one copy would leave the others untouched
// while creating the impression that the key had been erased. The real
// mitigation is that the key lives in one process for the life of that process.
type EnvKey [EnvKeySize]byte

// NewEnvKey mints an environment key.
//
// It returns no error because crypto/rand.Read has no failure mode a caller
// could act on: since Go 1.24 it panics rather than returning an error. An
// error return here would be a branch no caller can exercise and no test can
// cover.
func NewEnvKey() EnvKey {
	var k EnvKey
	copy(k[:], randomBytes(EnvKeySize))
	return k
}

// String deliberately does NOT render the key. Text does.
//
// A secret that prints itself under %v reaches a log the first moment someone
// prints a struct that holds one, and that log line outlives the incident. For
// the same reason EnvKey implements neither MarshalText nor MarshalJSON: the
// key must never be serialized by accident, only on purpose.
func (k EnvKey) String() string { return "EnvKey(" + k.ID().String() + ")" }

// ID is the public name of the key. Every ciphertext carries one so rotation
// can be gradual: a new key starts sealing while ciphertext under the old key
// still opens, instead of requiring a fleet-wide atomic re-encrypt that in
// practice never happens.
//
// 64 bits. The id is not a security boundary — the AEAD tag is what actually
// decides whether a key opens a blob, and the id only selects a candidate from
// a keyring. A homelab holds one key per environment, so single digits; the
// chance of a birthday collision among even a thousand keys is under 2^-44.
// Preimage resistance is what matters here and that is SHA-256's full strength:
// the id cannot be walked back to the key.
func (k EnvKey) ID() KeyID {
	var id KeyID
	copy(id[:], labeledHash(labelKeyID, k[:]))
	return id
}

// Text renders the key in the form an operator pastes into the approval page
// and a password manager holds.
//
// Crockford base32, checksummed and prefixed. The requirements were: survives a
// copy-paste through any chat client, survives being read aloud or retyped off
// a screen, and fails loudly when it does not. Crockford's alphabet drops I, L,
// O and U, so the classic transcription confusions have no way to encode; the
// decoder folds O to 0 and I/L to 1 so a human who writes one anyway is still
// understood. The 32-bit checksum is what turns a mistyped key into an error
// instead of a decrypt that yields garbage nobody recognises as garbage.
func (k EnvKey) Text() string {
	buf := make([]byte, 0, EnvKeySize+checksumSize)
	buf = append(buf, k[:]...)
	buf = append(buf, k.checksum()...)
	return EnvKeyPrefix + crockford.EncodeToString(buf)
}

// ParseEnvKey reads the form Text produces.
//
// It never truncates and never pads: a key of the wrong length is an error, not
// a key. Dashes and whitespace are stripped so an operator may group the text
// for readability, and case is folded so a key retyped in lowercase still
// works.
func ParseEnvKey(s string) (EnvKey, error) {
	s = stripSeparators(s)
	if len(s) < len(EnvKeyPrefix) || !strings.EqualFold(s[:len(EnvKeyPrefix)], EnvKeyPrefix) {
		return EnvKey{}, fmt.Errorf("%w: environment key must start with %q", ErrMalformedKey, EnvKeyPrefix)
	}

	body := normalizeCrockford(s[len(EnvKeyPrefix):])
	want := crockford.EncodedLen(EnvKeySize + checksumSize)
	if len(body) != want {
		return EnvKey{}, fmt.Errorf("%w: environment key has %d characters after the prefix, want %d", ErrMalformedKey, len(body), want)
	}

	raw, err := crockford.DecodeString(body)
	if err != nil {
		return EnvKey{}, fmt.Errorf("%w: %v", ErrMalformedKey, err)
	}
	// The encoded length is not a whole number of bytes, so the final character
	// carries bits the decoder discards. Without this check two different texts
	// decode to the same key, and a corruption in the last character would be
	// accepted silently — exactly the failure the checksum exists to prevent.
	if crockford.EncodeToString(raw) != body {
		return EnvKey{}, fmt.Errorf("%w: environment key is not canonically encoded", ErrMalformedKey)
	}

	var k EnvKey
	copy(k[:], raw[:EnvKeySize])
	if subtle.ConstantTimeCompare(k.checksum(), raw[EnvKeySize:]) != 1 {
		return EnvKey{}, fmt.Errorf("%w: environment key checksum does not match, it was mistyped or corrupted", ErrMalformedKey)
	}
	return k, nil
}

func (k EnvKey) checksum() []byte {
	return labeledHash(labelEnvKeySum, k[:])[:checksumSize]
}

// KeyID names the environment key that opens a ciphertext.
type KeyID [KeyIDSize]byte

func (id KeyID) String() string { return hex.EncodeToString(id[:]) }

// ParseKeyID reads the hex form, for looking a key up in a keyring.
func ParseKeyID(s string) (KeyID, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != KeyIDSize {
		return KeyID{}, fmt.Errorf("%w: key id must be %d hex characters", ErrMalformedKey, 2*KeyIDSize)
	}
	var id KeyID
	copy(id[:], b)
	return id, nil
}

// Fingerprint is the short form of a machine's public key, for an approver to
// compare against what the machine printed at registration.
//
// 96 bits, six groups of four hex digits. The operative attack is not a
// birthday collision but a second preimage: an attacker who can register a
// machine whose fingerprint matches a real one gets secrets wrapped to a key it
// holds. That costs 2^96 here. 64 bits would have been shorter to read and
// within reach of a grinding adversary, and the whole point of the fingerprint
// is that a human decision rests on it.
type Fingerprint [FingerprintSize]byte

func (f Fingerprint) String() string {
	s := strings.ToUpper(hex.EncodeToString(f[:]))
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(s[i : i+4])
	}
	return b.String()
}

// ParseFingerprint reads the grouped hex form, tolerating missing groups
// separators and either case.
func ParseFingerprint(s string) (Fingerprint, error) {
	b, err := hex.DecodeString(strings.ToLower(stripSeparators(s)))
	if err != nil || len(b) != FingerprintSize {
		return Fingerprint{}, fmt.Errorf("%w: fingerprint must be %d hex characters", ErrMalformedKey, 2*FingerprintSize)
	}
	var f Fingerprint
	copy(f[:], b)
	return f, nil
}

// FingerprintOf derives a machine's fingerprint from its public key.
func FingerprintOf(pub *ecdh.PublicKey) Fingerprint {
	var f Fingerprint
	copy(f[:], labeledHash(labelFingerprint, pub.Bytes()))
	return f
}

// NewMachineKey generates the keypair an agent creates at registration and
// keeps for the life of the machine. It is a fresh key, never the machine's
// WireGuard key: reusing key material across two protocols means a weakness in
// either one becomes a weakness in both.
func NewMachineKey() (*ecdh.PrivateKey, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate machine key: %w", err)
	}
	return priv, nil
}

// MarshalMachinePublicKey renders a public key for the registration request.
func MarshalMachinePublicKey(pub *ecdh.PublicKey) string {
	return MachinePublicKeyPrefix + base64.StdEncoding.EncodeToString(pub.Bytes())
}

// ParseMachinePublicKey reads a public key submitted by an agent.
//
// ecdh.NewPublicKey rejects points that are not on the curve, which is the
// check that stops a hostile registration from steering the approver's browser
// into an invalid-curve attack on the key it is about to wrap.
func ParseMachinePublicKey(s string) (*ecdh.PublicKey, error) {
	b, err := decodePrefixed(s, MachinePublicKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: machine public key: %v", ErrMalformedKey, err)
	}
	pub, err := ecdh.P256().NewPublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("%w: machine public key: %v", ErrMalformedKey, err)
	}
	return pub, nil
}

// MarshalMachinePrivateKey renders the private key for a root-only 0600 file.
// The result is a single line with no trailing newline; writing one is the
// caller's choice and parsing tolerates either.
func MarshalMachinePrivateKey(priv *ecdh.PrivateKey) string {
	return MachinePrivateKeyPrefix + base64.StdEncoding.EncodeToString(priv.Bytes())
}

// ParseMachinePrivateKey reads what MarshalMachinePrivateKey wrote.
func ParseMachinePrivateKey(s string) (*ecdh.PrivateKey, error) {
	b, err := decodePrefixed(s, MachinePrivateKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: machine private key: %v", ErrMalformedKey, err)
	}
	priv, err := ecdh.P256().NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("%w: machine private key: %v", ErrMalformedKey, err)
	}
	return priv, nil
}

// Addr names one value inside one config: the (environment, app, role) the
// config is addressed by, plus the key name within it.
//
// It is NOT stored in the envelope. It is authenticated additional data, which
// means the opener has to supply it from its own context and a mismatch is an
// authentication failure rather than a wrong value quietly applied. That is the
// whole point: hz holds the blobs and decides which one to serve, so without
// this an agent asking for prod/redline/app#DB_PASSWORD would accept the
// ciphertext of staging's, or of a different key in the same config, or an
// older blessed value, and every one of those authenticates perfectly. An
// address carried INSIDE the envelope could only ever agree with itself.
type Addr struct {
	Environment string
	App         string
	Role        string
	Key         string
}

func (a Addr) String() string {
	return a.Environment + "/" + a.App + "/" + a.Role + "#" + a.Key
}

func (a Addr) context() []byte {
	return canonicalContext(labelAddr, a.Environment, a.App, a.Role, a.Key)
}

// MachineAddr names one machine-scoped secret. Machine is hz's machine id, not
// the hostname: the agent learns it once at approval and persists it beside its
// private key, so it is something the opener knows independently rather than
// something a later response can change.
type MachineAddr struct {
	Machine string
	Key     string
}

func (a MachineAddr) String() string { return a.Machine + "#" + a.Key }

func (a MachineAddr) context() []byte {
	return canonicalContext(labelMachineAddr, a.Machine, a.Key)
}

// canonicalContext encodes an address so no two different tuples can produce
// the same bytes. Every field is preceded by its length as a 4-byte big-endian
// count, so a separator appearing inside a field changes nothing. Joining with
// a delimiter instead would let environment "a" with app "b/c" collide with
// environment "a/b" and app "c", and a collision here is exactly the
// substitution the additional data exists to refuse.
func canonicalContext(label string, fields ...string) []byte {
	n := 4 + len(label)
	for _, f := range fields {
		n += 4 + len(f)
	}
	out := make([]byte, 0, n)
	out = appendField(out, label)
	for _, f := range fields {
		out = appendField(out, f)
	}
	return out
}

func appendField(dst []byte, s string) []byte {
	if uint64(len(s)) > math.MaxUint32 {
		// Unreachable for any real address, and a truncated length would be an
		// ambiguity in the one encoding that must not have one.
		panic("configmgr: address field is too long to encode")
	}
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

// aad assembles what the AEAD authenticates: the envelope header as stored,
// then the address the caller supplied.
func aad(header, context []byte) []byte {
	out := make([]byte, 0, len(header)+len(context))
	out = append(out, header...)
	return append(out, context...)
}

// EnvelopeHeader is the part of an envelope anybody may read, including hz,
// which holds these blobs and can open none of them.
type EnvelopeHeader struct {
	Version   byte
	Kind      EnvelopeKind
	KeyID     KeyID       // KindEnvSealed only
	Recipient Fingerprint // KindWrappedEnvKey and KindMachineSealed only
}

// ParseEnvelopeHeader reads an envelope's routing information without
// attempting to decrypt it. hz uses it to index blobs by key id and by
// recipient; a client uses it to decide which key to reach for.
func ParseEnvelopeHeader(envelope []byte) (EnvelopeHeader, error) {
	if len(envelope) < 2 {
		return EnvelopeHeader{}, fmt.Errorf("%w: %d bytes is too short to have a header", ErrMalformedEnvelope, len(envelope))
	}
	h := EnvelopeHeader{Version: envelope[0], Kind: EnvelopeKind(envelope[1])}
	if h.Version != EnvelopeVersion {
		return EnvelopeHeader{}, fmt.Errorf("%w: version %d, this build speaks %d", ErrMalformedEnvelope, h.Version, EnvelopeVersion)
	}
	switch h.Kind {
	case KindEnvSealed:
		if len(envelope) < envMinLen {
			return EnvelopeHeader{}, fmt.Errorf("%w: %s envelope is %d bytes, minimum is %d", ErrMalformedEnvelope, h.Kind, len(envelope), envMinLen)
		}
		copy(h.KeyID[:], envelope[2:2+KeyIDSize])
	case KindWrappedEnvKey, KindMachineSealed:
		if len(envelope) < ecMinLen {
			return EnvelopeHeader{}, fmt.Errorf("%w: %s envelope is %d bytes, minimum is %d", ErrMalformedEnvelope, h.Kind, len(envelope), ecMinLen)
		}
		copy(h.Recipient[:], envelope[2:2+FingerprintSize])
	default:
		return EnvelopeHeader{}, fmt.Errorf("%w: %s", ErrMalformedEnvelope, h.Kind)
	}
	return h, nil
}

// Seal encrypts a secret value under an environment key.
//
// The environment key is used as the AES key directly, with no KDF. It is
// already 32 uniformly random bytes, so a KDF would add nothing except a step
// the browser has to replicate exactly to interoperate.
//
// addr is authenticated but not stored. See Addr.
//
// It returns no error: with a 32-byte key neither aes.NewCipher nor
// cipher.NewGCM has a reachable failure, and crypto/rand does not fail.
func Seal(k EnvKey, addr Addr, plaintext []byte) []byte {
	id := k.ID()
	hdr := make([]byte, 0, envHeaderLen)
	hdr = append(hdr, EnvelopeVersion, byte(KindEnvSealed))
	hdr = append(hdr, id[:]...)

	// A fresh random nonce per seal, never a counter. GCM with a repeated
	// nonce under the same key does not merely leak whether two plaintexts
	// matched: it leaks their XOR, and it leaks the GHASH authentication
	// subkey, after which an attacker can forge arbitrary ciphertext that
	// verifies. There is no counter available here anyway — seals happen in a
	// browser, in hz, and on any number of boxes, with no shared state to count
	// in. At 96 random bits the chance of a repeat is negligible long past the
	// number of secrets a fleet will ever hold.
	nonce := randomBytes(NonceSize)

	out := make([]byte, 0, envMinLen+len(plaintext))
	out = append(out, hdr...)
	out = append(out, nonce...)
	return mustGCM(k[:]).Seal(out, nonce, plaintext, aad(hdr, addr.context()))
}

// Open decrypts what Seal produced. addr must be the address the caller asked
// for, taken from its own request rather than from hz's answer; a blob served
// under any other address fails authentication.
func Open(k EnvKey, addr Addr, envelope []byte) ([]byte, error) {
	h, err := ParseEnvelopeHeader(envelope)
	if err != nil {
		return nil, err
	}
	if h.Kind != KindEnvSealed {
		return nil, fmt.Errorf("%w: a %s envelope does not open with an environment key", ErrMalformedEnvelope, h.Kind)
	}
	if id := k.ID(); h.KeyID != id {
		return nil, fmt.Errorf("%w: envelope names key %s, holding %s", ErrKeyMismatch, h.KeyID, id)
	}
	nonce := envelope[envHeaderLen : envHeaderLen+NonceSize]
	pt, err := mustGCM(k[:]).Open(nil, nonce, envelope[envHeaderLen+NonceSize:], aad(envelope[:envHeaderLen], addr.context()))
	if err != nil {
		// Flattened on purpose. GCM reports one failure for a wrong key, a
		// flipped ciphertext bit, a flipped nonce bit, a rewritten header and a
		// blob served under the wrong address alike, and that is the correct
		// amount of information to pass on: the only fact a caller may act on
		// is that this plaintext does not exist.
		return nil, ErrAuthentication
	}
	return pt, nil
}

// WrapEnvKey is the capability grant. The approver's browser runs this against
// the public key in a pending registration; hz relays the result and can never
// open it. An unapproved machine cannot decrypt anything even holding every
// blob in the database, because nobody ever handed it the key.
// A wrapped environment key carries no address: it is not one config's value,
// and the recipient fingerprint plus the ECDH itself already bind it to exactly
// one machine.
func WrapEnvKey(recipient *ecdh.PublicKey, k EnvKey) ([]byte, error) {
	return sealTo(recipient, KindWrappedEnvKey, purposeWrapEnvKey, nil, k[:])
}

// UnwrapEnvKey is the agent's side of the grant, run once at approval. The
// recovered key is persisted at 0600 so later boots need no human.
func UnwrapEnvKey(priv *ecdh.PrivateKey, envelope []byte) (EnvKey, error) {
	pt, err := openFrom(priv, KindWrappedEnvKey, purposeWrapEnvKey, nil, envelope)
	if err != nil {
		return EnvKey{}, err
	}
	if len(pt) != EnvKeySize {
		return EnvKey{}, fmt.Errorf("%w: wrapped key is %d bytes, want %d", ErrMalformedEnvelope, len(pt), EnvKeySize)
	}
	var k EnvKey
	copy(k[:], pt)
	return k, nil
}

// SealToMachine encrypts a value to one machine, with no environment key
// involved. This is what makes per-device revocation expressible: the blob
// decrypts on exactly one box, so deleting it revokes there and nowhere else,
// and setting one needs only the machine's public key, which hz publishes.
func SealToMachine(recipient *ecdh.PublicKey, addr MachineAddr, plaintext []byte) ([]byte, error) {
	return sealTo(recipient, KindMachineSealed, purposeMachineSeal, addr.context(), plaintext)
}

// OpenFromMachine decrypts a machine-scoped secret. As with Open, addr is
// authenticated and must come from what the agent knows about itself.
func OpenFromMachine(priv *ecdh.PrivateKey, addr MachineAddr, envelope []byte) ([]byte, error) {
	return openFrom(priv, KindMachineSealed, purposeMachineSeal, addr.context(), envelope)
}

// EncodeEnvelope renders an envelope for a JSON field. Standard base64, because
// that is what a browser's atob and btoa speak without a shim.
func EncodeEnvelope(envelope []byte) string {
	return base64.StdEncoding.EncodeToString(envelope)
}

// DecodeEnvelope reads what EncodeEnvelope wrote.
func DecodeEnvelope(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("%w: not base64: %v", ErrMalformedEnvelope, err)
	}
	return b, nil
}

// sealTo is the ephemeral-ECDH construction shared by key wrapping and
// machine-scoped secrets. The two differ only in the kind byte and the HKDF
// purpose label, which is what keeps a blob minted for one from ever being
// accepted as the other.
func sealTo(recipient *ecdh.PublicKey, kind EnvelopeKind, purpose string, context, plaintext []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	ephPub := eph.PublicKey().Bytes()

	fp := FingerprintOf(recipient)
	hdr := make([]byte, 0, ecHeaderLen)
	hdr = append(hdr, EnvelopeVersion, byte(kind))
	hdr = append(hdr, fp[:]...)
	hdr = append(hdr, ephPub...)

	key, err := wrappingKey(shared, purpose, ephPub, recipient.Bytes())
	if err != nil {
		return nil, err
	}
	nonce := randomBytes(NonceSize)
	out := make([]byte, 0, ecMinLen+len(plaintext))
	out = append(out, hdr...)
	out = append(out, nonce...)
	return mustGCM(key).Seal(out, nonce, plaintext, aad(hdr, context)), nil
}

func openFrom(priv *ecdh.PrivateKey, kind EnvelopeKind, purpose string, context, envelope []byte) ([]byte, error) {
	h, err := ParseEnvelopeHeader(envelope)
	if err != nil {
		return nil, err
	}
	if h.Kind != kind {
		return nil, fmt.Errorf("%w: expected a %s envelope, got %s", ErrMalformedEnvelope, kind, h.Kind)
	}
	pub := priv.PublicKey()
	if fp := FingerprintOf(pub); h.Recipient != fp {
		return nil, fmt.Errorf("%w: envelope is addressed to %s, this key is %s", ErrKeyMismatch, h.Recipient, fp)
	}

	ephPub := envelope[2+FingerprintSize : ecHeaderLen]
	eph, err := ecdh.P256().NewPublicKey(ephPub)
	if err != nil {
		return nil, fmt.Errorf("%w: ephemeral key: %v", ErrMalformedEnvelope, err)
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, fmt.Errorf("%w: ecdh: %v", ErrMalformedEnvelope, err)
	}
	key, err := wrappingKey(shared, purpose, ephPub, pub.Bytes())
	if err != nil {
		return nil, err
	}
	nonce := envelope[ecHeaderLen : ecHeaderLen+NonceSize]
	pt, err := mustGCM(key).Open(nil, nonce, envelope[ecHeaderLen+NonceSize:], aad(envelope[:ecHeaderLen], context))
	if err != nil {
		return nil, ErrAuthentication
	}
	return pt, nil
}

// wrappingKey derives the AES key for one envelope from an ECDH shared secret.
//
// Both public keys go into the HKDF info, following RFC 9180's DHKEM, which
// hashes the encapsulated key and the recipient key into its context. Two
// things fall out. The derived key becomes a function of WHO the envelope is
// for, not only of the raw DH output, so an envelope cannot be relabelled for a
// different recipient and reinterpreted. And the purpose label makes a key
// derived for wrapping an environment key useless for opening a machine-scoped
// secret, which is the separation an envelope's kind byte only claims.
//
// The recipient's fingerprint is also in the AAD, but it does a different job:
// it makes "this blob is not for this machine" a clear error rather than an
// authentication failure an operator has to guess at.
func wrappingKey(shared []byte, purpose string, ephPub, recipientPub []byte) ([]byte, error) {
	info := make([]byte, 0, len(purpose)+1+2*PublicKeySize)
	info = append(info, purpose...)
	info = append(info, 0x00)
	info = append(info, ephPub...)
	info = append(info, recipientPub...)

	key, err := hkdf.Key(sha256.New, shared, []byte(hkdfSalt), string(info), aesKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive wrapping key: %w", err)
	}
	return key, nil
}

// crockford is Douglas Crockford's base32 alphabet: no I, L, O or U.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// normalizeCrockford folds the characters a human is likely to write in place
// of the ones the alphabet actually uses.
func normalizeCrockford(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 'O':
			return '0'
		case 'I', 'L':
			return '1'
		default:
			return r
		}
	}, strings.ToUpper(s))
}

// stripSeparators removes the grouping a human may have added.
func stripSeparators(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, s)
}

func decodePrefixed(s, prefix string) ([]byte, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), prefix)
	if !ok {
		return nil, fmt.Errorf("expected a %q prefix", prefix)
	}
	b, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return nil, fmt.Errorf("not base64: %w", err)
	}
	return b, nil
}

func labeledHash(label string, data []byte) []byte {
	h := sha256.New()
	h.Write([]byte(label))
	h.Write(data)
	return h.Sum(nil)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Unreachable: crypto/rand.Read panics on failure rather than
		// returning one. A panic here is the honest response anyway — there is
		// no safe way to continue without entropy, and an error return would
		// tempt a caller to try.
		panic("configmgr: crypto/rand failed: " + err.Error())
	}
	return b
}

// mustGCM panics only on arguments this package cannot construct: the key is
// always 32 bytes and AES's block size is always 16.
func mustGCM(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("configmgr: aes: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("configmgr: gcm: " + err.Error())
	}
	return aead
}
