package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

func versionMW(t *testing.T) http.Handler {
	t.Helper()
	s := &Server{}
	reached := false
	h := s.apiVersionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { _ = reached })
	return h
}

func callWith(t *testing.T, path string, version string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if version != "" {
		r.Header.Set(hzapi.HeaderVersion, version)
	}
	w := httptest.NewRecorder()
	versionMW(t).ServeHTTP(w, r)
	return w
}

func TestAPIVersionMiddlewareAcceptsTheCurrentVersion(t *testing.T) {
	w := callWith(t, "/api/v1/anything", strconv.Itoa(hzapi.Version))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// hz-client and every consumer built before this existed send no header.
// Refusing them would have broken the fleet the moment this shipped.
func TestAPIVersionMiddlewareServesUnversionedClients(t *testing.T) {
	if !hzapi.UnversionedOK {
		t.Skip("UnversionedOK is false; legacy clients are deliberately refused now")
	}
	w := callWith(t, "/api/v1/anything", "")
	if w.Code != http.StatusOK {
		t.Fatalf("an unversioned request got %d; every existing consumer sends no header", w.Code)
	}
}

func TestAPIVersionMiddlewareRefusesAnImpossibleVersion(t *testing.T) {
	w := callWith(t, "/api/v1/anything", strconv.Itoa(hzapi.Version+1))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	// The refusal must name both sides, or an operator is reading source to
	// find out what happened.
	body := w.Body.String()
	if !contains(body, strconv.Itoa(hzapi.Version+1)) || !contains(body, strconv.Itoa(hzapi.Version)) {
		t.Errorf("refusal %q does not name both versions", body)
	}
}

// A header present but unparseable is a client bug, not a legacy client, and
// must not be served as one.
func TestAPIVersionMiddlewareRefusesAMalformedHeader(t *testing.T) {
	w := callWith(t, "/api/v1/anything", "v2.1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed version got %d, want 400 — it was served as legacy", w.Code)
	}
}

// Every response carries the server's range, including refusals, so a client
// that has just been turned away can report what turned it away.
func TestAPIVersionMiddlewareAdvertisesOnRefusals(t *testing.T) {
	for _, v := range []string{strconv.Itoa(hzapi.Version), strconv.Itoa(hzapi.Version + 1), ""} {
		w := callWith(t, "/api/v1/anything", v)
		if w.Header().Get(hzapi.HeaderVersion) == "" || w.Header().Get(hzapi.HeaderMin) == "" {
			t.Errorf("version %q: response carries %v, want both numbers", v, w.Header())
		}
	}
}

// Only /api/ is checked. The admin UI ships in the same binary as the server it
// calls, so it cannot skew, and the downloadable client script has no contract.
func TestAPIVersionMiddlewareIgnoresNonAPIPaths(t *testing.T) {
	for _, p := range []string{"/admin/haproxy/hz-client", "/assets/index.js", "/"} {
		w := callWith(t, p, strconv.Itoa(hzapi.Version+99))
		if w.Code != http.StatusOK {
			t.Errorf("%s got %d; a non-API path was version-checked", p, w.Code)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
