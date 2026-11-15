// Package wgkey is the one place that knows what a WireGuard key looks like.
//
// WHY A LEAF PACKAGE. Two callers need the same answer and neither may depend
// on the other: internal/config validates a SegmentMember.PublicKey before it
// is saved, and internal/agent MINTS this box's key at enrolment. A helper
// living in either would drag that package's world into the other — the agent
// does not want internal/config's whole estate model, and internal/config must
// not reach for a package that shells to `wg`. So the shared fact lives here,
// with nothing but the standard library under it.
//
// THERE IS A THIRD CALLER THAT DOES NOT IMPORT THIS YET:
// internal/wireguard.ValidatePublicKey, a 44-character regex that predates the
// segment model and guards the gateway's own wg0.conf. It is a second answer to
// one question and should become a call to Valid — left alone here only because
// internal/wireguard's purity guard treats the renderer's import list as part of
// the contract, so folding it in is its own change with its own seam-test edit.
//
// WHAT A WIREGUARD KEY IS. Curve25519, 32 bytes, carried as standard base64:
// 43 characters and a '=', 44 in all. That is true of the PUBLIC half and of
// the PRIVATE half alike, which is the uncomfortable fact this package states
// out loud rather than papers over: NOTHING HERE CAN TELL THEM APART. A
// validator that accepts a public key accepts a private one of the same shape,
// so "hz never holds a private key" is a property of what the agent SENDS and
// of what the record can HOLD (one field, named PublicKey, filled from the
// public half) — never of a check performed on the bytes. See
// internal/agent/segmentkey.go, which is the half that keeps the promise.
package wgkey

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeyLen is Curve25519's key length in bytes.
const KeyLen = 32

// EncodedLen is how long that is in standard base64, padding included. It is
// the length `wg genkey` prints and the length a config file carries.
const EncodedLen = 44

// ErrNotAKey is any string that is not a WireGuard key. A value rather than a
// formatted string so a caller can match on it; the wrapping message says which
// of the three ways it failed.
var ErrNotAKey = errors.New("not a WireGuard key")

// Parse decodes a WireGuard key and returns its 32 bytes.
//
// CANONICAL FORM IS REQUIRED, not merely decodable. base64 of 32 bytes spends
// 258 bits on 256, so two trailing bits are structurally zero and a string that
// sets them decodes to the same key while not being equal to it — two spellings
// of one peer, which is exactly the kind of near-duplicate that makes a peer set
// disagree with itself. Re-encoding and comparing is the whole check: every key
// `wg genkey`, `wg pubkey` or this package ever produced is an encoding of
// bytes and so is canonical by construction, so this cannot refuse a real key.
func Parse(s string) ([]byte, error) {
	if len(s) != EncodedLen {
		return nil, fmt.Errorf("%w: %d characters, want %d (base64 of %d bytes)", ErrNotAKey, len(s), EncodedLen, KeyLen)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: not base64: %v", ErrNotAKey, err)
	}
	if len(b) != KeyLen {
		return nil, fmt.Errorf("%w: decodes to %d bytes, want %d", ErrNotAKey, len(b), KeyLen)
	}
	if base64.StdEncoding.EncodeToString(b) != s {
		return nil, fmt.Errorf("%w: not the canonical base64 of those %d bytes (trailing bits set)", ErrNotAKey, KeyLen)
	}
	return b, nil
}

// Valid reports whether s is a WireGuard key, for callers with nothing useful
// to say about which way it is not one.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// Encode renders 32 bytes as a WireGuard key.
func Encode(b []byte) (string, error) {
	if len(b) != KeyLen {
		return "", fmt.Errorf("%w: %d bytes, want %d", ErrNotAKey, len(b), KeyLen)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Generate mints a key pair, in process.
//
// IN GO RATHER THAN BY SHELLING TO `wg`, unlike internal/wireguard's
// GenerateKeyPair, and the difference is the caller: that one runs on the
// gateway, where the `wg` binary is a given. This one runs at ENROLMENT, on a
// box that may not have WireGuard installed yet and whose whole tunnel is still
// a projection gap. An enrolment that failed because a tool was missing would
// leave the box keyless for a reason that has nothing to do with authority.
//
// crypto/ecdh's X25519 is the same curve and the same base point `wg pubkey`
// uses, so a pair minted here is interchangeable with one minted by wg —
// asserted against RFC 7748's test vector in wgkey_test.go rather than assumed.
//
// The private half is returned to ONE caller, which writes it 0600 and never
// transmits it. It is a string because that is what goes in the file and in
// wg0.conf; there is no version of this that keeps it off the heap, and
// pretending otherwise with a byte slice would be theatre.
func Generate() (private, public string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("minting a WireGuard key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv.Bytes()),
		base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// Public derives the public half of a private key — the `wg pubkey` operation,
// so a stored private key is the single source of the pair and the two cannot
// drift apart on disk.
func Public(private string) (string, error) {
	b, err := Parse(private)
	if err != nil {
		return "", err
	}
	priv, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotAKey, err)
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}
