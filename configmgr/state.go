package configmgr

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// The client state tree: everything a managed box keeps on disk so that a boot
// needs neither hz nor a human.
//
//	<state>/machine.key                       one keypair per machine
//	<state>/machine.id                        hz's identity for this box
//	<state>/<env>/<app>/<role>/config.key     the unwrapped environment key
//	<state>/<env>/<app>/<role>/last-known-good
//
// # The address IS the path
//
// Nothing in a cache file says which address it belongs to, because nothing
// needs to: the directory it sits in is the address. A box restarted with a
// different --env looks in a directory that does not exist, so it cannot boot
// another environment's cache. That is a structural property of the layout, not
// a check somebody has to remember to write.
//
// The cache holds SEALED envelopes, never plaintext, which gives the same
// guarantee a second time and cryptographically: a cache file copied into
// another address's directory still fails the AEAD, because the address the
// opener supplies is the one it asked for. Copying the file is not enough; the
// attacker would need the other address's key, and if they had that they did
// not need the file.
//
// # machine.id sits beside machine.key, and is written first
//
// The machine id is half the authenticated address of every machine-scoped
// secret, so it is state, not a field to re-read out of each answer — an
// address a later response could change authenticates nothing. It is written
// before config.key, so the presence of a config.key implies the id is already
// there.
//
// # Segments are validated, never interpolated
//
// Every directory segment is an address field an operator chose, so each one
// passes checkName (keystore.go's validName charset) before it is used. ".." is
// unrepresentable in that charset rather than merely rejected.
//
// # No freshness anywhere
//
// Nothing here records an expiry, a TTL or a last-checked time, and nothing
// reads one. A three-year-old cache is valid config. See plan/config-manager.md,
// "Constraint: nothing in the boot path may depend on freshness".

const (
	machineKeyName = "machine.key"
	machineIDName  = "machine.id"
	envKeyName     = "config.key"
	cacheName      = "last-known-good"
)

// maxStateFileSize caps the machine key, the machine id and the environment
// key. All three are a couple of hundred bytes.
const maxStateFileSize = 64 << 10

// maxCacheFileSize caps the last-known-good file. A config is key names and
// base64 envelopes; a megabyte is far past any real one and far short of a file
// worth reading into memory by accident.
const maxCacheFileSize = 4 << 20

// maxMachineIDLen bounds hz's identifier for the box. It is authenticated data,
// not a path segment, so its charset is hz's business — but its length is ours.
const maxMachineIDLen = 256

// Errors a caller is expected to branch on.
var (
	// ErrNotEnrolled means this address holds no environment key: the box has
	// never been approved here, or it was approved for a different address.
	// It is not a failure — it is the state every box starts in.
	ErrNotEnrolled = errors.New("no environment key for this address")

	// ErrNoCache means this address has no last-known-good config. On a box
	// that has booted once, it cannot happen; on one that never has, there is
	// genuinely nothing to fall back to.
	ErrNoCache = errors.New("no last-known-good config for this address")

	// ErrBadState means a file in the tree is not what this build can read.
	ErrBadState = errors.New("malformed client state")
)

// State is the tree on one box, rooted at an absolute path.
//
// Absolute, for the same reason the keystore's root is: a cwd-relative state
// directory means a repository that ships a hostile one gets consulted by
// whoever runs a binary inside it.
type State struct {
	root string
}

// OpenState roots a state tree, creating it if it is not there. The root is
// 0700: everything below it is either a key or a config, and neither is
// anyone else's business.
func OpenState(root string) (*State, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: no state directory given", ErrBadState)
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: state directory %q is not absolute", ErrBadState, root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}
	return &State{root: filepath.Clean(root)}, nil
}

// Root is the directory the tree lives in, for error messages.
func (s *State) Root() string { return s.root }

// MachineKey loads the box's private key. It does not create one; see
// EnsureMachineKey.
func (s *State) MachineKey() (*ecdh.PrivateKey, error) {
	b, err := s.readSecret(filepath.Join(s.root, machineKeyName))
	if err != nil {
		return nil, err
	}
	priv, err := ParseMachinePrivateKey(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", machineKeyName, err)
	}
	return priv, nil
}

// EnsureMachineKey returns the box's private key, generating and persisting one
// the first time. The private half is written 0600 and never leaves: the only
// thing that goes on the wire is MarshalMachinePublicKey of its public half.
//
// One keypair per machine, not per registration. A box running two roles shares
// it, so either role can unwrap anything granted to that machine — separate
// users with separate state trees, or the separation is advisory. See
// plan/config-manager.md, "How a client knows its own address".
func (s *State) EnsureMachineKey() (*ecdh.PrivateKey, error) {
	priv, err := s.MachineKey()
	if err == nil {
		return priv, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	priv, err = NewMachineKey()
	if err != nil {
		return nil, fmt.Errorf("generating a machine key: %w", err)
	}
	if err := s.writeSecret(filepath.Join(s.root, machineKeyName), []byte(MarshalMachinePrivateKey(priv)+"\n")); err != nil {
		return nil, err
	}
	return priv, nil
}

// MachineID is hz's identity for this box, learned once at approval.
func (s *State) MachineID() (string, error) {
	b, err := s.readSecret(filepath.Join(s.root, machineIDName))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if err := checkMachineID(id); err != nil {
		return "", err
	}
	return id, nil
}

// PutMachineID persists hz's identity for this box. It is written once at the
// first approval; a later approval naming the same id is a no-op, and one
// naming a DIFFERENT id is refused.
//
// Refused, rather than overwritten, because the id is authenticated data: every
// machine-scoped secret on this box opens at MachineAddr{Machine: this id}.
// Letting a later answer move it would make the binding meaningless, which is
// the whole reason the id is persisted instead of read out of each response.
func (s *State) PutMachineID(id string) error {
	if err := checkMachineID(id); err != nil {
		return err
	}
	switch held, err := s.MachineID(); {
	case err == nil && held == id:
		return nil
	case err == nil:
		return fmt.Errorf("%w: hz names machine %q, this box is already %q", ErrBadState, id, held)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	return s.writeSecret(filepath.Join(s.root, machineIDName), []byte(id+"\n"))
}

// EnvKey loads the unwrapped environment key for one registration, or
// ErrNotEnrolled if this box has never been approved at that address.
func (s *State) EnvKey(addr EnvKeyAddr) (EnvKey, error) {
	dir, err := s.addrDir(addr)
	if err != nil {
		return EnvKey{}, err
	}
	b, err := s.readSecret(filepath.Join(dir, envKeyName))
	if errors.Is(err, os.ErrNotExist) {
		return EnvKey{}, fmt.Errorf("%w: %s", ErrNotEnrolled, addr)
	}
	if err != nil {
		return EnvKey{}, err
	}
	// Stored as EnvKey.Text() rather than raw base64: that form carries its own
	// checksum, so a corrupted file fails in ParseEnvKey with a diagnosis
	// instead of yielding 32 wrong bytes that look like a wrong-key problem.
	k, err := ParseEnvKey(strings.TrimSpace(string(b)))
	if err != nil {
		return EnvKey{}, fmt.Errorf("%s at %s: %w", envKeyName, addr, err)
	}
	return k, nil
}

// PutEnvKey persists the key recovered from an approval. It refuses to replace
// a different key already held at the address: a registration is approved once,
// and silently swapping the key under a path that still looks familiar is how
// a box ends up unable to open its own cache.
func (s *State) PutEnvKey(addr EnvKeyAddr, k EnvKey) error {
	switch held, err := s.EnvKey(addr); {
	case err == nil && held.ID() == k.ID():
		return nil
	case err == nil:
		return fmt.Errorf("%w: %s already holds key %s, refusing to replace it with %s", ErrBadState, addr, held.ID(), k.ID())
	case !errors.Is(err, ErrNotEnrolled):
		return err
	}
	dir, err := s.addrDir(addr)
	if err != nil {
		return err
	}
	return s.writeSecret(filepath.Join(dir, envKeyName), []byte(k.Text()+"\n"))
}

// cacheFile is the last-known-good record: the winning response exactly as hz
// served it, still sealed, plus the sequence floors.
//
// The floors ride in the same file because they are written at the same moment
// — a config is applied and its sequence is recorded together or not at all —
// and because keeping the layout to the three documented paths is worth more
// than a fourth file that can disagree with this one.
//
// Version is the binary version the cached config was resolved FOR. It is
// recorded so a boot that differs can say so; it is not a check. Refusing a
// cache written by another version would brick a legitimate binary rollback on
// a box that cannot reach hz, which is the failure the whole design forbids.
type cacheFile struct {
	Version  string         `json:"version"`
	Response ConfigResponse `json:"response"`

	// Floors is version → the highest sequence ever applied at that version.
	// Keyed by version so rolling a binary back still legitimately selects a
	// lower sequence, and holding no clock so it survives a box that has been
	// off for three years. See ConfigResponse.Sequence.
	Floors map[string]int64 `json:"floors"`
}

// cache reads the last-known-good record for one address. It is unexported
// because cacheFile is: the record is an implementation detail of the boot
// path, and a caller that wants the floors has Floors.
func (s *State) cache(addr EnvKeyAddr) (*cacheFile, error) {
	dir, err := s.addrDir(addr)
	if err != nil {
		return nil, err
	}
	b, err := s.readFile(filepath.Join(dir, cacheName), maxCacheFileSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoCache, addr)
	}
	if err != nil {
		return nil, err
	}
	// Strict: unknown fields and trailing bytes are refused, so a file written
	// by a future version is an error rather than a config with a field
	// quietly defaulted. A quietly defaulted field is the bug class this
	// project exists to kill.
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var cf cacheFile
	if err := dec.Decode(&cf); err != nil {
		return nil, fmt.Errorf("%w: %s at %s: %v", ErrBadState, cacheName, addr, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: %s at %s: trailing bytes", ErrBadState, cacheName, addr)
	}
	if cf.Floors == nil {
		cf.Floors = map[string]int64{}
	}
	return &cf, nil
}

// Floors reads just the sequence floors, treating a missing or unreadable cache
// as no floors at all.
//
// Unreadable is deliberately not fatal here: the floor is a defence against hz
// serving an older blessed config, and a box whose cache file is corrupt still
// has to boot. It loses the floor, which is exactly as much as it should lose.
func (s *State) Floors(addr EnvKeyAddr) map[string]int64 {
	cf, err := s.cache(addr)
	if err != nil {
		return map[string]int64{}
	}
	return cf.Floors
}

// PutCache writes the last-known-good record, raising the floor for version to
// the applied sequence. The write is atomic: a temp file in the same directory,
// fsynced, then renamed, so an interrupted boot leaves the previous cache
// intact rather than a truncated one.
func (s *State) PutCache(addr EnvKeyAddr, version string, resp ConfigResponse, floors map[string]int64) error {
	dir, err := s.addrDir(addr)
	if err != nil {
		return err
	}
	next := make(map[string]int64, len(floors)+1)
	for v, seq := range floors {
		next[v] = seq
	}
	if seq, ok := next[version]; !ok || resp.Sequence > seq {
		next[version] = resp.Sequence
	}
	b, err := json.MarshalIndent(cacheFile{Version: version, Response: resp, Floors: next}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", cacheName, err)
	}
	return s.writeSecret(filepath.Join(dir, cacheName), append(b, '\n'))
}

// Addresses lists every address this tree holds an environment key for, sorted.
//
// It exists so a box can say, loudly, that it holds state for an address other
// than the one it was just started with. Fail-closed on a mistyped --env is
// still a bad ten minutes if nobody can see why.
func (s *State) Addresses() []EnvKeyAddr {
	var out []EnvKeyAddr
	envs, _ := os.ReadDir(s.root)
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		apps, _ := os.ReadDir(filepath.Join(s.root, env.Name()))
		for _, app := range apps {
			if !app.IsDir() {
				continue
			}
			roles, _ := os.ReadDir(filepath.Join(s.root, env.Name(), app.Name()))
			for _, role := range roles {
				if !role.IsDir() {
					continue
				}
				if _, err := os.Lstat(filepath.Join(s.root, env.Name(), app.Name(), role.Name(), envKeyName)); err != nil {
					continue
				}
				out = append(out, EnvKeyAddr{Environment: env.Name(), App: app.Name(), Role: role.Name()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// addrDir is the only place an address becomes a path. Every segment passes
// checkName first, so no address field can walk out of the tree — "." and ".."
// are not merely rejected by that charset, they are unrepresentable in it.
func (s *State) addrDir(addr EnvKeyAddr) (string, error) {
	if err := checkName("environment", addr.Environment); err != nil {
		return "", err
	}
	if err := checkName("app", addr.App); err != nil {
		return "", err
	}
	// An empty role is not a default: it breaks the path even though the AAD
	// would encode an empty field happily. The unprefixed default maps to a
	// NAMED default role.
	if err := checkName("role", addr.Role); err != nil {
		return "", err
	}
	return filepath.Join(s.root, addr.Environment, addr.App, addr.Role), nil
}

// readSecret reads a file that must not be readable by anyone else.
func (s *State) readSecret(path string) ([]byte, error) {
	return s.readFile(path, maxStateFileSize)
}

// readFile opens with O_NOFOLLOW — a symlinked component is an error rather
// than a redirect to a file that passes every other check — and refuses
// anything group- or world-accessible, the way ssh refuses a private key.
//
// The mode check is an fstat on the descriptor that is already open, not a stat
// on the path: stat-then-open is two resolutions of the same name, and on a
// shared box the gap between them is the whole attack.
func (s *State) readFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInsecureKey, path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s is mode %#o, want 0600", ErrInsecureKey, path, st.Mode().Perm())
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrBadState, path, limit)
	}
	return b, nil
}

// writeSecret writes 0600, atomically. The temp file is created in the target
// directory so the rename cannot cross a filesystem, and it is fsynced before
// the rename so a power cut leaves either the old file or the new one.
func (s *State) writeSecret(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func checkMachineID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: hz named no machine id", ErrBadState)
	}
	if len(id) > maxMachineIDLen {
		return fmt.Errorf("%w: machine id is %d bytes, maximum is %d", ErrBadState, len(id), maxMachineIDLen)
	}
	if strings.ContainsAny(id, "\x00\n\r") {
		return fmt.Errorf("%w: machine id contains a control character", ErrBadState)
	}
	return nil
}
