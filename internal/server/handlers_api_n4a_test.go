package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// N4a over the parent's handlers: upload, promote's upload check, apply, hold,
// desired, the instance token, download scope and retention.

const nestedMachine = "redline-prod-hz"

type n4aFixture struct {
	deployFixture
	// childHits counts requests to the address the parent's records give for
	// the nested hz — the parent must never dial it (#1).
	childHits *hitCounter
}

type hitCounter struct {
	mu sync.Mutex
	n  int
}

func (h *hitCounter) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.n }

func newN4aFixture(t *testing.T) n4aFixture {
	t.Helper()
	f := newDeployFixture(t)
	f.s.artifactDir = filepath.Join(t.TempDir(), "artifacts")
	hits := &hitCounter{}
	childAddr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.mu.Lock()
		hits.n++
		hits.mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(childAddr.Close)
	if err := f.s.updateConfig(func(c *config.Config) {
		c.Machines = append(append([]config.Machine(nil), c.Machines...),
			config.Machine{Name: nestedMachine, HZ: &config.MachineHZ{URL: childAddr.URL}},
			config.Machine{Name: "other-hz", HZ: &config.MachineHZ{URL: "http://192.0.2.9:8080"}},
		)
		envs := append([]config.Environment(nil), c.Environments...)
		for i := range envs {
			if envs[i].Name == "prod" {
				envs[i].Upstream = nestedMachine
			}
		}
		envs = append(envs, config.Environment{Project: "redline", Name: "loadtest", Posture: "prod", Upstream: "other-hz"})
		c.Environments = envs
	}); err != nil {
		t.Fatal(err)
	}
	return n4aFixture{deployFixture: f, childHits: hits}
}

func digestOf(body string) string {
	s := sha256.Sum256([]byte(body))
	return hex.EncodeToString(s[:])
}

func (f n4aFixture) raw(method, target, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.s.handleAPIArtifact(w, r)
	return w
}

func (f n4aFixture) upload(t *testing.T, token, project, body string) (string, *httptest.ResponseRecorder) {
	t.Helper()
	sha := digestOf(body)
	return sha, f.raw(http.MethodPut, "/api/v1/artifacts/"+sha+"?project="+project, token, []byte(body))
}

func (f n4aFixture) mint(t *testing.T, machine string) string {
	t.Helper()
	w := f.call(f.s.handleAPIMachineHZToken, http.MethodPost, "/api/v1/machines/hz-token", f.pat, apitypes.InstanceTokenReq{Machine: machine})
	if w.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.InstanceTokenResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	if !strings.HasPrefix(out.Token, instanceTokenPrefix) {
		t.Fatalf("token = %q", out.Token)
	}
	return out.Token
}

func (f n4aFixture) apply(env, version string) *httptest.ResponseRecorder {
	return f.call(f.s.handleAPIEnvironmentApply, http.MethodPost, "/api/v1/environments/apply", f.pat,
		apitypes.ApplyReq{Project: "redline", Environment: env, Version: version})
}

func (f n4aFixture) desired(token, env, ifNoneMatch string) *httptest.ResponseRecorder {
	q := url.Values{"project": {"redline"}, "environment": {env}}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deploys/desired?"+q.Encode(), nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	f.s.handleAPIDeployDesired(w, r)
	return w
}

// stageAndPromote uploads body, reports it on staging as version, promotes it
// to prod and returns its sha.
func (f n4aFixture) stageAndPromote(t *testing.T, version, body string) string {
	t.Helper()
	sha, w := f.upload(t, stagingSvcToken, "redline", body)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	f.reportOnly(t, stagingSvcToken, stagingReport(version, sha))
	if w := f.promote(toProd(version)); w.Code != http.StatusOK {
		t.Fatalf("promote %s: %d %s", version, w.Code, w.Body.String())
	}
	return sha
}

// ---------------------------------------------------------------------------

func TestArtifactUpload(t *testing.T) {
	f := newN4aFixture(t)
	body := "redline bundle 1.4.0"
	sha := digestOf(body)

	// A hash that is not the path's: 400, nothing stored, no temp file left.
	w := f.raw(http.MethodPut, "/api/v1/artifacts/"+sha+"?project=redline", stagingSvcToken, []byte("tampered"))
	if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "mismatch") {
		t.Fatalf("mismatch: %d %q", w.Code, errorOf(w))
	}
	if entries, _ := os.ReadDir(f.s.artifactDir); len(entries) != 0 {
		t.Fatalf("a refused upload left %v", entries)
	}
	if _, err := f.s.users.LookupArtifact(context.Background(), sha); err == nil {
		t.Fatal("a refused upload was recorded")
	}

	// Stored, then idempotent.
	_, w = f.upload(t, stagingSvcToken, "redline", body)
	var out apitypes.ArtifactUploadResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	if w.Code != http.StatusOK || !out.Stored || out.Existing || out.SHA256 != sha || out.Size != int64(len(body)) {
		t.Fatalf("upload: %d %+v", w.Code, out)
	}
	st, err := os.Stat(filepath.Join(f.s.artifactDir, sha))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stored file: %v %v", st, err)
	}
	_, w = f.upload(t, f.pat, "redline", body)
	out = apitypes.ArtifactUploadResp{}
	_ = json.NewDecoder(w.Body).Decode(&out)
	if w.Code != http.StatusOK || !out.Existing {
		t.Fatalf("re-upload: %d %+v", w.Code, out)
	}
	rec, _ := f.s.users.LookupArtifact(context.Background(), sha)
	if rec.UploadedBy != "service:redline-staging" || rec.Project != "redline" {
		t.Fatalf("record = %+v", rec)
	}

	// The size cap, said in the refusal.
	if err := f.s.updateConfig(func(c *config.Config) { c.ArtifactMaxBytes = 4 }); err != nil {
		t.Fatal(err)
	}
	_, w = f.upload(t, f.pat, "redline", "more than four bytes")
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(errorOf(w), "size cap of 4 bytes") || !strings.Contains(errorOf(w), "artifact_max_bytes") {
		t.Fatalf("cap: %d %q", w.Code, errorOf(w))
	}

	// Scope: ?project= required; a service token only into its own project;
	// an unknown token 401; an instance token cannot upload.
	w = f.raw(http.MethodPut, "/api/v1/artifacts/"+sha, f.pat, []byte(body))
	if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "?project= is required") {
		t.Fatalf("no project: %d %q", w.Code, errorOf(w))
	}
	_, w = f.upload(t, looseSvcToken, "redline", "x")
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "only into its own project") {
		t.Fatalf("loose service: %d %q", w.Code, errorOf(w))
	}
	_, w = f.upload(t, "nope", "redline", "x")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", w.Code)
	}
	tok := f.mint(t, nestedMachine)
	_, w = f.upload(t, tok, "redline", "x")
	if w.Code != http.StatusForbidden {
		t.Fatalf("instance token upload: %d", w.Code)
	}
	w = f.raw(http.MethodPut, "/api/v1/artifacts/../../etc?project=redline", f.pat, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a path that is not a sha: %d", w.Code)
	}
}

func TestPromoteRefusesAnArtifactNeverUploaded(t *testing.T) {
	f := newN4aFixture(t)
	f.reportOnly(t, stagingSvcToken, stagingReport("1.4.0", deployShaA))
	w := f.promote(toProd("1.4.0"))
	want := "artifact " + deployShaA[:12] + " is not uploaded to hz; prod cannot pull it — the staging deploy uploads it before it reports"
	if w.Code != http.StatusConflict || errorOf(w) != want {
		t.Fatalf("promote of a never-uploaded artifact: %d %q\nwant %q", w.Code, errorOf(w), want)
	}
	if f.declared(t, "prod") != "" {
		t.Fatal("a refused promote changed the declared version")
	}
}

func TestApplyOnlyTheNewestPromotion(t *testing.T) {
	f := newN4aFixture(t)
	if w := f.apply("prod", "1.4.0"); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "nothing was promoted into redline/prod") {
		t.Fatalf("apply before any promotion: %d %q", w.Code, errorOf(w))
	}
	sha14 := f.stageAndPromote(t, "1.4.0", "bundle 1.4.0")
	f.stageAndPromote(t, "1.5.0", "bundle 1.5.0")

	w := f.apply("prod", "1.4.0")
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "the newest promotion into redline/prod is 1.5.0") {
		t.Fatalf("apply of an older promotion: %d %q", w.Code, errorOf(w))
	}
	w = f.apply("prod", "1.5.0")
	var out apitypes.ApplyResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	if w.Code != http.StatusOK || !out.Applied || out.Existing || out.Version != "1.5.0" || out.ArtifactSHA256 == sha14 {
		t.Fatalf("apply: %d %+v", w.Code, out)
	}
	a, err := f.s.users.LatestApply(context.Background(), "redline", "prod")
	if err != nil || a.AppliedBy != "user:carl (token:redline-deploy)" || a.AppliedAt.IsZero() || a.PromotionID != out.PromotionID {
		t.Fatalf("apply record: %+v %v", a, err)
	}
	// Again: no second row.
	w = f.apply("prod", "1.5.0")
	out = apitypes.ApplyResp{}
	_ = json.NewDecoder(w.Body).Decode(&out)
	if w.Code != http.StatusOK || !out.Existing {
		t.Fatalf("re-apply: %d %+v", w.Code, out)
	}
	if all, _ := f.s.users.Applies(context.Background(), "redline"); len(all) != 1 {
		t.Fatalf("applies = %d, want 1", len(all))
	}
	// Append-only against raw SQL.
	if _, err := f.s.users.ExecContext(context.Background(), `UPDATE applies SET version = '9.9.9'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("raw UPDATE of applies: %v", err)
	}
	if _, err := f.s.users.ExecContext(context.Background(), `DELETE FROM applies`); err == nil {
		t.Fatal("raw DELETE of applies succeeded")
	}
	// Not an admin: refused.
	w = f.call(f.s.handleAPIEnvironmentApply, http.MethodPost, "/api/v1/environments/apply", stagingSvcToken,
		apitypes.ApplyReq{Project: "redline", Environment: "prod", Version: "1.5.0"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("service-token apply: %d", w.Code)
	}
}

func TestDesiredIsTheNewestApplyAndHoldChangesTheETag(t *testing.T) {
	f := newN4aFixture(t)
	if w := f.desired(f.pat, "prod", ""); w.Code != http.StatusNotFound || errorOf(w) != "nothing applied to redline/prod" {
		t.Fatalf("before any apply: %d %q", w.Code, errorOf(w))
	}
	sha := f.stageAndPromote(t, "1.4.0", "bundle 1.4.0")
	// Promoted but not applied: held by default.
	if w := f.desired(f.pat, "prod", ""); w.Code != http.StatusNotFound {
		t.Fatalf("promoted, not applied: %d — a promote alone must not change what the rung pulls", w.Code)
	}
	if w := f.apply("prod", "1.4.0"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	// A newer promotion, NOT applied: desired still answers the apply.
	f.stageAndPromote(t, "1.5.0", "bundle 1.5.0")

	w := f.desired(f.pat, "prod", "")
	var d apitypes.DesiredResp
	_ = json.NewDecoder(w.Body).Decode(&d)
	if w.Code != http.StatusOK || d.Version != "1.4.0" || d.ArtifactSHA256 != sha || d.ApplyID == 0 || d.PromotionID == 0 ||
		d.AppliedBy == "" || d.AppliedAt == "" || d.Hold != nil {
		t.Fatalf("desired = %d %+v", w.Code, d)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if w := f.desired(f.pat, "prod", etag); w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", w.Code)
	}

	hold := func(on bool, reason string) *httptest.ResponseRecorder {
		path, h := "/api/v1/environments/hold", f.s.handleAPIEnvironmentHold(on)
		if !on {
			path = "/api/v1/environments/unhold"
		}
		return f.call(h, http.MethodPost, path, f.pat, apitypes.HoldReq{Project: "redline", Environment: "prod", Reason: reason})
	}
	if w := hold(true, ""); w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "needs a reason") {
		t.Fatalf("hold without a reason: %d %q", w.Code, errorOf(w))
	}
	if w := hold(false, ""); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "is not held") {
		t.Fatalf("unhold of a rung not held: %d %q", w.Code, errorOf(w))
	}
	if w := hold(true, "cutover window"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = f.desired(f.pat, "prod", etag)
	d = apitypes.DesiredResp{}
	_ = json.NewDecoder(w.Body).Decode(&d)
	if w.Code != http.StatusOK || d.Hold == nil || d.Hold.Reason != "cutover window" || !strings.Contains(d.Hold.By, "carl") || d.Hold.At == "" {
		t.Fatalf("after hold, with the old tag: %d %+v — a hold must change the ETag", w.Code, d)
	}
	held := w.Header().Get("ETag")
	if held == etag {
		t.Fatal("the hold did not change the ETag")
	}
	if w := hold(false, ""); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = f.desired(f.pat, "prod", held)
	d = apitypes.DesiredResp{}
	_ = json.NewDecoder(w.Body).Decode(&d)
	if w.Code != http.StatusOK || d.Hold != nil || w.Header().Get("ETag") == held || w.Header().Get("ETag") == etag {
		t.Fatalf("after unhold: %d %+v etag %s", w.Code, d, w.Header().Get("ETag"))
	}
}

func TestInstanceTokenScope(t *testing.T) {
	f := newN4aFixture(t)
	sha := f.stageAndPromote(t, "1.4.0", "bundle 1.4.0")
	if w := f.apply("prod", "1.4.0"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	tok := f.mint(t, nestedMachine)
	m, _ := f.s.cfg().FindMachine(nestedMachine)
	if m.HZ.TokenSHA256 != tokenDigest(tok) || strings.Contains(mustJSON(t, f.s.cfg()), tok) {
		t.Fatal("the token is stored, or its digest is not")
	}

	// Its three endpoints, for its own rung.
	if w := f.desired(tok, "prod", ""); w.Code != http.StatusOK {
		t.Fatalf("desired: %d %s", w.Code, w.Body.String())
	}
	pulls, _ := f.s.users.InstancePulls(context.Background())
	if pulls[nestedMachine].Environment != "prod" {
		t.Fatalf("the pull was not recorded: %+v", pulls)
	}
	iw := f.call(f.s.handleAPIInstances, http.MethodGet, "/api/v1/instances", f.pat, nil)
	var rows []apitypes.InstanceResp
	_ = json.NewDecoder(iw.Body).Decode(&rows)
	seen := map[string]*apitypes.InstanceNestedResp{}
	for _, row := range rows {
		seen[row.Name] = row.Nested
	}
	if n := seen[nestedMachine]; n == nil || !n.HasToken || n.LastPullAt == 0 || n.LastPullRung != "redline/prod" {
		t.Fatalf("instances row for %s: %+v", nestedMachine, n)
	}
	if n := seen["other-hz"]; n == nil || n.HasToken || n.LastPullAt != 0 {
		t.Fatalf("instances row for other-hz (no token, never pulled): %+v", n)
	}
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+sha, tok, nil); w.Code != http.StatusOK || w.Body.String() != "bundle 1.4.0" {
		t.Fatalf("download: %d %q", w.Code, w.Body.String())
	}
	rep := apitypes.DeployReportReq{Project: "redline", Environment: "prod", App: "redline", Version: "1.4.0",
		ArtifactSHA256: sha, Host: "prod-1", ForwardedFor: "service:redline-prod"}
	f.reportOnly(t, tok, rep)
	latest, _ := f.s.users.LatestDeployReport(context.Background(), "redline", "prod")
	if latest.ReportedBy != "instance:"+nestedMachine+" (for service:redline-prod)" {
		t.Fatalf("forwarded report attributed to %q", latest.ReportedBy)
	}

	// Another rung: 403 on each.
	if w := f.desired(tok, "loadtest", ""); w.Code != http.StatusForbidden {
		t.Fatalf("desired for another instance's rung: %d", w.Code)
	}
	if w := f.desired(tok, "staging", ""); w.Code != http.StatusForbidden {
		t.Fatalf("desired for a rung placed here: %d", w.Code)
	}
	other := rep
	other.Environment = "staging"
	if w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", tok, other); w.Code != http.StatusForbidden {
		t.Fatalf("report for staging: %d", w.Code)
	}
	// An artifact never applied into its rungs.
	staged, _ := f.upload(t, stagingSvcToken, "redline", "staging only")
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+staged, tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("download of an unapplied artifact: %d", w.Code)
	}
	// forwarded_for from a non-instance caller is refused.
	if w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", f.pat, rep); w.Code != http.StatusBadRequest {
		t.Fatalf("forwarded_for from an admin: %d", w.Code)
	}

	// Re-mint revokes the old token.
	tok2 := f.mint(t, nestedMachine)
	if w := f.desired(tok, "prod", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("the replaced token still works: %d", w.Code)
	}
	if w := f.desired(tok2, "prod", ""); w.Code != http.StatusOK {
		t.Fatalf("the new token: %d", w.Code)
	}
	// A machine with no marker gets no token.
	if w := f.call(f.s.handleAPIMachineHZToken, http.MethodPost, "/api/v1/machines/hz-token", f.pat, apitypes.InstanceTokenReq{Machine: "nope"}); w.Code != http.StatusConflict {
		t.Fatalf("mint for an undeclared machine: %d", w.Code)
	}
}

// The instance token authenticates three calls. Every admin route refuses it
// — through the full handler, so a route that skipped isAdmin would show.
// Positive control: every path in the list resolves to its OWN mux entry (not
// the SPA catch-all), so a 401 here is that route's answer and not a typo'd
// path falling through to something else.
func TestInstanceTokenIsRefusedOnAdminRoutes(t *testing.T) {
	f := newN4aFixture(t)
	tok := f.mint(t, nestedMachine)
	mux := f.s.setupRoutes()
	h := f.s.handler()
	routes := []struct{ method, path string }{
		{"POST", "/api/v1/environments/promote"},
		{"POST", "/api/v1/environments/apply"},
		{"POST", "/api/v1/environments/hold"},
		{"POST", "/api/v1/environments/unhold"},
		{"GET", "/api/v1/deploys/check?project=redline&environment=prod&version=1.4.0&artifact_sha256=" + deployShaA},
		{"GET", "/api/v1/deploys/latest"},
		{"GET", "/api/v1/deploys/applied"},
		{"GET", "/api/v1/promotions"},
		{"GET", "/api/v1/artifacts"},
		{"POST", "/api/v1/machines/hz-token"},
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
	}
	for _, rt := range routes {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
		if _, pattern := mux.Handler(r); pattern != r.URL.Path {
			t.Errorf("instrument: %s resolves to mux pattern %q, not its own route — the list is wrong", r.URL.Path, pattern)
			continue
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		r.RemoteAddr = "192.0.2.50:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("%s %s with an instance token answered %d, want 401/403", rt.method, rt.path, w.Code)
		}
	}
}

func TestArtifactDownloadScopeAndGone(t *testing.T) {
	f := newN4aFixture(t)
	missing := strings.Repeat("d", 64)
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+missing, f.pat, nil); w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "was never uploaded") {
		t.Fatalf("never uploaded: %d %q", w.Code, errorOf(w))
	}
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+missing, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	sha, _ := f.upload(t, stagingSvcToken, "redline", "bundle")
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+sha, stagingSvcToken, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("a service token downloading: %d", w.Code)
	}
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+sha, f.pat, nil); w.Code != http.StatusOK || w.Header().Get("X-Artifact-SHA256") != sha {
		t.Fatalf("admin download: %d %v", w.Code, w.Header())
	}
	// The file removed behind hz's back: "missing, never deleted by hz".
	_ = os.Remove(filepath.Join(f.s.artifactDir, sha))
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+sha, f.pat, nil); w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "no deletion is recorded") {
		t.Fatalf("file missing: %d %q", w.Code, errorOf(w))
	}
	// Deleted by retention: says so.
	if err := f.s.users.RecordArtifactEvent(context.Background(), sha, db.ArtifactDeleted, "not kept: test", "retention"); err != nil {
		t.Fatal(err)
	}
	if w := f.raw(http.MethodGet, "/api/v1/artifacts/"+sha, f.pat, nil); w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "retention deleted it") {
		t.Fatalf("deleted: %d %q", w.Code, errorOf(w))
	}
}

// Retention end to end: an old, unpinned artifact is deleted — file gone, row
// kept with a 'deleted' event, the log says why — and pinned ones survive.
func TestRetentionDeletesAnOldUnpinnedArtifact(t *testing.T) {
	f := newN4aFixture(t)
	kept := f.stageAndPromote(t, "1.4.0", "bundle 1.4.0")
	ctx := context.Background()

	// An upload 30 days old that nothing pins. The record is inserted by raw
	// SQL with its date — the table refuses an UPDATE, which is the point.
	old := "an old unpinned bundle"
	oldSHA := digestOf(old)
	if err := os.WriteFile(filepath.Join(f.s.artifactDir, oldSHA), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.users.ExecContext(ctx, `INSERT INTO artifacts (sha256, project, size, uploaded_at, uploaded_by)
		VALUES (?, 'redline', ?, datetime('now', '-30 days'), 'test')`, oldSHA, len(old)); err != nil {
		t.Fatal(err)
	}
	// And one 30 days old that IS pinned (it is the promoted one): backdate by
	// re-inserting under a different sha is impossible, so assert on `kept`
	// being young and pinned both — it must survive either way.
	dels := f.s.runArtifactRetention(ctx, "test")
	if len(dels) != 1 || dels[0].SHA != oldSHA || !strings.Contains(dels[0].Why, "not kept") {
		t.Fatalf("deletions = %+v", dels)
	}
	if _, err := os.Stat(filepath.Join(f.s.artifactDir, oldSHA)); !os.IsNotExist(err) {
		t.Fatalf("the file survived: %v", err)
	}
	rec, err := f.s.users.LookupArtifact(ctx, oldSHA)
	if err != nil || !rec.Deleted() || !strings.Contains(rec.DeletedWhy, "not kept") {
		t.Fatalf("record after deletion: %+v %v", rec, err)
	}
	if has, _ := os.Stat(filepath.Join(f.s.artifactDir, kept)); has == nil {
		t.Fatal("the promoted artifact was deleted")
	}
	// The listing says "deleted <when>".
	w := f.call(f.s.handleAPIArtifacts, http.MethodGet, "/api/v1/artifacts", f.pat, nil)
	var list []apitypes.ArtifactResp
	_ = json.NewDecoder(w.Body).Decode(&list)
	found := false
	for _, a := range list {
		if a.SHA256 == oldSHA && a.DeletedAt != "" && a.DeletedWhy != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("listing: %+v", list)
	}
	// Run again: nothing more to delete.
	if again := f.s.runArtifactRetention(ctx, "test"); len(again) != 0 {
		t.Fatalf("a second run deleted %+v", again)
	}
	// A re-upload restores it and it ages from the re-upload.
	_, up := f.upload(t, stagingSvcToken, "redline", old)
	if up.Code != http.StatusOK {
		t.Fatal(up.Body.String())
	}
	rec, _ = f.s.users.LookupArtifact(ctx, oldSHA)
	if rec.Deleted() {
		t.Fatalf("the re-uploaded artifact still reads deleted: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(f.s.artifactDir, oldSHA)); err != nil {
		t.Fatalf("the re-upload was deleted again at once: %v", err)
	}
}
