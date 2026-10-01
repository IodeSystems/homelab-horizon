package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const shaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestLineOfIsTheSemverCore(t *testing.T) {
	for v, want := range map[string]string{
		"1.9.0":       "1.9.0",
		"1.9.0-1.2":   "1.9.0", // a hotfix stays on its line
		"1.9.1-0.7":   "1.9.1", // redline 1.9.1.0#7
		"1.10.0-rc.1": "1.10.0",
		"1.09.0-0.1":  "1.9.0", // one spelling per line
	} {
		got, err := LineOf(v)
		if err != nil || got != want {
			t.Errorf("LineOf(%q) = %q, %v; want %q", v, got, err, want)
		}
	}
	for _, bad := range []string{"", "v1.9.0", "1.9", "1.9.0+7", "latest"} {
		if _, err := LineOf(bad); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("LineOf(%q): err = %v, want ErrInvalidVersion", bad, err)
		}
	}
	if err := CheckLine("1.9.0"); err != nil {
		t.Errorf("1.9.0 is a line: %v", err)
	}
	for _, bad := range []string{"1.9.0-1.2", "1.09.0", "1.9", ""} {
		if err := CheckLine(bad); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("CheckLine(%q): err = %v, want ErrInvalidVersion", bad, err)
		}
	}
}

func lines(r RungLines) []string {
	out := make([]string, 0, len(r.Supported))
	for _, s := range r.Supported {
		out = append(out, s.Line+"="+s.Kinds())
	}
	return out
}

// ran is one prod deploy report, as DeployReportsOf returns it.
func ran(id int64, version string) DeployReport {
	return DeployReport{
		ID: id, Project: "redline", Environment: "prod", App: "redline", Version: version,
		Host: "ubuntu@192.0.2.161", ReportedAt: time.Date(2026, 10, 1, 17, 0, int(id), 0, time.UTC),
	}
}

func TestDeriveSupportedLines(t *testing.T) {
	// Reported nothing, nothing pinned: no line, no gap — the gate requires none.
	if r := DeriveSupportedLines("redline", "prod", nil, nil); len(r.Supported) != 0 || len(r.Gaps) != 0 {
		t.Fatalf("no report = %+v, want no line and no gap", r)
	}
	if got := NoneRequired("redline", "prod"); got != "none required: redline/prod has reported running nothing yet" {
		t.Fatalf("the sentence = %q", got)
	}

	// The current line is the NEWEST report's, and its why names the report.
	r := DeriveSupportedLines("redline", "prod", []DeployReport{ran(7, "1.0.0-0.4")}, nil)
	if got := strings.Join(lines(r), " "); got != "1.0.0=current" {
		t.Fatalf("one report = %q", got)
	}
	if d := r.Supported[0].Why[0].Detail; d != "redline/prod reported running 1.0.0-0.4 (report #7, 2026-10-01T17:00:07Z, ubuntu@192.0.2.161)" {
		t.Fatalf("current names the report: %q", d)
	}

	// current + prior; the prior SKIPS same-line reports (hotfixes, redeploys).
	reports := []DeployReport{ran(5, "1.0.1-0.3"), ran(4, "1.0.0-1.1"), ran(3, "1.0.0-0.1")}
	r = DeriveSupportedLines("redline", "prod", reports, nil)
	if got := strings.Join(lines(r), " "); got != "1.0.1=current 1.0.0=prior" {
		t.Fatalf("current+prior = %q", got)
	}
	if !strings.Contains(r.Supported[1].Why[0].Detail, "reported running 1.0.0-1.1 (report #4,") {
		t.Fatalf("the prior names the NEWEST report of its line: %+v", r.Supported[1])
	}
	// Hotfixes on the current line do not produce a prior.
	reports = []DeployReport{ran(7, "1.0.1-2.1"), ran(6, "1.0.1-1.1"), ran(5, "1.0.1-0.3"), ran(4, "1.0.0-1.1")}
	if got := strings.Join(lines(DeriveSupportedLines("redline", "prod", reports, nil)), " "); got != "1.0.1=current 1.0.0=prior" {
		t.Fatalf("same-line reports skipped = %q", got)
	}
	// Same-line reports only: no prior.
	reports = []DeployReport{ran(3, "1.0.0-1.1"), ran(2, "1.0.0-0.1"), ran(1, "1.0.0-0.1")}
	if got := strings.Join(lines(DeriveSupportedLines("redline", "prod", reports, nil)), " "); got != "1.0.0=current" {
		t.Fatalf("one line only = %q", got)
	}
	// Only ONE prior, however many lines came before.
	reports = []DeployReport{ran(3, "1.2.0"), ran(2, "1.1.0"), ran(1, "1.0.0")}
	if got := strings.Join(lines(DeriveSupportedLines("redline", "prod", reports, nil)), " "); got != "1.2.0=current 1.1.0=prior" {
		t.Fatalf("one prior = %q", got)
	}
	// A rollback that REPORTED: the line rolled back to is current, the line
	// it left is prior.
	rollback := []DeployReport{ran(4, "1.0.0-0.1"), ran(3, "1.0.1-0.3"), ran(2, "1.0.0-0.1")}
	if got := strings.Join(lines(DeriveSupportedLines("redline", "prod", rollback, nil)), " "); got != "1.0.0=current 1.0.1=prior" {
		t.Fatalf("after a reported rollback = %q", got)
	}

	// A pin adds its line, with its reason; a pin of a supported line merges.
	pins := []LinePin{{Line: "1.0.0", Reason: "customer X stays on 1.0 until March"}, {Line: "1.2.0", Reason: "belt"}}
	r = DeriveSupportedLines("redline", "prod", reports, pins)
	if got := strings.Join(lines(r), " "); got != "1.2.0=current,pinned 1.1.0=prior 1.0.0=pinned" {
		t.Fatalf("pinned = %q", got)
	}
	if r.Supported[2].Why[0].Detail != "customer X stays on 1.0 until March" {
		t.Fatalf("the pin's reason is its why: %+v", r.Supported[2])
	}
	// Pins alone, nothing reported: the pinned line, no current.
	if got := strings.Join(lines(DeriveSupportedLines("redline", "prod", nil, pins[:1])), " "); got != "1.0.0=pinned" {
		t.Fatalf("pins only = %q", got)
	}

	// Unknown is not empty: a report hz cannot read a line from, and a pin
	// that is not a line, are gaps — never "no supported line".
	r = DeriveSupportedLines("redline", "prod", []DeployReport{ran(9, "deb-1.2")}, []LinePin{{Line: "1.9", Reason: "typo"}})
	if len(r.Supported) != 0 || len(r.Gaps) != 2 ||
		!strings.Contains(r.Gaps[0], `report #9 of redline/prod records "deb-1.2"`) || !strings.Contains(r.Gaps[1], `pins "1.9"`) {
		t.Fatalf("gaps = %+v", r)
	}
	// An unreadable NEWEST report leaves the current line unknown (a gap); the
	// newest readable line before it is still prior — it is what ran there.
	r = DeriveSupportedLines("redline", "prod", []DeployReport{ran(9, "deb-1.2"), ran(8, "1.0.0-0.1")}, nil)
	if got := strings.Join(lines(r), " "); got != "1.0.0=prior" || len(r.Gaps) != 1 {
		t.Fatalf("unreadable newest = %q %+v", got, r.Gaps)
	}
}

func kept(line, sha, by string) KeptBackup {
	return KeptBackup{
		Project: "redline", Line: line, BackupSHA256: sha, Location: "s3://redline-kept/" + line + ".sql.zst",
		TakenByVersion: by, RecordedBy: "service:redline-prod",
	}
}

func TestKeptBackupsNewestWinsAndAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.LatestKeptBackup(ctx, "redline", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no kept backup: %v", err)
	}
	if _, err := d.RecordKeptBackup(ctx, kept("1.0.0", shaA, "1.0.0-0.1")); err != nil {
		t.Fatal(err)
	}
	id, err := d.RecordKeptBackup(ctx, kept("1.0.0", shaB, "1.0.0-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordKeptBackup(ctx, kept("1.1.0", shaC, "1.1.0-0.1")); err != nil {
		t.Fatal(err)
	}
	b, err := d.LatestKeptBackup(ctx, "redline", "1.0.0")
	if err != nil || b.ID != id || b.BackupSHA256 != shaB || b.RecordedAt.IsZero() {
		t.Fatalf("newest wins: %+v, %v", b, err)
	}
	all, err := d.LatestKeptBackups(ctx, "redline")
	if err != nil || len(all) != 2 || all[0].BackupSHA256 != shaB || all[1].Line != "1.1.0" {
		t.Fatalf("latest per line = %+v, %v", all, err)
	}
	if other, _ := d.LatestKeptBackups(ctx, "other"); len(other) != 0 {
		t.Fatalf("another project's = %+v", other)
	}

	for _, stmt := range []string{
		`UPDATE kept_backups SET backup_sha256 = '` + shaC + `'`,
		`DELETE FROM kept_backups`,
	} {
		if _, err := d.ExecContext(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", stmt, err)
		}
	}
}

func TestKeptBackupRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	for name, tc := range map[string]struct {
		mut  func(*KeptBackup)
		want error
	}{
		"line is a version":    {func(b *KeptBackup) { b.Line = "1.0.0-0.1" }, ErrInvalidVersion},
		"taken off the line":   {func(b *KeptBackup) { b.TakenByVersion = "1.0.1-0.1" }, ErrInvalidVersion},
		"bad sha":              {func(b *KeptBackup) { b.BackupSHA256 = "abc" }, ErrInvalidSHA256},
		"no location":          {func(b *KeptBackup) { b.Location = " " }, ErrInvalidLocator},
		"control in location":  {func(b *KeptBackup) { b.Location = "s3://x\n" }, ErrInvalidLocator},
		"build_url too long":   {func(b *KeptBackup) { b.BuildURL = strings.Repeat("x", MaxLocatorLen+1) }, ErrInvalidLocator},
		"bad taken_by_version": {func(b *KeptBackup) { b.TakenByVersion = "v1.0.0" }, ErrInvalidVersion},
	} {
		b := kept("1.0.0", shaA, "1.0.0-0.1")
		tc.mut(&b)
		if _, err := d.RecordKeptBackup(ctx, b); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func rtest(version, line, sha string, passed bool) RestoreTest {
	return RestoreTest{
		Project: "redline", Environment: "staging", Version: version, Line: line, BackupSHA256: sha,
		Passed: passed, BuildURL: "https://ci.test/builds/7", ReportedBy: "service:redline-staging",
	}
}

func TestRestoreTestsNewestWinsAndAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.LatestRestoreTest(ctx, "redline", "staging", "1.0.1-0.3", "1.0.0", shaA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no test: %v", err)
	}
	if _, err := d.RecordRestoreTest(ctx, rtest("1.0.1-0.3", "1.0.0", shaA, false)); err != nil {
		t.Fatal(err)
	}
	id, err := d.RecordRestoreTest(ctx, rtest("1.0.1-0.3", "1.0.0", shaA, true))
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.LatestRestoreTest(ctx, "redline", "staging", "1.0.1-0.3", "1.0.0", shaA)
	if err != nil || got.ID != id || !got.Passed || got.BuildURL != "https://ci.test/builds/7" {
		t.Fatalf("newest test = %+v, %v", got, err)
	}
	// Against another backup of the line: found only by the any-backup query.
	if _, err := d.LatestRestoreTest(ctx, "redline", "staging", "1.0.1-0.3", "1.0.0", shaB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("against another sha: %v", err)
	}
	if any, err := d.LatestRestoreTest(ctx, "redline", "staging", "1.0.1-0.3", "1.0.0", ""); err != nil || any.ID != id {
		t.Fatalf("any backup: %+v, %v", any, err)
	}
	for _, stmt := range []string{`UPDATE restore_tests SET passed = 1`, `DELETE FROM restore_tests`} {
		if _, err := d.ExecContext(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", stmt, err)
		}
	}
	if _, err := d.RecordRestoreTest(ctx, rtest("1.0.1-0.3", "1.0.0-0.1", shaA, true)); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("a line that is a version: %v", err)
	}
}

func TestPromotionPinsBuildURLAndRecordsItsLines(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	kb, _ := d.RecordKeptBackup(ctx, kept("1.0.0", shaA, "1.0.0-0.1"))
	rt, _ := d.RecordRestoreTest(ctx, rtest("1.0.1-0.3", "1.0.0", shaA, true))

	first, err := d.RecordPromotion(ctx, Promotion{
		Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.0.0-0.1",
		ArtifactSHA256: shaA, PromotedBy: "user:carl", BuildURL: "file:///dist/builds/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.RecordPromotion(ctx, Promotion{
		Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.0.1-0.3",
		ArtifactSHA256: shaB, PromotedBy: "user:carl", BuildURL: "https://ci.test/builds/3",
		Lines: []PromotionLine{{Line: "1.0.0", Why: "current", KeptBackupID: kb, RestoreTestID: rt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	into, err := d.Promotions(ctx, "redline", 0)
	if err != nil || len(into) != 2 || into[0].ID != second || into[1].ID != first {
		t.Fatalf("into = %+v, %v", into, err)
	}
	if into[1].RestoreGate != RestoreGateNoneRequired || into[1].BuildURL != "file:///dist/builds/1" {
		t.Fatalf("first release row = %+v", into[1])
	}
	if into[0].RestoreGate != RestoreGateChecked || into[0].BuildURL != "https://ci.test/builds/3" {
		t.Fatalf("checked row = %+v", into[0])
	}
	pl, err := d.PromotionLines(ctx, second)
	if err != nil || len(pl) != 1 || pl[0] != (PromotionLine{Line: "1.0.0", Why: "current", KeptBackupID: kb, RestoreTestID: rt}) {
		t.Fatalf("promotion lines = %+v, %v", pl, err)
	}
	for _, stmt := range []string{`UPDATE promotion_lines SET line = '9.9.9'`, `DELETE FROM promotion_lines`} {
		if _, err := d.ExecContext(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", stmt, err)
		}
	}
	if _, err := d.RecordPromotion(ctx, Promotion{
		Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.0.0",
		ArtifactSHA256: shaA, PromotedBy: "user:carl", BuildURL: strings.Repeat("u", MaxLocatorLen+1),
	}); !errors.Is(err, ErrInvalidLocator) {
		t.Fatalf("too-long build_url: %v", err)
	}
}

// A row written before 0016 reads 'predates', never "none required". Inserted
// by raw SQL without the column, which is what every pre-0016 writer did.
func TestPromotionBeforeTheGateReadsPredates(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.ExecContext(ctx, `
		INSERT INTO promotions (project, from_env, to_env, version, artifact_sha256, promoted_by)
		VALUES ('redline', 'staging', 'prod', '1.0.0', ?, 'user:carl')`, shaA); err != nil {
		t.Fatal(err)
	}
	into, err := d.Promotions(ctx, "redline", 0)
	if err != nil || len(into) != 1 || into[0].RestoreGate != RestoreGatePredates || into[0].BuildURL != "" {
		t.Fatalf("pre-gate row = %+v, %v", into, err)
	}
}

// The down migration runs. Positive control first: the tables exist.
func TestReleaseLinesDownMigrationRuns(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	count := func(name string) int {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tbl := range []string{"kept_backups", "restore_tests", "promotion_lines"} {
		if count(tbl) != 1 {
			t.Fatalf("instrument: %s not found before the down migration", tbl)
		}
	}
	body, err := migrationFS.ReadFile("migrations/0016_release_lines.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range []string{"kept_backups", "restore_tests", "promotion_lines"} {
		if count(tbl) != 0 {
			t.Errorf("%s survived the down migration", tbl)
		}
	}
	if _, err := d.ExecContext(ctx, `SELECT build_url FROM deploy_reports`); err == nil {
		t.Error("deploy_reports.build_url survived the down migration")
	}
}
