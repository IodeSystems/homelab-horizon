package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	// A secret outside the served root that traversal must not reach.
	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "secret.txt"), []byte("top secret"), 0644); err != nil {
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

	if rec.Body.String() == "top secret" || rec.Code == http.StatusOK && rec.Body.Len() > 0 && rec.Body.String() != "ok" {
		// http.FileServer cleans the path and confines it to root; we should
		// never see content from outside it.
		t.Errorf("traversal leaked content: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// Rebuild must atomically replace the map: dropping a service removes its host.
func TestStaticServer_RebuildReplaces(t *testing.T) {
	root := t.TempDir()
	ss := newStaticServer()
	ss.Rebuild(&config.Config{Services: []config.Service{
		{Name: "a", Domains: []string{"a.example.com"}, Proxy: &config.ProxyConfig{StaticRoot: root}},
	}})
	if _, ok := ss.rootFor("a.example.com"); !ok {
		t.Fatal("expected a.example.com after first rebuild")
	}
	ss.Rebuild(&config.Config{Services: nil})
	if _, ok := ss.rootFor("a.example.com"); ok {
		t.Error("expected a.example.com to be gone after rebuild with no services")
	}
}
