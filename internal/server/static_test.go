package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestStaticServer_HostRouting(t *testing.T) {
	// Two separate roots, each claimed by a different host.
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "index.html"), []byte("site A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "index.html"), []byte("site B"), 0644); err != nil {
		t.Fatal(err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{
		Services: []config.Service{
			{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: rootA}},
			{Name: "b", Domains: []string{"b.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: rootB}},
			{Name: "proxied", Domains: []string{"p.example.com"}, Proxy: &config.ProxyConfig{Backend: "10.0.0.1:80"}},
		},
	})

	tests := []struct {
		name     string
		host     string
		wantCode int
		wantBody string
	}{
		{"host a serves root A", "a.example.com", http.StatusOK, "site A"},
		{"host b serves root B", "b.example.com", http.StatusOK, "site B"},
		{"host with port strips port", "a.example.com:443", http.StatusOK, "site A"},
		{"proxied host is not static", "p.example.com", http.StatusNotFound, ""},
		{"unknown host 404s", "nope.example.com", http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			ss.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestStaticServer_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{
		Services: []config.Service{
			{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/../../../../etc/passwd", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("traversal not rejected: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// A symlink inside the root pointing outside it must not be followed — the
// nastiest escape, since hz runs as root.
func TestStaticServer_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "leak")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/leak", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)

	if rec.Body.String() == "top secret" {
		t.Errorf("symlink escape leaked secret: code=%d", rec.Code)
	}
}

// Dotfiles (.env, .git/config, .ssh) must never be served.
func TestStaticServer_DeniesDotfiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]"), 0644); err != nil {
		t.Fatal(err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})

	for _, p := range []string{"/.env", "/.git/config"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "a.example.com"
		rec := httptest.NewRecorder()
		ss.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404 (body %q)", p, rec.Code, rec.Body.String())
		}
	}
}

// Directories are not listed: a dir without index.html 404s instead of leaking
// a file listing.
func TestStaticServer_NoDirectoryListing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "private-name.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("dir without index served: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-name.txt") {
		t.Errorf("directory listing leaked filenames: %q", rec.Body.String())
	}
}

// Rebuild must atomically replace the map: dropping a service removes its host.
func TestStaticServer_RebuildReplaces(t *testing.T) {
	root := t.TempDir()
	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})
	if _, ok := ss.siteFor("a.example.com"); !ok {
		t.Fatal("expected a.example.com after first rebuild")
	}
	ss.Rebuild(&config.Config{Services: nil})
	if _, ok := ss.siteFor("a.example.com"); ok {
		t.Error("expected a.example.com to be gone after rebuild with no services")
	}
}

func TestDeriveStaticSites(t *testing.T) {
	sites := deriveStaticSites(&config.Config{Services: []config.Service{
		{Name: "docs", Domains: []string{"docs.example.com", "www.example.com"},
			Proxy: &config.ProxyConfig{StaticRoot: "/srv/docs", SPA: true}},
		{Name: "api", Domains: []string{"api.example.com"},
			Proxy: &config.ProxyConfig{Backend: "127.0.0.1:9000"}},
	}})
	if len(sites) != 2 {
		t.Fatalf("want 2 static hosts, got %d: %+v", len(sites), sites)
	}
	if s := sites["docs.example.com"]; s.Root != "/srv/docs" || !s.SPA {
		t.Errorf("docs site = %+v, want {/srv/docs true}", s)
	}
	if _, ok := sites["api.example.com"]; ok {
		t.Error("proxied service must not appear as a static site")
	}
}

// consumeSites applies newline-delimited JSON maps from the parent; the last
// one wins (atomic replace), which is the child's update path.
func TestStaticServer_ConsumeSites(t *testing.T) {
	ss := newStaticServer()
	input := `{"a.example.com":{"root":"/srv/a"}}` + "\n" +
		`{"b.example.com":{"root":"/srv/b","spa":true}}` + "\n"
	ss.consumeSites(strings.NewReader(input))

	if _, ok := ss.siteFor("a.example.com"); ok {
		t.Error("a.example.com should be gone after the second map replaced it")
	}
	site, ok := ss.siteFor("b.example.com")
	if !ok || site.Root != "/srv/b" || !site.SPA {
		t.Errorf("b site = %+v ok=%v, want {/srv/b true}", site, ok)
	}
}

func TestStaticServer_SPAFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("APP"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("code"), 0644); err != nil {
		t.Fatal(err)
	}

	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root, SPA: true}},
	}})

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{"client route falls back to index", "/dashboard/settings", http.StatusOK, "APP"},
		{"existing asset served normally", "/app.js", http.StatusOK, "code"},
		{"missing asset still 404s (not index)", "/missing.js", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = "a.example.com"
			rec := httptest.NewRecorder()
			ss.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}

	// Without the SPA flag, an unknown client route 404s.
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-SPA unknown route: code = %d, want 404", rec.Code)
	}
}

// A site-provided 404.html is served (with a 404 status) for not-found paths;
// otherwise the built-in error page is used.
func TestStaticServer_Custom404(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "404.html"), []byte("custom not found"), 0644); err != nil {
		t.Fatal(err)
	}
	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if rec.Body.String() != "custom not found" {
		t.Errorf("body = %q, want custom 404.html content", rec.Body.String())
	}

	// A site without 404.html falls back to the built-in page.
	root2 := t.TempDir()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "b", Domains: []string{"b.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root2}},
	}})
	req = httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Host = "b.example.com"
	rec = httptest.NewRecorder()
	ss.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fallback code = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Not Found") {
		t.Errorf("built-in page missing status text: %q", rec.Body.String())
	}
}

func TestStaticServer_HardeningHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>hi</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})

	// nosniff present and explicit content-type by extension.
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Host = "a.example.com"
	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q, want nosniff", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("content-type = %q, want text/javascript", ct)
	}

	// Non-GET/HEAD is rejected.
	req = httptest.NewRequest(http.MethodPost, "/index.html", nil)
	req.Host = "a.example.com"
	rec = httptest.NewRecorder()
	ss.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST code = %d, want 405", rec.Code)
	}
}
