package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// The build registry (migration 0018): build numbers come FROM hz.
//
// A builder — a declared machine holding a builder token — starts a build and
// hz allocates its number from build_sequences, per project, in the same
// transaction that inserts the build. The number is never derived from a max
// over builds rows: a sequence only goes up (by trigger), and a failed or
// abandoned build keeps the number it was given.
//
// SEEDING. Numbers existed before hz issued them (redline minted #1–#5 on the
// dev box). The FIRST allocation for a project therefore starts above every
// version already recorded for it — the last numeric prerelease identifier of
// each ("1.0.0-0.4" → 4) — computed inside that transaction, not in the
// migration, because more reports can arrive before the first build. After
// that, an allocation whose version is already recorded anywhere is skipped
// past, and logged; hz never hands out an existing version.

// AbandonAfter is how long a build with no finish and no log or state activity
// runs before it READS as abandoned. Derived on read (#8), never written.
const AbandonAfter = 2 * time.Hour

// Build results, and the derived "abandoned".
const (
	BuildRunning   = "running"
	BuildPassed    = "passed"
	BuildFailed    = "failed"
	BuildAbandoned = "abandoned"
)

var (
	// ErrBuildFinished: a finish is final — a second finish, or a log or state
	// update after one, is refused.
	ErrBuildFinished = errors.New("build already finished")
	// ErrInvalidBuild is a malformed start, state or finish.
	ErrInvalidBuild = errors.New("invalid build")
	// ErrSeedLower: the next build number is only ever raised.
	ErrSeedLower = errors.New("build number seed is lower than the next number")
)

// BuildStep is one step of a build's state, as the builder reports it.
type BuildStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// Build is one builds row.
type Build struct {
	ID             int64
	Project        string
	Number         int64
	Commit         string
	VersionCore    string
	Version        string
	Describe       string
	Machine        string
	StartedAt      time.Time
	LastActivityAt time.Time
	Step           string
	Status         string
	Steps          []BuildStep
	// FinishedAt and Result are set together, once; Result is "" while the
	// build has no finish.
	FinishedAt     *time.Time
	Result         string
	ArtifactSHA256 string
	FailedStep     string
	// LogDeletedAt is set when retention deleted the log file.
	LogDeletedAt  *time.Time
	LogDeletedWhy string
}

// Outcome is the build's result, or — with no finish — "abandoned" when it has
// been silent for AbandonAfter, else "running". Derived from now; never stored.
func (b Build) Outcome(now time.Time) string {
	if b.Result != "" {
		return b.Result
	}
	if now.Sub(b.LastActivityAt) > AbandonAfter {
		return BuildAbandoned
	}
	return BuildRunning
}

// CheckVersionCore refuses a version core that is not MAJOR.MINOR.FIX.HOTFIX:
// four non-negative integers in their one spelling (no sign, no leading zero),
// so the version built from it has one spelling too.
func CheckVersionCore(core string) error {
	parts := strings.Split(core, ".")
	if len(parts) != 4 {
		return fmt.Errorf("%w: version_core %q is not MAJOR.MINOR.FIX.HOTFIX (four non-negative integers)", ErrInvalidBuild, core)
	}
	for _, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return fmt.Errorf("%w: version_core %q: %q is not a non-negative integer in its one spelling (no leading zero)", ErrInvalidBuild, core, p)
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return fmt.Errorf("%w: version_core %q: %q is not a non-negative integer", ErrInvalidBuild, core, p)
			}
		}
		if _, err := strconv.ParseInt(p, 10, 32); err != nil {
			return fmt.Errorf("%w: version_core %q: %q is out of range", ErrInvalidBuild, core, p)
		}
	}
	return nil
}

// BuildVersion is the wire version of build `number` on `core`: redline's
// MAJOR.MINOR.FIX.HOTFIX#BUILD as semver MAJOR.MINOR.FIX-HOTFIX.BUILD
// ("1.0.0.0", 7 → "1.0.0-0.7"), checked by CheckDeployVersion — the one parser.
func BuildVersion(core string, number int64) (string, error) {
	if err := CheckVersionCore(core); err != nil {
		return "", err
	}
	if number < 1 {
		return "", fmt.Errorf("%w: build number %d", ErrInvalidBuild, number)
	}
	p := strings.Split(core, ".")
	v := p[0] + "." + p[1] + "." + p[2] + "-" + p[3] + "." + strconv.FormatInt(number, 10)
	if err := CheckDeployVersion(v); err != nil {
		return "", err
	}
	return v, nil
}

// LastNumericIdentifier is N in a version whose prerelease ENDS in a numeric
// identifier ("1.0.0-0.4" → 4, "1.0.0-rc.1.1414" → 1414). ok is false for a
// version hz cannot parse, a release with no prerelease, or a prerelease whose
// last identifier is not numeric. It reads through parseVersion, hz's one
// parser.
func LastNumericIdentifier(version string) (int64, bool) {
	p, err := parseVersion(version)
	if err != nil || p.prerelease == "" {
		return 0, false
	}
	ids := strings.Split(p.prerelease, ".")
	n, ok := numericIdentifier(ids[len(ids)-1])
	if !ok {
		return 0, false
	}
	return int64(n), true
}

// normalizeCommit lowercases a git commit and refuses anything not 40 hex.
func normalizeCommit(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 40 {
		return "", fmt.Errorf("%w: commit must be the full 40-hex-character sha, got %d characters", ErrInvalidBuild, len(s))
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: commit %q is not hex", ErrInvalidBuild, s)
		}
	}
	return s, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// versionSources is every recorded version of one project, in every table
// that carries one. The single ? is bound once per arm.
const versionSources = `
	SELECT version FROM deploy_reports WHERE project = ?
	UNION SELECT version FROM promotions WHERE project = ?
	UNION SELECT version FROM applies WHERE project = ?
	UNION SELECT version FROM restore_tests WHERE project = ?
	UNION SELECT taken_by_version FROM kept_backups WHERE project = ?
	UNION SELECT version FROM cm_registrations WHERE project = ?
	UNION SELECT observed_version FROM cm_registrations WHERE project = ? AND observed_version IS NOT NULL
	UNION SELECT version FROM builds WHERE project = ?`

const versionSourceArms = 8

func projectArgs(project string) []any {
	args := make([]any, versionSourceArms)
	for i := range args {
		args[i] = project
	}
	return args
}

// ProjectVersions is every version recorded for project in hz.db, distinct.
func (d *DB) ProjectVersions(ctx context.Context, project string) ([]string, error) {
	return projectVersions(ctx, d, project)
}

func projectVersions(ctx context.Context, q queryer, project string) ([]string, error) {
	rows, err := q.QueryContext(ctx, versionSources, projectArgs(project)...)
	if err != nil {
		return nil, fmt.Errorf("read recorded versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SeedFrom is the first number above every version given: max(N)+1 over their
// last numeric prerelease identifiers, and 1 when none has one.
func SeedFrom(versions []string) int64 {
	var max int64
	for _, v := range versions {
		if n, ok := LastNumericIdentifier(v); ok && n > max {
			max = n
		}
	}
	return max + 1
}

// ensureSequence makes the project's sequence row exist and answers its next
// number. Its FIRST statement is a write, so the transaction takes SQLite's
// write lock before it reads anything — a concurrent start waits on
// busy_timeout rather than reading a snapshot another start is about to
// invalidate. When the row is new, it is seeded from the recorded versions.
func ensureSequence(ctx context.Context, tx *sql.Tx, project string, extra []string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO build_sequences (project, next) VALUES (?, 1) ON CONFLICT (project) DO NOTHING`, project)
	if err != nil {
		return 0, fmt.Errorf("build sequence: %w", err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if created == 1 {
		recorded, err := projectVersions(ctx, tx, project)
		if err != nil {
			return 0, err
		}
		seed := SeedFrom(append(recorded, extra...))
		if seed > 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE build_sequences SET next = ? WHERE project = ?`, seed, project); err != nil {
				return 0, fmt.Errorf("seed build sequence: %w", err)
			}
			slog.Info("build numbers seeded above the versions already recorded", "project", project, "next", seed)
		}
		return seed, nil
	}
	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT next FROM build_sequences WHERE project = ?`, project).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

// BuildStart is what a builder sends to start a build.
type BuildStart struct {
	Project     string
	Commit      string
	VersionCore string
	Describe    string
	Machine     string
}

// StartBuild allocates the project's next build number and records the build,
// in one transaction. extra is versions recorded outside hz.db (a rung's
// declared version in config.json) that the seed and the in-use check also
// count. skipped is every number passed over because its version was in use.
func (d *DB) StartBuild(ctx context.Context, s BuildStart, extra []string) (b *Build, skipped []int64, err error) {
	if strings.TrimSpace(s.Project) == "" || strings.TrimSpace(s.Machine) == "" {
		return nil, nil, fmt.Errorf("%w: a build needs a project and a machine", ErrInvalidBuild)
	}
	commit, err := normalizeCommit(s.Commit)
	if err != nil {
		return nil, nil, err
	}
	if err := CheckVersionCore(s.VersionCore); err != nil {
		return nil, nil, err
	}
	if err := CheckLocator("describe", s.Describe); err != nil || len(s.Describe) > 256 {
		return nil, nil, fmt.Errorf("%w: describe must be at most 256 bytes with no control characters", ErrInvalidBuild)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	n, err := ensureSequence(ctx, tx, s.Project, extra)
	if err != nil {
		return nil, nil, err
	}
	inUse := map[string]bool{}
	for _, v := range extra {
		inUse[v] = true
	}
	recorded, err := projectVersions(ctx, tx, s.Project)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range recorded {
		inUse[v] = true
	}
	var version string
	for {
		version, err = BuildVersion(s.VersionCore, n)
		if err != nil {
			return nil, nil, err
		}
		if !inUse[version] {
			break
		}
		slog.Warn("build number skipped: its version is already recorded", "project", s.Project, "number", n, "version", version)
		skipped = append(skipped, n)
		n++
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO builds (project, number, commit_sha, version_core, version, describe, machine)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.Project, n, commit, s.VersionCore, version, s.Describe, s.Machine)
	if err != nil {
		return nil, nil, fmt.Errorf("record build: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_sequences SET next = ? WHERE project = ?`, n+1, s.Project); err != nil {
		return nil, nil, fmt.Errorf("advance build sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	b, err = d.BuildByID(ctx, id)
	return b, skipped, err
}

// NextBuildNumber is the number the project's next build would get, before the
// in-use skip: the stored sequence, or — no build yet — the seed it would get.
func (d *DB) NextBuildNumber(ctx context.Context, project string, extra []string) (int64, error) {
	var next int64
	err := d.QueryRowContext(ctx, `SELECT next FROM build_sequences WHERE project = ?`, project).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		recorded, err := d.ProjectVersions(ctx, project)
		if err != nil {
			return 0, err
		}
		return SeedFrom(append(recorded, extra...)), nil
	}
	return next, err
}

// RaiseBuildNumber sets the project's next build number to next — only ever
// upward: a lower number is ErrSeedLower and changes nothing. previous is the
// next number before the call (the seed, when no build was started yet).
func (d *DB) RaiseBuildNumber(ctx context.Context, project string, next int64, extra []string) (previous int64, err error) {
	if strings.TrimSpace(project) == "" || next < 1 {
		return 0, fmt.Errorf("%w: a seed needs a project and a next number of at least 1", ErrInvalidBuild)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := ensureSequence(ctx, tx, project, extra)
	if err != nil {
		return 0, err
	}
	if next < cur {
		return cur, fmt.Errorf("%w: %s's next build number is %d; %d would hand out numbers again — the seed only raises it", ErrSeedLower, project, cur, next)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_sequences SET next = ? WHERE project = ?`, next, project); err != nil {
		return 0, err
	}
	return cur, tx.Commit()
}

const buildCols = `id, project, number, commit_sha, version_core, version, describe, machine, started_at, last_activity_at,
	step, status, steps, finished_at, result, artifact_sha256, failed_step, log_deleted_at, log_deleted_why`

func scanBuild(row interface{ Scan(...any) error }) (*Build, error) {
	var b Build
	var steps string
	var finished, logDeleted sql.NullTime
	var result, sha sql.NullString
	if err := row.Scan(&b.ID, &b.Project, &b.Number, &b.Commit, &b.VersionCore, &b.Version, &b.Describe, &b.Machine,
		&b.StartedAt, &b.LastActivityAt, &b.Step, &b.Status, &steps, &finished, &result, &sha, &b.FailedStep,
		&logDeleted, &b.LogDeletedWhy); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(steps), &b.Steps); err != nil {
		return nil, fmt.Errorf("build %d steps: %w", b.ID, err)
	}
	if finished.Valid {
		t := finished.Time
		b.FinishedAt = &t
	}
	if logDeleted.Valid {
		t := logDeleted.Time
		b.LogDeletedAt = &t
	}
	b.Result, b.ArtifactSHA256 = result.String, sha.String
	return &b, nil
}

func (d *DB) oneBuild(ctx context.Context, where string, args ...any) (*Build, error) {
	b, err := scanBuild(d.QueryRowContext(ctx, `SELECT `+buildCols+` FROM builds WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// BuildByID is one build, or ErrNotFound.
func (d *DB) BuildByID(ctx context.Context, id int64) (*Build, error) {
	return d.oneBuild(ctx, `id = ?`, id)
}

// BuildByNumber is one project's build by number, or ErrNotFound.
func (d *DB) BuildByNumber(ctx context.Context, project string, number int64) (*Build, error) {
	return d.oneBuild(ctx, `project = ? AND number = ?`, project, number)
}

// BuildByVersion is the build that minted this version in this project, or
// ErrNotFound — a version hz did not number.
func (d *DB) BuildByVersion(ctx context.Context, project, version string) (*Build, error) {
	return d.oneBuild(ctx, `project = ? AND version = ?`, project, version)
}

// PassedBuildOf is the newest PASSED build of project whose artifact is sha,
// or ErrNotFound.
func (d *DB) PassedBuildOf(ctx context.Context, project, sha string) (*Build, error) {
	return d.oneBuild(ctx, `project = ? AND artifact_sha256 = ? AND result = 'passed' ORDER BY id DESC LIMIT 1`, project, sha)
}

// Builds lists builds newest first — one project's, or every project's when
// project is "". limit <= 0 means 100.
func (d *DB) Builds(ctx context.Context, project string, limit int) ([]Build, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT `+buildCols+` FROM builds WHERE (? = '' OR project = ?) ORDER BY id DESC LIMIT ?`,
		project, project, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// finishedErr is ErrBuildFinished for a build that exists and is finished, and
// ErrNotFound for one that does not exist.
func (d *DB) finishedErr(ctx context.Context, id int64) error {
	b, err := d.BuildByID(ctx, id)
	if err != nil {
		return err
	}
	if b.FinishedAt != nil {
		return fmt.Errorf("%w: build #%d finished %s at %s", ErrBuildFinished, b.Number, b.Result, b.FinishedAt.UTC().Format(time.RFC3339))
	}
	return fmt.Errorf("build %d: no row updated", id)
}

// TouchBuild records log activity on an unfinished build.
func (d *DB) TouchBuild(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE builds SET last_activity_at = CURRENT_TIMESTAMP WHERE id = ? AND finished_at IS NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return d.finishedErr(ctx, id)
	}
	return nil
}

// Step statuses a builder may report.
var stepStatuses = map[string]bool{"pending": true, "running": true, "passed": true, "failed": true, "skipped": true}

// MaxBuildSteps bounds a state update's step list.
const MaxBuildSteps = 200

func checkLabel(what, s string) error {
	if err := CheckLocator(what, s); err != nil || len(s) > 256 {
		return fmt.Errorf("%w: %s must be at most 256 bytes with no control characters", ErrInvalidBuild, what)
	}
	return nil
}

func checkStamp(what, s string) error {
	if s == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return fmt.Errorf("%w: %s %q is not RFC 3339", ErrInvalidBuild, what, s)
	}
	return nil
}

// UpdateBuildState replaces an unfinished build's state: the current step,
// its overall status, and the step list.
func (d *DB) UpdateBuildState(ctx context.Context, id int64, step, status string, steps []BuildStep) error {
	if status != BuildRunning && status != BuildPassed && status != BuildFailed {
		return fmt.Errorf("%w: status %q is not running, passed or failed", ErrInvalidBuild, status)
	}
	if err := checkLabel("step", step); err != nil {
		return err
	}
	if len(steps) > MaxBuildSteps {
		return fmt.Errorf("%w: %d steps; the limit is %d", ErrInvalidBuild, len(steps), MaxBuildSteps)
	}
	if steps == nil {
		steps = []BuildStep{}
	}
	for i, s := range steps {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("%w: step %d has no name", ErrInvalidBuild, i)
		}
		if err := checkLabel("step name", s.Name); err != nil {
			return err
		}
		if !stepStatuses[s.Status] {
			return fmt.Errorf("%w: step %q status %q is not pending, running, passed, failed or skipped", ErrInvalidBuild, s.Name, s.Status)
		}
		if err := checkStamp("started_at", s.StartedAt); err != nil {
			return err
		}
		if err := checkStamp("finished_at", s.FinishedAt); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	res, err := d.ExecContext(ctx, `
		UPDATE builds SET step = ?, status = ?, steps = ?, last_activity_at = CURRENT_TIMESTAMP
		WHERE id = ? AND finished_at IS NULL`, step, status, string(raw), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return d.finishedErr(ctx, id)
	}
	return nil
}

// FinishBuild records a build's one finish. passed carries the artifact (whose
// upload and project the server has checked); failed carries none. A second
// finish is ErrBuildFinished.
func (d *DB) FinishBuild(ctx context.Context, id int64, result, artifactSHA, failedStep string) error {
	var sha any
	switch result {
	case BuildPassed:
		s, err := NormalizeSHA256(artifactSHA)
		if err != nil {
			return fmt.Errorf("%w: a passed build names its artifact: %v", ErrInvalidBuild, err)
		}
		if failedStep != "" {
			return fmt.Errorf("%w: a passed build has no failed_step", ErrInvalidBuild)
		}
		sha = s
	case BuildFailed:
		if strings.TrimSpace(artifactSHA) != "" {
			return fmt.Errorf("%w: a failed build takes no artifact — nothing it built may be promoted", ErrInvalidBuild)
		}
		if err := checkLabel("failed_step", failedStep); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: result %q is not passed or failed", ErrInvalidBuild, result)
	}
	res, err := d.ExecContext(ctx, `
		UPDATE builds SET result = ?, status = ?, artifact_sha256 = ?, failed_step = ?, finished_at = CURRENT_TIMESTAMP
		WHERE id = ? AND finished_at IS NULL`, result, result, sha, failedStep, id)
	if err != nil {
		return fmt.Errorf("finish build: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return d.finishedErr(ctx, id)
	}
	return nil
}

// RecordBuildLogDeleted marks a build's log file as deleted by retention.
func (d *DB) RecordBuildLogDeleted(ctx context.Context, id int64, why string) error {
	if strings.TrimSpace(why) == "" {
		return errors.New("a log deletion needs a reason")
	}
	_, err := d.ExecContext(ctx, `UPDATE builds SET log_deleted_at = CURRENT_TIMESTAMP, log_deleted_why = ? WHERE id = ? AND log_deleted_at IS NULL`, why, id)
	return err
}

// IsGrandfathered reports whether sha was uploaded before migration 0018 —
// promotable with no build record.
func (d *DB) IsGrandfathered(ctx context.Context, sha string) (bool, error) {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_grandfathered WHERE sha256 = ?`, sha).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
