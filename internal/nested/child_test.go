package nested

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The child against an httptest parent that speaks the wire shapes. The real
// parent is internal/server's; the end-to-end rehearsal there runs the two
// together. This one isolates the child so each failure is one cause.

const parentToken = "hzi_test-instance-token"

type fakeParent struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	desired  *apitypes.DesiredResp
	etag     string
	blobs    map[string][]byte // what /artifacts/{sha} serves, by the sha asked
	reports  []apitypes.DeployReportReq
	failPOST bool
	calls    []string // "METHOD path"
}

func newFakeParent(t *testing.T) *fakeParent {
	p := &fakeParent{t: t, blobs: map[string][]byte{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeParent) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+parentToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/deploys/desired":
		if p.desired == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "nothing applied to redline/prod"})
			return
		}
		w.Header().Set("ETag", p.etag)
		if r.Header.Get("If-None-Match") == p.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(p.desired)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/artifacts/"):
		b, ok := p.blobs[strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/deploys/report":
		if p.failPOST {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var rep apitypes.DeployReportReq
		_ = json.NewDecoder(r.Body).Decode(&rep)
		p.reports = append(p.reports, rep)
		_ = json.NewEncoder(w).Encode(apitypes.DeployReportResultResp{Recorded: true, ID: int64(len(p.reports))})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// apply sets what the parent says is applied; body is the artifact's bytes.
func (p *fakeParent) apply(id int64, version, body string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	sum := sha256.Sum256([]byte(body))
	sha := hex.EncodeToString(sum[:])
	p.desired = &apitypes.DesiredResp{Version: version, ArtifactSHA256: sha, PromotionID: id, ApplyID: id, AppliedBy: "user:carl", AppliedAt: "2026-10-01T00:00:00Z"}
	p.etag = `"apply-` + version + `"`
	p.blobs[sha] = []byte(body)
	return sha
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

var prodRung = config.UpstreamRung{Project: "redline", Environment: "prod"}

func newTestChild(t *testing.T, url, dataDir string) (*Child, *logBuf) {
	t.Helper()
	tokenFile := filepath.Join(dataDir, "upstream.token")
	if _, err := os.Stat(tokenFile); err != nil {
		if err := os.WriteFile(tokenFile, []byte(parentToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := New(config.UpstreamHZ{URL: url, TokenFile: tokenFile, Rungs: []config.UpstreamRung{prodRung}}, dataDir)
	logs := &logBuf{}
	c.Log = slog.New(slog.NewTextHandler(logs, nil))
	return c, logs
}

func ctx() context.Context { return context.Background() }

func TestChildPullsVerifiesCachesAndServes(t *testing.T) {
	p := newFakeParent(t)
	dir := t.TempDir()
	c, _ := newTestChild(t, p.srv.URL, dir)

	if r := c.Answer(prodRung, ""); r.Status != 503 || !strings.Contains(r.Error, "never reached the parent hz") {
		t.Fatalf("before any poll: %+v", r)
	}
	c.PollOnce(ctx())
	if r := c.Answer(prodRung, ""); r.Status != 404 || !strings.Contains(r.Error, "nothing applied") {
		t.Fatalf("parent has nothing applied: %+v, want 404 (the parent SAID so)", r)
	}

	sha := p.apply(1, "1.4.0", "bundle 1.4.0")
	c.PollOnce(ctx())
	r := c.Answer(prodRung, "")
	if r.Status != 200 || r.Desired == nil || r.Desired.ArtifactSHA256 != sha || r.Upstream != "" {
		t.Fatalf("after an apply: %+v", r)
	}
	if r304 := c.Answer(prodRung, r.ETag); r304.Status != 304 {
		t.Fatalf("If-None-Match with the cached tag: %d", r304.Status)
	}
	st, err := os.Stat(c.CachePath(prodRung))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v %v", st, err)
	}
	if got := c.ArtifactRungs(sha); len(got) != 1 {
		t.Fatalf("ArtifactRungs = %v", got)
	}
	f, _, err := c.Store.Open(sha)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// A second poll is a 304: nothing downloaded again.
	before := len(p.calls)
	c.PollOnce(ctx())
	if got := p.calls[before:]; len(got) != 1 || got[0] != "GET /api/v1/deploys/desired" {
		t.Fatalf("an in-sync poll made %v", got)
	}

	// A new apply replaces the cache; the previous artifact is kept for a
	// rollback, and the one before that is pruned.
	sha2 := p.apply(2, "1.5.0", "bundle 1.5.0")
	c.PollOnce(ctx())
	sha3 := p.apply(3, "1.6.0", "bundle 1.6.0")
	c.PollOnce(ctx())
	cache, _ := c.LoadCache(prodRung)
	if cache.Desired.ArtifactSHA256 != sha3 || cache.PreviousSHA256 != sha2 {
		t.Fatalf("cache = %+v", cache)
	}
	if has, _ := c.Store.Has(sha); has {
		t.Error("the artifact two applies back was not pruned")
	}
	if has, _ := c.Store.Has(sha2); !has {
		t.Error("the previous artifact (a rollback target) was pruned")
	}
}

// A body that does not hash to the sha the parent named is not stored, not
// cached, not served, and is LOUD.
func TestChildRefusesABadArtifact(t *testing.T) {
	p := newFakeParent(t)
	c, logs := newTestChild(t, p.srv.URL, t.TempDir())
	good := p.apply(1, "1.4.0", "bundle 1.4.0")
	c.PollOnce(ctx())

	p.mu.Lock()
	p.desired = &apitypes.DesiredResp{Version: "1.5.0", ArtifactSHA256: strings.Repeat("e", 64), ApplyID: 2, PromotionID: 2}
	p.etag = `"apply-1.5.0"`
	p.blobs[strings.Repeat("e", 64)] = []byte("NOT the bytes that hash to eee…")
	p.mu.Unlock()
	c.PollOnce(ctx())

	r := c.Answer(prodRung, "")
	if r.Status != 200 || r.Desired.ArtifactSHA256 != good {
		t.Fatalf("after a bad artifact the child serves %+v; want the previous verified apply", r)
	}
	if has, _ := c.Store.Has(strings.Repeat("e", 64)); has {
		t.Fatal("a mismatched artifact was stored")
	}
	if len(c.ArtifactRungs(strings.Repeat("e", 64))) != 0 {
		t.Fatal("a mismatched artifact is served")
	}
	if !strings.Contains(logs.String(), "LOUD: nested hz did NOT cache") || !strings.Contains(logs.String(), "mismatch") {
		t.Fatalf("not loud:\n%s", logs.String())
	}

	// No cache at all and a bad first artifact: nothing is served, 503.
	p2 := newFakeParent(t)
	c2, _ := newTestChild(t, p2.srv.URL, t.TempDir())
	p2.mu.Lock()
	p2.desired = &apitypes.DesiredResp{Version: "1.5.0", ArtifactSHA256: strings.Repeat("e", 64), ApplyID: 2}
	p2.etag = `"x"`
	p2.blobs[strings.Repeat("e", 64)] = []byte("wrong")
	p2.mu.Unlock()
	c2.PollOnce(ctx())
	if r := c2.Answer(prodRung, ""); r.Status != 503 {
		t.Fatalf("no verified cache: %+v", r)
	}
}

func TestChildServesItsCacheWhenTheParentIsDown(t *testing.T) {
	p := newFakeParent(t)
	dir := t.TempDir()
	c, logs := newTestChild(t, p.srv.URL, dir)
	sha := p.apply(1, "1.4.0", "bundle 1.4.0")
	c.PollOnce(ctx())

	p.srv.Close() // the parent is gone
	c.PollOnce(ctx())
	r := c.Answer(prodRung, "")
	if r.Status != 200 || r.Desired.ArtifactSHA256 != sha {
		t.Fatalf("parent down with a cache: %+v", r)
	}
	if !strings.HasPrefix(r.Upstream, "unreachable since ") {
		t.Fatalf("X-HZ-Upstream = %q", r.Upstream)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimPrefix(r.Upstream, "unreachable since ")); err != nil {
		t.Fatalf("the header's time is not RFC3339: %q", r.Upstream)
	}
	if !strings.Contains(logs.String(), "LOUD: parent hz unreachable") {
		t.Fatalf("not loud:\n%s", logs.String())
	}

	// No cache and the parent down: 503, never 404.
	c2, _ := newTestChild(t, "http://127.0.0.1:1", t.TempDir())
	c2.PollOnce(ctx())
	r = c2.Answer(prodRung, "")
	if r.Status != 503 || !strings.Contains(r.Error, "never reached the parent hz") {
		t.Fatalf("parent down, no cache: %+v", r)
	}
}

// CLAUDE.md #5, the house test: a cache three years old, read by a fresh
// process with the parent unreachable, still answers — and the artifact it
// names is still served.
func TestCacheNeverGoesStale(t *testing.T) {
	p := newFakeParent(t)
	dir := t.TempDir()
	c, _ := newTestChild(t, p.srv.URL, dir)
	sha := p.apply(1, "1.4.0", "bundle 1.4.0")
	c.PollOnce(ctx())

	long := time.Now().Add(-3 * 365 * 24 * time.Hour)
	for _, path := range []string{c.CachePath(prodRung), c.Store.Path(sha)} {
		if err := os.Chtimes(path, long, long); err != nil {
			t.Fatal(err)
		}
	}
	p.srv.Close()

	fresh, _ := newTestChild(t, p.srv.URL, dir)
	fresh.PollOnce(ctx())
	r := fresh.Answer(prodRung, "")
	if r.Status != 200 || r.Desired == nil || r.Desired.ArtifactSHA256 != sha {
		t.Fatalf("a three-year-old cache is valid desired state: %+v", r)
	}
	if len(fresh.ArtifactRungs(sha)) != 1 {
		t.Fatal("the three-year-old cache's artifact is not served")
	}
}

func TestReportForwardingQueuesAndDrains(t *testing.T) {
	p := newFakeParent(t)
	c, logs := newTestChild(t, p.srv.URL, t.TempDir())
	rep := apitypes.DeployReportReq{Project: "redline", Environment: "prod", App: "redline", Version: "1.4.0",
		ArtifactSHA256: strings.Repeat("a", 64), Host: "prod-1", ForwardedFor: "service:redline-prod"}

	p.failPOST = true
	if err := c.Enqueue(rep); err != nil {
		t.Fatal(err)
	}
	rep2 := rep
	rep2.Version = "1.4.1"
	if err := c.Enqueue(rep2); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainOnce(ctx()); err == nil {
		t.Fatal("a drain against a failing parent reported success")
	}
	if n, _ := c.QueueDepth(); n != 2 {
		t.Fatalf("queued = %d after a failed drain, want 2", n)
	}
	if !strings.Contains(logs.String(), "LOUD: nested hz could not forward") {
		t.Fatalf("not loud:\n%s", logs.String())
	}

	p.mu.Lock()
	p.failPOST = false
	p.mu.Unlock()
	if err := c.DrainOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.QueueDepth(); n != 0 {
		t.Fatalf("queued = %d after recovery", n)
	}
	if len(p.reports) != 2 || p.reports[0].Version != "1.4.0" || p.reports[1].Version != "1.4.1" || p.reports[0].ForwardedFor != "service:redline-prod" {
		t.Fatalf("parent received %+v", p.reports)
	}
	// Drained once is drained: a further drain sends nothing.
	if err := c.DrainOnce(ctx()); err != nil || len(p.reports) != 2 {
		t.Fatalf("a second drain re-sent: %d, %v", len(p.reports), err)
	}
}

// Everything the child sends the parent, enumerated: GET desired, GET an
// artifact, POST a report. Nothing else — and the fake parent records every
// request, so an empty list would fail the positive control first.
func TestTheChildOnlyGetsDesiredAndArtifactsAndPostsReports(t *testing.T) {
	p := newFakeParent(t)
	c, _ := newTestChild(t, p.srv.URL, t.TempDir())
	p.apply(1, "1.4.0", "bundle")
	c.PollOnce(ctx())
	_ = c.Enqueue(apitypes.DeployReportReq{Project: "redline", Environment: "prod", Version: "1.4.0", ArtifactSHA256: strings.Repeat("a", 64)})
	_ = c.DrainOnce(ctx())
	if len(p.calls) < 3 {
		t.Fatalf("instrument: the parent recorded %v", p.calls)
	}
	for _, call := range p.calls {
		switch {
		case call == "GET /api/v1/deploys/desired",
			strings.HasPrefix(call, "GET /api/v1/artifacts/"),
			call == "POST /api/v1/deploys/report":
		default:
			t.Errorf("the child sent %q", call)
		}
	}
}

func TestChildRefusesARedirect(t *testing.T) {
	target := newFakeParent(t)
	redirect := httptest.NewServer(http.RedirectHandler(target.srv.URL+"/api/v1/deploys/desired", http.StatusMovedPermanently))
	t.Cleanup(redirect.Close)
	c, logs := newTestChild(t, redirect.URL, t.TempDir())
	c.PollOnce(ctx())
	if len(target.calls) != 0 {
		t.Fatalf("the child followed a redirect: %v", target.calls)
	}
	if !strings.Contains(logs.String(), "redirected") {
		t.Fatalf("the refusal does not say why:\n%s", logs.String())
	}
}

func TestUnreadableTokenIsLoudNotFatal(t *testing.T) {
	p := newFakeParent(t)
	dir := t.TempDir()
	c := New(config.UpstreamHZ{URL: p.srv.URL, TokenFile: filepath.Join(dir, "missing.token"), Rungs: []config.UpstreamRung{prodRung}}, dir)
	logs := &logBuf{}
	c.Log = slog.New(slog.NewTextHandler(logs, nil))
	c.CheckToken()
	c.PollOnce(ctx())
	if !strings.Contains(logs.String(), "LOUD: nested hz cannot read its instance token") {
		t.Fatalf("not loud:\n%s", logs.String())
	}
	if len(p.calls) != 0 {
		t.Fatalf("polled without a token: %v", p.calls)
	}
	if r := c.Answer(prodRung, ""); r.Status != 503 || !strings.Contains(r.Error, "token") {
		t.Fatalf("answer = %+v", r)
	}
}
