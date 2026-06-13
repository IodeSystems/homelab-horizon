// Package sitedeploy manages atomic releases of a static site directory served
// by hz. A site's served path (static_root) is an hz-managed symlink pointing
// into a sibling releases directory; deploying extracts an uploaded tar.gz into
// a fresh release and atomically repoints the symlink. Because the static file
// server resolves the symlink per request, the swap is zero-downtime, and old
// releases are retained for rollback.
//
// Uploads are untrusted input handled by the root process, so extraction is
// deliberately strict: path traversal, absolute paths, and non-regular entries
// (symlinks, devices) are rejected, and total size / file count are capped.
package sitedeploy

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Limits bound a single upload to guard against resource exhaustion.
type Limits struct {
	MaxBytes int64 // total uncompressed bytes across all files
	MaxFiles int   // total regular files
}

// DefaultLimits is a generous-but-finite cap for a static site.
var DefaultLimits = Limits{MaxBytes: 512 << 20, MaxFiles: 20000}

// Manager handles releases for one static site.
type Manager struct {
	live     string // the served symlink path (== static_root)
	releases string // sibling directory holding release dirs
	keep     int    // releases to retain (including current)
	uid, gid int    // chown extracted files to this owner; <0 disables
}

// Result summarizes a deploy.
type Result struct {
	Release string `json:"release"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Swapped bool   `json:"swapped"`
}

// Release describes a retained release.
type Release struct {
	ID      string `json:"id"`
	Current bool   `json:"current"`
}

// New returns a Manager for the given served path. keep is the number of
// releases to retain (minimum 1). uid/gid, when >= 0, are applied to every
// extracted file so an unprivileged file-server process can read them; pass -1
// to skip chown (e.g. when not running as root).
func New(staticRoot string, keep, uid, gid int) *Manager {
	if keep < 1 {
		keep = 1
	}
	clean := filepath.Clean(staticRoot)
	return &Manager{
		live:     clean,
		releases: clean + "-releases",
		keep:     keep,
		uid:      uid,
		gid:      gid,
	}
}

// Deploy extracts the tar.gz from r into a new release identified by id. Unless
// dryRun, it then atomically swaps the live symlink to the new release and
// prunes old ones. On any failure the partial release is removed.
func (m *Manager) Deploy(r io.Reader, id string, dryRun bool, lim Limits) (*Result, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.releases, 0755); err != nil {
		return nil, fmt.Errorf("create releases dir: %w", err)
	}

	dest := filepath.Join(m.releases, id)
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("release %q already exists", id)
	}
	tmp := dest + ".incoming"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	files, bytes, err := extractTarGz(r, tmp, lim)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if m.uid >= 0 && m.gid >= 0 {
		if err := chownTree(tmp, m.uid, m.gid); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("chown release: %w", err)
		}
	}

	if dryRun {
		_ = os.RemoveAll(tmp)
		return &Result{Release: id, Files: files, Bytes: bytes, Swapped: false}, nil
	}

	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("finalize release: %w", err)
	}
	if err := m.swap(dest); err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	m.prune()
	return &Result{Release: id, Files: files, Bytes: bytes, Swapped: true}, nil
}

// swap atomically points the live symlink at relDir.
func (m *Manager) swap(relDir string) error {
	// Use a target relative to the symlink's directory so the tree is
	// relocatable.
	target, err := filepath.Rel(filepath.Dir(m.live), relDir)
	if err != nil {
		target = relDir
	}

	info, err := os.Lstat(m.live)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		// Existing symlink: atomic replace via temp symlink + rename.
		tmpLink := m.live + ".swap"
		_ = os.Remove(tmpLink)
		if err := os.Symlink(target, tmpLink); err != nil {
			return fmt.Errorf("stage symlink: %w", err)
		}
		if err := os.Rename(tmpLink, m.live); err != nil {
			_ = os.Remove(tmpLink)
			return fmt.Errorf("swap symlink: %w", err)
		}
	case err == nil && info.IsDir():
		// A real directory occupies the served path. Adopt it only if empty;
		// otherwise refuse rather than destroy operator data.
		if entries, derr := os.ReadDir(m.live); derr != nil || len(entries) > 0 {
			return fmt.Errorf("static_root %q is a non-empty directory; point it at a path hz manages for atomic deploys", m.live)
		}
		if err := os.Remove(m.live); err != nil {
			return fmt.Errorf("clear empty static_root: %w", err)
		}
		if err := os.Symlink(target, m.live); err != nil {
			return fmt.Errorf("create symlink: %w", err)
		}
	case os.IsNotExist(err):
		if err := os.Symlink(target, m.live); err != nil {
			return fmt.Errorf("create symlink: %w", err)
		}
	default:
		if err != nil {
			return fmt.Errorf("stat static_root: %w", err)
		}
		return fmt.Errorf("static_root %q is not a directory or symlink", m.live)
	}
	return nil
}

// Rollback repoints the live symlink to the most recent release that is not the
// current one. Returns the release it rolled back to.
func (m *Manager) Rollback() (string, error) {
	rels, err := m.Releases()
	if err != nil {
		return "", err
	}
	for _, r := range rels { // Releases is newest-first
		if !r.Current {
			if err := m.swap(filepath.Join(m.releases, r.ID)); err != nil {
				return "", err
			}
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no previous release to roll back to")
}

// Releases lists retained releases, newest first, marking the current one.
func (m *Manager) Releases() ([]Release, error) {
	entries, err := os.ReadDir(m.releases)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cur := m.currentID()
	var ids []string
	for _, e := range entries {
		if e.IsDir() && validateID(e.Name()) == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids))) // IDs are sortable timestamps
	out := make([]Release, 0, len(ids))
	for _, id := range ids {
		out = append(out, Release{ID: id, Current: id == cur})
	}
	return out, nil
}

// currentID returns the release the live symlink points at, or "".
func (m *Manager) currentID() string {
	target, err := os.Readlink(m.live)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// prune removes releases beyond the keep count, never removing the current one.
func (m *Manager) prune() {
	rels, err := m.Releases()
	if err != nil {
		return
	}
	kept := 0
	for _, r := range rels {
		kept++
		if kept <= m.keep || r.Current {
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.releases, r.ID))
	}
}

// extractTarGz extracts a gzip-compressed tar from r into dest, enforcing
// limits and rejecting unsafe entries. Returns the file count and total bytes.
func extractTarGz(r io.Reader, dest string, lim Limits) (int, int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var files int
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("tar: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		if name == "." || name == "/" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return 0, 0, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(dest, name)
		// Defense in depth: the joined path must stay under dest.
		if rel, err := filepath.Rel(dest, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return 0, 0, fmt.Errorf("path escapes destination: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return 0, 0, err
			}
		case tar.TypeReg:
			files++
			if files > lim.MaxFiles {
				return 0, 0, fmt.Errorf("archive exceeds %d files", lim.MaxFiles)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return 0, 0, err
			}
			n, err := writeFileLimited(target, tr, lim.MaxBytes-total)
			total += n
			if err != nil {
				return 0, 0, err
			}
		default:
			// Reject symlinks, hardlinks, devices, fifos: a static site is
			// regular files and directories only.
			return 0, 0, fmt.Errorf("unsupported entry %q (only files and directories allowed)", hdr.Name)
		}
	}
	return files, total, nil
}

// writeFileLimited copies at most budget bytes from r into a new file at path,
// erroring if the source is larger. Files are written 0644 so an unprivileged
// reader can serve them.
func writeFileLimited(path string, r io.Reader, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("archive exceeds size limit")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	// Copy budget+1 so an exactly-budget file passes but a larger one trips.
	n, err := io.CopyN(f, r, budget+1)
	if err != nil && err != io.EOF {
		if n > budget {
			return n, fmt.Errorf("archive exceeds size limit")
		}
		return n, err
	}
	if n > budget {
		return n, fmt.Errorf("archive exceeds size limit")
	}
	return n, nil
}

// chownTree recursively sets ownership of root and everything under it.
func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

// validateID guards the release identifier used as a directory name.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("empty release id")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return fmt.Errorf("invalid release id %q", id)
		}
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("invalid release id %q", id)
	}
	return nil
}
