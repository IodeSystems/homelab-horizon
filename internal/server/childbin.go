package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The parent's store of SERVER binaries for nested hz children.
//
// hz cannot embed itself, so the binary a fresh child installs is uploaded
// beside the parent's own deploy (bin/deploy, `make child-bins-upload`):
//
//	<data dir>/child-bin/homelab-horizon-<os>-<arch>     the binary
//	<data dir>/child-bin/<os>-<arch>.json                {"version": ..., "sha256": ...}
//
// The manifest is written by whoever uploaded the binary, from the file it
// built. The store re-hashes the file and refuses one whose digest does not
// match its manifest — a half-copied upload must read as missing, not be
// handed to a box that would then fail its own sha256 check halfway through a
// bootstrap.

// ChildPlatforms are the platforms a nested hz is bootstrapped on.
var ChildPlatforms = []string{"linux-amd64", "linux-arm64"}

const childBinPrefix = "homelab-horizon-"

var childPlatformRe = regexp.MustCompile(`^linux-(amd64|arm64)$`)

// childBin is one stored binary, verified.
type childBin struct {
	Platform string
	Path     string
	Version  string
	SHA256   string
	Size     int64
}

type childBinManifest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// childBinStore reads the directory. It caches the digest per (path, size,
// mtime) so a 30MB re-hash happens once per upload, not once per request.
type childBinStore struct {
	Dir string

	mu    sync.Mutex
	cache map[string]childBinDigest
}

type childBinDigest struct {
	size  int64
	mtime time.Time
	sum   string
}

func newChildBinStore(dir string) *childBinStore {
	return &childBinStore{Dir: dir, cache: map[string]childBinDigest{}}
}

// Get returns the platform's binary, or an error that is the sentence to show.
func (s *childBinStore) Get(platform string) (childBin, error) {
	if s == nil || s.Dir == "" {
		return childBin{}, errors.New("this hz has no child-binary store (dry run, or no data directory)")
	}
	if !childPlatformRe.MatchString(platform) {
		return childBin{}, fmt.Errorf("no child binary for %q — a nested hz is bootstrapped on %s", platform, strings.Join(ChildPlatforms, " or "))
	}
	path := filepath.Join(s.Dir, childBinPrefix+platform)
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return childBin{}, fmt.Errorf("no %s server binary is uploaded to %s — run `make child-bins-upload HOST=<this hz>` (or bin/deploy) from the hz repo", platform, s.Dir)
	}
	if err != nil {
		return childBin{}, err
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, platform+".json"))
	if err != nil {
		return childBin{}, fmt.Errorf("the %s binary in %s has no readable manifest (%s.json: %v) — re-upload it", platform, s.Dir, platform, err)
	}
	var m childBinManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return childBin{}, fmt.Errorf("the %s manifest in %s does not parse: %v — re-upload it", platform, s.Dir, err)
	}
	m.SHA256 = strings.ToLower(strings.TrimSpace(m.SHA256))
	sum, err := s.digest(path, fi)
	if err != nil {
		return childBin{}, err
	}
	if m.SHA256 != sum {
		return childBin{}, fmt.Errorf("the %s binary in %s does not match its manifest (file %s, manifest %q) — a partial upload; re-upload it", platform, s.Dir, sum[:12], m.SHA256)
	}
	return childBin{Platform: platform, Path: path, Version: strings.TrimSpace(m.Version), SHA256: sum, Size: fi.Size()}, nil
}

// Available is every supported platform that Get answers, sorted, and the
// supported ones it does not.
func (s *childBinStore) Available() (have, missing []string) {
	for _, p := range ChildPlatforms {
		if _, err := s.Get(p); err == nil {
			have = append(have, p)
		} else {
			missing = append(missing, p)
		}
	}
	sort.Strings(have)
	return have, missing
}

func (s *childBinStore) digest(path string, fi os.FileInfo) (string, error) {
	s.mu.Lock()
	if d, ok := s.cache[path]; ok && d.size == fi.Size() && d.mtime.Equal(fi.ModTime()) {
		s.mu.Unlock()
		return d.sum, nil
	}
	s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	s.cache[path] = childBinDigest{size: fi.Size(), mtime: fi.ModTime(), sum: sum}
	s.mu.Unlock()
	return sum, nil
}
