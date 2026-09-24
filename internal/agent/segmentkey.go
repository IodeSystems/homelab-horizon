package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/wgkey"
)

// THE BOX MINTS ITS OWN KEYS, one pair per segment, and hz never sees a private
// one.
//
// WHAT WAS MISSING. internal/projection could resolve a membership to an
// interface, an address and a peer set, and then had to say out loud that it
// could not emit a `[Peer]` block for any of it, because nothing filled
// SegmentMember.PublicKey. The missing link was not the record — that field has
// been there since item 15 — it was that enrolment never reported a key. This
// file is the box's half of closing that.
//
// WHO MINTS, AND WHY IT IS NOT HZ. A private key that hz generated would be a
// private key that crossed a network and sat in hz's memory, and hz being
// compromised would then hand over every tunnel at once — the opposite of the
// property architecture.md states for segments ("a compromise of wg-code must
// not hand over wg-redline"). So the box mints, the private half is written
// 0600 into a 0700 directory beside the agent credential, and the only thing
// that ever leaves is the public half.
//
// NOTHING MINTED FOR A NON-GATEWAY BOX BEFORE THIS. internal/wireguard's
// GenerateKeyPair shells to `wg genkey` and is the GATEWAY's, called by hz
// itself; an enrolling box had no key of any kind. This change mints, rather
// than deferring it to a later step, because a key the box has and hz does not
// know about closes nothing — the report and the mint are one act or the gap
// stays open.
//
// IN PROCESS RATHER THAN BY SHELLING TO `wg`. A box is enrolled before it is
// configured; requiring the wireguard-tools package at enrolment time would
// fail enrolment for a reason that has nothing to do with authority, and would
// fail it at the box, which is the one place this whole model exists to stop
// sending people. wgkey.Generate uses the same curve and is checked against
// Curve25519's own test vector.
//
// ONE FILE PER SEGMENT, HOLDING THE PRIVATE HALF ONLY. The public half is
// derived on read, so the pair cannot drift apart on disk — the failure where a
// box reports a key it cannot actually use is silent, and takes the form of a
// tunnel that never handshakes.

// SegmentKeyDirMode and SegmentKeyFileMode: root-only, the same as the agent
// credential. A 0755 directory of 0600 keys still tells every user on the box
// which segments it is on. Exported so `hz-agent enroll` can PRINT the mode it
// wrote rather than restating a number that could drift from this one.
const (
	SegmentKeyDirMode  = 0o700
	SegmentKeyFileMode = 0o600
)

// SegmentKeyStore holds this machine's per-segment WireGuard private keys.
type SegmentKeyStore struct {
	// Dir is where the key files live. One directory, one file per segment.
	Dir string
}

// Path is the file holding one segment's private key.
//
// The name is the segment with everything outside [A-Za-z0-9._-] replaced, plus
// eight hex of the segment's SHA-256. The readable part is for whoever runs
// `ls` in this directory at 3am; the hash is what makes it a NAME rather than a
// guess — "seg:storefront" and "seg/storefront" sanitise to the same string and
// must not share a key.
func (s SegmentKeyStore) Path(segment string) string {
	sum := sha256.Sum256([]byte(segment))
	var b strings.Builder
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return filepath.Join(s.Dir, b.String()+"-"+hex.EncodeToString(sum[:4])+".key")
}

// PublicKey returns the public half of the key this box holds for a segment,
// without minting one. Empty when there is none, or when what is on disk is not
// a key — a caller that wanted one minted asks for that explicitly.
func (s SegmentKeyStore) PublicKey(segment string) string {
	b, err := os.ReadFile(s.Path(segment))
	if err != nil {
		return ""
	}
	pub, err := wgkey.Public(strings.TrimSpace(string(b)))
	if err != nil {
		return ""
	}
	return pub
}

// EnsureKey returns this box's public key for a segment, minting the pair if
// there is not one already. minted says whether this call created it, so the
// caller can tell "here is the key I have had all along" from "here is a key
// that did not exist a moment ago" — which are different things to say to hz
// and very different things to say to an operator.
//
// A file that exists and does not hold a key is REPLACED rather than reported:
// a truncated write or a half-copied directory leaves one, and the alternative
// is a box that can never enrol again without somebody deleting a file by hand.
func (s SegmentKeyStore) EnsureKey(segment string) (public string, minted bool, err error) {
	if strings.TrimSpace(s.Dir) == "" {
		return "", false, fmt.Errorf("no directory to keep %s's key in", segment)
	}
	if pub := s.PublicKey(segment); pub != "" {
		return pub, false, nil
	}
	return s.mint(segment)
}

// RotateKey mints a NEW pair for a segment, replacing whatever is there.
//
// Deliberate and separate from EnsureKey, because the consequence is not local:
// the old public key is what every peer on that segment is configured to talk
// to, so rotating one here means hz has to accept the replacement and every
// peer has to be re-rendered. That is why the flag that reaches this is the
// same flag that tells hz the change is a rotation and not an impostor.
func (s SegmentKeyStore) RotateKey(segment string) (public string, err error) {
	if strings.TrimSpace(s.Dir) == "" {
		return "", fmt.Errorf("no directory to keep %s's key in", segment)
	}
	pub, _, err := s.mint(segment)
	return pub, err
}

// mint writes a fresh pair and returns the public half.
//
// Through a temp file in the same directory and a rename, like the credential:
// a reader that catches the write mid-flight gets the old key or the new one,
// never half of one. The mode is set BEFORE the private key is written — a
// world-readable window of a few microseconds is a window.
func (s SegmentKeyStore) mint(segment string) (string, bool, error) {
	if err := os.MkdirAll(s.Dir, SegmentKeyDirMode); err != nil {
		return "", false, fmt.Errorf("creating %s: %w", s.Dir, err)
	}
	// MkdirAll leaves an EXISTING directory's mode alone, which is how the
	// agent's token directory ended up 0755 on the audit VM with a 0600 file
	// inside it. Asserted rather than assumed, for the same reason.
	if err := os.Chmod(s.Dir, SegmentKeyDirMode); err != nil {
		return "", false, fmt.Errorf("securing %s: %w", s.Dir, err)
	}

	private, public, err := wgkey.Generate()
	if err != nil {
		return "", false, err
	}

	path := s.Path(segment)
	tmp, err := os.CreateTemp(s.Dir, ".key-*")
	if err != nil {
		return "", false, fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(SegmentKeyFileMode); err != nil {
		_ = tmp.Close()
		return "", false, err
	}
	if _, err := tmp.WriteString(private + "\n"); err != nil {
		_ = tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, fmt.Errorf("writing %s: %w", path, err)
	}
	return public, true, nil
}

// Report builds the SegmentKeys an enrolment carries: this box's public key for
// each named segment, minting any that do not exist yet.
//
// rotate mints fresh pairs for every segment instead, which is what an operator
// asks for when the box has been rebuilt or a key is believed leaked.
//
// Sorted by segment, so two identical boxes send identical requests and a diff
// of two enrolments is about what changed rather than about map ordering.
func (s SegmentKeyStore) Report(segments []string, rotate bool) ([]SegmentKey, error) {
	var out []SegmentKey
	seen := map[string]bool{}
	for _, name := range segments {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		var (
			pub string
			err error
		)
		if rotate {
			pub, err = s.RotateKey(name)
		} else {
			pub, _, err = s.EnsureKey(name)
		}
		if err != nil {
			return nil, fmt.Errorf("this machine's key for %s: %w", name, err)
		}
		out = append(out, SegmentKey{Segment: name, PublicKey: pub})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Segment < out[j].Segment })
	return out, nil
}
