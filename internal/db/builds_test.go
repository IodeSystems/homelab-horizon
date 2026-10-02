package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Migration 0018: the build registry, its numbering, and the grandfathered list.

const commitA = "0123456789abcdef0123456789abcdef01234567"

func start(t *testing.T, d *DB, project, machine string) *Build {
	t.Helper()
	b, _, err := d.StartBuild(context.Background(), BuildStart{Project: project, Commit: commitA, VersionCore: "1.0.0.0", Machine: machine}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return b
}

// Two builders start builds at once. Every number is unique, each builder's
// numbers increase, and together they are exactly 1..2n — no number lost, none
// shared. This is the test positive control (a) — numbering by max(number)+1
// instead of the sequence — turns red.
func TestBuildNumbersAreUniqueUnderConcurrency(t *testing.T) {
	d := open(t)
	const each = 25
	var wg sync.WaitGroup
	got := make([][]int64, 2)
	errs := make(chan error, 2*each)
	for g, machine := range []string{"builder-a", "builder-b"} {
		wg.Add(1)
		go func(g int, machine string) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b, _, err := d.StartBuild(context.Background(), BuildStart{Project: "redline", Commit: commitA, VersionCore: "1.0.0.0", Machine: machine}, nil)
				if err != nil {
					errs <- err
					continue
				}
				got[g] = append(got[g], b.Number)
			}
		}(g, machine)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent start failed: %v", err)
	}
	seen := map[int64]bool{}
	for g, nums := range got {
		for i, n := range nums {
			if seen[n] {
				t.Errorf("number %d handed out twice", n)
			}
			seen[n] = true
			if i > 0 && n <= nums[i-1] {
				t.Errorf("builder %d's numbers went %d then %d", g, nums[i-1], n)
			}
		}
	}
	for n := int64(1); n <= 2*each; n++ {
		if !seen[n] {
			t.Errorf("number %d never handed out (got %v)", n, got)
		}
	}
}

func TestAFailedBuildKeepsItsNumber(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	first := start(t, d, "redline", "builder-a")
	if err := d.FinishBuild(ctx, first.ID, BuildFailed, "", "test"); err != nil {
		t.Fatal(err)
	}
	second := start(t, d, "redline", "builder-a")
	if second.Number != first.Number+1 {
		t.Fatalf("after a failed #%d the next is #%d", first.Number, second.Number)
	}
	again, err := d.BuildByNumber(ctx, "redline", first.Number)
	if err != nil || again.Result != BuildFailed || again.ID != first.ID {
		t.Fatalf("the failed build lost its number: %+v %v", again, err)
	}
	// Numbers are per project.
	if other := start(t, d, "storefront", "builder-a"); other.Number != 1 {
		t.Fatalf("another project's first build is #%d", other.Number)
	}
}

// An insert that fails inside the start's transaction hands out no number, and
// the next start's number is above every number handed out before it.
func TestNumbersNeverRepeatAfterAFailedInsert(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `CREATE TRIGGER test_refuse BEFORE INSERT ON builds WHEN NEW.describe = 'boom'
		BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`); err != nil {
		t.Fatal(err)
	}
	var issued []int64
	for i := 0; i < 6; i++ {
		describe := ""
		if i%2 == 1 {
			describe = "boom"
		}
		b, _, err := d.StartBuild(ctx, BuildStart{Project: "redline", Commit: commitA, VersionCore: "1.0.0.0", Describe: describe, Machine: "builder-a"}, nil)
		if describe == "boom" {
			if err == nil || !strings.Contains(err.Error(), "refused by the test") {
				t.Fatalf("instrument: the forced insert failure did not happen: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range issued {
			if b.Number <= n {
				t.Fatalf("after a failed insert, #%d was handed out again or out of order (issued %v)", b.Number, issued)
			}
		}
		issued = append(issued, b.Number)
	}
	if len(issued) != 3 {
		t.Fatalf("issued %v", issued)
	}
}

func TestVersionCoreAndTheVersionItMakes(t *testing.T) {
	for _, bad := range []string{"", "1.0.0", "1.0.0.0.0", "1.0.0.-1", "1.0.0.a", "01.0.0.0", "1.0.0.00", " 1.0.0.0", "1..0.0", "1.0.0.99999999999"} {
		if err := CheckVersionCore(bad); !errors.Is(err, ErrInvalidBuild) {
			t.Errorf("CheckVersionCore(%q) = %v, want refused", bad, err)
		}
	}
	for _, good := range []string{"0.0.0.0", "1.0.0.0", "10.20.30.4"} {
		if err := CheckVersionCore(good); err != nil {
			t.Errorf("CheckVersionCore(%q) = %v", good, err)
		}
	}
	v, err := BuildVersion("1.0.0.0", 7)
	if err != nil || v != "1.0.0-0.7" {
		t.Fatalf("BuildVersion = %q, %v", v, err)
	}
	if err := CheckDeployVersion(v); err != nil {
		t.Fatalf("the build version does not pass the deploy check: %v", err)
	}
	// Ordered by number — numerically, through the one comparator.
	v9, _ := BuildVersion("1.0.0.0", 9)
	v10, _ := BuildVersion("1.0.0.0", 10)
	if c, err := CompareVersions(v9, v10); err != nil || c >= 0 {
		t.Fatalf("CompareVersions(%s, %s) = %d, %v; want <0", v9, v10, c, err)
	}
	// A hotfix orders above its base, on the same line.
	h, _ := BuildVersion("1.0.0.1", 2)
	if c, _ := CompareVersions(h, v10); c <= 0 {
		t.Fatalf("hotfix %s does not order above %s", h, v10)
	}
	if l, _ := LineOf(h); l != "1.0.0" {
		t.Fatalf("line of %s = %s", h, l)
	}
	for v, want := range map[string]int64{"1.0.0-0.4": 4, "1.0.0-rc.1.1414": 1414} {
		if n, ok := LastNumericIdentifier(v); !ok || n != want {
			t.Errorf("LastNumericIdentifier(%q) = %d, %v", v, n, ok)
		}
	}
	for _, v := range []string{"1.0.0", "1.0.0-rc", "not a version", "1.0.0-0.x"} {
		if _, ok := LastNumericIdentifier(v); ok {
			t.Errorf("LastNumericIdentifier(%q) answered a number", v)
		}
	}
}

func TestBuildStateAndAFinishIsFinal(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	b := start(t, d, "redline", "builder-a")
	steps := []BuildStep{{Name: "test", Status: "passed", StartedAt: "2026-10-02T10:00:00Z", FinishedAt: "2026-10-02T10:05:00Z"}, {Name: "package", Status: "running"}}
	if err := d.UpdateBuildState(ctx, b.ID, "package", BuildRunning, steps); err != nil {
		t.Fatal(err)
	}
	got, _ := d.BuildByID(ctx, b.ID)
	if got.Step != "package" || len(got.Steps) != 2 || got.Steps[0] != steps[0] {
		t.Fatalf("state = %+v", got)
	}
	for _, bad := range [][]BuildStep{{{Name: "", Status: "passed"}}, {{Name: "x", Status: "done"}}, {{Name: "x", Status: "passed", StartedAt: "yesterday"}}} {
		if err := d.UpdateBuildState(ctx, b.ID, "x", BuildRunning, bad); !errors.Is(err, ErrInvalidBuild) {
			t.Errorf("state %+v: %v", bad, err)
		}
	}
	if err := d.UpdateBuildState(ctx, b.ID, "x", "done", nil); !errors.Is(err, ErrInvalidBuild) {
		t.Errorf("status done: %v", err)
	}
	if err := d.FinishBuild(ctx, b.ID, BuildFailed, shaA, "test"); !errors.Is(err, ErrInvalidBuild) {
		t.Errorf("a failed build with an artifact: %v", err)
	}
	if err := d.FinishBuild(ctx, b.ID, BuildPassed, "", ""); !errors.Is(err, ErrInvalidBuild) {
		t.Errorf("a passed build with no artifact: %v", err)
	}
	if err := d.FinishBuild(ctx, b.ID, BuildPassed, shaA, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishBuild(ctx, b.ID, BuildFailed, "", "x"); !errors.Is(err, ErrBuildFinished) {
		t.Errorf("second finish: %v", err)
	}
	if err := d.UpdateBuildState(ctx, b.ID, "x", BuildRunning, nil); !errors.Is(err, ErrBuildFinished) {
		t.Errorf("state after finish: %v", err)
	}
	if err := d.TouchBuild(ctx, b.ID); !errors.Is(err, ErrBuildFinished) {
		t.Errorf("log after finish: %v", err)
	}
	// The trigger holds it too, under any writer.
	if _, err := d.ExecContext(ctx, `UPDATE builds SET result = 'failed', artifact_sha256 = NULL WHERE id = ?`, b.ID); err == nil || !strings.Contains(err.Error(), "final") {
		t.Errorf("raw re-finish: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE builds SET number = 99 WHERE id = ?`, b.ID); err == nil {
		t.Error("a build's number was edited")
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM builds WHERE id = ?`, b.ID); err == nil {
		t.Error("a build was deleted")
	}
	if p, err := d.PassedBuildOf(ctx, "redline", shaA); err != nil || p.ID != b.ID {
		t.Fatalf("PassedBuildOf = %+v %v", p, err)
	}
	if _, err := d.PassedBuildOf(ctx, "storefront", shaA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's PassedBuildOf: %v", err)
	}
}

func TestAbandonedIsDerivedOnRead(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	b := start(t, d, "redline", "builder-a")
	if got := b.Outcome(b.LastActivityAt.Add(time.Hour)); got != BuildRunning {
		t.Fatalf("1h quiet = %s", got)
	}
	if got := b.Outcome(b.LastActivityAt.Add(AbandonAfter + time.Second)); got != BuildAbandoned {
		t.Fatalf("2h quiet = %s", got)
	}
	// Activity resets it; nothing about "abandoned" is stored.
	if _, err := d.ExecContext(ctx, `UPDATE builds SET last_activity_at = datetime('now', '-3 hours') WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := d.BuildByID(ctx, b.ID)
	if old.Outcome(time.Now()) != BuildAbandoned || old.Status != BuildRunning || old.Result != "" {
		t.Fatalf("quiet build = %+v (%s)", old, old.Outcome(time.Now()))
	}
	if err := d.TouchBuild(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := d.BuildByID(ctx, b.ID)
	if fresh.Outcome(time.Now()) != BuildRunning {
		t.Fatalf("after a log chunk = %s", fresh.Outcome(time.Now()))
	}
	// A finished build is never abandoned, however old.
	_ = d.FinishBuild(ctx, b.ID, BuildFailed, "", "")
	done, _ := d.BuildByID(ctx, b.ID)
	if done.Outcome(time.Now().Add(48*time.Hour)) != BuildFailed {
		t.Fatal("a finished build read as abandoned")
	}
}

// The seed: numbers already issued outside hz are never issued again. This is
// the test positive control "drop the seed" turns red.
func TestFirstBuildIsAboveExistingVersions(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if _, err := d.RecordDeployReport(ctx, DeployReport{Project: "redline", Environment: "staging", Version: "1.0.0-0.4",
		ArtifactSHA256: shaA, ReportedBy: "service:redline-staging"}); err != nil {
		t.Fatal(err)
	}
	// Another project's versions do not count, and neither does a version with
	// no numeric last identifier.
	_, _ = d.RecordDeployReport(ctx, DeployReport{Project: "storefront", Environment: "staging", Version: "1.0.0-0.40", ArtifactSHA256: shaA, ReportedBy: "x"})
	_, _ = d.RecordDeployReport(ctx, DeployReport{Project: "redline", Environment: "staging", Version: "2.0.0-rc", ArtifactSHA256: shaA, ReportedBy: "x"})
	if n, _ := d.NextBuildNumber(ctx, "redline", nil); n != 5 {
		t.Fatalf("next before the first build = %d, want 5", n)
	}
	b := start(t, d, "redline", "builder-a")
	if b.Number != 5 || b.Version != "1.0.0-0.5" {
		t.Fatalf("first build = #%d %s, want #5 1.0.0-0.5 (1.0.0-0.4 is already reported)", b.Number, b.Version)
	}
	// A declared version from config.json counts too.
	c, _, err := d.StartBuild(ctx, BuildStart{Project: "acme", Commit: commitA, VersionCore: "1.0.0.0", Machine: "m"}, []string{"3.1.0-2.11"})
	if err != nil || c.Number != 12 {
		t.Fatalf("seeded from a declared version: %+v %v", c, err)
	}
}

func TestRaiseOnlyAndSkipAVersionInUse(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if _, err := d.RecordDeployReport(ctx, DeployReport{Project: "redline", Environment: "staging", Version: "1.0.0-0.4",
		ArtifactSHA256: shaA, ReportedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	// #5 was minted on the dev box and never reported: raise past it.
	prev, err := d.RaiseBuildNumber(ctx, "redline", 6, nil)
	if err != nil || prev != 5 {
		t.Fatalf("raise to 6: prev=%d %v", prev, err)
	}
	if _, err := d.RaiseBuildNumber(ctx, "redline", 5, nil); !errors.Is(err, ErrSeedLower) || !strings.Contains(err.Error(), "only raises") {
		t.Fatalf("lowering: %v", err)
	}
	b := start(t, d, "redline", "builder-a")
	if b.Number != 6 {
		t.Fatalf("after raising to 6 the build is #%d", b.Number)
	}
	// Lowering below a fresh sequence's seed is refused too.
	_, _ = d.RecordDeployReport(ctx, DeployReport{Project: "acme", Environment: "staging", Version: "1.0.0-0.9", ArtifactSHA256: shaA, ReportedBy: "x"})
	if _, err := d.RaiseBuildNumber(ctx, "acme", 3, nil); !errors.Is(err, ErrSeedLower) {
		t.Fatalf("lowering below the seed: %v", err)
	}
	// After the seed, a dev-box run reports 1.0.0-0.7: the next allocation
	// would mint it again, and skips it.
	if _, err := d.RecordDeployReport(ctx, DeployReport{Project: "redline", Environment: "staging", Version: "1.0.0-0.7",
		ArtifactSHA256: shaB, ReportedBy: "x"}); err != nil {
		t.Fatal(err)
	}
	next, skipped, err := d.StartBuild(ctx, BuildStart{Project: "redline", Commit: commitA, VersionCore: "1.0.0.0", Machine: "builder-a"}, nil)
	if err != nil || next.Number != 8 || len(skipped) != 1 || skipped[0] != 7 {
		t.Fatalf("skip: #%d skipped=%v %v; want #8 skipping 7", next.Number, skipped, err)
	}
	// A different core with that number is not in use.
	_, _ = d.RecordDeployReport(ctx, DeployReport{Project: "redline", Environment: "staging", Version: "1.0.0-0.10", ArtifactSHA256: shaB, ReportedBy: "x"})
	c, skipped, _ := d.StartBuild(ctx, BuildStart{Project: "redline", Commit: commitA, VersionCore: "1.1.0.0", Machine: "builder-a"}, nil)
	if c.Number != 9 || len(skipped) != 0 {
		t.Fatalf("#%d skipped %v", c.Number, skipped)
	}
	if _, err := d.ExecContext(ctx, `UPDATE build_sequences SET next = 1 WHERE project = 'redline'`); err == nil {
		t.Fatal("the sequence was lowered by a raw UPDATE")
	}
}

// Every artifact present when 0018 runs is grandfathered — from the rows, not
// a clock — and the list is closed afterwards.
func TestMigrationGrandfathersTheArtifactsThatExist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")
	old, err := OpenAt(path, 17)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.RecordArtifact(ctx, Artifact{SHA256: shaA, Project: "redline", Size: 1, UploadedBy: "service:redline-staging"}); err != nil {
		t.Fatal(err)
	}
	// Uploaded "in the future" by its clock: a time cutoff would get this wrong.
	if _, err := old.ExecContext(ctx, `INSERT INTO artifacts (sha256, project, size, uploaded_at, uploaded_by) VALUES (?, 'redline', 1, '2099-01-01 00:00:00', 'x')`, shaB); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, sha := range []string{shaA, shaB} {
		if ok, err := d.IsGrandfathered(ctx, sha); err != nil || !ok {
			t.Fatalf("%s… not grandfathered: %v", sha[:4], err)
		}
	}
	shaC := strings.Repeat("c", 64)
	_, _ = d.RecordArtifact(ctx, Artifact{SHA256: shaC, Project: "redline", Size: 1, UploadedBy: "x"})
	if ok, _ := d.IsGrandfathered(ctx, shaC); ok {
		t.Fatal("an upload after 0018 was grandfathered")
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO artifact_grandfathered (sha256) VALUES (?)`, shaC); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("the grandfathered list grew: %v", err)
	}
	// Promotions written before 0018 read as predating the check.
	if _, err := d.ExecContext(ctx, `INSERT INTO promotions (project, from_env, to_env, version, artifact_sha256, promoted_by) VALUES ('redline','staging','prod','1.0.0',?, 'u')`, shaA); err != nil {
		t.Fatal(err)
	}
	ps, _ := d.Promotions(ctx, "redline", 0)
	if len(ps) != 1 || ps[0].Provenance != ProvenancePredates || ps[0].BuildID != 0 {
		t.Fatalf("old promotion = %+v", ps)
	}
}

func TestBuildsDownMigrationRuns(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	body, err := migrationFS.ReadFile("migrations/0018_builds.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'builds'`).Scan(&n)
	if n != 1 {
		t.Fatal("instrument: builds not found before the down migration")
	}
	if _, err := d.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range []string{"builds", "build_sequences", "artifact_grandfathered"} {
		_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s survived the down migration", tbl)
		}
	}
}
