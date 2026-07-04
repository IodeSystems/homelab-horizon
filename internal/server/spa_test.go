package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSPACacheHeaders guards the post-deploy staleness fix: the app shell
// (index.html, and the SPA fallback for unknown routes) must be revalidated on
// every load, while content-hashed assets are cached immutably. If index.html
// were cacheable, a deploy that changes the bundle hash would leave clients on a
// stale shell pointing at chunks that 404, white-screening the app.
func TestSPACacheHeaders(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).setupSPA(mux)

	cases := []struct {
		name   string
		path   string
		wantCC string
	}{
		{"index", "/app/", "no-cache"},
		{"spa-fallback", "/app/services", "no-cache"},
		{"hashed-asset", "/app/assets/index-B_yT2LCx.js", "public, max-age=31536000, immutable"},
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
