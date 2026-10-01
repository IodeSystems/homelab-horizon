package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Release lines, kept backups and restore tests (migration 0016).
//
// plan/plan.md "Versions, lines and the restore test", decided 2026-09-30:
//
//   - A version's LINE is its semver core: 1.9.0-1.2 is on line 1.9.0. Redline's
//     MAJOR.MINOR.FIX.HOTFIX#BUILD travels as MAJOR.MINOR.FIX-HOTFIX.BUILD, so a
//     hotfix and a rebuild stay on their line. Derived by parseVersion, hz's one
//     parser — there is no second one, and no 4-part parser.
//   - The SUPPORTED lines of a rung are DERIVED (CLAUDE.md #8), never stored:
//     the line of its declared version, the most recent DIFFERENT line promoted
//     into it, and any line its project pins (config.json, with a reason).
//   - Each supported line keeps one backup, held by the app; hz records it. The
//     newest record per (project, line) is the kept backup.
//   - A restore test is "build V restored line L's kept backup, migrated, tests
//     passed" — or failed. The promotion gate reads the newest one.

// LineOf is the line a version is on: its MAJOR.MINOR.PATCH, re-spelled from
// the parsed numbers so "1.09.0" and "1.9.0" are one line. The version must be
// one CheckDeployVersion accepts — the same rule a report and a promotion obey.
func LineOf(version string) (string, error) {
	if err := CheckDeployVersion(version); err != nil {
		return "", err
	}
	p, err := parseVersion(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", p.major, p.minor, p.patch), nil
}

// CheckLine refuses a string that is not a line in its one spelling: a bare
// MAJOR.MINOR.PATCH, no prerelease, no leading zero.
func CheckLine(line string) error {
	l, err := LineOf(line)
	if err != nil {
		return fmt.Errorf("%w: line %q is not MAJOR.MINOR.PATCH", ErrInvalidVersion, line)
	}
	if l != line {
		return fmt.Errorf("%w: %q is a version, not a line — a line is a version's MAJOR.MINOR.PATCH (%s)", ErrInvalidVersion, line, l)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The derivation.

// The reasons a line is supported.
const (
	WhyCurrent = "current"
	WhyPrior   = "prior"
	WhyPinned  = "pinned"
)

// LinePin is a project's pin, as config.json declares it. The db package does
// not import config (the agent links config and must not link SQLite), so the
// server copies pins into this shape.
type LinePin struct {
	Line   string
	Reason string
}

// LineWhy is one reason a line is supported.
type LineWhy struct {
	Kind   string // WhyCurrent, WhyPrior or WhyPinned
	Detail string // the sentence: what declares it, which promotion, the pin's reason
}

// SupportedLine is one line and every reason it is supported.
type SupportedLine struct {
	Line string
	Why  []LineWhy
}

// Kinds is the line's reasons joined — the `why` stored on promotion_lines.
func (s SupportedLine) Kinds() string {
	k := make([]string, 0, len(s.Why))
	for _, w := range s.Why {
		k = append(k, w.Kind)
	}
	return strings.Join(k, ",")
}

// RungLines is the derivation's answer for one rung. Supported empty AND Gaps
// empty is "no supported line" — the first release. A gap is "hz cannot say":
// a declared version that is not semver, or a pin that is not a line. They are
// different answers (CLAUDE.md #2), and the gate refuses on a gap.
type RungLines struct {
	Supported []SupportedLine
	Gaps      []string
}

// DeriveSupportedLines computes one rung's supported lines. Pure: the caller
// supplies the declared version (config.json), every promotion INTO the rung
// newest first (PromotionsInto), and the project's pins.
//
// The prior line is the line of the newest promotion whose line differs from
// the current line. Same-line promotions — hotfixes, rebuilds — are skipped:
// "always one prior" means one prior LINE.
func DeriveSupportedLines(project, environment, declared string, into []Promotion, pins []LinePin) RungLines {
	var out RungLines
	add := func(line string, why LineWhy) {
		for i := range out.Supported {
			if out.Supported[i].Line == line {
				out.Supported[i].Why = append(out.Supported[i].Why, why)
				return
			}
		}
		out.Supported = append(out.Supported, SupportedLine{Line: line, Why: []LineWhy{why}})
	}

	current := ""
	if declared != "" {
		l, err := LineOf(declared)
		if err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("%s/%s declares %q, which hz cannot read a line from (%v) — "+
				"so it cannot say which kept backup a release must restore", project, environment, declared, err))
		} else {
			current = l
			add(l, LineWhy{Kind: WhyCurrent, Detail: project + "/" + environment + " declares " + declared})
		}
	}
	for _, p := range into {
		l, err := LineOf(p.Version)
		if err != nil {
			// A promotions row is validated on insert; one that does not parse
			// is a gap, not a line to skip silently.
			out.Gaps = append(out.Gaps, fmt.Sprintf("promotion #%d records %q, which hz cannot read a line from", p.ID, p.Version))
			continue
		}
		if l == current {
			continue
		}
		add(l, LineWhy{Kind: WhyPrior, Detail: fmt.Sprintf("%s was promoted from %s by promotion #%d", p.Version, p.FromEnv, p.ID)})
		break
	}
	for _, pin := range pins {
		if err := CheckLine(pin.Line); err != nil {
			out.Gaps = append(out.Gaps, fmt.Sprintf("%s pins %q, which is not a line (%v) — unpin it and pin the MAJOR.MINOR.PATCH", project, pin.Line, err))
			continue
		}
		add(pin.Line, LineWhy{Kind: WhyPinned, Detail: pin.Reason})
	}
	return out
}

// ---------------------------------------------------------------------------
// Kept backups.

// KeptBackup is one row of kept_backups.
type KeptBackup struct {
	ID             int64
	Project        string
	Line           string
	BackupSHA256   string
	Location       string
	TakenByVersion string
	BuildURL       string
	RecordedAt     time.Time
	RecordedBy     string
}

// RecordKeptBackup appends one kept-backup record. The newest per (project,
// line) is the kept backup; an older one is superseded, never edited.
//
// TakenByVersion must be ON the line: a backup's schema is the schema of the
// build that took it, so a backup taken by 1.9.1-0.2 is a 1.9.1 backup however
// it is labelled.
func (d *DB) RecordKeptBackup(ctx context.Context, b KeptBackup) (int64, error) {
	if strings.TrimSpace(b.Project) == "" {
		return 0, errors.New("a kept backup needs a project")
	}
	if strings.TrimSpace(b.RecordedBy) == "" {
		return 0, errors.New("a kept backup needs a recorder")
	}
	if err := CheckLine(b.Line); err != nil {
		return 0, err
	}
	taken, err := LineOf(b.TakenByVersion)
	if err != nil {
		return 0, fmt.Errorf("taken_by_version: %w", err)
	}
	if taken != b.Line {
		return 0, fmt.Errorf("%w: a backup taken by %s is of line %s, not %s", ErrInvalidVersion, b.TakenByVersion, taken, b.Line)
	}
	sha, err := NormalizeSHA256(b.BackupSHA256)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(b.Location) == "" {
		return 0, fmt.Errorf("%w: location is required — where the app keeps the backup", ErrInvalidLocator)
	}
	if err := CheckLocator("location", b.Location); err != nil {
		return 0, err
	}
	if err := CheckLocator("build_url", b.BuildURL); err != nil {
		return 0, err
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO kept_backups (project, line, backup_sha256, location, taken_by_version, build_url, recorded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.Project, b.Line, sha, b.Location, b.TakenByVersion, b.BuildURL, b.RecordedBy)
	if err != nil {
		return 0, fmt.Errorf("record kept backup: %w", err)
	}
	return res.LastInsertId()
}

const keptBackupCols = `id, project, line, backup_sha256, location, taken_by_version, build_url, recorded_at, recorded_by`

func scanKeptBackup(row interface{ Scan(...any) error }) (*KeptBackup, error) {
	var b KeptBackup
	if err := row.Scan(&b.ID, &b.Project, &b.Line, &b.BackupSHA256, &b.Location, &b.TakenByVersion,
		&b.BuildURL, &b.RecordedAt, &b.RecordedBy); err != nil {
		return nil, err
	}
	return &b, nil
}

// LatestKeptBackup is the newest kept-backup record for a line, or ErrNotFound.
func (d *DB) LatestKeptBackup(ctx context.Context, project, line string) (*KeptBackup, error) {
	b, err := scanKeptBackup(d.QueryRowContext(ctx, `
		SELECT `+keptBackupCols+` FROM kept_backups
		WHERE project = ? AND line = ?
		ORDER BY id DESC LIMIT 1`, project, line))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// LatestKeptBackups is the newest kept backup of every line one project has
// recorded one for, sorted by line string. Supported or not: the caller says
// which are retired.
func (d *DB) LatestKeptBackups(ctx context.Context, project string) ([]KeptBackup, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+keptBackupCols+` FROM kept_backups
		WHERE id IN (SELECT MAX(id) FROM kept_backups WHERE project = ? GROUP BY line)
		ORDER BY line`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []KeptBackup
	for rows.Next() {
		b, err := scanKeptBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Restore tests.

// RestoreTest is one row of restore_tests.
type RestoreTest struct {
	ID           int64
	Project      string
	Environment  string
	Version      string
	Line         string
	BackupSHA256 string
	Passed       bool
	BuildURL     string
	ReportedAt   time.Time
	ReportedBy   string
}

// RecordRestoreTest appends one restore-test report. Whether the rung is
// declared is the server's question, as for a deploy report.
func (d *DB) RecordRestoreTest(ctx context.Context, t RestoreTest) (int64, error) {
	if strings.TrimSpace(t.Project) == "" || strings.TrimSpace(t.Environment) == "" {
		return 0, errors.New("a restore test needs a project and an environment")
	}
	if strings.TrimSpace(t.ReportedBy) == "" {
		return 0, errors.New("a restore test needs a reporter")
	}
	if err := CheckDeployVersion(t.Version); err != nil {
		return 0, err
	}
	if err := CheckLine(t.Line); err != nil {
		return 0, err
	}
	sha, err := NormalizeSHA256(t.BackupSHA256)
	if err != nil {
		return 0, err
	}
	if err := CheckLocator("build_url", t.BuildURL); err != nil {
		return 0, err
	}
	passed := 0
	if t.Passed {
		passed = 1
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO restore_tests (project, environment, version, line, backup_sha256, passed, build_url, reported_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Project, t.Environment, t.Version, t.Line, sha, passed, t.BuildURL, t.ReportedBy)
	if err != nil {
		return 0, fmt.Errorf("record restore test: %w", err)
	}
	return res.LastInsertId()
}

const restoreTestCols = `id, project, environment, version, line, backup_sha256, passed, build_url, reported_at, reported_by`

func scanRestoreTest(row interface{ Scan(...any) error }) (*RestoreTest, error) {
	var t RestoreTest
	var passed int
	if err := row.Scan(&t.ID, &t.Project, &t.Environment, &t.Version, &t.Line, &t.BackupSHA256,
		&passed, &t.BuildURL, &t.ReportedAt, &t.ReportedBy); err != nil {
		return nil, err
	}
	t.Passed = passed == 1
	return &t, nil
}

// LatestRestoreTest is the newest restore test of version on one rung against
// one line. backupSHA256 "" means against ANY backup of that line — the
// caller uses it to say a test exists but against a superseded backup.
// ErrNotFound when there is none.
func (d *DB) LatestRestoreTest(ctx context.Context, project, environment, version, line, backupSHA256 string) (*RestoreTest, error) {
	t, err := scanRestoreTest(d.QueryRowContext(ctx, `
		SELECT `+restoreTestCols+` FROM restore_tests
		WHERE project = ? AND environment = ? AND version = ? AND line = ?
		  AND (? = '' OR backup_sha256 = ?)
		ORDER BY id DESC LIMIT 1`, project, environment, version, line, backupSHA256, backupSHA256))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}
