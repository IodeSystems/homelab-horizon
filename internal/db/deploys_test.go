package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func report(version, sha string) DeployReport {
	return DeployReport{
		Project: "redline", Environment: "staging", App: "redline",
		Version: version, Describe: "v" + version + "-3-gabc123", ArtifactSHA256: sha,
		Host: "ubuntu@192.0.2.160", ReportedBy: "service:redline-staging",
	}
}

func TestCheckDeployVersion(t *testing.T) {
	for _, ok := range []string{"1.4.0", "0.0.1", "1.0.0-rc.1.1414", "10.20.30-alpha"} {
		if err := CheckDeployVersion(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " 1.4.0", "v1.4.0", "1.4.0+abc", "1.4", "latest", "1.4.0-", "abc123"} {
		if err := CheckDeployVersion(bad); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("%q: err = %v, want ErrInvalidVersion", bad, err)
		}
	}
}

func TestNormalizeSHA256(t *testing.T) {
	got, err := NormalizeSHA256(strings.ToUpper(shaA))
	if err != nil || got != shaA {
		t.Fatalf("uppercase digest = %q, %v; want it lowercased", got, err)
	}
	for _, bad := range []string{"", shaA[:63], shaA + "a", strings.Repeat("g", 64)} {
		if _, err := NormalizeSHA256(bad); !errors.Is(err, ErrInvalidSHA256) {
			t.Errorf("%q: err = %v, want ErrInvalidSHA256", bad, err)
		}
	}
}

func TestDeployReportLatestIsByIDAndNeverReportedIsNotFound(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	if _, err := d.LatestDeployReport(ctx, "redline", "staging"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("never reported: err = %v, want ErrNotFound", err)
	}
	if _, err := d.RecordDeployReport(ctx, report("1.3.9", shaA)); err != nil {
		t.Fatal(err)
	}
	// The same version again with a DIFFERENT artifact: a redeploy of one
	// commit can rebuild. A new row, and it wins.
	if _, err := d.RecordDeployReport(ctx, report("1.4.0", shaA)); err != nil {
		t.Fatal(err)
	}
	id, err := d.RecordDeployReport(ctx, report("1.4.0", shaB))
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.LatestDeployReport(ctx, "redline", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Version != "1.4.0" || got.ArtifactSHA256 != shaB {
		t.Fatalf("latest = %+v, want id %d 1.4.0/%s", got, id, shaB)
	}
	if got.ReportedAt.IsZero() || got.ReportedBy != "service:redline-staging" {
		t.Fatalf("stamp/attribution lost: %+v", got)
	}

	other := report("2.0.0", shaA)
	other.Environment = "prod"
	if _, err := d.RecordDeployReport(ctx, other); err != nil {
		t.Fatal(err)
	}
	all, err := d.LatestDeployReports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Environment != "prod" || all[1].Environment != "staging" || all[1].ArtifactSHA256 != shaB {
		t.Fatalf("latest per rung = %+v", all)
	}
}

// DeployReportsOf is one rung's reports newest first BY ID: a row whose
// reported_at is later (a box's clock is not ours) does not jump the queue.
func TestDeployReportsOfIsOneRungNewestFirstByID(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if got, err := d.DeployReportsOf(ctx, "redline", "prod"); err != nil || len(got) != 0 {
		t.Fatalf("never reported = %+v, %v; want an empty list", got, err)
	}
	// The oldest row, stamped far in the future.
	if _, err := d.ExecContext(ctx, `
		INSERT INTO deploy_reports (project, environment, app, version, describe, artifact_sha256, host, build_url, reported_at, reported_by)
		VALUES ('redline', 'prod', 'redline', '1.0.0-0.1', '', ?, 'ubuntu@host', '', '2099-01-01 00:00:00', 'service:redline-prod')`, shaA); err != nil {
		t.Fatal(err)
	}
	p := report("1.0.1-0.3", shaB)
	p.Environment = "prod"
	newest, err := d.RecordDeployReport(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordDeployReport(ctx, report("9.9.9", shaA)); err != nil { // staging: another rung
		t.Fatal(err)
	}
	got, err := d.DeployReportsOf(ctx, "redline", "prod")
	if err != nil || len(got) != 2 || got[0].ID != newest || got[0].Version != "1.0.1-0.3" || got[1].Version != "1.0.0-0.1" {
		t.Fatalf("prod's reports = %+v, %v; want id %d first, staging's absent", got, err, newest)
	}
}

func TestDeployReportRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.RecordDeployReport(ctx, report("v1.4.0", shaA)); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("leading v: %v", err)
	}
	if _, err := d.RecordDeployReport(ctx, report("1.4.0", "abc")); !errors.Is(err, ErrInvalidSHA256) {
		t.Fatalf("short sha: %v", err)
	}
	r := report("1.4.0", shaA)
	r.ReportedBy = ""
	if _, err := d.RecordDeployReport(ctx, r); err == nil {
		t.Fatal("a report with no reporter was accepted")
	}
}

// Append-only is structural: the triggers refuse an UPDATE or DELETE from ANY
// caller, including raw SQL that bypasses this package's writers.
func TestDeployReportsAndPromotionsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.RecordDeployReport(ctx, report("1.4.0", shaA)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordPromotion(ctx, Promotion{
		Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.4.0",
		ArtifactSHA256: shaA, PromotedBy: "user:carl",
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE deploy_reports SET version = '9.9.9'`,
		`DELETE FROM deploy_reports`,
		`UPDATE promotions SET artifact_sha256 = '` + shaB + `'`,
		`DELETE FROM promotions`,
	} {
		_, err := d.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", stmt, err)
		}
	}
	got, err := d.LatestDeployReport(ctx, "redline", "staging")
	if err != nil || got.Version != "1.4.0" {
		t.Fatalf("report after refused edits = %+v, %v", got, err)
	}
}

func TestPromotionLatestOfPinsTheNewest(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.LatestPromotionOf(ctx, "redline", "prod", "1.4.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no promotion: %v", err)
	}
	for _, sha := range []string{shaA, shaB} {
		if _, err := d.RecordPromotion(ctx, Promotion{
			Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.4.0",
			ArtifactSHA256: sha, PromotedBy: "user:carl", Downgrade: sha == shaB,
		}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := d.LatestPromotionOf(ctx, "redline", "prod", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.ArtifactSHA256 != shaB || !p.Downgrade || p.PromotedBy != "user:carl" || p.PromotedAt.IsZero() {
		t.Fatalf("latest promotion = %+v", p)
	}
	list, err := d.Promotions(ctx, "redline", 0)
	if err != nil || len(list) != 2 || list[0].ArtifactSHA256 != shaB {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if none, _ := d.Promotions(ctx, "other", 0); len(none) != 0 {
		t.Fatalf("another project's list = %+v", none)
	}
	if all, _ := d.Promotions(ctx, "", 0); len(all) != 2 {
		t.Fatalf("unscoped list = %+v", all)
	}
}
