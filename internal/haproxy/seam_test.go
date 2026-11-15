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

// TestCertStoreReplacesTheRead is the item-12-step-3 proof: a manager whose
// cert facts come from somewhere other than the disk renders a complete HTTPS
// config for a directory it never opens.
//
// Before this, config generation called os.ReadDir on /etc/haproxy/certs, so an
// unprivileged hz web would have rendered every HTTPS gateway as plain HTTP —
// silently, because an unreadable cert directory and an empty one are the same
// answer.
func TestCertStoreReplacesTheRead(t *testing.T) {
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetBackends([]Backend{{Name: "wiki", DomainMatches: []string{"wiki.example.test"}, Server: "10.0.0.2:80"}})
	h.SetTLSMinVersion("TLSv1.2")
	h.SetCertStore(func(dir string) []Cert {
		return []Cert{{File: "example.test.pem", DNSNames: []string{"*.example.test", "example.test"}}}
	})

	cfg := h.GenerateConfig(80, 443, &SSLConfig{Enabled: true, CertDir: "/definitely/not/a/real/directory"})
	for _, want := range []string{
		"bind *:443 ssl crt /definitely/not/a/real/directory/",
		"acl ssl_host hdr(host) -i example.test",
		"acl ssl_host hdr_end(host) -i .example.test",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("want %q in rendered config:\n%s", want, cfg)
		}
	}

	// And nil restores the real read, which finds nothing there.
	h.SetCertStore(nil)
	if cfg := h.GenerateConfig(80, 443, &SSLConfig{Enabled: true, CertDir: "/definitely/not/a/real/directory"}); strings.Contains(cfg, "bind *:443") {
		t.Errorf("the default store must read the disk, and there is nothing there:\n%s", cfg)
	}
}

// TestTLSAssetsForTakesFactsNotFiles pins the pure half of the old
// certRedirectPatterns: SANs in, host matches out, including the filename
// fallback for a bundle whose leaf could not be parsed.
func TestTLSAssetsForTakesFactsNotFiles(t *testing.T) {
	if got := TLSAssetsFor("/certs", nil); got != nil {
		t.Errorf("no certs must mean no HTTPS frontend, got %+v", got)
	}

	got := TLSAssetsFor("/certs", []Cert{
		{File: "example.test.pem", DNSNames: []string{"*.Example.TEST", "example.test.", ""}},
		{File: "broken.example.test.pem"}, // unparseable: falls back to the filename
		{File: "dup.test.pem", DNSNames: []string{"dup.test", "dup.test"}},
	})
	if got == nil {
		t.Fatal("certs present must mean an HTTPS frontend")
	}
	if got.CertDir != "/certs" {
		t.Errorf("CertDir = %q", got.CertDir)
	}
	wantExact := []string{"broken.example.test", "dup.test", "example.test"}
	wantSuffix := []string{".broken.example.test", ".example.test"}
	if strings.Join(got.Exact, ",") != strings.Join(wantExact, ",") {
		t.Errorf("Exact = %v, want %v", got.Exact, wantExact)
	}
	if strings.Join(got.Suffix, ",") != strings.Join(wantSuffix, ",") {
		t.Errorf("Suffix = %v, want %v", got.Suffix, wantSuffix)
	}
}

// TestTLSWantedDecidesBeforeAnyRead states the order the split depends on: the
// question "should there be HTTPS at all" is answered from configuration, so a
// gateway with SSL off never consults a cert store.
func TestTLSWantedDecidesBeforeAnyRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		ssl  *SSLConfig
		want bool
	}{
		{"nil", nil, false},
		{"disabled", &SSLConfig{Enabled: false, CertDir: "/certs"}, false},
		{"no directory", &SSLConfig{Enabled: true}, false},
		{"on", &SSLConfig{Enabled: true, CertDir: "/certs"}, true},
	} {
		if got := TLSWanted(tc.ssl); got != tc.want {
			t.Errorf("%s: TLSWanted = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A store that panics proves the short-circuit is real, not incidental.
	h := New("/etc/haproxy/haproxy.cfg", "/run/haproxy/admin.sock")
	h.SetCertStore(func(string) []Cert { panic("the cert store must not be consulted when SSL is off") })
	_ = h.GenerateConfig(80, 443, nil)
	_ = h.GenerateConfig(80, 443, &SSLConfig{Enabled: false, CertDir: "/certs"})
	_ = h.GenerateConfig(80, 443, &SSLConfig{Enabled: true})
}

// TestRenderError503 pins the page as a rendered artifact. It is a constant
// with no secret and no machine-specific value in it, which is the whole
// argument for the agent owning its write once hz stops writing files.
func TestRenderError503(t *testing.T) {
	page := RenderError503()
	if !strings.HasPrefix(page, "HTTP/1.0 503 Service Unavailable\r\n") {
		t.Error("the file HAProxy reads must start with a status line")
	}
	if !strings.Contains(page, "\r\n\r\n") {
		t.Error("headers must be terminated by a blank line or HAProxy rejects the file")
	}
	if !strings.Contains(page, "Service temporarily unavailable") {
		t.Error("the page lost its message")
	}
	if Error503Path != "errors/503.http" {
		t.Errorf("Error503Path = %q — WriteConfig and the agent's file list must agree on it", Error503Path)
	}
}

// TestWriteConfigWritesThe503Page is the other half: today WriteConfig is the
// ONLY writer of that file anywhere in hz, which is why item 12 step 5 has to
// name a new owner before hz stops writing files.
func TestWriteConfigWritesThe503Page(t *testing.T) {
	dir := t.TempDir()
	h := New(filepath.Join(dir, "haproxy.cfg"), "/run/haproxy/admin.sock")
	h.SetTLSMinVersion("TLSv1.2")
	if err := h.WriteConfig(80, 443, nil); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, Error503Path))
	if err != nil {
		t.Fatalf("read 503 page: %v", err)
	}
	if string(got) != RenderError503() {
		t.Error("the written 503 page is not what RenderError503 returns")
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
