package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/server/uiembed"
)

// spaServer returns a Server whose UI lives in a temp dir, plus the dir.
func spaServer(t *testing.T, files map[string]string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	s := &Server{}
	s.config.Store(&config.Config{UIDir: dir})
	return s, dir
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.setupSPA(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestSPAServesFromDisk(t *testing.T) {
	s, _ := spaServer(t, map[string]string{
		"index.html":           "<title>hz</title>",
		"assets/app-abc123.js": "console.log(1)",
	})

	if w := get(s, "/app/"); w.Code != 200 || !strings.Contains(w.Body.String(), "hz") {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	}
	if w := get(s, "/app/assets/app-abc123.js"); w.Code != 200 {
		t.Fatalf("asset: %d", w.Code)
	}
}

// The cache rules are load-bearing: a cached index.html after a deploy points
// at hashed chunks that no longer exist, and the app white-screens.
func TestSPACacheHeaders(t *testing.T) {
	s, _ := spaServer(t, map[string]string{
		"index.html":           "<title>hz</title>",
		"assets/app-abc123.js": "console.log(1)",
	})

	if got := get(s, "/app/assets/app-abc123.js").Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("hashed asset Cache-Control = %q, want immutable", got)
	}
	if got := get(s, "/app/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", got)
	}
	// A client-side route falls back to index.html, which must also revalidate.
	if got := get(s, "/app/settings").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("SPA fallback Cache-Control = %q, want no-cache", got)
	}
}

// Client-side routes must serve the shell with the URL preserved, or TanStack
// Router cannot pick up the route.
func TestSPAFallbackForUnknownPaths(t *testing.T) {
	s, _ := spaServer(t, map[string]string{"index.html": "SHELL"})
	w := get(s, "/app/dns/some-zone")
	if w.Code != 200 || w.Body.String() != "SHELL" {
		t.Fatalf("fallback: %d %q", w.Code, w.Body.String())
	}
}

// The embedded FS could not be walked out of; a directory on disk can. This is
// the vulnerability un-embedding introduces if the path is trusted.
func TestSPARefusesPathTraversal(t *testing.T) {
	s, dir := spaServer(t, map[string]string{"index.html": "SHELL"})
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOKEN"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, path := range []string{
		"/app/../secret.txt",
		"/app/..%2fsecret.txt",
		"/app/assets/../../secret.txt",
	} {
		w := get(s, path)
		if strings.Contains(w.Body.String(), "TOKEN") {
			t.Errorf("%s escaped the UI directory", path)
		}
	}
}

// Reachable only for a build with no UI compiled in and none on disk — which
// is what `go build ./...` produces. It lands on the login page, so it has to
// explain itself rather than 404.
func TestSPAExplainsAMissingUI(t *testing.T) {
	if _, embedded := uiembed.FS(); embedded {
		// Not a gap in coverage: with the UI compiled in, resolution always
		// succeeds and this page cannot be reached. Asserting it here would
		// require breaking the embed to prove a case that build does not have.
		t.Skip("this build has the UI compiled in; the missing-UI page is unreachable")
	}

	s := &Server{}
	s.config.Store(&config.Config{UIDir: filepath.Join(t.TempDir(), "absent")})

	w := get(s, "/app/")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"not installed", "make", "STATIC_DIR", "API"} {
		if !strings.Contains(body, want) {
			t.Errorf("the explanation should mention %q: %s", want, body)
		}
	}
}

// STATIC_DIR wins, because that is how a slot deploy points hz at its payload.
func TestSPAPrefersStaticDirEnv(t *testing.T) {
	s, _ := spaServer(t, map[string]string{"index.html": "CONFIG"})

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "index.html"), []byte("ENV"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("STATIC_DIR", other)

	if got := get(s, "/app/").Body.String(); got != "ENV" {
		t.Fatalf("served %q, want the STATIC_DIR copy", got)
	}
}
