//go:build unix

package sitedeploy

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// WHAT MAKES DROPPING THE CHOWN SAFE, checked rather than assumed.
//
// The static file server is meant to run as somebody other than the process
// that extracted the upload. Nothing chowns the tree to that user any more, so
// the ONLY thing that lets it read a release is the mode — and a mode that
// came from the ambient umask is not a contract. This deploys under a hostile
// umask and asserts every file and directory is still world-readable.
//
// Positive control: drop the explicit Chmod calls and this goes red at the
// first directory.
func TestAReleaseIsWorldReadableWhateverTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := t.TempDir()
	live := filepath.Join(dir, "site")
	m := New(live, 5)

	archive := bytes.NewReader(tgz(t, map[string]string{
		"index.html":           "<h1>home</h1>",
		"assets/app.js":        "console.log(1)",
		"assets/deep/more.css": "body{}",
	}))
	if _, err := m.Deploy(archive, "20260925T000000.000000Z", false, DefaultLimits); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	checked := 0
	err := filepath.WalkDir(m.releases, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		checked++
		mode := info.Mode().Perm()
		if d.IsDir() {
			if mode&0o055 != 0o055 {
				t.Errorf("directory %s is %o: an unprivileged file server cannot traverse or list it", p, mode)
			}
			return nil
		}
		if mode&0o044 != 0o044 {
			t.Errorf("file %s is %o: an unprivileged file server cannot read it", p, mode)
		}
		// And it must NOT be writable by anyone but the owner. The chown this
		// replaced handed the file server write access to its own site.
		if mode&0o022 != 0 {
			t.Errorf("file %s is %o: group- or world-writable", p, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked < 5 {
		t.Fatalf("walked %d entries; the tree is too small for this to have proved anything", checked)
	}
}

// OWNERSHIP STAYS WITH THE EXTRACTOR. The file server reads a release; it does
// not own one. A chown back to the serving user would make every 0644 file in
// the site writable by the process serving it.
func TestAReleaseIsNotHandedToAnotherOwner(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "site")
	m := New(live, 5)

	archive := bytes.NewReader(tgz(t, map[string]string{"index.html": "hi"}))
	if _, err := m.Deploy(archive, "20260925T000001.000000Z", false, DefaultLimits); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	info, err := os.Stat(filepath.Join(m.releases, "20260925T000001.000000Z", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no stat_t on this platform")
	}
	if int(st.Uid) != os.Getuid() {
		t.Fatalf("release owned by uid %d, not the extracting process's %d", st.Uid, os.Getuid())
	}
}
