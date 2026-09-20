package haproxy

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderStaysPure is the guard on the seam itself: render.go computes
// desired state and must not reach for the machine, so that half can run
// unprivileged (and off-box) once hz-agent owns the privileged half.
//
// An import list is a coarse check, but it is the one that catches the way this
// actually rots — someone adds an os.ReadFile to the renderer because the value
// was easier to read than to pass in.
func TestRenderStaysPure(t *testing.T) {
	banned := map[string]string{
		"os":            "reads or writes the filesystem",
		"os/exec":       "runs commands",
		"net":           "talks to the network",
		"net/http":      "talks to the network",
		"time":          "reads the clock",
		"math/rand":     "is not deterministic",
		"crypto/rand":   "is not deterministic",
		"io/ioutil":     "reads or writes the filesystem",
		"path/filepath": "resolves paths against a real filesystem",
	}

	f, err := parser.ParseFile(token.NewFileSet(), "render.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if why, bad := banned[path]; bad {
			t.Errorf("render.go imports %q, which %s — the pure half takes its inputs as arguments", path, why)
		}
	}
}

// TestRenderConfigNeedsNoMachine renders an HTTPS config naming a certificate
// directory that does not exist. The renderer never looks, because reading the
// cert store is the apply half's job (loadTLSAssets) and its result arrives as
// an argument.
func TestRenderConfigNeedsNoMachine(t *testing.T) {
	cfg := RenderConfig(ConfigInput{
		HTTPPort:  80,
		HTTPSPort: 443,
		Backends:  []Backend{{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "10.0.0.2:80"}},
		TLS: &TLSAssets{
			CertDir: "/definitely/not/a/real/directory/",
			Exact:   []string{"wiki.example.com"},
			Suffix:  []string{".office.example.com"},
		},
	})
	for _, want := range []string{
		"bind *:443 ssl crt /definitely/not/a/real/directory/",
		"acl ssl_host hdr(host) -i wiki.example.com",
		"acl ssl_host hdr_end(host) -i .office.example.com",
		"ssl-min-ver " + DefaultTLSMinVersion,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("want %q in rendered config:\n%s", want, cfg)
		}
	}
}

// TestWriteFileIfChangedSkipsIdenticalContents pins the reload discipline where
// it is now stated once. Every writer built on this inherits it; a writer that
// reports changed for identical bytes reloads HAProxy for nothing.
func TestWriteFileIfChangedSkipsIdenticalContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "file.txt")

	changed, err := writeFileIfChanged(path, []byte("one\n"), 0644)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !changed {
		t.Error("first write should report changed")
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	changed, err = writeFileIfChanged(path, []byte("one\n"), 0644)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if changed {
		t.Error("identical contents must not report changed")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("identical contents must not touch the file at all")
	}

	changed, err = writeFileIfChanged(path, []byte("two\n"), 0644)
	if err != nil {
		t.Fatalf("changed write: %v", err)
	}
	if !changed {
		t.Error("different contents must report changed")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "two\n" {
		t.Errorf("want new contents, got %q", body)
	}
}
