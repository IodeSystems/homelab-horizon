package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func siteTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func newSiteTestServer(t *testing.T, staticRoot string) *Server {
	t.Helper()
	s := &Server{}
	s.config.Store(&config.Config{
		Services: []config.Service{{
			Name:    "docs",
			Token:   "tok-123",
			Domains: []string{"docs.example.com"},
			Proxy:   &config.ProxyConfig{StaticRoot: staticRoot},
		}},
	})
	return s
}

func siteReq(method, path, token string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestSiteAPI_UploadRollbackReleases(t *testing.T) {
	live := filepath.Join(t.TempDir(), "site")
	s := newSiteTestServer(t, live)

	// Upload v1.
	rec := httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/upload", "tok-123", siteTarGz(t, map[string]string{"index.html": "v1"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload v1 = %d: %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(live, "index.html")); string(b) != "v1" {
		t.Fatalf("served = %q, want v1", b)
	}

	// Upload v2.
	rec = httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/upload", "tok-123", siteTarGz(t, map[string]string{"index.html": "v2"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload v2 = %d: %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(live, "index.html")); string(b) != "v2" {
		t.Fatalf("served = %q, want v2", b)
	}

	// Releases lists two.
	rec = httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodGet, "/api/site/releases", "tok-123", nil))
	var rels []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rels); err != nil {
		t.Fatalf("releases json: %v (%s)", err, rec.Body.String())
	}
	if len(rels) != 2 {
		t.Fatalf("releases = %d, want 2", len(rels))
	}

	// Rollback to v1.
	rec = httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/rollback", "tok-123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(live, "index.html")); string(b) != "v1" {
		t.Fatalf("after rollback served = %q, want v1", b)
	}
}

func TestSiteAPI_Validate_DryRun(t *testing.T) {
	live := filepath.Join(t.TempDir(), "site")
	s := newSiteTestServer(t, live)

	rec := httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/upload?validate=1", "tok-123", siteTarGz(t, map[string]string{"index.html": "x"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Lstat(live); !os.IsNotExist(err) {
		t.Error("dry-run validate created the live symlink")
	}
}

func TestSiteAPI_Auth(t *testing.T) {
	s := newSiteTestServer(t, filepath.Join(t.TempDir(), "site"))

	// Missing token.
	rec := httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodGet, "/api/site/releases", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}

	// Wrong token.
	rec = httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodGet, "/api/site/releases", "nope", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", rec.Code)
	}
}

func TestSiteAPI_RejectsNonStaticService(t *testing.T) {
	s := &Server{}
	s.config.Store(&config.Config{
		Services: []config.Service{{
			Name:    "api",
			Token:   "tok-123",
			Domains: []string{"api.example.com"},
			Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:9000"},
		}},
	})
	rec := httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/upload", "tok-123", siteTarGz(t, map[string]string{"index.html": "x"})))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-static service = %d, want 400", rec.Code)
	}
}
