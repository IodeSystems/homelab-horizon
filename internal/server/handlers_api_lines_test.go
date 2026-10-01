package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Release lines, kept backups and the restore-test gate, over the handlers.

const deployShaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func (f deployFixture) keep(t *testing.T, token string, req apitypes.KeptBackupReq) int64 {
	t.Helper()
	w := f.call(f.s.handleAPIKeptBackup, http.MethodPost, "/api/v1/backups/kept", token, req)
	if w.Code != http.StatusOK {
		t.Fatalf("kept backup %+v: %d %s", req, w.Code, w.Body.String())
	}
	var out apitypes.RecordedResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	return out.ID
}

func keptReq(line, sha, by string) apitypes.KeptBackupReq {
	return apitypes.KeptBackupReq{
		Project: "redline", Line: line, BackupSHA256: sha,
		Location: "s3://redline-kept/" + line + ".sql.zst", TakenByVersion: by,
	}
}

func (f deployFixture) restoreTest(t *testing.T, token string, req apitypes.RestoreTestReq) int64 {
	t.Helper()
	w := f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", token, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restore test %+v: %d %s", req, w.Code, w.Body.String())
	}
	var out apitypes.RecordedResp
	_ = json.NewDecoder(w.Body).Decode(&out)
	return out.ID
}

func rtReq(version, line, sha string, passed *bool) apitypes.RestoreTestReq {
	return apitypes.RestoreTestReq{
		Project: "redline", Environment: "staging", Version: version, Line: line,
		BackupSHA256: sha, Passed: passed, BuildURL: "https://ci.test/builds/" + version,
	}
}

func (f deployFixture) lines(t *testing.T) apitypes.ProjectLinesResp {
	t.Helper()
	w := f.call(f.s.handleAPIProjectLines, http.MethodGet, "/api/v1/projects/lines?project=redline", f.pat, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("lines: %d %s", w.Code, w.Body.String())
	}
	var out apitypes.ProjectLinesResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rungOf(t *testing.T, l apitypes.ProjectLinesResp, env string) apitypes.RungLinesResp {
	t.Helper()
	for _, r := range l.Rungs {
		if r.Environment == env {
			return r
		}
	}
	t.Fatalf("no rung %s in %+v", env, l)
	return apitypes.RungLinesResp{}
}

func supportedOf(r apitypes.RungLinesResp) string {
	var out []string
	for _, s := range r.Supported {
		kinds := make([]string, 0, len(s.Why))
		for _, w := range s.Why {
			kinds = append(kinds, w.Kind)
		}
		out = append(out, s.Line+"="+strings.Join(kinds, ","))
	}
	return strings.Join(out, " ")
}

// satisfyGate gives every line prod supports NOW a kept backup (when it has
// none) and a passing restore test of version against it — for the H2 tests,
// whose subject is not the restore gate.
func (f deployFixture) satisfyGate(t *testing.T, version string) {
	t.Helper()
	for _, sl := range rungOf(t, f.lines(t), "prod").Supported {
		sha := deployShaC
		if sl.KeptBackup == nil {
			f.keep(t, f.pat, keptReq(sl.Line, sha, sl.Line))
		} else {
			sha = sl.KeptBackup.BackupSHA256
		}
		f.restoreTest(t, f.pat, rtReq(version, sl.Line, sha, yes()))
	}
}

func promoteResp(t *testing.T, w interface{ Result() *http.Response }) apitypes.PromoteResp {
	t.Helper()
	var out apitypes.PromoteResp
	if err := json.NewDecoder(w.Result().Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// ---------------------------------------------------------------------------
// build_url

func TestBuildURLStoredReturnedAndPinned(t *testing.T) {
	f := newDeployFixture(t)
	for name, bad := range map[string]string{
		"too long":     "https://ci.test/" + strings.Repeat("x", db.MaxLocatorLen),
		"control char": "https://ci.test/builds/7\nX-Injected: 1",
	} {
		req := stagingReport("1.4.0", deployShaA)
		req.BuildURL = bad
		w := f.call(f.s.handleAPIDeployReport, http.MethodPost, "/api/v1/deploys/report", f.pat, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "invalid locator") {
			t.Errorf("%s: %d %q, want 400", name, w.Code, errorOf(w))
		}
	}
	// Any scheme: an R0 build names a bucket path.
	req := stagingReport("1.4.0", deployShaA)
	req.BuildURL = "gs://redline-builds/dist/builds/7/"
	f.report(t, stagingSvcToken, req)

	rw := f.call(f.s.handleAPIDeployLatest, http.MethodGet, "/api/v1/deploys/latest", f.pat, nil)
	var latest []apitypes.DeployReportResp
	_ = json.NewDecoder(rw.Body).Decode(&latest)
	if len(latest) != 1 || latest[0].BuildURL != "gs://redline-builds/dist/builds/7/" {
		t.Fatalf("latest = %+v", latest)
	}

	w := f.promote(toProd("1.4.0"))
	if w.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	if out := promoteResp(t, w); out.BuildURL != "gs://redline-builds/dist/builds/7/" {
		t.Fatalf("promote answered %+v", out)
	}
	// A later report with another build_url does not move the pinned one.
	later := stagingReport("1.4.1", deployShaB)
	later.BuildURL = "gs://redline-builds/dist/builds/8/"
	f.report(t, stagingSvcToken, later)
	lw := f.call(f.s.handleAPIPromotions, http.MethodGet, "/api/v1/promotions?project=redline", f.pat, nil)
	var list []apitypes.PromotionResp
	_ = json.NewDecoder(lw.Body).Decode(&list)
	if len(list) != 1 || list[0].BuildURL != "gs://redline-builds/dist/builds/7/" || list[0].RestoreGate != db.RestoreGateNoneRequired {
		t.Fatalf("promotion record %+v", list)
	}
}

// ---------------------------------------------------------------------------
// Scope

func TestRestoreTestScopeIsTheRung(t *testing.T) {
	f := newDeployFixture(t)
	f.restoreTest(t, stagingSvcToken, rtReq("1.0.1-0.3", "1.0.0", deployShaA, yes()))

	prod := rtReq("1.0.1-0.3", "1.0.0", deployShaA, yes())
	prod.Environment = "prod"
	w := f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", stagingSvcToken, prod)
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "only for redline/staging") {
		t.Fatalf("staging's token reporting prod: %d %q", w.Code, errorOf(w))
	}
	w = f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", looseSvcToken, rtReq("1.0.1-0.3", "1.0.0", deployShaA, yes()))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unattributed token: %d", w.Code)
	}
	w = f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", "", rtReq("1.0.1-0.3", "1.0.0", deployShaA, yes()))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	// Absent is not false.
	w = f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", stagingSvcToken, rtReq("1.0.1-0.3", "1.0.0", deployShaA, nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "passed is required") {
		t.Fatalf("absent passed: %d %q", w.Code, errorOf(w))
	}
	for name, mut := range map[string]func(*apitypes.RestoreTestReq){
		"line is a version": func(r *apitypes.RestoreTestReq) { r.Line = "1.0.0-0.1" },
		"bad sha":           func(r *apitypes.RestoreTestReq) { r.BackupSHA256 = "abc" },
		"bad version":       func(r *apitypes.RestoreTestReq) { r.Version = "v1.0.1" },
	} {
		req := rtReq("1.0.1-0.3", "1.0.0", deployShaA, yes())
		mut(&req)
		if w := f.call(f.s.handleAPIRestoreTestReport, http.MethodPost, "/api/v1/restore-tests/report", f.pat, req); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", name, w.Code, errorOf(w))
		}
	}
}

func TestKeptBackupScopeIsTheProject(t *testing.T) {
	f := newDeployFixture(t)
	// prod's token, any rung of redline: a backup belongs to a line.
	f.keep(t, prodSvcToken, keptReq("1.0.0", deployShaA, "1.0.0-0.1"))
	f.keep(t, stagingSvcToken, keptReq("1.0.0", deployShaB, "1.0.0-1.1"))

	other := keptReq("1.0.0", deployShaA, "1.0.0-0.1")
	other.Project = "nope"
	w := f.call(f.s.handleAPIKeptBackup, http.MethodPost, "/api/v1/backups/kept", prodSvcToken, other)
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "only for redline") {
		t.Fatalf("another project: %d %q", w.Code, errorOf(w))
	}
	w = f.call(f.s.handleAPIKeptBackup, http.MethodPost, "/api/v1/backups/kept", looseSvcToken, keptReq("1.0.0", deployShaA, "1.0.0-0.1"))
	if w.Code != http.StatusForbidden || !strings.Contains(errorOf(w), "attributed to none") {
		t.Fatalf("unattributed: %d %q", w.Code, errorOf(w))
	}
	w = f.call(f.s.handleAPIKeptBackup, http.MethodPost, "/api/v1/backups/kept", f.pat, other)
	if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), `no project "nope"`) {
		t.Fatalf("admin, undeclared project: %d %q", w.Code, errorOf(w))
	}
	off := keptReq("1.0.0", deployShaA, "1.0.1-0.1")
	w = f.call(f.s.handleAPIKeptBackup, http.MethodPost, "/api/v1/backups/kept", f.pat, off)
	if w.Code != http.StatusBadRequest || !strings.Contains(errorOf(w), "is of line 1.0.1, not 1.0.0") {
		t.Fatalf("taken off the line: %d %q", w.Code, errorOf(w))
	}
	b, err := f.s.users.LatestKeptBackup(context.Background(), "redline", "1.0.0")
	if err != nil || b.BackupSHA256 != deployShaB || b.RecordedBy != "service:redline-staging" {
		t.Fatalf("newest wins: %+v, %v", b, err)
	}
}

// ---------------------------------------------------------------------------
// The gate

// prodOn promotes a first release into prod (none required), putting prod on
// line 1.0.0.
func (f deployFixture) prodOn(t *testing.T, version string) {
	t.Helper()
	f.report(t, f.pat, stagingReport(version, deployShaA))
	if w := f.promote(toProd(version)); w.Code != http.StatusOK {
		t.Fatalf("first release %s: %d %s", version, w.Code, w.Body.String())
	}
}

func TestFirstReleaseSaysNoneRequired(t *testing.T) {
	f := newDeployFixture(t)
	if r := rungOf(t, f.lines(t), "prod"); r.NoneRequired != "none required: redline/prod has no supported line yet" || len(r.Supported) != 0 {
		t.Fatalf("lines before the first release: %+v", r)
	}
	f.report(t, f.pat, stagingReport("1.0.0-0.1", deployShaA))
	w := f.promote(toProd("1.0.0-0.1"))
	if w.Code != http.StatusOK {
		t.Fatalf("first release: %d %s", w.Code, w.Body.String())
	}
	out := promoteResp(t, w)
	if out.RestoreTests != "none required: redline/prod has no supported line yet" || out.LinesChecked == nil || len(out.LinesChecked) != 0 {
		t.Fatalf("first release answered %+v — the gate must SAY none required", out)
	}
	// The raw body carries the sentence, not an absent key.
	if !strings.Contains(w.Body.String(), `"restore_tests":"none required: redline/prod has no supported line yet"`) {
		t.Fatalf("restore_tests absent from the wire: %s", w.Body.String())
	}
}

func TestGateRefusesEachCase(t *testing.T) {
	f := newDeployFixture(t)
	f.prodOn(t, "1.0.0-0.1")
	f.report(t, f.pat, stagingReport("1.0.0-1.1", deployShaB))

	refusal := func(want string) {
		t.Helper()
		w := f.promote(toProd("1.0.0-1.1"))
		if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), want) {
			t.Fatalf("want 409 %q, got %d %q", want, w.Code, errorOf(w))
		}
		if got := f.declared(t, "prod"); got != "1.0.0-0.1" {
			t.Fatalf("a refused promote moved prod to %q", got)
		}
	}

	refusal("line 1.0.0 is supported on redline/prod but has no kept backup")
	f.keep(t, prodSvcToken, keptReq("1.0.0", deployShaA, "1.0.0-0.1"))
	refusal("1.0.0-1.1 has no restore test against line 1.0.0's kept backup aaaaaaaaaaaa")
	f.restoreTest(t, stagingSvcToken, rtReq("1.0.0-1.1", "1.0.0", deployShaA, no()))
	refusal("1.0.0-1.1 failed its restore test against line 1.0.0 (https://ci.test/builds/1.0.0-1.1)")

	// A passing test of ANOTHER rung does not count: the evidence is the source's.
	other := rtReq("1.0.0-1.1", "1.0.0", deployShaA, yes())
	other.Environment = "prod"
	f.restoreTest(t, prodSvcToken, other)
	refusal("failed its restore test")

	f.restoreTest(t, stagingSvcToken, rtReq("1.0.0-1.1", "1.0.0", deployShaA, yes()))
	// A newer kept backup supersedes the one the test restored.
	f.keep(t, prodSvcToken, keptReq("1.0.0", deployShaC, "1.0.0-0.1"))
	refusal("1.0.0-1.1 has no restore test against line 1.0.0's kept backup cccccccccccc — its newest test of line 1.0.0")
	refusal("which a newer kept backup superseded")

	id := f.restoreTest(t, stagingSvcToken, rtReq("1.0.0-1.1", "1.0.0", deployShaC, yes()))
	w := f.promote(toProd("1.0.0-1.1"))
	if w.Code != http.StatusOK {
		t.Fatalf("passed: %d %s", w.Code, w.Body.String())
	}
	out := promoteResp(t, w)
	if len(out.LinesChecked) != 1 || out.LinesChecked[0].Line != "1.0.0" || out.LinesChecked[0].RestoreTestID != id ||
		out.LinesChecked[0].BackupSHA256 != deployShaC || out.LinesChecked[0].Why != "current" ||
		!strings.Contains(out.RestoreTests, "1.0.0 (restore test #") {
		t.Fatalf("success answered %+v", out)
	}
	pl, err := f.s.users.PromotionLines(context.Background(), out.ID)
	if err != nil || len(pl) != 1 || pl[0].RestoreTestID != id {
		t.Fatalf("checked lines on the record: %+v, %v", pl, err)
	}
}

// Every failing line is its own sentence, not one generic refusal.
func TestGateNamesEveryFailingLine(t *testing.T) {
	f := newDeployFixture(t)
	f.prodOn(t, "1.0.0-0.1")
	next := *f.s.cfg()
	if err := next.PinLine("redline", "0.9.0", "the legacy import still restores 0.9"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		t.Fatal(err)
	}
	f.report(t, f.pat, stagingReport("1.0.1-0.1", deployShaB))
	w := f.promote(toProd("1.0.1-0.1"))
	msg := errorOf(w)
	if w.Code != http.StatusConflict || strings.Count(msg, "\n") != 1 ||
		!strings.Contains(msg, "line 1.0.0 is supported on redline/prod but has no kept backup") ||
		!strings.Contains(msg, "line 0.9.0 is supported on redline/prod but has no kept backup") {
		t.Fatalf("two lines, two sentences: %d %q", w.Code, msg)
	}
}

func TestGateRefusesWhatItCannotRead(t *testing.T) {
	f := newDeployFixture(t)
	next := *f.s.cfg()
	v := "deb-1.2"
	if _, err := next.SetEnvironment("redline", "prod", config.EnvironmentPatch{Version: &v}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		t.Fatal(err)
	}
	f.report(t, f.pat, stagingReport("1.0.0", deployShaA))
	req := toProd("1.0.0")
	req.AllowDowngrade = true // the downgrade check cannot order deb-1.2 either
	w := f.promote(req)
	if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), `hz cannot say which lines redline/prod supports: redline/prod declares "deb-1.2"`) {
		t.Fatalf("unreadable declaration: %d %q", w.Code, errorOf(w))
	}
	l := f.lines(t)
	if r := rungOf(t, l, "prod"); r.NoneRequired != "" || len(r.Gaps) != 1 {
		t.Fatalf("a gap is not none required: %+v", r)
	}
	if l.RetiredUnknown == "" || len(l.Retired) != 0 {
		t.Fatalf("retired with a gap must be unknown: %+v", l)
	}
}

// ---------------------------------------------------------------------------
// Pins and the lines read

func TestPinsAndRetiredLines(t *testing.T) {
	f := newDeployFixture(t)
	pin := func(req apitypes.LinePinReq) *http.Response {
		return f.call(f.s.handleAPILinePin(true), http.MethodPost, "/api/v1/projects/lines/pin", f.pat, req).Result()
	}
	if r := pin(apitypes.LinePinReq{Project: "redline", Line: "1.0.0"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("pin without a reason: %d", r.StatusCode)
	}
	if r := pin(apitypes.LinePinReq{Project: "redline", Line: "1.0", Reason: "x"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("pin of a non-line: %d", r.StatusCode)
	}
	if r := pin(apitypes.LinePinReq{Project: "redline", Line: "1.0.0-0.1", Reason: "x"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("pin of a version: %d", r.StatusCode)
	}
	if r := pin(apitypes.LinePinReq{Project: "redline", Line: "0.9.0", Reason: "legacy import"}); r.StatusCode != http.StatusOK {
		t.Fatalf("pin: %d", r.StatusCode)
	}
	onDisk, err := config.Load(f.s.configPath)
	if err != nil || len(onDisk.ProjectPins("redline")) != 1 || onDisk.ProjectPins("redline")[0].Reason != "legacy import" {
		t.Fatalf("the pin is declared state, on disk: %+v, %v", onDisk.ProjectPins("redline"), err)
	}

	// prod: 1.0.0 then 1.0.1 then 1.0.2 — supported {1.0.2 current, 1.0.1 prior, 0.9.0 pinned}.
	f.keep(t, f.pat, keptReq("0.9.0", deployShaA, "0.9.0"))
	for _, v := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		f.report(t, f.pat, stagingReport(v, deployShaA))
		f.satisfyGate(t, v)
		if w := f.promote(toProd(v)); w.Code != http.StatusOK {
			t.Fatalf("promote %s: %d %s", v, w.Code, w.Body.String())
		}
		if v != "1.0.2" {
			f.keep(t, f.pat, keptReq(v, deployShaB, v))
		}
	}
	l := f.lines(t)
	prod := rungOf(t, l, "prod")
	if got := supportedOf(prod); got != "1.0.2=current 1.0.1=prior 0.9.0=pinned" {
		t.Fatalf("prod supports %q", got)
	}
	if prod.Supported[2].Why[0].Detail != "legacy import" || prod.Supported[0].KeptBackup != nil ||
		prod.Supported[1].KeptBackup == nil || prod.Supported[1].KeptBackup.BackupSHA256 != deployShaB {
		t.Fatalf("prod lines %+v", prod.Supported)
	}
	// A pin is the PROJECT's: staging, which declares nothing and was never
	// promoted into, supports the pinned line and nothing else. It has no
	// `from`, so no restore test is asked of it.
	if s := rungOf(t, l, "staging"); supportedOf(s) != "0.9.0=pinned" || s.NoneRequired != "" || s.Supported[0].Restore.Status != restoreNoSource {
		t.Fatalf("staging %+v", s)
	}
	// 1.0.0 has a kept backup and is supported nowhere: retired. hz says so.
	if len(l.Retired) != 1 || l.Retired[0].Line != "1.0.0" || l.RetiredUnknown != "" {
		t.Fatalf("retired = %+v (%q)", l.Retired, l.RetiredUnknown)
	}
	if _, err := f.s.users.LatestKeptBackup(context.Background(), "redline", "1.0.0"); err != nil {
		t.Fatalf("hz deleted a retired line's record: %v", err)
	}
	// The restore column is the NEXT promotion's evidence: staging's newest
	// report (1.0.2), which satisfyGate tested against 1.0.1's kept backup.
	if r := prod.Supported[1].Restore; r.Version != "1.0.2" || r.Status != restorePassed || r.Test == nil {
		t.Fatalf("restore status of 1.0.1 for 1.0.2: %+v", r)
	}

	// Unpin: 0.9.0 is no longer supported, and is retired too.
	w := f.call(f.s.handleAPILinePin(false), http.MethodPost, "/api/v1/projects/lines/unpin", f.pat, apitypes.LinePinReq{Project: "redline", Line: "0.9.0"})
	if w.Code != http.StatusOK {
		t.Fatalf("unpin: %d %s", w.Code, w.Body.String())
	}
	if l := f.lines(t); len(l.Retired) != 2 || len(rungOf(t, l, "prod").Supported) != 2 {
		t.Fatalf("after unpin: %+v", l)
	}
	w = f.call(f.s.handleAPILinePin(false), http.MethodPost, "/api/v1/projects/lines/unpin", f.pat, apitypes.LinePinReq{Project: "redline", Line: "0.9.0"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unpin of an unpinned line: %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// The rehearsal: the path redline walks, from its first release through a
// hotfix to its second line. Staging is the source rung throughout; "dev
// 1.0.1-0.3" arrives on staging as the next line.

func TestRehearsalReleaseLines(t *testing.T) {
	f := newDeployFixture(t)

	// 1. The first release. prod declares nothing and was never promoted into.
	first := stagingReport("1.0.0-0.1", deployShaA)
	first.BuildURL = "https://ci.test/builds/1"
	f.report(t, stagingSvcToken, first)
	w := f.promote(toProd("1.0.0-0.1"))
	if w.Code != http.StatusOK {
		t.Fatalf("first release: %d %s", w.Code, w.Body.String())
	}
	if out := promoteResp(t, w); out.RestoreTests != "none required: redline/prod has no supported line yet" {
		t.Fatalf("first release: %+v", out)
	}

	// 2. A hotfix on staging: 1.0.0.1#1 -> 1.0.0-1.1, line 1.0.0.
	f.report(t, stagingSvcToken, stagingReport("1.0.0-1.1", deployShaB))
	expect := func(want string) {
		t.Helper()
		w := f.promote(toProd("1.0.0-1.1"))
		if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), want) {
			t.Fatalf("want refusal %q, got %d %q", want, w.Code, errorOf(w))
		}
	}
	expect("line 1.0.0 is supported on redline/prod but has no kept backup")

	// 3. prod keeps a backup of line 1.0.0.
	f.keep(t, prodSvcToken, keptReq("1.0.0", deployShaC, "1.0.0-0.1"))
	expect("1.0.0-1.1 has no restore test against line 1.0.0's kept backup cccccccccccc")

	// 4. The restore test fails, then passes.
	f.restoreTest(t, stagingSvcToken, rtReq("1.0.0-1.1", "1.0.0", deployShaC, no()))
	expect("1.0.0-1.1 failed its restore test against line 1.0.0 (https://ci.test/builds/1.0.0-1.1)")
	f.restoreTest(t, stagingSvcToken, rtReq("1.0.0-1.1", "1.0.0", deployShaC, yes()))
	if w := f.promote(toProd("1.0.0-1.1")); w.Code != http.StatusOK {
		t.Fatalf("hotfix: %d %s", w.Code, w.Body.String())
	}

	// 5. A hotfix promoted on the same line: still ONE supported line.
	if got := supportedOf(rungOf(t, f.lines(t), "prod")); got != "1.0.0=current" {
		t.Fatalf("after the hotfix prod supports %q", got)
	}

	// 6. The next line arrives: 1.0.1-0.3. Until it lands, 1.0.0 is the only
	//    supported line and the only test asked of it.
	f.report(t, stagingSvcToken, stagingReport("1.0.1-0.3", deployShaA))
	if r := rungOf(t, f.lines(t), "prod"); supportedOf(r) != "1.0.0=current" || r.Supported[0].Restore.Status != restoreMissing ||
		r.Supported[0].Restore.Version != "1.0.1-0.3" {
		t.Fatalf("before 1.0.1 lands: %+v", r)
	}
	f.restoreTest(t, stagingSvcToken, rtReq("1.0.1-0.3", "1.0.0", deployShaC, yes()))
	w = f.promote(toProd("1.0.1-0.3"))
	if w.Code != http.StatusOK {
		t.Fatalf("1.0.1-0.3: %d %s", w.Code, w.Body.String())
	}
	if out := promoteResp(t, w); len(out.LinesChecked) != 1 || out.LinesChecked[0].Line != "1.0.0" {
		t.Fatalf("1.0.1-0.3 checked %+v — want line 1.0.0 only, computed BEFORE the promotion", out.LinesChecked)
	}

	// 7. After: current and always one prior.
	prod := rungOf(t, f.lines(t), "prod")
	if got := supportedOf(prod); got != "1.0.1=current 1.0.0=prior" {
		t.Fatalf("after 1.0.1 prod supports %q", got)
	}
	if prod.Supported[0].KeptBackup != nil || prod.Supported[0].Restore.Status != restoreNoKeptBackup ||
		prod.Supported[1].KeptBackup == nil {
		t.Fatalf("1.0.1 has no kept backup yet; 1.0.0 has: %+v", prod.Supported)
	}

	// 8. The record: none-required, then checked, checked.
	list, err := f.s.users.Promotions(context.Background(), "redline", 0)
	if err != nil || len(list) != 3 || list[2].RestoreGate != db.RestoreGateNoneRequired ||
		list[1].RestoreGate != db.RestoreGateChecked || list[0].RestoreGate != db.RestoreGateChecked ||
		list[2].BuildURL != "https://ci.test/builds/1" {
		t.Fatalf("promotion record %+v, %v", list, err)
	}
}

// A rollback may skip the restore-test gate, explicitly (skipRestoreTests), and
// only to the exact build the target already ran — operator, 2026-10-01.
func TestRollbackMaySkipRestoreTestsOnlyForTheBuildThatRan(t *testing.T) {
	f := newDeployFixture(t)
	f.prodOn(t, "1.0.0-0.1") // first release, artifact A
	f.report(t, f.pat, stagingReport("1.0.1-0.3", deployShaB))
	f.satisfyGate(t, "1.0.1-0.3")
	if w := f.promote(toProd("1.0.1-0.3")); w.Code != http.StatusOK {
		t.Fatalf("1.0.1-0.3: %d %s", w.Code, w.Body.String())
	}

	refusal := func(req apitypes.PromoteReq, want string) {
		t.Helper()
		w := f.promote(req)
		if w.Code != http.StatusConflict || !strings.Contains(errorOf(w), want) {
			t.Fatalf("want 409 %q, got %d %q", want, w.Code, errorOf(w))
		}
		if got := f.declared(t, "prod"); got != "1.0.1-0.3" {
			t.Fatalf("a refused rollback moved prod to %q", got)
		}
	}
	rollback := func(version string, skip bool) apitypes.PromoteReq {
		r := toProd(version)
		r.AllowDowngrade, r.SkipRestoreTests = true, skip
		return r
	}

	// Never promoted into prod: the skip is refused.
	f.report(t, f.pat, stagingReport("1.0.0-0.0", deployShaA))
	refusal(rollback("1.0.0-0.0", true), "1.0.0-0.0 was never promoted into it")

	// The version prod ran, but a REBUILD of it: refused.
	f.report(t, f.pat, stagingReport("1.0.0-0.1", deployShaB))
	refusal(rollback("1.0.0-0.1", true), "skipRestoreTests covers only the build that ran there")

	// The build that ran — without the flag, the gate still applies.
	f.report(t, f.pat, stagingReport("1.0.0-0.1", deployShaA))
	refusal(rollback("1.0.0-0.1", false), "has no restore test against line")
	// The skip does not imply the downgrade.
	noDown := rollback("1.0.0-0.1", true)
	noDown.AllowDowngrade = false
	refusal(noDown, "pass allowDowngrade")

	w := f.promote(rollback("1.0.0-0.1", true))
	if w.Code != http.StatusOK {
		t.Fatalf("rollback with skip: %d %s", w.Code, w.Body.String())
	}
	out := promoteResp(t, w)
	if !out.Downgrade || !strings.HasPrefix(out.RestoreTests, "skipped: redline/prod ran 1.0.0-0.1 (this artifact) before") ||
		len(out.LinesChecked) != 0 {
		t.Fatalf("rollback answered %+v", out)
	}
	p, err := f.s.users.LatestPromotionOf(context.Background(), "redline", "prod", "1.0.0-0.1")
	if err != nil || p.ID != out.ID || p.RestoreGate != db.RestoreGateSkipped || !p.Downgrade {
		t.Fatalf("the record: %+v, %v", p, err)
	}
	if code, c := f.check("prod", "1.0.0-0.1", deployShaA); code != http.StatusOK || !c.OK {
		t.Fatalf("check after rollback: %d %+v", code, c)
	}
}
