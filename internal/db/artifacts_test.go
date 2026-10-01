package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Migration 0017: artifacts, applies, holds, instance pulls.

func TestArtifactRecordIsIdempotentAndOutlivesItsFile(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	if _, err := d.LookupArtifact(ctx, shaA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("never uploaded: err = %v, want ErrNotFound", err)
	}
	existing, err := d.RecordArtifact(ctx, Artifact{SHA256: shaA, Project: "redline", Size: 10, UploadedBy: "service:redline-staging"})
	if err != nil || existing {
		t.Fatalf("first upload: existing=%v err=%v", existing, err)
	}
	existing, err = d.RecordArtifact(ctx, Artifact{SHA256: shaA, Project: "redline", Size: 10, UploadedBy: "user:carl"})
	if err != nil || !existing {
		t.Fatalf("re-upload: existing=%v err=%v, want existing", existing, err)
	}
	a, err := d.LookupArtifact(ctx, shaA)
	if err != nil || a.UploadedBy != "service:redline-staging" || a.Deleted() {
		t.Fatalf("record = %+v, %v; the first uploader stays on record", a, err)
	}

	if err := d.RecordArtifactEvent(ctx, shaA, ArtifactDeleted, "not kept: old", "retention"); err != nil {
		t.Fatal(err)
	}
	a, _ = d.LookupArtifact(ctx, shaA)
	if !a.Deleted() || a.DeletedWhy != "not kept: old" {
		t.Fatalf("after deletion = %+v", a)
	}
	if err := d.RecordArtifactEvent(ctx, shaA, ArtifactRestored, "re-uploaded", "user:carl"); err != nil {
		t.Fatal(err)
	}
	a, _ = d.LookupArtifact(ctx, shaA)
	if a.Deleted() {
		t.Fatalf("a restored artifact reads as deleted: %+v", a)
	}
	all, err := d.Artifacts(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("Artifacts = %+v, %v", all, err)
	}
}

func TestAppliesHoldsAndArtifactsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.RecordArtifact(ctx, Artifact{SHA256: shaA, Project: "redline", Size: 1, UploadedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordArtifactEvent(ctx, shaA, ArtifactDeleted, "why", "retention"); err != nil {
		t.Fatal(err)
	}
	pid, err := d.RecordPromotion(ctx, Promotion{Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.4.0", ArtifactSHA256: shaA, PromotedBy: "user:carl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordApply(ctx, Apply{Project: "redline", Environment: "prod", Version: "1.4.0", ArtifactSHA256: shaA, PromotionID: pid, AppliedBy: "user:carl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordHold(ctx, HoldEvent{Project: "redline", Environment: "prod", Kind: HoldOn, Reason: "cutover", By: "user:carl"}); err != nil {
		t.Fatal(err)
	}
	// Positive control: the rows exist, so a refused UPDATE is the trigger
	// and not a WHERE that matched nothing.
	for _, tbl := range []string{"artifacts", "artifact_events", "applies", "rung_holds"} {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("instrument: %s has %d rows (%v)", tbl, n, err)
		}
	}
	for _, stmt := range []string{
		`UPDATE artifacts SET size = 2`,
		`DELETE FROM artifacts`,
		`UPDATE artifact_events SET reason = 'x'`,
		`DELETE FROM artifact_events`,
		`UPDATE applies SET version = '9.9.9'`,
		`DELETE FROM applies`,
		`UPDATE rung_holds SET reason = 'x'`,
		`DELETE FROM rung_holds`,
	} {
		if _, err := d.ExecContext(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only trigger", stmt, err)
		}
	}
}

func TestLatestApplyAndHoldAreByID(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.LatestApply(ctx, "redline", "prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing applied: %v", err)
	}
	if _, err := d.LatestHold(ctx, "redline", "prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("never held: %v", err)
	}
	p1, _ := d.RecordPromotion(ctx, Promotion{Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.4.0", ArtifactSHA256: shaA, PromotedBy: "u"})
	p2, _ := d.RecordPromotion(ctx, Promotion{Project: "redline", FromEnv: "staging", ToEnv: "prod", Version: "1.5.0", ArtifactSHA256: shaB, PromotedBy: "u"})
	latest, err := d.LatestPromotionInto(ctx, "redline", "prod")
	if err != nil || latest.ID != p2 {
		t.Fatalf("LatestPromotionInto = %+v, %v", latest, err)
	}
	if _, err := d.RecordApply(ctx, Apply{Project: "redline", Environment: "prod", Version: "1.4.0", ArtifactSHA256: shaA, PromotionID: p1, AppliedBy: "u"}); err != nil {
		t.Fatal(err)
	}
	a2, _ := d.RecordApply(ctx, Apply{Project: "redline", Environment: "prod", Version: "1.5.0", ArtifactSHA256: shaB, PromotionID: p2, AppliedBy: "u"})
	got, err := d.LatestApply(ctx, "redline", "prod")
	if err != nil || got.ID != a2 || got.Version != "1.5.0" {
		t.Fatalf("LatestApply = %+v, %v", got, err)
	}
	ok, err := d.AppliedTo(ctx, shaA, [][2]string{{"redline", "prod"}})
	if err != nil || !ok {
		t.Fatalf("AppliedTo older apply = %v, %v", ok, err)
	}
	ok, _ = d.AppliedTo(ctx, shaA, [][2]string{{"redline", "staging"}})
	if ok {
		t.Fatal("AppliedTo answered for a rung nothing was applied to")
	}

	if _, err := d.RecordHold(ctx, HoldEvent{Project: "redline", Environment: "prod", Kind: HoldOn, By: "u"}); err == nil {
		t.Fatal("a hold without a reason was recorded")
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO rung_holds (project, environment, kind, reason, actor) VALUES ('redline','prod','hold','','u')`); err == nil {
		t.Fatal("raw SQL recorded a hold without a reason")
	}
	_, _ = d.RecordHold(ctx, HoldEvent{Project: "redline", Environment: "prod", Kind: HoldOn, Reason: "cutover", By: "u"})
	_, _ = d.RecordHold(ctx, HoldEvent{Project: "redline", Environment: "prod", Kind: HoldOff, By: "u"})
	h, err := d.LatestHold(ctx, "redline", "prod")
	if err != nil || h.Held() {
		t.Fatalf("after unhold = %+v, %v", h, err)
	}
}

func TestInstancePullOverwrites(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if err := d.RecordInstancePull(ctx, "redline-prod-hz", "redline", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordInstancePull(ctx, "redline-prod-hz", "redline", "loadtest"); err != nil {
		t.Fatal(err)
	}
	pulls, err := d.InstancePulls(ctx)
	if err != nil || len(pulls) != 1 || pulls["redline-prod-hz"].Environment != "loadtest" {
		t.Fatalf("pulls = %+v, %v", pulls, err)
	}
}

func TestArtifactsDownMigrationRuns(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	count := func(name string) int {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tables := []string{"artifacts", "artifact_events", "applies", "rung_holds", "instance_pulls"}
	for _, tbl := range tables {
		if count(tbl) != 1 {
			t.Fatalf("instrument: %s not found before the down migration", tbl)
		}
	}
	body, err := migrationFS.ReadFile("migrations/0017_artifacts_applies.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range tables {
		if count(tbl) != 0 {
			t.Errorf("%s survived the down migration", tbl)
		}
	}
}
