package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Deploy reports and the promotion record (migrations 0014, 0015).
//
// plan/plan.md "Redline push to prod — the plan", steps H1–H3. A report is what
// a rung SAID it runs; a promotion is who moved a version up a rung, when, and
// which artifact. Both are observations, so both live here and not in
// config.json (declared state, peer-synced), and both are append-only — the
// migrations hold triggers that refuse UPDATE and DELETE, so this file has no
// writer that could do either.
//
// "Latest" is always by id, never by clock. The ids come from AUTOINCREMENT and
// only go up; reported_at is second-resolution and a box's clock is not ours.

// ErrInvalidSHA256 means an artifact digest is not 64 hex characters.
var ErrInvalidSHA256 = errors.New("invalid artifact sha256")

// DeployReport is one row of deploy_reports.
type DeployReport struct {
	ID             int64
	Project        string
	Environment    string
	App            string
	Version        string
	Describe       string
	ArtifactSHA256 string
	Host           string
	// BuildURL is where the build's tests and logs are kept; "" when the
	// report carried none (every report before migration 0016).
	BuildURL   string
	ReportedAt time.Time
	ReportedBy string
}

// Promotion is one row of promotions.
type Promotion struct {
	ID             int64
	Project        string
	FromEnv        string
	ToEnv          string
	Version        string
	ArtifactSHA256 string
	PromotedAt     time.Time
	PromotedBy     string
	// Downgrade is true when --allow-downgrade was NEEDED, not merely passed.
	Downgrade bool
	// BuildURL is the pinned report's build_url, pinned beside its artifact.
	BuildURL string
	// RestoreGate is what the restore-test gate answered: RestoreGateChecked
	// (Lines holds each line and its evidence), RestoreGateNoneRequired (the
	// target had no supported line), or RestoreGatePredates on a row written
	// before the gate existed. Derived from Lines on write; read back as stored.
	RestoreGate string
	// Lines is set on write only; PromotionLines reads them back.
	Lines []PromotionLine
}

// The restore-gate answers a promotion row carries (migration 0016).
const (
	RestoreGatePredates     = "predates"
	RestoreGateNoneRequired = "none-required"
	RestoreGateChecked      = "checked"
)

// PromotionLine is one line a promotion was checked against and the evidence
// that satisfied it.
type PromotionLine struct {
	Line          string
	Why           string
	KeptBackupID  int64
	RestoreTestID int64
}

// CheckDeployVersion refuses a version that the promotion gate could not order.
//
// It is parseVersion — hz's one semver parser — plus two refusals parseVersion
// does not make: a leading "v" and build metadata. parseVersion strips both,
// which is right for comparing; it is wrong for a value that is stored and then
// matched, because "v1.4.0" and "1.4.0+abc" would be three spellings of one
// version in a table read by equality. Build metadata belongs in `describe`.
// A prerelease ("1.0.0-rc.1.1414") is accepted and ordered by CompareVersions.
func CheckDeployVersion(v string) error {
	if v != strings.TrimSpace(v) || v == "" {
		return fmt.Errorf("%w: %q", ErrInvalidVersion, v)
	}
	if strings.HasPrefix(v, "v") || strings.HasPrefix(v, "V") {
		return fmt.Errorf("%w: %q has a leading v — send the bare MAJOR.MINOR.PATCH[-PRERELEASE]", ErrInvalidVersion, v)
	}
	if strings.Contains(v, "+") {
		return fmt.Errorf("%w: %q carries build metadata — that belongs in describe, not version", ErrInvalidVersion, v)
	}
	if _, err := parseVersion(v); err != nil {
		return err
	}
	return nil
}

// ErrInvalidLocator means a build_url or a backup location is too long or
// carries a control character.
var ErrInvalidLocator = errors.New("invalid locator")

// MaxLocatorLen bounds a build_url or a kept backup's location.
const MaxLocatorLen = 2048

// CheckLocator refuses a build_url or location hz would store and render
// unsafely: over MaxLocatorLen, or holding a control character (a newline in a
// value shown in a table, or a terminal escape in `hz lines`). Any scheme is
// accepted — an R0 build names a bucket path — so this is NOT a URL parse.
// "" passes; whether a field is required is the caller's question.
func CheckLocator(what, s string) error {
	if len(s) > MaxLocatorLen {
		return fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrInvalidLocator, what, len(s), MaxLocatorLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s carries a control character (%U)", ErrInvalidLocator, what, r)
		}
	}
	return nil
}

// NormalizeSHA256 lowercases a hex digest and refuses anything that is not 64
// hex characters. Lowercase because `sha256sum` prints lowercase, and a digest
// compared by equality must have one spelling.
func NormalizeSHA256(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", fmt.Errorf("%w: want 64 hex characters, got %d", ErrInvalidSHA256, len(s))
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: %q is not hex", ErrInvalidSHA256, string(c))
		}
	}
	return s, nil
}

// RecordDeployReport appends one report and returns its id. It validates the
// version and digest shape; whether the rung is DECLARED is the server's
// question, because the declaration lives in config.json, not here.
func (d *DB) RecordDeployReport(ctx context.Context, r DeployReport) (int64, error) {
	if strings.TrimSpace(r.Project) == "" || strings.TrimSpace(r.Environment) == "" {
		return 0, errors.New("a deploy report needs a project and an environment")
	}
	if strings.TrimSpace(r.ReportedBy) == "" {
		return 0, errors.New("a deploy report needs a reporter")
	}
	if err := CheckDeployVersion(r.Version); err != nil {
		return 0, err
	}
	sha, err := NormalizeSHA256(r.ArtifactSHA256)
	if err != nil {
		return 0, err
	}
	if err := CheckLocator("build_url", r.BuildURL); err != nil {
		return 0, err
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO deploy_reports
		    (project, environment, app, version, describe, artifact_sha256, host, build_url, reported_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Project, r.Environment, r.App, r.Version, r.Describe, sha, r.Host, r.BuildURL, r.ReportedBy)
	if err != nil {
		return 0, fmt.Errorf("record deploy report: %w", err)
	}
	return res.LastInsertId()
}

const deployReportCols = `id, project, environment, app, version, describe, artifact_sha256, host, build_url, reported_at, reported_by`

func scanDeployReport(row interface{ Scan(...any) error }) (*DeployReport, error) {
	var r DeployReport
	if err := row.Scan(&r.ID, &r.Project, &r.Environment, &r.App, &r.Version, &r.Describe,
		&r.ArtifactSHA256, &r.Host, &r.BuildURL, &r.ReportedAt, &r.ReportedBy); err != nil {
		return nil, err
	}
	return &r, nil
}

// LatestDeployReport is the newest report for a rung, or ErrNotFound — "never
// reported" is an answer, not an empty row.
func (d *DB) LatestDeployReport(ctx context.Context, project, environment string) (*DeployReport, error) {
	r, err := scanDeployReport(d.QueryRowContext(ctx, `
		SELECT `+deployReportCols+` FROM deploy_reports
		WHERE project = ? AND environment = ?
		ORDER BY id DESC LIMIT 1`, project, environment))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// LatestDeployReports is the newest report of every rung that has one, sorted
// by project then environment. A rung that never reported is absent, which the
// caller renders as "nothing reported", never as a blank.
func (d *DB) LatestDeployReports(ctx context.Context) ([]DeployReport, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+deployReportCols+` FROM deploy_reports
		WHERE id IN (SELECT MAX(id) FROM deploy_reports GROUP BY project, environment)
		ORDER BY project, environment`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployReport
	for rows.Next() {
		r, err := scanDeployReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RecordPromotion appends one promotion — and, in the same transaction, one
// promotion_lines row per line it was checked against — and returns its id.
// restore_gate is derived from p.Lines: none means the gate required nothing.
func (d *DB) RecordPromotion(ctx context.Context, p Promotion) (int64, error) {
	if strings.TrimSpace(p.Project) == "" || strings.TrimSpace(p.FromEnv) == "" || strings.TrimSpace(p.ToEnv) == "" {
		return 0, errors.New("a promotion needs a project, a source and a target")
	}
	if strings.TrimSpace(p.PromotedBy) == "" {
		return 0, errors.New("a promotion needs a promoter")
	}
	if err := CheckDeployVersion(p.Version); err != nil {
		return 0, err
	}
	sha, err := NormalizeSHA256(p.ArtifactSHA256)
	if err != nil {
		return 0, err
	}
	if err := CheckLocator("build_url", p.BuildURL); err != nil {
		return 0, err
	}
	downgrade := 0
	if p.Downgrade {
		downgrade = 1
	}
	gate := RestoreGateNoneRequired
	if len(p.Lines) > 0 {
		gate = RestoreGateChecked
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO promotions (project, from_env, to_env, version, artifact_sha256, promoted_by, downgrade, build_url, restore_gate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Project, p.FromEnv, p.ToEnv, p.Version, sha, p.PromotedBy, downgrade, p.BuildURL, gate)
	if err != nil {
		return 0, fmt.Errorf("record promotion: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, l := range p.Lines {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO promotion_lines (promotion_id, line, why, kept_backup_id, restore_test_id)
			VALUES (?, ?, ?, ?, ?)`, id, l.Line, l.Why, l.KeptBackupID, l.RestoreTestID); err != nil {
			return 0, fmt.Errorf("record promotion line %s: %w", l.Line, err)
		}
	}
	return id, tx.Commit()
}

// PromotionLines is the lines one promotion was checked against, by line.
func (d *DB) PromotionLines(ctx context.Context, promotionID int64) ([]PromotionLine, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT line, why, kept_backup_id, restore_test_id FROM promotion_lines
		WHERE promotion_id = ? ORDER BY line`, promotionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PromotionLine
	for rows.Next() {
		var l PromotionLine
		if err := rows.Scan(&l.Line, &l.Why, &l.KeptBackupID, &l.RestoreTestID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const promotionCols = `id, project, from_env, to_env, version, artifact_sha256, promoted_at, promoted_by, downgrade, build_url, restore_gate`

func scanPromotion(row interface{ Scan(...any) error }) (*Promotion, error) {
	var p Promotion
	var downgrade int
	if err := row.Scan(&p.ID, &p.Project, &p.FromEnv, &p.ToEnv, &p.Version, &p.ArtifactSHA256,
		&p.PromotedAt, &p.PromotedBy, &downgrade, &p.BuildURL, &p.RestoreGate); err != nil {
		return nil, err
	}
	p.Downgrade = downgrade == 1
	return &p, nil
}

// LatestPromotionOf is the newest promotion of one version into one rung, or
// ErrNotFound. Its artifact is the PINNED one the deploy check compares.
func (d *DB) LatestPromotionOf(ctx context.Context, project, toEnv, version string) (*Promotion, error) {
	p, err := scanPromotion(d.QueryRowContext(ctx, `
		SELECT `+promotionCols+` FROM promotions
		WHERE project = ? AND to_env = ? AND version = ?
		ORDER BY id DESC LIMIT 1`, project, toEnv, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// Promotions lists promotions newest first — one project's, or every
// project's when project is "". limit <= 0 means 50.
func (d *DB) Promotions(ctx context.Context, project string, limit int) ([]Promotion, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+promotionCols+` FROM promotions
		WHERE (? = '' OR project = ?)
		ORDER BY id DESC LIMIT ?`, project, project, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Promotion
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PromotionsInto is every promotion into one rung, newest first — the input
// DeriveSupportedLines reads the prior line from.
func (d *DB) PromotionsInto(ctx context.Context, project, toEnv string) ([]Promotion, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+promotionCols+` FROM promotions
		WHERE project = ? AND to_env = ?
		ORDER BY id DESC`, project, toEnv)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Promotion
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
