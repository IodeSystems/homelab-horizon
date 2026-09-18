package configmgr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// The client keystore: the tree on a client's disk that answers "which key
// opens this envelope", and "which key should I seal with".
//
//	<root>/secrets/keys/<environment>/<app>/<role>/<label>.<keyid>.key
//	~/.hz/secrets/keys/prod/redline/app/2026-09.a3f1c02b9d4e5f60.key
//
// hz never holds an environment key, so this tree is the only place one lives.
// Everything below exists because the obvious implementation of a file lookup
// is wrong in a specific, exploitable way; each check names its reason.
//
// # Nothing is ever interpolated into a path
//
// Every segment of that path is free text an operator chose: the environment,
// the app, the role, and the label. An environment named "../../.." or a label
// of "2026-09/../.." walks out of the tree. Two independent defences, because
// either alone has been enough to lose this argument before:
//
//   - every segment must match validName before it is used at all, and
//   - the tree is walked one component at a time with openat(2), so no path
//     string is ever built and handed to the kernel to re-resolve. The only
//     joined paths in this file are in error messages.
//
// # The permission checks happen on the descriptor, not on the path
//
// stat-then-open is two resolutions of the same name, and on a shared box the
// gap between them is the whole attack: check a 0600 file you own, have it
// replaced, then open somebody else's. So every check here is fstat(2) on a
// descriptor that is already open, and the descriptor for each directory is
// the one the next openat starts from. A name is resolved exactly once.
//
// Parent directories are checked too. A key file's own 0600 says nothing if it
// sits in a 0777 directory, because anyone can unlink it and put their own
// there. Checking stops at the root: what is above the root is the operator's
// business, and ascending would fail on any /tmp-rooted tree.
//
// # Symlinks are not followed
//
// Every open carries O_NOFOLLOW, so a symlinked component is ELOOP rather than
// a redirect to a file that passes every other check.
//
// # The root is anchored outside the working directory
//
// Default ~/.hz, overridable only by KeystoreRootEnv, and only by an absolute
// path. A cwd-relative ".hz/" would mean a repository that ships a hostile one
// gets consulted by anyone who runs the CLI inside it.

// KeystoreRootEnv overrides the keystore root. Its value must be an absolute
// path; a relative one is refused rather than resolved against the working
// directory, which is the whole point of having the variable be explicit.
const KeystoreRootEnv = "HZ_HOME"

// keyTreePrefix is the path from the root to the key tree. Fixed segments, so
// they are not subject to validName.
var keyTreePrefix = []string{"secrets", "keys"}

const keyFileSuffix = ".key"

// maxKeyFileSize caps what is read off disk. A key file is a couple of hundred
// bytes; anything near this is either corruption or an attempt to make a client
// read a large file into memory on someone else's behalf.
const maxKeyFileSize = 64 << 10

// validName is the charset every path segment must match: address fields and
// the label alike. Lowercase, because two names differing only in case are one
// name on a case-insensitive filesystem and two directories on ext4, and that
// ambiguity is the bug class this project exists to kill. No dot, which is what
// makes <label>.<keyid>.key parse unambiguously, and what makes "." and ".."
// unrepresentable rather than merely rejected.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maxNameLen = 64

// Errors callers are expected to branch on.
var (
	// ErrNoSuchKey means the tree holds no key for that address and id. It is
	// not a security failure: a client that has never been approved for an
	// address legitimately has nothing there.
	ErrNoSuchKey = errors.New("no such key in the keystore")

	// ErrInsecureKey means the key exists but the filesystem does not protect
	// it: wrong mode, wrong owner, a symlink, or a directory anyone can write.
	ErrInsecureKey = errors.New("key file is not adequately protected")

	// ErrBadName means an address field or a label is not a legal path segment.
	ErrBadName = errors.New("not a legal keystore path segment")

	// ErrMalformedKeyFile means the bytes on disk are not a key file this build
	// can read, or the key they hold is not the key the filename claims.
	ErrMalformedKeyFile = errors.New("malformed key file")

	// ErrRefuseToSeal is the seal-side half of the pointer asymmetry. See
	// Keystore.SealingKey.
	ErrRefuseToSeal = errors.New("refusing to seal")

	// ErrStaleKey is the open-side half, and it is advisory: it is what
	// StaleKey returns, and opening proceeds anyway.
	ErrStaleKey = errors.New("key is not the one hz calls current")
)

// Keystore is a client's key tree, rooted at an absolute path.
type Keystore struct {
	root string
	// uid is the effective uid at construction, compared against the owner of
	// every file and directory touched. Captured once so a single keystore
	// cannot be half-checked against two identities.
	uid int
}

// DefaultKeystore opens the keystore at KeystoreRootEnv, or at ~/.hz.
func DefaultKeystore() (*Keystore, error) {
	if root := os.Getenv(KeystoreRootEnv); root != "" {
		return NewKeystore(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("keystore: no home directory and %s is unset: %w", KeystoreRootEnv, err)
	}
	return NewKeystore(filepath.Join(home, ".hz"))
}

// NewKeystore opens the keystore at an explicit root, which must be absolute.
// The root is not required to exist yet; Keystore.Put creates what it needs.
func NewKeystore(root string) (*Keystore, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("keystore: root %q is not absolute; a working-directory-relative keystore would let a repository supply its own keys", root)
	}
	return &Keystore{root: filepath.Clean(root), uid: os.Geteuid()}, nil
}

// Root is the absolute path the keystore is anchored at.
func (ks *Keystore) Root() string { return ks.root }

// KeyInfo describes one key file without disclosing its material.
type KeyInfo struct {
	ID        KeyID
	Label     string
	CreatedAt time.Time
}

func (i KeyInfo) String() string {
	return fmt.Sprintf("%s (%s, created %s)", i.ID, i.Label, i.CreatedAt.UTC().Format(time.RFC3339))
}

// CurrentKey is hz's advisory pointer to the current key at an address.
//
// It is a type rather than a *KeyID so that its ZERO VALUE means "nobody
// asked". A caller who forgets to consult hz gets a refusal from SealingKey
// instead of a silent seal under whichever key file happened to sort last,
// which is the accident this type exists to prevent.
//
// The two states a caller can construct are deliberately both explicit:
// CurrentKeyIs when hz answered, CurrentKeyUnavailable when it did not.
type CurrentKey struct {
	state pointerState
	id    KeyID
}

type pointerState uint8

const (
	pointerNotConsulted pointerState = iota // the zero value: nobody asked
	pointerKnown
	pointerUnavailable
)

// CurrentKeyIs records the key id hz named as current for an address.
func CurrentKeyIs(id KeyID) CurrentKey { return CurrentKey{state: pointerKnown, id: id} }

// CurrentKeyUnavailable records that hz was asked and had no answer: it was
// unreachable, or the address is brand new and no key has been used there yet.
// The two are not distinguished, because the client cannot tell them apart and
// must behave the same way either way.
func CurrentKeyUnavailable() CurrentKey { return CurrentKey{state: pointerUnavailable} }

func (c CurrentKey) String() string {
	switch c.state {
	case pointerKnown:
		return c.id.String()
	case pointerUnavailable:
		return "unavailable"
	default:
		return "not consulted"
	}
}

// StaleKey is the OPEN side of the pointer asymmetry: it reports, as an
// advisory error wrapping ErrStaleKey, that the key an envelope named is not
// the one hz calls current. A caller logs it and opens anyway.
//
// It is deliberately NOT folded into KeyFor. An error returned from a resolve
// call is the one thing callers reliably treat as fatal, and opening must never
// become fatal here: old ciphertext under an old key is the normal state during
// a rotation, and a client that refused it would break every boot that had not
// yet been re-sealed. A pointer that was never consulted, or that hz could not
// supply, warns about nothing.
func StaleKey(id KeyID, current CurrentKey) error {
	if current.state != pointerKnown || current.id == id {
		return nil
	}
	return fmt.Errorf("%w: opening with %s, hz calls %s current", ErrStaleKey, id, current.id)
}

// KeyFor resolves one specific key, for OPENING.
//
// Every envelope names its key id, so this is a direct lookup and needs no
// pointer from hz — which is what keeps the boot path free of any dependency on
// freshness. A box that cannot reach hz still opens its cached config.
//
// The label half of the filename is unknown here, so the role directory is
// scanned for the one name ending in .<keyid>.key. Two files claiming one id is
// a refusal, not a choice: that is what a swapped key file looks like.
//
// addr.Key is ignored. The keystore is addressed by (environment, app, role);
// the config key name is bound into the AEAD, not into the path.
func (ks *Keystore) KeyFor(addr Addr, id KeyID) (EnvKey, error) {
	segments, err := ks.addrSegments(addr)
	if err != nil {
		return EnvKey{}, err
	}

	dir, err := ks.openDir(segments, false)
	if err != nil {
		return EnvKey{}, err
	}
	defer func() { _ = dir.Close() }()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return EnvKey{}, fmt.Errorf("keystore: read %s: %w", ks.display(segments), err)
	}

	// Suffix match on an id that is already eight bytes of KeyID. There is no
	// glob here and no pattern metacharacter can reach this point: see KeyForID
	// for the door the untyped form comes through.
	want := "." + id.String() + keyFileSuffix
	var found []string
	for _, name := range names {
		if strings.HasSuffix(name, want) && len(name) > len(want) {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return EnvKey{}, fmt.Errorf("%w: %s at %s", ErrNoSuchKey, id, ks.display(segments))
	case 1:
	default:
		slices.Sort(found)
		return EnvKey{}, fmt.Errorf("%w: %d files claim key %s at %s: %s", ErrMalformedKeyFile, len(found), id, ks.display(segments), strings.Join(found, ", "))
	}

	k, _, err := ks.loadKeyFile(dir, segments, found[0])
	if err != nil {
		return EnvKey{}, err
	}
	return k, nil
}

// KeyForID is KeyFor for a key id in its wire form, and it is the only door the
// untyped form comes through.
//
// The id arrives from the envelope, which arrives from hz. A hex string that is
// pasted into a filename without being checked first is a glob pattern: hz
// supplies "*" and the client matches an arbitrary key file, whichever one it
// finds. ParseKeyID requires exactly 16 hex characters and nothing else, and it
// runs BEFORE any directory is opened, so a malformed id never reaches the
// filesystem at all.
func (ks *Keystore) KeyForID(addr Addr, id string) (EnvKey, error) {
	parsed, err := ParseKeyID(id)
	if err != nil {
		return EnvKey{}, fmt.Errorf("keystore: %w", err)
	}
	// ParseKeyID is lenient for an operator pasting an id: it trims whitespace
	// and folds case. A filename cannot afford either, because two spellings
	// would then denote one key, so the wire form must be canonical exactly.
	if parsed.String() != id {
		return EnvKey{}, fmt.Errorf("%w: key id %q is not canonically encoded", ErrMalformedKey, id)
	}
	return ks.KeyFor(addr, parsed)
}

// SealingKey resolves the key to SEAL with, which is the direction with no key
// id in hand and therefore the direction that has to make a choice.
//
// current is not optional, and it has no default: a caller who has not
// consulted hz's pointer holds the zero CurrentKey and is refused. That is the
// signature's job — sealing under an unvetted key must not be reachable by
// forgetting an argument.
//
// The rules, and why each one:
//
//   - The newest key is the one with the greatest created_at INSIDE the file.
//     Not the filename, which is a convention an operator can typo, and not
//     mtime, which lies after any copy, rsync or restore.
//
//   - hz named a key and it is the newest one held: seal with it.
//
//   - hz named a different key: REFUSE. Sealing under the newest file on disk
//     would let anyone who can drop a file into the keystore — a shared dev
//     box, a malicious postinstall, an emailed key file — set created_at to
//     next year and become the sealing key for everything that developer pushes
//     afterwards. Note that the refusal is not "use hz's key instead": obeying
//     the pointer would let a compromised hz pin the fleet to a key it had
//     already stolen. Neither side is trusted to win, so disagreement is a stop.
//
//   - hz had no answer and exactly one key is held: seal with it. There is
//     nothing to arbitrate — a brand-new address has no pointer yet, and this
//     is what lets the first value at an address be pushed at all.
//
//   - hz had no answer and two or more keys are held: REFUSE, and say so. This
//     is precisely the drop-a-file case with the arbiter missing; created_at is
//     attacker-controlled and there is nothing to check it against.
//
// None of this touches the boot path. Boots open, opening never consults the
// pointer, and an absent pointer therefore cannot brick anything.
func (ks *Keystore) SealingKey(addr Addr, current CurrentKey) (EnvKey, KeyInfo, error) {
	if current.state == pointerNotConsulted {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w for %s: hz's current-key pointer was not consulted", ErrRefuseToSeal, addr)
	}

	keys, err := ks.load(addr)
	if err != nil {
		return EnvKey{}, KeyInfo{}, err
	}
	if len(keys) == 0 {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: nothing to seal with at %s", ErrNoSuchKey, addr)
	}

	if current.state == pointerUnavailable {
		if len(keys) > 1 {
			return EnvKey{}, KeyInfo{}, fmt.Errorf("%w for %s: %d keys are held and hz has no current-key pointer to arbitrate; created_at alone is whatever the newest file says", ErrRefuseToSeal, addr, len(keys))
		}
		return keys[0].key, keys[0].KeyInfo, nil
	}

	newest := keys[len(keys)-1]
	if len(keys) > 1 && keys[len(keys)-2].CreatedAt.Equal(newest.CreatedAt) {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w for %s: %s and %s share a created_at, so there is no newest key", ErrRefuseToSeal, addr, keys[len(keys)-2].ID, newest.ID)
	}
	if newest.ID != current.id {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w for %s: newest key held is %s, hz calls %s current", ErrRefuseToSeal, addr, newest.ID, current.id)
	}
	return newest.key, newest.KeyInfo, nil
}

// List reports the keys held for an address, oldest first, without their
// material.
//
// A file at the address that does not verify is an error, not a skipped entry:
// the directory holds key files and nothing else, so one that fails is either
// corruption or tampering and both have to be seen. Files that do not end in
// .key are ignored, since an editor swapfile is not a claim to be a key.
//
// Opening does not go through here. KeyFor loads exactly the file the envelope
// named, so a broken sibling cannot take a boot down — only sealing, which is a
// developer action with a human standing there, sees the whole directory.
func (ks *Keystore) List(addr Addr) ([]KeyInfo, error) {
	keys, err := ks.load(addr)
	if err != nil {
		return nil, err
	}
	out := make([]KeyInfo, len(keys))
	for i, k := range keys {
		out[i] = k.KeyInfo
	}
	return out, nil
}

// Put writes a key file, creating the tree as needed.
//
// It refuses to overwrite: a key file is write-once, and silently replacing one
// is how material gets swapped under a filename that still looks familiar.
// createdAt must be set, because it is the field current-key resolution turns
// on and a zero time would sort first or last depending on nothing.
func (ks *Keystore) Put(addr Addr, label string, k EnvKey, createdAt time.Time) error {
	segments, err := ks.addrSegments(addr)
	if err != nil {
		return err
	}
	if err := checkName("label", label); err != nil {
		return err
	}
	if createdAt.IsZero() {
		return fmt.Errorf("keystore: key %s has no created_at, which is the only thing current-key resolution can turn on", k.ID())
	}

	body, err := json.MarshalIndent(keyFile{
		Key:       k.Text(),
		CreatedAt: createdAt.UTC(),
		Label:     label,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("keystore: encode key file: %w", err)
	}
	body = append(body, '\n')

	dir, err := ks.openDir(segments, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	name := keyFileName(label, k.ID())
	fd, err := syscall.Openat(int(dir.Fd()), name,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("keystore: %s already exists; a key file is write-once", filepath.Join(ks.display(segments), name))
		}
		return fmt.Errorf("keystore: create %s: %w", filepath.Join(ks.display(segments), name), err)
	}
	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("keystore: write %s: %w", filepath.Join(ks.display(segments), name), err)
	}
	// A partial write leaves a truncated file, which readKeyFile rejects as
	// malformed JSON rather than accepting as a shorter key. Syncing anyway, so
	// a crash right after a successful Put does not lose the only copy.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("keystore: sync %s: %w", filepath.Join(ks.display(segments), name), err)
	}
	return f.Close()
}

// keyFile is what a key file holds.
//
// JSON, for three reasons. It is unambiguous and it cannot be truncated
// silently: an object needs its closing brace, so a short write or a clipped
// copy is a syntax error rather than a shorter key — which a length-prefixed or
// line-oriented format only achieves by hand-rolling the check. It is
// self-describing enough that an operator can read created_at and the label out
// of the file with cat, which matters because those are exactly the fields a
// human has to reason about during a rotation. And the decode is strict here:
// unknown fields are refused, trailing bytes are refused, and every field must
// be present, so a file written by a future version is an error rather than a
// key with a field quietly defaulted.
//
// The material rides as EnvKey.Text() rather than raw base64. That form carries
// its own checksum, so a corrupted key file fails in ParseEnvKey with a
// diagnosis instead of yielding 32 wrong bytes that decrypt to nothing and look
// like a wrong-key problem. It is also the one form EnvKey serializes into on
// purpose: the type deliberately implements neither MarshalText nor MarshalJSON
// so a key cannot be written out by accident, and honouring that here means
// spelling the conversion out.
//
// The key id is NOT a field. It is derived from the material on every load and
// compared against the filename, so there is exactly one place a key can claim
// an identity and exactly one place that claim is checked. A stored id would be
// a second place to lie.
type keyFile struct {
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	Label     string    `json:"label"`
}

// loadedKey is a KeyInfo with the material still attached.
type loadedKey struct {
	KeyInfo
	key EnvKey
}

// load reads every key file at an address, oldest first.
func (ks *Keystore) load(addr Addr) ([]loadedKey, error) {
	segments, err := ks.addrSegments(addr)
	if err != nil {
		return nil, err
	}

	dir, err := ks.openDir(segments, false)
	if errors.Is(err, ErrNoSuchKey) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("keystore: read %s: %w", ks.display(segments), err)
	}
	slices.Sort(names)

	var out []loadedKey
	for _, name := range names {
		if !strings.HasSuffix(name, keyFileSuffix) {
			continue
		}
		k, info, err := ks.loadKeyFile(dir, segments, name)
		if err != nil {
			return nil, err
		}
		out = append(out, loadedKey{KeyInfo: info, key: k})
	}
	slices.SortFunc(out, func(a, b loadedKey) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return out, nil
}

// loadKeyFile opens one key file relative to an already-open role directory and
// verifies everything about it.
func (ks *Keystore) loadKeyFile(dir *os.File, segments []string, name string) (EnvKey, KeyInfo, error) {
	shown := filepath.Join(ks.display(segments), name)

	// The filename's label is cosmetic and only validated; the file's label is
	// the one returned.
	_, id, err := parseKeyFileName(name)
	if err != nil {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s: %v", ErrMalformedKeyFile, shown, err)
	}

	// O_NOFOLLOW so a symlink is ELOOP rather than a redirect. O_NONBLOCK so a
	// fifo planted under a key's name returns a descriptor to be rejected
	// rather than blocking the client forever on an open.
	fd, err := syscall.Openat(int(dir.Fd()), name,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s is a symlink", ErrInsecureKey, shown)
		}
		if errors.Is(err, syscall.ENOENT) {
			return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s", ErrNoSuchKey, shown)
		}
		return EnvKey{}, KeyInfo{}, fmt.Errorf("keystore: open %s: %w", shown, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()

	if err := ks.checkFile(fd, shown); err != nil {
		return EnvKey{}, KeyInfo{}, err
	}

	kf, err := readKeyFile(f, shown)
	if err != nil {
		return EnvKey{}, KeyInfo{}, err
	}

	k, err := ParseEnvKey(kf.Key)
	if err != nil {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s: %v", ErrMalformedKeyFile, shown, err)
	}
	// The filename is a claim; the material is the fact. A renamed file fails
	// here rather than resolving to the wrong key.
	if got := k.ID(); got != id {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s holds key %s", ErrMalformedKeyFile, shown, got)
	}
	if err := checkName("label", kf.Label); err != nil {
		return EnvKey{}, KeyInfo{}, fmt.Errorf("%w: %s: %v", ErrMalformedKeyFile, shown, err)
	}
	return k, KeyInfo{ID: id, Label: kf.Label, CreatedAt: kf.CreatedAt}, nil
}

func readKeyFile(r io.Reader, shown string) (keyFile, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxKeyFileSize+1))
	if err != nil {
		return keyFile{}, fmt.Errorf("keystore: read %s: %w", shown, err)
	}
	if len(body) > maxKeyFileSize {
		return keyFile{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrMalformedKeyFile, shown, maxKeyFileSize)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var kf keyFile
	if err := dec.Decode(&kf); err != nil {
		return keyFile{}, fmt.Errorf("%w: %s: %v", ErrMalformedKeyFile, shown, err)
	}
	// A second JSON value after the first would mean two different key files
	// concatenated, and taking the first silently is how a swap goes unnoticed.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return keyFile{}, fmt.Errorf("%w: %s has trailing data after the key", ErrMalformedKeyFile, shown)
	}
	// Absent fields decode as zero values, which is the one way JSON can accept
	// a truncated meaning without a syntax error, so every field is required.
	switch {
	case kf.Key == "":
		return keyFile{}, fmt.Errorf("%w: %s has no key", ErrMalformedKeyFile, shown)
	case kf.Label == "":
		return keyFile{}, fmt.Errorf("%w: %s has no label", ErrMalformedKeyFile, shown)
	case kf.CreatedAt.IsZero():
		return keyFile{}, fmt.Errorf("%w: %s has no created_at", ErrMalformedKeyFile, shown)
	}
	return kf, nil
}

func keyFileName(label string, id KeyID) string {
	return label + "." + id.String() + keyFileSuffix
}

// parseKeyFileName splits <label>.<keyid>.key. The label charset forbids a dot,
// so the split is unambiguous.
func parseKeyFileName(name string) (string, KeyID, error) {
	base, ok := strings.CutSuffix(name, keyFileSuffix)
	if !ok {
		return "", KeyID{}, fmt.Errorf("name does not end in %s", keyFileSuffix)
	}
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 {
		return "", KeyID{}, errors.New("name is not <label>.<keyid>.key")
	}
	label, idText := base[:dot], base[dot+1:]
	if err := checkName("label", label); err != nil {
		return "", KeyID{}, err
	}
	id, err := ParseKeyID(idText)
	if err != nil {
		return "", KeyID{}, err
	}
	// ParseKeyID accepts either case and trims space; the filename must be the
	// canonical form or two names denote one key.
	if id.String() != idText {
		return "", KeyID{}, fmt.Errorf("key id %q is not canonically encoded", idText)
	}
	return label, id, nil
}

// addrSegments validates the three address fields that are path segments and
// returns the full segment list from the root. addr.Key is not one of them: the
// config key name is bound into the AEAD, not into the path, and it has its own
// charset (DB_PASSWORD is a legal key and an illegal directory name).
func (ks *Keystore) addrSegments(addr Addr) ([]string, error) {
	for _, f := range []struct{ what, value string }{
		{"environment", addr.Environment},
		{"app", addr.App},
		{"role", addr.Role},
	} {
		if err := checkName(f.what, f.value); err != nil {
			return nil, err
		}
	}
	segments := make([]string, 0, len(keyTreePrefix)+3)
	segments = append(segments, keyTreePrefix...)
	return append(segments, addr.Environment, addr.App, addr.Role), nil
}

func checkName(what, name string) error {
	if name == "" {
		return fmt.Errorf("%w: %s is empty", ErrBadName, what)
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("%w: %s is %d bytes, maximum is %d", ErrBadName, what, len(name), maxNameLen)
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("%w: %s %q must match %s", ErrBadName, what, name, validName)
	}
	return nil
}

// openDir walks segments from the root, one openat per component, and returns a
// descriptor on the last. Every descriptor along the way is fstat-ed before it
// is used as the base of the next openat, so a name is resolved exactly once
// and the thing checked is the thing opened.
//
// With create, missing components are made 0700. Without it, a missing
// component is ErrNoSuchKey — a client that was never approved for an address
// has no directory there, and that is not a failure.
//
// The caller closes the returned file.
func (ks *Keystore) openDir(segments []string, create bool) (*os.File, error) {
	fd, err := syscall.Open(ks.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) && create {
			if err := os.MkdirAll(ks.root, 0o700); err != nil {
				return nil, fmt.Errorf("keystore: create root %s: %w", ks.root, err)
			}
			fd, err = syscall.Open(ks.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		}
		if err != nil {
			if errors.Is(err, syscall.ENOENT) {
				return nil, fmt.Errorf("%w: keystore root %s does not exist", ErrNoSuchKey, ks.root)
			}
			if errors.Is(err, syscall.ELOOP) {
				return nil, fmt.Errorf("%w: keystore root %s is a symlink", ErrInsecureKey, ks.root)
			}
			return nil, fmt.Errorf("keystore: open root %s: %w", ks.root, err)
		}
	}
	if err := ks.checkDir(fd, ks.root); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}

	for i, name := range segments {
		shown := filepath.Join(append([]string{ks.root}, segments[:i+1]...)...)
		next, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.ENOENT) && create {
			if err := syscall.Mkdirat(fd, name, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
				_ = syscall.Close(fd)
				return nil, fmt.Errorf("keystore: create %s: %w", shown, err)
			}
			next, err = syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		}
		_ = syscall.Close(fd)
		if err != nil {
			switch {
			case errors.Is(err, syscall.ENOENT):
				return nil, fmt.Errorf("%w: %s does not exist", ErrNoSuchKey, shown)
			case errors.Is(err, syscall.ELOOP):
				return nil, fmt.Errorf("%w: %s is a symlink", ErrInsecureKey, shown)
			case errors.Is(err, syscall.ENOTDIR):
				return nil, fmt.Errorf("%w: %s is not a directory", ErrInsecureKey, shown)
			default:
				return nil, fmt.Errorf("keystore: open %s: %w", shown, err)
			}
		}
		fd = next
		if err := ks.checkDir(fd, shown); err != nil {
			_ = syscall.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), ks.display(segments)), nil
}

// checkDir refuses a directory anyone else can write or that somebody else
// owns. A 0777 directory means the key inside it can be unlinked and replaced
// whatever mode the key itself carries, so the file's own bits are not the
// whole answer.
//
// Group and other READ on a directory is tolerated: it discloses labels and key
// ids, which are public names, and not a byte of key material. Requiring 0700
// all the way up would refuse a perfectly ordinary /home/user at 0755.
func (ks *Keystore) checkDir(fd int, shown string) error {
	st, err := fstat(fd)
	if err != nil {
		return fmt.Errorf("keystore: fstat %s: %w", shown, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("%w: %s is not a directory", ErrInsecureKey, shown)
	}
	if int(st.Uid) != ks.uid {
		return fmt.Errorf("%w: %s is owned by uid %d, not %d", ErrInsecureKey, shown, st.Uid, ks.uid)
	}
	if st.Mode&0o022 != 0 {
		return fmt.Errorf("%w: %s is group- or world-writable (mode %04o), so the key inside it can be replaced", ErrInsecureKey, shown, st.Mode&0o7777)
	}
	return nil
}

// checkFile refuses a key file the way ssh refuses a private key: readable or
// writable by anyone but its owner, or owned by anyone but us. 0600 belonging
// to somebody else is still somebody else's file.
func (ks *Keystore) checkFile(fd int, shown string) error {
	st, err := fstat(fd)
	if err != nil {
		return fmt.Errorf("keystore: fstat %s: %w", shown, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("%w: %s is not a regular file", ErrInsecureKey, shown)
	}
	if int(st.Uid) != ks.uid {
		return fmt.Errorf("%w: %s is owned by uid %d, not %d", ErrInsecureKey, shown, st.Uid, ks.uid)
	}
	if st.Mode&0o077 != 0 {
		return fmt.Errorf("%w: %s is accessible to group or other (mode %04o), it must be 0600", ErrInsecureKey, shown, st.Mode&0o7777)
	}
	return nil
}

// fstat is the check that matters: it names the open descriptor, never a path,
// so nothing can be substituted between the check and the read.
func fstat(fd int) (syscall.Stat_t, error) {
	var st syscall.Stat_t
	err := syscall.Fstat(fd, &st)
	return st, err
}

// display renders a path for an error message only. It is never opened.
func (ks *Keystore) display(segments []string) string {
	return filepath.Join(append([]string{ks.root}, segments...)...)
}
