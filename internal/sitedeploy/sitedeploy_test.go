package sitedeploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// tgz builds a gzip-compressed tar from name->content. A content of "" with a
// trailing slash name is a directory entry.
func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tgzRaw builds an archive from explicit headers (for malicious entries).
func tgzRaw(t *testing.T, hdrs []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if i < len(bodies) {
			_, _ = tw.Write([]byte(bodies[i]))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func served(t *testing.T, live, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(live, rel))
	if err != nil {
		t.Fatalf("read %s via live symlink: %v", rel, err)
	}
	return string(b)
}

func TestDeploy_AtomicSwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "site")
	m := New(live, 5, -1, -1)

	if _, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"index.html": "v1"})), "20260101T000001Z", false, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	if got := served(t, live, "index.html"); got != "v1" {
		t.Fatalf("after deploy 1 = %q, want v1", got)
	}

	if _, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"index.html": "v2"})), "20260101T000002Z", false, DefaultLimits); err != nil {
		t.Fatal(err)
	}
	if got := served(t, live, "index.html"); got != "v2" {
		t.Fatalf("after deploy 2 = %q, want v2", got)
	}

	// live must be a symlink (atomic swap target), not a real dir.
	if info, _ := os.Lstat(live); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("live path is not a symlink")
	}

	rels, err := m.Releases()
	if err != nil || len(rels) != 2 {
		t.Fatalf("releases = %v (err %v), want 2", rels, err)
	}
	if !rels[0].Current || rels[0].ID != "20260101T000002Z" {
		t.Errorf("newest should be current: %+v", rels[0])
	}

	rolled, err := m.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if rolled != "20260101T000001Z" {
		t.Errorf("rolled to %q, want the v1 release", rolled)
	}
	if got := served(t, live, "index.html"); got != "v1" {
		t.Fatalf("after rollback = %q, want v1", got)
	}
}

func TestDeploy_Prune(t *testing.T) {
	dir := t.TempDir()
	m := New(filepath.Join(dir, "site"), 2, -1, -1)
	ids := []string{"20260101T000001Z", "20260101T000002Z", "20260101T000003Z"}
	for _, id := range ids {
		if _, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"index.html": id})), id, false, DefaultLimits); err != nil {
			t.Fatal(err)
		}
	}
	rels, _ := m.Releases()
	if len(rels) != 2 {
		t.Fatalf("kept %d releases, want 2 (pruned)", len(rels))
	}
	// The oldest must be gone, newest retained and current.
	if _, err := os.Stat(filepath.Join(dir, "site-releases", "20260101T000001Z")); !os.IsNotExist(err) {
		t.Error("oldest release was not pruned")
	}
}

func TestDeploy_DryRunDoesNotSwap(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "site")
	m := New(live, 5, -1, -1)
	res, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"index.html": "x"})), "20260101T000001Z", true, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if res.Swapped {
		t.Error("dry run reported a swap")
	}
	if _, err := os.Lstat(live); !os.IsNotExist(err) {
		t.Error("dry run created the live symlink")
	}
	if rels, _ := m.Releases(); len(rels) != 0 {
		t.Errorf("dry run left %d releases, want 0", len(rels))
	}
}

func TestDeploy_RejectsUnsafeArchives(t *testing.T) {
	cases := []struct {
		name   string
		hdrs   []*tar.Header
		bodies []string
	}{
		{"traversal", []*tar.Header{{Name: "../escape.txt", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}}, []string{"x"}},
		{"absolute", []*tar.Header{{Name: "/etc/evil", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}}, []string{"x"}},
		{"nested traversal", []*tar.Header{{Name: "a/../../escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}}, []string{"x"}},
		{"symlink entry", []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, nil},
		{"hardlink entry", []*tar.Header{{Name: "hl", Typeflag: tar.TypeLink, Linkname: "index.html"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m := New(filepath.Join(dir, "site"), 5, -1, -1)
			_, err := m.Deploy(bytes.NewReader(tgzRaw(t, tc.hdrs, tc.bodies)), "20260101T000001Z", false, DefaultLimits)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if _, lerr := os.Lstat(filepath.Join(dir, "site")); !os.IsNotExist(lerr) {
				t.Error("rejected upload still swapped live")
			}
		})
	}
}

func TestDeploy_EnforcesLimits(t *testing.T) {
	dir := t.TempDir()
	m := New(filepath.Join(dir, "site"), 5, -1, -1)

	// Size limit.
	_, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"big": "0123456789"})), "20260101T000001Z", false, Limits{MaxBytes: 5, MaxFiles: 10})
	if err == nil {
		t.Error("expected size-limit rejection")
	}

	// File-count limit.
	_, err = m.Deploy(bytes.NewReader(tgz(t, map[string]string{"a": "x", "b": "x", "c": "x"})), "20260101T000002Z", false, Limits{MaxBytes: 1 << 20, MaxFiles: 2})
	if err == nil {
		t.Error("expected file-count rejection")
	}
}

func TestDeploy_RefusesNonEmptyRealDir(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "site")
	if err := os.MkdirAll(live, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "existing"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	m := New(live, 5, -1, -1)
	if _, err := m.Deploy(bytes.NewReader(tgz(t, map[string]string{"index.html": "v1"})), "20260101T000001Z", false, DefaultLimits); err == nil {
		t.Error("expected refusal to clobber a non-empty real directory")
	}
}
