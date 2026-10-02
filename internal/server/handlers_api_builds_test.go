package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The build registry over the handlers: builders, numbering, logs, state,
// finish, provenance on promote, log retention, and the whole path end to end.

const secondBuilder = "build-2"

type buildFixture struct {
	n4aFixture
	h http.Handler
}

func newBuildFixture(t *testing.T) buildFixture {
	t.Helper()
	f := newN4aFixture(t)
	f.s.buildDir = filepath.Join(t.TempDir(), "builds")
	if err := f.s.updateConfig(func(c *config.Config) {
		c.Projects = append(append([]config.Project(nil), c.Projects...), config.Project{Name: "storefront"})
		c.Machines = append(append([]config.Machine(nil), c.Machines...), config.Machine{Name: secondBuilder}, config.Machine{Name: "laptop"})
		c.AdminURL = "https://hz.example.com/"
	}); err != nil {
		t.Fatal(err)
	}
	return buildFixture{n4aFixture: f, h: f.s.handler()}
}

// do sends a request through the full handler stack.
func (f buildFixture) do(method, path, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.RemoteAddr = "192.0.2.50:1234"
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f buildFixture) doJSON(method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	return f.do(method, path, token, raw)
}

func decodeAs[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %T from %d %s: %v", out, w.Code, w.Body.String(), err)
	}
	return out
}

func (f buildFixture) mintBuilder(t *testing.T, machine string, projects ...string) string {
	t.Helper()
	w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: machine, Projects: projects})
	if w.Code != http.StatusOK {
		t.Fatalf("mint builder %s: %d %s", machine, w.Code, w.Body.String())
	}
	out := decodeAs[apitypes.BuilderTokenResp](t, w)
	if !strings.HasPrefix(out.Token, builderTokenPrefix) || len(out.Token) != len(builderTokenPrefix)+64 {
		t.Fatalf("token = %q", out.Token)
	}
	return out.Token
}

func startReq(project, machine string) apitypes.BuildStartReq {
	return apitypes.BuildStartReq{Project: project, Commit: testCommit, VersionCore: "1.0.0.0", Describe: "v1.0.0-3-g0123456", Machine: machine}
}

func (f buildFixture) start(t *testing.T, token string, req apitypes.BuildStartReq) apitypes.BuildStartResp {
	t.Helper()
	w := f.doJSON("POST", "/api/v1/builds", token, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start %+v: %d %s", req, w.Code, w.Body.String())
	}
	return decodeAs[apitypes.BuildStartResp](t, w)
}

func buildPath(id int64, verb string) string {
	p := "/api/v1/builds/" + strconv.FormatInt(id, 10)
	if verb != "" {
		p += "/" + verb
	}
	return p
}

func TestBuilderMintRotateRevoke(t *testing.T) {
	f := newBuildFixture(t)
	if w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: "nope", Projects: []string{"redline"}}); w.Code != http.StatusConflict ||
		!strings.Contains(errorOf(w), "cannot make itself a builder") {
		t.Fatalf("undeclared machine: %d %q", w.Code, errorOf(w))
	}
	if w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: secondBuilder, Projects: []string{"nosuch"}}); w.Code != http.StatusConflict {
		t.Fatalf("undeclared project: %d", w.Code)
	}
	tok := f.mintBuilder(t, secondBuilder, "redline")
	f.start(t, tok, startReq("redline", secondBuilder))

	// The list carries no token and no digest.
	w := f.do("GET", "/api/v1/builders", f.pat, nil)
	list := decodeAs[[]apitypes.BuilderResp](t, w)
	if len(list) != 2 || strings.Contains(w.Body.String(), tokenDigest(tok)) || strings.Contains(w.Body.String(), tok) {
		t.Fatalf("builders = %s", w.Body.String())
	}

	// Re-mint rotates: the old token is refused, the new one works.
	w = f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: secondBuilder, Projects: []string{"redline", "storefront"}})
	rot := decodeAs[apitypes.BuilderTokenResp](t, w)
	if !rot.Rotated {
		t.Fatalf("re-mint not marked rotated: %+v", rot)
	}
	if w := f.doJSON("POST", "/api/v1/builds", tok, startReq("redline", secondBuilder)); w.Code != http.StatusUnauthorized {
		t.Fatalf("the rotated-out token: %d", w.Code)
	}
	f.start(t, rot.Token, startReq("storefront", secondBuilder))

	// Revoked → 401.
	if w := f.do("DELETE", "/api/v1/builders/"+secondBuilder, f.pat, nil); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w := f.doJSON("POST", "/api/v1/builds", rot.Token, startReq("redline", secondBuilder)); w.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked builder: %d", w.Code)
	}
	if w := f.do("DELETE", "/api/v1/builders/"+secondBuilder, f.pat, nil); w.Code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", w.Code)
	}
}

func TestBuildStartScopeAndShape(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")
	other := f.mintBuilder(t, "laptop", "storefront")

	out := f.start(t, tok, startReq("redline", secondBuilder))
	if out.Number != 1 || out.Version != "1.0.0-0.1" || out.BuildURL != "https://hz.example.com/p/redline/builds/1" {
		t.Fatalf("start = %+v", out)
	}
	if err := db.CheckDeployVersion(out.Version); err != nil {
		t.Fatalf("the version does not parse: %v", err)
	}
	cases := []struct {
		name  string
		token string
		req   apitypes.BuildStartReq
		code  int
	}{
		{"another project", tok, startReq("storefront", secondBuilder), http.StatusForbidden},
		{"another machine", tok, startReq("redline", "laptop"), http.StatusForbidden},
		{"the other builder for redline", other, startReq("redline", "laptop"), http.StatusForbidden},
		{"an admin token", f.pat, startReq("redline", secondBuilder), http.StatusUnauthorized},
		{"a service token", stagingSvcToken, startReq("redline", secondBuilder), http.StatusUnauthorized},
		{"no token", "", startReq("redline", secondBuilder), http.StatusUnauthorized},
		{"short commit", tok, apitypes.BuildStartReq{Project: "redline", Commit: "0123456", VersionCore: "1.0.0.0", Machine: secondBuilder}, http.StatusBadRequest},
		{"three-part core", tok, apitypes.BuildStartReq{Project: "redline", Commit: testCommit, VersionCore: "1.0.0", Machine: secondBuilder}, http.StatusBadRequest},
		{"negative core", tok, apitypes.BuildStartReq{Project: "redline", Commit: testCommit, VersionCore: "1.0.0.-1", Machine: secondBuilder}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if w := f.doJSON("POST", "/api/v1/builds", c.token, c.req); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.code)
		}
	}
	// Refusals consume no number.
	if next := f.start(t, tok, startReq("redline", secondBuilder)); next.Number != 2 {
		t.Fatalf("after refusals the next is #%d", next.Number)
	}
}

// Two builders, concurrent starts, through the handlers.
func TestConcurrentStartsFromTwoBuilders(t *testing.T) {
	f := newBuildFixture(t)
	toks := map[string]string{
		secondBuilder: f.mintBuilder(t, secondBuilder, "redline"),
		"laptop":      f.mintBuilder(t, "laptop", "redline"),
	}
	const each = 15
	var mu sync.Mutex
	var wg sync.WaitGroup
	got := map[string][]int64{}
	for machine, tok := range toks {
		wg.Add(1)
		go func(machine, tok string) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				w := f.doJSON("POST", "/api/v1/builds", tok, startReq("redline", machine))
				if w.Code != http.StatusOK {
					t.Errorf("%s start: %d %s", machine, w.Code, w.Body.String())
					continue
				}
				var out apitypes.BuildStartResp
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				mu.Lock()
				got[machine] = append(got[machine], out.Number)
				mu.Unlock()
			}
		}(machine, tok)
	}
	wg.Wait()
	seen := map[int64]bool{}
	for machine, nums := range got {
		for i, n := range nums {
			if seen[n] {
				t.Errorf("#%d handed out twice", n)
			}
			seen[n] = true
			if i > 0 && n <= nums[i-1] {
				t.Errorf("%s got #%d after #%d", machine, n, nums[i-1])
			}
		}
	}
	if len(seen) != 2*each {
		t.Fatalf("handed out %d numbers, want %d: %v", len(seen), 2*each, got)
	}
}

func TestBuildSeedEndpoint(t *testing.T) {
	f := newBuildFixture(t)
	f.reportOnly(t, stagingSvcToken, stagingReport("1.0.0-0.4", deployShaA))
	tok := f.mintBuilder(t, secondBuilder, "redline")
	if w := f.doJSON("POST", "/api/v1/builds/seed", f.pat, apitypes.BuildSeedReq{Project: "redline", Next: 3}); w.Code != http.StatusConflict ||
		!strings.Contains(errorOf(w), "only raises") {
		t.Fatalf("lowering below the seed: %d %q", w.Code, errorOf(w))
	}
	w := f.doJSON("POST", "/api/v1/builds/seed", f.pat, apitypes.BuildSeedReq{Project: "redline", Next: 6})
	if w.Code != http.StatusOK || decodeAs[apitypes.BuildSeedResp](t, w).Previous != 5 {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	if w := f.doJSON("POST", "/api/v1/builds/seed", tok, apitypes.BuildSeedReq{Project: "redline", Next: 60}); w.Code != http.StatusUnauthorized {
		t.Fatalf("a builder seeding: %d", w.Code)
	}
	if out := f.start(t, tok, startReq("redline", secondBuilder)); out.Number != 6 || out.Version != "1.0.0-0.6" {
		t.Fatalf("after the seed: %+v", out)
	}
}

func TestBuildLogAppendCapAndTail(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")
	other := f.mintBuilder(t, "laptop", "redline")
	b := f.start(t, tok, startReq("redline", secondBuilder))

	if w := f.do("POST", buildPath(b.ID, "log"), tok, []byte("hello\n")); w.Code != http.StatusOK {
		t.Fatalf("append: %d %s", w.Code, w.Body.String())
	}
	if w := f.do("POST", buildPath(b.ID, "log"), tok, []byte("world\n")); decodeAs[apitypes.BuildLogAppendResp](t, w).Size != 12 {
		t.Fatalf("second append: %s", w.Body.String())
	}
	if w := f.do("POST", buildPath(b.ID, "log"), other, []byte("x")); w.Code != http.StatusForbidden {
		t.Fatalf("another builder's chunk: %d", w.Code)
	}
	if w := f.do("POST", buildPath(b.ID, "log"), f.pat, []byte("x")); w.Code != http.StatusUnauthorized {
		t.Fatalf("an admin writing a log: %d", w.Code)
	}
	path := filepath.Join(f.s.buildDir, "redline", "1.log")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", fi, err)
	}

	read := func(q string) *httptest.ResponseRecorder { return f.do("GET", buildPath(b.ID, "log")+q, f.pat, nil) }
	if w := read(""); w.Body.String() != "hello\nworld\n" || w.Header().Get("X-Log-Next-Offset") != "12" {
		t.Fatalf("whole log: %q %v", w.Body.String(), w.Header())
	}
	if w := read("?offset=6"); w.Body.String() != "world\n" || w.Header().Get("X-Log-Offset") != "6" {
		t.Fatalf("from offset 6: %q", w.Body.String())
	}
	if w := read("?offset=12"); w.Body.Len() != 0 || w.Header().Get("X-Log-Next-Offset") != "12" {
		t.Fatalf("at the end: %q %v", w.Body.String(), w.Header())
	}
	if w := read("?tail=3"); w.Body.String() != "ld\n" {
		t.Fatalf("tail 3: %q", w.Body.String())
	}
	if w := read("?offset=-1"); w.Code != http.StatusBadRequest {
		t.Fatalf("negative offset: %d", w.Code)
	}
	if w := f.do("GET", buildPath(b.ID, "log"), tok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("a builder reading a log: %d", w.Code)
	}

	// The cap: fill to 10 bytes short, then a 100-byte chunk is cut at the
	// cap with one truncation line, and the next chunk is a 413.
	big := bytes.Repeat([]byte("x"), BuildLogCap-12-10)
	if w := f.do("POST", buildPath(b.ID, "log"), tok, big); w.Code != http.StatusOK {
		t.Fatalf("big append: %d %s", w.Code, w.Body.String())
	}
	w := f.do("POST", buildPath(b.ID, "log"), tok, bytes.Repeat([]byte("y"), 100))
	cut := decodeAs[apitypes.BuildLogAppendResp](t, w)
	if w.Code != http.StatusOK || !cut.Truncated || cut.Appended != 10 {
		t.Fatalf("crossing the cap: %d %+v", w.Code, cut)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.HasSuffix(raw[:BuildLogCap], bytes.Repeat([]byte("y"), 10)) || bytes.Count(raw, []byte("[hz] log truncated")) != 1 {
		t.Fatalf("the log does not end with 10 y's and one truncation line: %q", raw[BuildLogCap-20:])
	}
	if w := f.do("POST", buildPath(b.ID, "log"), tok, []byte("more")); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("after the cap: %d", w.Code)
	}
	if again, _ := os.ReadFile(path); bytes.Count(again, []byte("[hz] log truncated")) != 1 || len(again) != len(raw) {
		t.Fatal("a chunk after the cap changed the log")
	}
	if w := read("?tail=40"); w.Header().Get("X-Log-Truncated") != "true" {
		t.Fatalf("truncated header: %v", w.Header())
	}
	built := decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(b.ID, ""), f.pat, nil))
	if !built.LogTruncated || built.LogSize != int64(len(raw)) {
		t.Fatalf("build = %+v", built)
	}

	// After a finish, the log is closed too.
	if w := f.doJSON("POST", buildPath(b.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "failed", FailedStep: "test"}); w.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", w.Code, w.Body.String())
	}
	b2 := f.start(t, tok, startReq("redline", secondBuilder))
	_ = f.do("POST", buildPath(b2.ID, "finish"), tok, []byte(`{"result":"failed"}`))
	if w := f.do("POST", buildPath(b2.ID, "log"), tok, []byte("late")); w.Code != http.StatusConflict {
		t.Fatalf("log after finish: %d", w.Code)
	}
}

func TestBuildStateUpdates(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")
	b := f.start(t, tok, startReq("redline", secondBuilder))
	state := apitypes.BuildStateReq{Step: "test", Status: "running", Steps: []apitypes.BuildStepResp{
		{Name: "compile", Status: "passed", StartedAt: "2026-10-02T10:00:00Z", FinishedAt: "2026-10-02T10:01:00Z"},
		{Name: "test", Status: "running", StartedAt: "2026-10-02T10:01:00Z"},
		{Name: "package", Status: "pending"},
	}}
	w := f.doJSON("PUT", buildPath(b.ID, "state"), tok, state)
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	got := decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(b.ID, ""), f.pat, nil))
	if got.Step != "test" || len(got.Steps) != 3 || got.Steps[0] != state.Steps[0] || got.Result != "running" {
		t.Fatalf("read back = %+v", got)
	}
	if w := f.doJSON("PUT", buildPath(b.ID, "state"), tok, apitypes.BuildStateReq{Step: "x", Status: "done"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", w.Code)
	}
	if w := f.doJSON("PUT", buildPath(b.ID, "state"), tok, apitypes.BuildStateReq{Step: "x", Status: "running", Steps: []apitypes.BuildStepResp{{Name: "", Status: "passed"}}}); w.Code != http.StatusBadRequest {
		t.Fatalf("unnamed step: %d", w.Code)
	}
	if w := f.doJSON("PUT", buildPath(b.ID, "state"), f.pat, state); w.Code != http.StatusUnauthorized {
		t.Fatalf("an admin writing state: %d", w.Code)
	}
	if w := f.doJSON("PUT", buildPath(9999, "state"), tok, state); w.Code != http.StatusNotFound {
		t.Fatalf("no such build: %d", w.Code)
	}
	if w := f.doJSON("PUT", buildPath(9999, "state"), "", state); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous on a missing build: %d — the id must not be revealed", w.Code)
	}
}

func TestFinishRules(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline", "storefront")
	b := f.start(t, tok, startReq("redline", secondBuilder))
	finish := func(req apitypes.BuildFinishReq) *httptest.ResponseRecorder {
		return f.doJSON("POST", buildPath(b.ID, "finish"), tok, req)
	}
	notUploaded := digestOf("never uploaded")
	if w := finish(apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: notUploaded}); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "not uploaded") {
		t.Fatalf("passed, artifact not uploaded: %d %q", w.Code, errorOf(w))
	}
	otherSha, w := f.upload(t, tok, "storefront", "storefront bundle")
	if w.Code != http.StatusOK {
		t.Fatalf("builder upload into storefront: %d %s", w.Code, w.Body.String())
	}
	if w := finish(apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: otherSha}); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "storefront") {
		t.Fatalf("passed with another project's artifact: %d %q", w.Code, errorOf(w))
	}
	if w := finish(apitypes.BuildFinishReq{Result: "passed"}); w.Code != http.StatusBadRequest {
		t.Fatalf("passed with no artifact: %d", w.Code)
	}
	if w := finish(apitypes.BuildFinishReq{Result: "failed", ArtifactSHA256: otherSha}); w.Code != http.StatusBadRequest {
		t.Fatalf("failed with an artifact: %d", w.Code)
	}
	if w := finish(apitypes.BuildFinishReq{Result: "maybe"}); w.Code != http.StatusBadRequest {
		t.Fatalf("result maybe: %d", w.Code)
	}
	sha, _ := f.upload(t, tok, "redline", "redline bundle")
	w = finish(apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: sha})
	if w.Code != http.StatusOK || decodeAs[apitypes.BuildResp](t, w).ArtifactSHA256 != sha {
		t.Fatalf("passed: %d %s", w.Code, w.Body.String())
	}
	if w := finish(apitypes.BuildFinishReq{Result: "failed", FailedStep: "x"}); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "final") {
		t.Fatalf("double finish: %d %q", w.Code, errorOf(w))
	}
	if w := f.doJSON("PUT", buildPath(b.ID, "state"), tok, apitypes.BuildStateReq{Step: "x", Status: "running"}); w.Code != http.StatusConflict {
		t.Fatalf("state after finish: %d", w.Code)
	}
	// A builder may upload only into its granted projects.
	narrow := f.mintBuilder(t, "laptop", "storefront")
	if _, w := f.upload(t, narrow, "redline", "not yours"); w.Code != http.StatusForbidden {
		t.Fatalf("upload outside the grant: %d", w.Code)
	}
}

func TestAbandonedIsDerivedOnReadThroughTheAPI(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")
	b := f.start(t, tok, startReq("redline", secondBuilder))
	if _, err := f.s.users.ExecContext(context.Background(), `UPDATE builds SET last_activity_at = datetime('now', '-3 hours') WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	got := decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(b.ID, ""), f.pat, nil))
	if got.Result != "abandoned" {
		t.Fatalf("3h quiet reads %q", got.Result)
	}
	// A log chunk revives it — nothing about "abandoned" was stored.
	_ = f.do("POST", buildPath(b.ID, "log"), tok, []byte("still here\n"))
	got = decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(b.ID, ""), f.pat, nil))
	if got.Result != "running" {
		t.Fatalf("after a chunk reads %q", got.Result)
	}
	list := decodeAs[[]apitypes.BuildResp](t, f.do("GET", "/api/v1/builds?project=redline&number=1", f.pat, nil))
	if len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("by number: %+v", list)
	}
}

// Provenance on promote: the artifact of a passed build by a granted builder,
// or a grandfathered one. TestProvenanceOnPromote is the test positive control
// (b) — skipping the check — turns red.
func TestProvenanceOnPromote(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")

	// Uploaded, reported, no build → refused, naming the rule.
	bare, _ := f.upload(t, stagingSvcToken, "redline", "no build made this")
	f.reportOnly(t, stagingSvcToken, stagingReport("1.4.0", bare))
	if w := f.promote(toProd("1.4.0")); w.Code != http.StatusConflict ||
		!strings.Contains(errorOf(w), "a promotion needs the artifact of a passed build by a builder granted for the project") {
		t.Fatalf("no build: %d %q", w.Code, errorOf(w))
	}

	// A failed build's artifact → refused: failing names no artifact, so
	// nothing vouches for the bytes it uploaded.
	failed := f.start(t, tok, startReq("redline", secondBuilder))
	failedSha, _ := f.upload(t, tok, "redline", "a failed build's bytes")
	_ = f.doJSON("POST", buildPath(failed.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "failed", FailedStep: "test"})
	f.reportOnly(t, stagingSvcToken, stagingReport("1.4.1", failedSha))
	if w := f.promote(toProd("1.4.1")); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "no build record") {
		t.Fatalf("failed build's artifact: %d %q", w.Code, errorOf(w))
	}

	// A passed build → promoted, and the row names the build.
	passed := f.start(t, tok, startReq("redline", secondBuilder))
	sha, _ := f.upload(t, tok, "redline", "a passed build's bytes")
	if w := f.doJSON("POST", buildPath(passed.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: sha}); w.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", w.Code, w.Body.String())
	}
	f.reportOnly(t, stagingSvcToken, stagingReport(passed.Version, sha))
	w := f.promote(toProd(passed.Version))
	if w.Code != http.StatusOK {
		t.Fatalf("passed build: %d %s", w.Code, w.Body.String())
	}
	pr := decodeAs[apitypes.PromoteResp](t, w)
	if pr.Provenance != "build" || pr.BuildID != passed.ID || pr.BuildNumber != passed.Number {
		t.Fatalf("promote = %+v", pr)
	}
	promos, _ := f.s.users.Promotions(context.Background(), "redline", 1)
	if promos[0].Provenance != db.ProvenanceBuild || promos[0].BuildID != passed.ID {
		t.Fatalf("promotion row = %+v", promos[0])
	}

	// Revoke the builder: it can start no NEW build, and its past passed build
	// stays promotable — a rollback target (the grant counts at build time).
	if w := f.do("DELETE", "/api/v1/builders/"+secondBuilder, f.pat, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	f.reportOnly(t, stagingSvcToken, stagingReport(passed.Version, sha))
	if w := f.promote(toProd(passed.Version)); w.Code != http.StatusOK {
		t.Fatalf("a revoked builder's passed build must stay promotable: %d %q", w.Code, errorOf(w))
	}
}

// An artifact uploaded before migration 0018 is promotable and marked.
func TestGrandfatheredArtifactIsPromotableAndMarked(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")
	old, err := db.OpenAt(path, 17)
	if err != nil {
		t.Fatal(err)
	}
	sha := digestOf("uploaded before builds existed")
	if _, err := old.RecordArtifact(ctx, db.Artifact{SHA256: sha, Project: "redline", Size: 1, UploadedBy: "service:redline-staging"}); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	user, _ := store.CreateUser(ctx, "carl", "", db.RoleAdmin)
	pat, _, _ := store.CreateAPIToken(ctx, user.ID, "t", 0, false)
	f.s.users, f.pat = store, pat

	arts := decodeAs[[]apitypes.ArtifactResp](t, f.do("GET", "/api/v1/artifacts", pat, nil))
	if len(arts) != 1 || arts[0].Provenance != "no build record" {
		t.Fatalf("artifacts = %+v", arts)
	}
	f.reportOnly(t, stagingSvcToken, stagingReport("1.0.0-0.4", sha))
	w := f.promote(toProd("1.0.0-0.4"))
	if w.Code != http.StatusOK || decodeAs[apitypes.PromoteResp](t, w).Provenance != "grandfathered" {
		t.Fatalf("grandfathered: %d %s", w.Code, w.Body.String())
	}
	ps := decodeAs[[]apitypes.PromotionResp](t, f.do("GET", "/api/v1/promotions", pat, nil))
	if ps[0].Provenance != "grandfathered" || ps[0].BuildID != 0 {
		t.Fatalf("promotion = %+v", ps[0])
	}
	// And the first build of redline is numbered above the reported #4.
	tok := f.mintBuilder(t, secondBuilder, "redline")
	if b := f.start(t, tok, startReq("redline", secondBuilder)); b.Number != 5 {
		t.Fatalf("first build after a reported 1.0.0-0.4 is #%d", b.Number)
	}
}

func TestBuildLogRetentionDeletesWithTheArtifactAndAfter30Days(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	tok := f.mintBuilder(t, secondBuilder, "redline")

	// A passed build whose artifact retention later deletes.
	passed := f.start(t, tok, startReq("redline", secondBuilder))
	_ = f.do("POST", buildPath(passed.ID, "log"), tok, []byte("passed log\n"))
	sha, _ := f.upload(t, tok, "redline", "kept until retention")
	_ = f.doJSON("POST", buildPath(passed.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: sha})
	// A passed build whose artifact stays.
	kept := f.start(t, tok, startReq("redline", secondBuilder))
	_ = f.do("POST", buildPath(kept.ID, "log"), tok, []byte("kept log\n"))
	keptSha, _ := f.upload(t, tok, "redline", "stays")
	_ = f.doJSON("POST", buildPath(kept.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: keptSha})
	// A failed build 31 days ago, and one yesterday. finished_at is final by
	// trigger, so the old one is inserted with its date.
	for i, ago := range []string{"-31 days", "-1 days"} {
		n := int64(100 + i)
		if _, err := f.s.users.ExecContext(ctx, `INSERT INTO builds (project, number, commit_sha, version_core, version, machine, started_at, last_activity_at, status, result, finished_at)
			VALUES ('redline', ?, ?, '1.0.0.0', ?, ?, datetime('now', ?), datetime('now', ?), 'failed', 'failed', datetime('now', ?))`,
			n, testCommit, "1.0.0-0."+strconv.FormatInt(n, 10), secondBuilder, ago, ago, ago); err != nil {
			t.Fatal(err)
		}
		p, _ := f.s.buildLogPath("redline", n)
		_ = os.WriteFile(p, []byte("failed log\n"), 0o600)
	}
	if err := f.s.users.RecordArtifactEvent(ctx, sha, db.ArtifactDeleted, "not kept: test", "retention"); err != nil {
		t.Fatal(err)
	}

	dels := f.s.runBuildLogRetention(ctx, "test")
	var nums []int64
	for _, d := range dels {
		nums = append(nums, d.Number)
	}
	if len(nums) != 2 || nums[0] != passed.Number || nums[1] != 100 {
		t.Fatalf("deleted %v, want [%d 100]", nums, passed.Number)
	}
	for n, want := range map[int64]bool{passed.Number: false, kept.Number: true, 100: false, 101: true} {
		p, _ := f.s.buildLogPath("redline", n)
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("log #%d exists=%v, want %v", n, err == nil, want)
		}
	}
	w := f.do("GET", buildPath(passed.ID, "log"), f.pat, nil)
	if w.Code != http.StatusGone || !strings.Contains(errorOf(w), "deleted by retention") {
		t.Fatalf("a deleted log: %d %q", w.Code, errorOf(w))
	}
	got := decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(passed.ID, ""), f.pat, nil))
	if got.LogDeletedAt == "" || !strings.Contains(got.LogDeletedWhy, "artifact") {
		t.Fatalf("build = %+v", got)
	}
	// A second pass deletes nothing more.
	if again := f.s.runBuildLogRetention(ctx, "test"); len(again) != 0 {
		t.Fatalf("second pass: %+v", again)
	}
}

// The builder token authenticates five calls. Every admin route refuses it —
// through the full handler. Positive control (c), letting it pass isAdmin,
// turns this red. The instrument check: each path resolves to its own mux
// entry or its own subtree, never the SPA catch-all.
func TestBuilderTokenIsRefusedOnAdminRoutes(t *testing.T) {
	f := newBuildFixture(t)
	tok := f.mintBuilder(t, secondBuilder, "redline")
	b := f.start(t, tok, startReq("redline", secondBuilder))
	mux := f.s.setupRoutes()
	routes := append(adminRoutesUnderTest(),
		struct{ method, path string }{"GET", buildPath(b.ID, "")},
		struct{ method, path string }{"GET", buildPath(b.ID, "log")},
		struct{ method, path string }{"GET", "/api/v1/artifacts/" + deployShaA},
	)
	for _, rt := range routes {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
		_, pattern := mux.Handler(r)
		if pattern != r.URL.Path && (pattern == "/" || !strings.HasSuffix(pattern, "/") || !strings.HasPrefix(r.URL.Path, pattern)) {
			t.Errorf("instrument: %s resolves to mux pattern %q, not its own route — the list is wrong", r.URL.Path, pattern)
			continue
		}
		if w := f.do(rt.method, rt.path, tok, []byte("{}")); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a builder token answered %d, want 401/403", rt.method, rt.path, w.Code)
		}
	}
	// And the five it may call, it can: the instrument's other half.
	for _, c := range []struct {
		method, path string
		body         []byte
	}{
		{"POST", buildPath(b.ID, "log"), []byte("x")},
		{"PUT", buildPath(b.ID, "state"), []byte(`{"step":"a","status":"running","steps":[]}`)},
	} {
		if w := f.do(c.method, c.path, tok, c.body); w.Code != http.StatusOK {
			t.Errorf("builder %s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// adminRoutesUnderTest is the admin surface a non-admin token must not reach:
// the N4a list plus the build registry's own admin routes.
func adminRoutesUnderTest() []struct{ method, path string } {
	return []struct{ method, path string }{
		{"POST", "/api/v1/environments/promote"},
		{"POST", "/api/v1/environments/apply"},
		{"POST", "/api/v1/environments/hold"},
		{"POST", "/api/v1/environments/unhold"},
		{"GET", "/api/v1/deploys/check?project=redline&environment=prod&version=1.4.0&artifact_sha256=" + deployShaA},
		{"GET", "/api/v1/deploys/latest"},
		{"GET", "/api/v1/deploys/applied"},
		{"GET", "/api/v1/deploys/desired?project=redline&environment=prod"},
		{"GET", "/api/v1/promotions"},
		{"GET", "/api/v1/artifacts"},
		{"POST", "/api/v1/machines/hz-token"},
		{"POST", "/api/v1/nested/bootstrap"},
		{"GET", "/api/v1/machines"},
		{"POST", "/api/v1/machines/set"},
		{"GET", "/api/v1/environments"},
		{"POST", "/api/v1/environments/set"},
		{"GET", "/api/v1/projects"},
		{"GET", "/api/v1/projects/lines?project=redline"},
		{"POST", "/api/v1/projects/lines/pin"},
		{"POST", "/api/v1/backups/kept"},
		{"GET", "/api/v1/cm/configs"},
		{"GET", "/api/v1/cm/registrations"},
		{"GET", "/api/v1/users"},
		{"POST", "/api/v1/users"},
		{"GET", "/api/v1/settings"},
		{"GET", "/api/v1/services"},
		{"POST", "/api/v1/services/edit"},
		{"GET", "/api/v1/vpn/peers"},
		{"GET", "/api/v1/account/tokens"},
		{"GET", "/api/v1/dashboard"},
		{"GET", "/api/v1/builders"},
		{"POST", "/api/v1/builders"},
		{"GET", "/api/v1/builds"},
		{"POST", "/api/v1/builds/seed"},
	}
}

// End to end: a builder starts → logs → states → uploads → finishes; staging
// reports the build's version; promote; the promotion row names the build,
// and the report and the promotion link hz's build page.
func TestBuildEndToEnd(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	tok := f.mintBuilder(t, secondBuilder, "redline")

	b := f.start(t, tok, startReq("redline", secondBuilder))
	if w := f.do("POST", buildPath(b.ID, "log"), tok, []byte("$ make test\nok\n")); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := f.doJSON("PUT", buildPath(b.ID, "state"), tok, apitypes.BuildStateReq{Step: "package", Status: "running",
		Steps: []apitypes.BuildStepResp{{Name: "test", Status: "passed"}, {Name: "package", Status: "running"}}}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	sha, w := f.upload(t, tok, "redline", "redline "+b.Version)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if rec, _ := f.s.users.LookupArtifact(ctx, sha); rec.UploadedBy != "builder:"+secondBuilder {
		t.Fatalf("uploaded by %q", rec.UploadedBy)
	}
	if w := f.doJSON("POST", buildPath(b.ID, "finish"), tok, apitypes.BuildFinishReq{Result: "passed", ArtifactSHA256: sha}); w.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", w.Code, w.Body.String())
	}

	rep := stagingReport(b.Version, sha)
	rep.BuildURL = b.BuildURL
	f.reportOnly(t, stagingSvcToken, rep)
	latest := decodeAs[[]apitypes.DeployReportResp](t, f.do("GET", "/api/v1/deploys/latest", f.pat, nil))
	if len(latest) != 1 || latest[0].BuildNumber != b.Number || latest[0].BuildURL != b.BuildURL {
		t.Fatalf("latest = %+v", latest)
	}

	w = f.promote(toProd(b.Version))
	if w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	ps := decodeAs[[]apitypes.PromotionResp](t, f.do("GET", "/api/v1/promotions?project=redline", f.pat, nil))
	if len(ps) != 1 || ps[0].Provenance != "build" || ps[0].BuildID != b.ID || ps[0].BuildNumber != b.Number || ps[0].ArtifactSHA256 != sha {
		t.Fatalf("promotion = %+v", ps)
	}
	arts := decodeAs[[]apitypes.ArtifactResp](t, f.do("GET", "/api/v1/artifacts", f.pat, nil))
	if len(arts) != 1 || arts[0].Provenance != "build" || arts[0].BuildNumber != b.Number {
		t.Fatalf("artifacts = %+v", arts)
	}
	got := decodeAs[apitypes.BuildResp](t, f.do("GET", buildPath(b.ID, ""), f.pat, nil))
	if got.Result != "passed" || got.LogSize != int64(len("$ make test\nok\n")) || got.Commit != testCommit || got.Machine != secondBuilder {
		t.Fatalf("build = %+v", got)
	}
}
