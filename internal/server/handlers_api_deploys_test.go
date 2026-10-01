package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// H1–H3 of plan/plan.md "Redline push to prod — the plan", over the handlers.

const (
	deployShaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deployShaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	stagingSvcToken = "svc-redline-staging-token"
	prodSvcToken    = "svc-redline-prod-token"
	looseSvcToken   = "svc-loose-token"
)

type deployFixture struct {
	s   *Server
	pat string // carl's personal API token, named "redline-deploy"
}

func newDeployFixture(t *testing.T) deployFixture {
	t.Helper()
	s := newTestServer(t, &config.Config{
		Projects: []config.Project{{Name: "redline"}},
		Environments: []config.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
			// No `from`: the structural refusal's fixture.
			{Project: "redline", Name: "dev", Posture: "dev"},
		},
		Services: []config.Service{
			{Name: "redline-staging", Domains: []string{"staging.redline.test"}, Project: "redline", Environment: "staging", Token: stagingSvcToken},
			{Name: "redline-prod", Domains: []string{"redline.test"}, Project: "redline", Environment: "prod", Token: prodSvcToken},
			{Name: "loose", Domains: []string{"loose.test"}, Token: looseSvcToken},
		},
	})
	store, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.users = store

	ctx := context.Background()
	user, err := store.CreateUser(ctx, "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := store.CreateAPIToken(ctx, user.ID, "redline-deploy", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	return deployFixture{s: s, pat: raw}
}

func (f deployFixture) call(h http.HandlerFunc, method, target, token string, body any) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	} else {
		rdr = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, rdr)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func stagingReport(version, sha string) apitypes.DeployReportReq {
	return apitypes.DeployReportReq{
		Project: "redline", Environment: "staging", App: "redline",
		Version: version, Describe: "v" + version + "-3-gabc123", ArtifactSHA256: sha,
		Host: "ubuntu@192.0.2.160",
	}
}

func (f deployFixture) report(t *testing.T, token string, req apitypes.DeployReportReq) apitypes.DeployReportResultResp {
	t.Helper()
	w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", token, req)
	if w.Code != http.StatusOK {
		t.Fatalf("report %+v: %d %s", req, w.Code, w.Body.String())
	}
	var out apitypes.DeployReportResultResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Recorded || out.ID <= 0 {
		t.Fatalf("report answered %+v", out)
	}
	return out
}

func (f deployFixture) promote(req apitypes.PromoteReq) *httptest.ResponseRecorder {
	return f.call(f.s.handleAPIEnvironmentPromote, http.MethodPost, "/api/v1/environments/promote", f.pat, req)
}

func (f deployFixture) check(env, version, sha string) (int, apitypes.DeployCheckResp) {
	q := url.Values{"project": {"redline"}, "environment": {env}, "version": {version}, "artifact_sha256": {sha}}
	w := f.call(f.s.handleAPIDeployCheck, http.MethodGet, "/api/v1/deploys/check?"+q.Encode(), f.pat, nil)
	var out apitypes.DeployCheckResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	return w.Code, out
}

func errorOf(w *httptest.ResponseRecorder) string {
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Error
}

func toProd(version string) apitypes.PromoteReq {
	return apitypes.PromoteReq{Project: "redline", From: "staging", To: "prod", Version: version}
}

func (f deployFixture) declared(t *testing.T, env string) string {
	t.Helper()
	e, err := f.s.cfg().LookupEnvironment("redline", env)
	if err != nil {
		t.Fatal(err)
	}
	return e.Version
}

// ---------------------------------------------------------------------------
// H1 — report

func TestDeployReportValidation(t *testing.T) {
	f := newDeployFixture(t)
	for name, tc := range map[string]struct {
		mut  func(*apitypes.DeployReportReq)
		want string
	}{
		"unknown project":     {func(r *apitypes.DeployReportReq) { r.Project = "nope" }, "no such environment"},
		"unknown environment": {func(r *apitypes.DeployReportReq) { r.Environment = "qa" }, "no such environment"},
		"not semver":          {func(r *apitypes.DeployReportReq) { r.Version = "latest" }, "invalid version"},
		"leading v":           {func(r *apitypes.DeployReportReq) { r.Version = "v1.4.0" }, "leading v"},
		"build metadata":      {func(r *apitypes.DeployReportReq) { r.Version = "1.4.0+abc" }, "build metadata"},
		"short sha":           {func(r *apitypes.DeployReportReq) { r.ArtifactSHA256 = "abc123" }, "invalid artifact sha256"},
		"non-hex sha":         {func(r *apitypes.DeployReportReq) { r.ArtifactSHA256 = strings.Repeat("z", 64) }, "invalid artifact sha256"},
		"no host":             {func(r *apitypes.DeployReportReq) { r.Host = "" }, "host is required"},
		"no app":              {func(r *apitypes.DeployReportReq) { r.App = "" }, "app is required"},
	} {
		req := stagingReport("1.4.0", deployShaA)
		tc.mut(&req)
		w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", f.pat, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), tc.want) {
			t.Errorf("%s: %d %q, want 400 naming %q", name, w.Code, errorOf(w), tc.want)
		}
	}
	// A prerelease is a version: redline reports rc builds.
	f.report(t, f.pat, stagingReport("1.0.0-rc.1.1414", deployShaA))
}

func TestDeployReportServiceTokenScope(t *testing.T) {
	f := newDeployFixture(t)

	f.report(t, stagingSvcToken, stagingReport("1.4.0", deployShaA))
	latest, err := f.s.users.LatestDeployReport(context.Background(), "redline", "staging")
	if err != nil || latest.ReportedBy != "service:redline-staging" {
		t.Fatalf("a service-token report is attributed to its service: %+v, %v", latest, err)
	}

	prod := stagingReport("1.4.0", deployShaA)
	prod.Environment = "prod"
	w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", stagingSvcToken, prod)
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "only for redline/staging") {
		t.Fatalf("staging's token reporting prod: %d %q, want 403 naming its own rung", w.Code, errorOf(w))
	}

	w = f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", looseSvcToken, stagingReport("1.4.0", deployShaA))
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "attributed to none") {
		t.Fatalf("an unattributed service's token: %d %q, want 403", w.Code, errorOf(w))
	}

	for name, tok := range map[string]string{"unknown token": "not-a-token", "no token": "", "bad personal token": db.APITokenPrefix + "nope"} {
		w = f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", tok, stagingReport("1.4.0", deployShaA))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}

	// The admin API token is not scoped: it reports for any declared rung.
	f.report(t, f.pat, prod)
}

// The service token cannot promote or check — those are the admin's.
func TestServiceTokenCannotPromoteOrCheck(t *testing.T) {
	f := newDeployFixture(t)
	f.report(t, stagingSvcToken, stagingReport("1.4.0", deployShaA))
	w := f.call(f.s.handleAPIEnvironmentPromote, http.MethodPost, "/api/v1/environments/promote", stagingSvcToken, toProd("1.4.0"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("service token promoting: %d, want 401", w.Code)
	}
	w = f.call(f.s.handleAPIDeployCheck, http.MethodGet, "/api/v1/deploys/check?project=redline&environment=prod&version=1.4.0&artifact_sha256="+deployShaA, prodSvcToken, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("service token checking: %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// H2 — promote

func TestPromoteRefusedWithoutEvidence(t *testing.T) {
	f := newDeployFixture(t)

	w := f.promote(toProd("1.4.0"))
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "staging has not reported running 1.4.0; it has never reported") {
		t.Fatalf("no report: %d %q", w.Code, errorOf(w))
	}

	f.report(t, f.pat, stagingReport("1.3.9", deployShaA))
	w = f.promote(toProd("1.4.0"))
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "staging has not reported running 1.4.0; it last reported 1.3.9 at ") ||
		!strings.Contains(errorOf(w), "ubuntu@192.0.2.160") {
		t.Fatalf("different version reported: %d %q", w.Code, errorOf(w))
	}

	// Reported once, then superseded: the NEWEST report is the evidence.
	f.report(t, f.pat, stagingReport("1.4.0", deployShaA))
	f.report(t, f.pat, stagingReport("1.4.1", deployShaA))
	w = f.promote(toProd("1.4.0"))
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "it last reported 1.4.1") {
		t.Fatalf("superseded report: %d %q", w.Code, errorOf(w))
	}
	if got := f.declared(t, "prod"); got != "" {
		t.Fatalf("a refused promote wrote prod's version: %q", got)
	}
	if list, _ := f.s.users.Promotions(context.Background(), "redline", 0); len(list) != 0 {
		t.Fatalf("a refused promote wrote a record: %+v", list)
	}
}

func TestPromoteRefusedOnStructuralFailure(t *testing.T) {
	f := newDeployFixture(t)
	f.report(t, f.pat, stagingReport("1.4.0", deployShaA))

	// dev declares no `from`, so there is no edge into it.
	w := f.promote(apitypes.PromoteReq{Project: "redline", From: "staging", To: "dev", Version: "1.4.0"})
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), config.ErrNoPromotionEdge.Error()) {
		t.Fatalf("no edge: %d %q", w.Code, errorOf(w))
	}
	w = f.promote(apitypes.PromoteReq{Project: "redline", From: "staging", To: "qa", Version: "1.4.0"})
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "no such environment") {
		t.Fatalf("undeclared target: %d %q", w.Code, errorOf(w))
	}
	// Backwards along the edge: prod -> staging is not upward, and staging
	// has no `from` anyway.
	w = f.promote(apitypes.PromoteReq{Project: "redline", From: "prod", To: "staging", Version: "1.4.0"})
	if w.Code != http.StatusConflict {
		t.Fatalf("backwards: %d %q", w.Code, errorOf(w))
	}
}

func TestPromoteSucceedsAndRecordsWho(t *testing.T) {
	f := newDeployFixture(t)
	f.report(t, stagingSvcToken, stagingReport("1.4.0", deployShaA))

	w := f.promote(toProd("1.4.0"))
	if w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.PromoteResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Promoted || out.Version != "1.4.0" || out.ArtifactSHA256 != deployShaA || out.Downgrade {
		t.Fatalf("promote answered %+v", out)
	}
	if got := f.declared(t, "prod"); got != "1.4.0" {
		t.Fatalf("prod declares %q after the promote, want 1.4.0", got)
	}
	// Through updateConfig, so it is on disk too — the env-set write path.
	onDisk, err := config.Load(f.s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := onDisk.LookupEnvironment("redline", "prod"); e.Version != "1.4.0" {
		t.Fatalf("config.json declares %q", e.Version)
	}
	p, err := f.s.users.LatestPromotionOf(context.Background(), "redline", "prod", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.FromEnv != "staging" || p.ArtifactSHA256 != deployShaA || p.Downgrade ||
		!strings.Contains(p.PromotedBy, "carl") || !strings.Contains(p.PromotedBy, "redline-deploy") {
		t.Fatalf("promotion record %+v — want who = carl's redline-deploy token", p)
	}
}

func TestPromoteDowngradeNeedsTheFlagAndIsRecorded(t *testing.T) {
	f := newDeployFixture(t)
	f.report(t, f.pat, stagingReport("1.5.0", deployShaA))
	if w := f.promote(toProd("1.5.0")); w.Code != http.StatusOK {
		t.Fatalf("promote 1.5.0: %d %s", w.Code, w.Body.String())
	}

	// Staging rolls back to 1.4.0 and reports it. Promoting it is a downgrade.
	f.report(t, f.pat, stagingReport("1.4.0", deployShaB))
	w := f.promote(toProd("1.4.0"))
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "lower than prod's declared 1.5.0") {
		t.Fatalf("downgrade without the flag: %d %q", w.Code, errorOf(w))
	}
	if got := f.declared(t, "prod"); got != "1.5.0" {
		t.Fatalf("refused downgrade moved prod to %q", got)
	}

	req := toProd("1.4.0")
	req.AllowDowngrade = true
	f.satisfyGate(t, "1.4.0") // the restore-test gate applies to a rollback too
	w = f.promote(req)
	if w.Code != http.StatusOK {
		t.Fatalf("downgrade with the flag: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.PromoteResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	if !out.Downgrade {
		t.Fatalf("a rollback must be recorded as one: %+v", out)
	}
	p, _ := f.s.users.LatestPromotionOf(context.Background(), "redline", "prod", "1.4.0")
	if p == nil || !p.Downgrade {
		t.Fatalf("promotion row %+v, want downgrade=true", p)
	}

	// The flag on an UPWARD promotion is not a downgrade, and says so.
	f.report(t, f.pat, stagingReport("1.6.0", deployShaA))
	f.satisfyGate(t, "1.6.0")
	up := toProd("1.6.0")
	up.AllowDowngrade = true
	w = f.promote(up)
	_ = json.NewDecoder(w.Body).Decode(&out)
	if w.Code != http.StatusOK || out.Downgrade {
		t.Fatalf("upward with the flag: %d %+v", w.Code, out)
	}
}

// Prerelease ordering goes through db.CompareVersions, hz's one comparator:
// rc.1.1415 is above rc.1.1414 (numeric, not lexical), and the release above both.
func TestPromotePrereleaseOrdering(t *testing.T) {
	f := newDeployFixture(t)
	for _, v := range []string{"1.0.0-rc.1.9", "1.0.0-rc.1.10", "1.0.0-rc.1.1415", "1.0.0"} {
		f.report(t, f.pat, stagingReport(v, deployShaA))
		f.satisfyGate(t, v)
		if w := f.promote(toProd(v)); w.Code != http.StatusOK {
			t.Fatalf("upward to %s refused: %d %q", v, w.Code, errorOf(w))
		}
	}
	f.report(t, f.pat, stagingReport("1.0.0-rc.1.1414", deployShaA))
	if w := f.promote(toProd("1.0.0-rc.1.1414")); w.Code != http.StatusConflict {
		t.Fatalf("rc below the release promoted without the flag: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// H3 — check

func TestDeployCheck(t *testing.T) {
	f := newDeployFixture(t)

	code, out := f.check("prod", "1.4.0", deployShaA)
	if code != http.StatusConflict || out.OK || !strings.Contains(out.Reason, "nothing declared") {
		t.Fatalf("nothing declared: %d %+v", code, out)
	}
	code, out = f.check("qa", "1.4.0", deployShaA)
	if code != http.StatusConflict || !strings.Contains(out.Reason, "nothing declared") {
		t.Fatalf("undeclared rung: %d %+v", code, out)
	}

	f.report(t, f.pat, stagingReport("1.4.0", deployShaA))
	if w := f.promote(toProd("1.4.0")); w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}

	code, out = f.check("prod", "1.4.0", deployShaA)
	if code != http.StatusOK || !out.OK {
		t.Fatalf("the promoted artifact: %d %+v", code, out)
	}
	code, out = f.check("prod", "1.4.0", strings.ToUpper(deployShaA))
	if code != http.StatusOK || !out.OK {
		t.Fatalf("the same digest in uppercase: %d %+v", code, out)
	}
	code, out = f.check("prod", "1.3.9", deployShaA)
	if code != http.StatusConflict || !strings.Contains(out.Reason, "declares 1.4.0, not 1.3.9") {
		t.Fatalf("version mismatch: %d %+v", code, out)
	}
	code, out = f.check("prod", "1.4.0", deployShaB)
	if code != http.StatusConflict || !strings.Contains(out.Reason, "artifact differs from the one promoted") {
		t.Fatalf("artifact mismatch: %d %+v", code, out)
	}
	code, out = f.check("prod", "1.4.0", "abc")
	if code != http.StatusBadRequest || out.OK {
		t.Fatalf("malformed sha: %d %+v", code, out)
	}

	// A version set by hand — `hz env set --version` — that no promotion
	// recorded is refused: the check is not "does the string match".
	next := *f.s.cfg()
	v := "2.0.0"
	if _, err := next.SetEnvironment("redline", "prod", config.EnvironmentPatch{Version: &v}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		t.Fatal(err)
	}
	code, out = f.check("prod", "2.0.0", deployShaA)
	if code != http.StatusConflict || !strings.Contains(out.Reason, "no promotion recorded it") {
		t.Fatalf("set by hand: %d %+v", code, out)
	}
}

// ---------------------------------------------------------------------------
// The rehearsal (plan/plan.md step T), end to end over the handlers: the path
// redline's bin/deploy will walk, and the three refusals it must meet.

func TestRehearsalStagingToProd(t *testing.T) {
	f := newDeployFixture(t)

	// R2: staging's deploy reports 1.4.0 — twice, rebuilt between (refinement 2).
	f.report(t, stagingSvcToken, stagingReport("1.4.0", deployShaA))
	f.report(t, stagingSvcToken, stagingReport("1.4.0", deployShaB))

	// H2: promote staging -> prod. It pins the NEWEST report's artifact.
	w := f.promote(toProd("1.4.0"))
	if w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.PromoteResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	if out.ArtifactSHA256 != deployShaB {
		t.Fatalf("pinned %s, want the latest report's %s", out.ArtifactSHA256, deployShaB)
	}

	// R3 / H3: bin/deploy prod asks before it ships.
	if code, c := f.check("prod", "1.4.0", deployShaB); code != http.StatusOK || !c.OK {
		t.Fatalf("the promoted artifact refused: %d %+v", code, c)
	}
	if code, c := f.check("prod", "1.4.0", deployShaA); code != http.StatusConflict || !strings.Contains(c.Reason, "artifact differs from the one promoted") {
		t.Fatalf("the superseded build of 1.4.0 accepted: %d %+v", code, c)
	}

	// A skip: 1.5.0 was never reported by staging.
	w = f.promote(toProd("1.5.0"))
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "staging has not reported running 1.5.0; it last reported 1.4.0") {
		t.Fatalf("unreported 1.5.0: %d %q", w.Code, errorOf(w))
	}
	if code, _ := f.check("prod", "1.5.0", deployShaA); code != http.StatusConflict {
		t.Fatalf("check for the refused 1.5.0: %d", code)
	}

	// And the record the PCI claim needs.
	lw := f.call(f.s.handleAPIPromotions, http.MethodGet, "/api/v1/promotions?project=redline", f.pat, nil)
	var list []apitypes.PromotionResp
	_ = json.NewDecoder(lw.Body).Decode(&list)
	if len(list) != 1 || list[0].From != "staging" || list[0].To != "prod" || list[0].Version != "1.4.0" ||
		!strings.Contains(list[0].PromotedBy, "carl") || list[0].PromotedAt == "" {
		t.Fatalf("promotion record %+v", list)
	}
	rw := f.call(f.s.handleAPIDeployLatest, http.MethodGet, "/api/v1/deploys/latest", f.pat, nil)
	var latest []apitypes.DeployReportResp
	_ = json.NewDecoder(rw.Body).Decode(&latest)
	if len(latest) != 1 || latest[0].Environment != "staging" || latest[0].ArtifactSHA256 != deployShaB || latest[0].ReportedBy != "service:redline-staging" {
		t.Fatalf("latest reports %+v", latest)
	}
}
