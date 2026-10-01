// Package artifact is hz's content-addressed artifact store: a build bundle
// kept as <dir>/<sha256>, mode 0600, written by streaming to a temp file while
// hashing and renamed into place only when the hash is the one claimed.
//
// THE I/O HALF. store.go touches the filesystem; retention.go decides, purely,
// what to delete (seam_test.go).
//
// The parent hz stores what a staging deploy uploads under
// <data dir>/artifacts; a nested hz stores what it pulled under
// <data dir>/upstream/artifacts. The same code, two directories: the child's
// pruning must never reach a file the parent side was given.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrHashMismatch: the bytes do not hash to the sha they were sent as.
var ErrHashMismatch = errors.New("artifact sha256 mismatch")

// ErrTooLarge: the body exceeded the cap.
var ErrTooLarge = errors.New("artifact exceeds the size cap")

// Store is one artifact directory.
type Store struct {
	Dir string
}

// Path is where an artifact's file lives. sha must already be a normalised
// 64-hex digest; Path does not check, Valid does.
func (s Store) Path(sha string) string { return filepath.Join(s.Dir, sha) }

// Valid reports a 64-character lowercase hex digest — the only spelling a
// path built from a request may take.
func Valid(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Has reports whether the file is present.
func (s Store) Has(sha string) (bool, error) {
	if !Valid(sha) {
		return false, fmt.Errorf("invalid artifact sha256 %q", sha)
	}
	_, err := os.Stat(s.Path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Put streams r into the store under want, hashing as it goes. At most max
// bytes are read; one more is ErrTooLarge. A body that hashes to anything but
// want is ErrHashMismatch, and in both cases the temp file is removed and
// nothing is stored. On success the file is fsynced, 0600, and renamed into
// place atomically — a reader never sees half an artifact.
func (s Store) Put(r io.Reader, want string, max int64) (size int64, err error) {
	if !Valid(want) {
		return 0, fmt.Errorf("invalid artifact sha256 %q", want)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return 0, fmt.Errorf("create artifact dir: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return 0, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, max+1))
	if err != nil {
		return n, fmt.Errorf("read body: %w", err)
	}
	if n > max {
		return n, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return n, fmt.Errorf("%w: the body hashes to %s, not %s", ErrHashMismatch, got, want)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return n, err
	}
	if err := tmp.Sync(); err != nil {
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	if err := os.Rename(tmpName, s.Path(want)); err != nil {
		return n, fmt.Errorf("store artifact: %w", err)
	}
	done = true
	return n, nil
}

// Open opens a stored artifact for reading, with its size. A missing file is
// an error wrapping os.ErrNotExist.
func (s Store) Open(sha string) (*os.File, int64, error) {
	if !Valid(sha) {
		return nil, 0, fmt.Errorf("invalid artifact sha256 %q", sha)
	}
	f, err := os.Open(s.Path(sha))
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// Remove deletes a stored artifact's file. Already gone is not an error.
func (s Store) Remove(sha string) error {
	if !Valid(sha) {
		return fmt.Errorf("invalid artifact sha256 %q", sha)
	}
	err := os.Remove(s.Path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List is every stored sha (temp files and anything not a digest skipped).
func (s Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && Valid(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
