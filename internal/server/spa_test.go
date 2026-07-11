package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	homelabUI "github.com/iodesystems/homelab-horizon/ui"
)

// TestSPACacheHeaders guards the post-deploy staleness fix: the app shell
// (index.html, and the SPA fallback for unknown routes) must be revalidated on
// every load, while content-hashed assets are cached immutably. If index.html
// were cacheable, a deploy that changes the bundle hash would leave clients on a
// stale shell pointing at chunks that 404, white-screening the app.
func TestSPACacheHeaders(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).setupSPA(mux)

	// Use a real content-hashed asset discovered from the embedded build rather
	// than a hardcoded Vite hash — the hash changes on every UI rebuild, so a
	// literal filename here goes stale and the request falls through to the SPA
	// shell (this test's original flakiness).
	asset := firstHashedAsset(t)

	cases := []struct {
		name   string
		path   string
		wantCC string
	}{
		{"index", "/app/", "no-cache"},
		{"spa-fallback", "/app/services", "no-cache"},
		{"hashed-asset", "/app/assets/" + asset, "public, max-age=31536000, immutable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200", tc.path, rec.Code)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != tc.wantCC {
				t.Errorf("%s: Cache-Control = %q, want %q", tc.path, cc, tc.wantCC)
			}
		})
	}
}

// firstHashedAsset returns the name of a real .js/.css file under the embedded
// dist/assets, skipping the test when the UI hasn't been built (no assets to
// serve).
func firstHashedAsset(t *testing.T) string {
	t.Helper()
	distFS, err := fs.Sub(homelabUI.DistFS, "dist")
	if err != nil {
		t.Skipf("no embedded dist: %v", err)
	}
	entries, err := fs.ReadDir(distFS, "assets")
	if err != nil {
		t.Skipf("no embedded dist/assets (UI not built): %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".js") || strings.HasSuffix(e.Name(), ".css")) {
			return e.Name()
		}
	}
	t.Skip("no hashed asset in embedded dist/assets")
	return ""
}
