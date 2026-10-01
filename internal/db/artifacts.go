package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The artifact store's records, the apply record, rung holds and instance
// pulls (migration 0017, plan/plan.md N4a).
//
// A promotion APPROVES a build for a rung; an APPLY is what the rung runs.
// `desired` reads the newest apply, never the newest promotion — so a rung is
// held by default. A hold is the emergency stop on top. All of it is
// append-only by trigger except instance_pulls, a last-seen observation.

// Artifact is one artifacts row and its file's current state.
type Artifact struct {
	SHA256     string
	Project    string
	Size       int64
	UploadedAt time.Time
	UploadedBy string
	// DeletedAt is set when retention deleted the file and nothing re-uploaded
	// it since; DeletedWhy is the reason it was not kept. The row outlives the
	// file: "never uploaded" (ErrNotFound) and "deleted" are different answers.
	DeletedAt  *time.Time
	DeletedWhy string
	// StoredAt is when the current file was written: the upload, or the
	// newest re-upload after a deletion. Retention ages from this.
	StoredAt time.Time
}

// Deleted reports whether the file is gone by retention.
func (a Artifact) Deleted() bool { return a.DeletedAt != nil }

// Artifact kinds of event.
const (
	ArtifactDeleted  = "deleted"
	ArtifactRestored = "restored"
)

// RecordArtifact records a first upload. It returns existing=true, and writes
// nothing, when the sha is already recorded — an upload is idempotent, and the
// first uploader is the one on record.
func (d *DB) RecordArtifact(ctx context.Context, a Artifact) (existing bool, err error) {
	sha, err := NormalizeSHA256(a.SHA256)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(a.Project) == "" || strings.TrimSpace(a.UploadedBy) == "" {
		return false, errors.New("an artifact needs a project and an uploader")
	}
	if a.Size < 0 {
		return false, errors.New("an artifact has no negative size")
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO artifacts (sha256, project, size, uploaded_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (sha256) DO NOTHING`, sha, a.Project, a.Size, a.UploadedBy)
	if err != nil {
		return false, fmt.Errorf("record artifact: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// RecordArtifactEvent appends a 'deleted' or 'restored' event for a recorded
// artifact.
func (d *DB) RecordArtifactEvent(ctx context.Context, sha, kind, reason, by string) error {
	if kind != ArtifactDeleted && kind != ArtifactRestored {
		return fmt.Errorf("unknown artifact event %q", kind)
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(by) == "" {
		return errors.New("an artifact event needs a reason and an actor")
	}
	_, err := d.ExecContext(ctx, `INSERT INTO artifact_events (sha256, kind, reason, actor) VALUES (?, ?, ?, ?)`,
		sha, kind, reason, by)
	if err != nil {
		return fmt.Errorf("record artifact event: %w", err)
	}
	return nil
}

const artifactSelect = `
	SELECT a.sha256, a.project, a.size, a.uploaded_at, a.uploaded_by, e.kind, e.event_at, e.reason
	FROM artifacts a
	LEFT JOIN artifact_events e ON e.id = (SELECT MAX(id) FROM artifact_events WHERE sha256 = a.sha256)`

func scanArtifact(row interface{ Scan(...any) error }) (*Artifact, error) {
	var a Artifact
	var kind, reason sql.NullString
	var at sql.NullTime
	if err := row.Scan(&a.SHA256, &a.Project, &a.Size, &a.UploadedAt, &a.UploadedBy, &kind, &at, &reason); err != nil {
		return nil, err
	}
	a.StoredAt = a.UploadedAt
	if kind.Valid && at.Valid {
		switch kind.String {
		case ArtifactDeleted:
			t := at.Time
			a.DeletedAt = &t
			a.DeletedWhy = reason.String
		case ArtifactRestored:
			a.StoredAt = at.Time
		}
	}
	return &a, nil
}

// LookupArtifact is one artifact's record, or ErrNotFound: never uploaded.
func (d *DB) LookupArtifact(ctx context.Context, sha string) (*Artifact, error) {
	a, err := scanArtifact(d.QueryRowContext(ctx, artifactSelect+` WHERE a.sha256 = ?`, sha))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// Artifacts is every artifact record, oldest upload first.
func (d *DB) Artifacts(ctx context.Context) ([]Artifact, error) {
	rows, err := d.QueryContext(ctx, artifactSelect+` ORDER BY a.uploaded_at, a.sha256`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Applies.

// Apply is one applies row.
type Apply struct {
	ID             int64
	Project        string
	Environment    string
	Version        string
	ArtifactSHA256 string
	PromotionID    int64
	AppliedAt      time.Time
	AppliedBy      string
}

// RecordApply appends one apply. Whether it is ALLOWED (the newest promotion
// into the rung is this one) is the server's question, asked under promoteMu.
func (d *DB) RecordApply(ctx context.Context, a Apply) (int64, error) {
	if strings.TrimSpace(a.Project) == "" || strings.TrimSpace(a.Environment) == "" {
		return 0, errors.New("an apply needs a project and an environment")
	}
	if strings.TrimSpace(a.AppliedBy) == "" {
		return 0, errors.New("an apply needs an actor")
	}
	if err := CheckDeployVersion(a.Version); err != nil {
		return 0, err
	}
	sha, err := NormalizeSHA256(a.ArtifactSHA256)
	if err != nil {
		return 0, err
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO applies (project, environment, version, artifact_sha256, promotion_id, applied_by)
		VALUES (?, ?, ?, ?, ?, ?)`, a.Project, a.Environment, a.Version, sha, a.PromotionID, a.AppliedBy)
	if err != nil {
		return 0, fmt.Errorf("record apply: %w", err)
	}
	return res.LastInsertId()
}

const applyCols = `id, project, environment, version, artifact_sha256, promotion_id, applied_at, applied_by`

func scanApply(row interface{ Scan(...any) error }) (*Apply, error) {
	var a Apply
	if err := row.Scan(&a.ID, &a.Project, &a.Environment, &a.Version, &a.ArtifactSHA256,
		&a.PromotionID, &a.AppliedAt, &a.AppliedBy); err != nil {
		return nil, err
	}
	return &a, nil
}

// LatestApply is the newest apply into a rung, or ErrNotFound: nothing applied.
func (d *DB) LatestApply(ctx context.Context, project, environment string) (*Apply, error) {
	a, err := scanApply(d.QueryRowContext(ctx, `
		SELECT `+applyCols+` FROM applies WHERE project = ? AND environment = ?
		ORDER BY id DESC LIMIT 1`, project, environment))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// Applies is every apply, newest first; project "" is every project.
func (d *DB) Applies(ctx context.Context, project string) ([]Apply, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+applyCols+` FROM applies WHERE (? = '' OR project = ?) ORDER BY id DESC`, project, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Apply
	for rows.Next() {
		a, err := scanApply(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// AppliedTo reports whether any apply into one of these rungs pinned sha —
// the instance token's download scope.
func (d *DB) AppliedTo(ctx context.Context, sha string, rungs [][2]string) (bool, error) {
	for _, r := range rungs {
		var n int
		if err := d.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM applies WHERE project = ? AND environment = ? AND artifact_sha256 = ?`,
			r[0], r[1], sha).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// LatestPromotionInto is the newest promotion into a rung, of any version, or
// ErrNotFound. It is the only promotion an apply may apply.
func (d *DB) LatestPromotionInto(ctx context.Context, project, toEnv string) (*Promotion, error) {
	p, err := scanPromotion(d.QueryRowContext(ctx, `
		SELECT `+promotionCols+` FROM promotions WHERE project = ? AND to_env = ?
		ORDER BY id DESC LIMIT 1`, project, toEnv))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PromotionByID is one promotion, or ErrNotFound.
func (d *DB) PromotionByID(ctx context.Context, id int64) (*Promotion, error) {
	p, err := scanPromotion(d.QueryRowContext(ctx, `SELECT `+promotionCols+` FROM promotions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ---------------------------------------------------------------------------
// Holds.

// Hold kinds.
const (
	HoldOn  = "hold"
	HoldOff = "unhold"
)

// HoldEvent is one rung_holds row.
type HoldEvent struct {
	ID          int64
	Project     string
	Environment string
	Kind        string
	Reason      string
	At          time.Time
	By          string
}

// Held reports whether this event leaves the rung held.
func (h HoldEvent) Held() bool { return h.Kind == HoldOn }

// RecordHold appends a hold (reason required) or an unhold.
func (d *DB) RecordHold(ctx context.Context, h HoldEvent) (int64, error) {
	if strings.TrimSpace(h.Project) == "" || strings.TrimSpace(h.Environment) == "" {
		return 0, errors.New("a hold needs a project and an environment")
	}
	if strings.TrimSpace(h.By) == "" {
		return 0, errors.New("a hold needs an actor")
	}
	switch h.Kind {
	case HoldOn:
		if strings.TrimSpace(h.Reason) == "" {
			return 0, errors.New("a hold needs a reason — it stops a rung, and whoever lifts it needs to know why")
		}
	case HoldOff:
	default:
		return 0, fmt.Errorf("unknown hold kind %q", h.Kind)
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO rung_holds (project, environment, kind, reason, actor) VALUES (?, ?, ?, ?, ?)`,
		h.Project, h.Environment, h.Kind, strings.TrimSpace(h.Reason), h.By)
	if err != nil {
		return 0, fmt.Errorf("record hold: %w", err)
	}
	return res.LastInsertId()
}

// LatestHold is the newest hold event of a rung, or ErrNotFound: never held.
// The rung is held when the event is a hold.
func (d *DB) LatestHold(ctx context.Context, project, environment string) (*HoldEvent, error) {
	var h HoldEvent
	err := d.QueryRowContext(ctx, `
		SELECT id, project, environment, kind, reason, event_at, actor FROM rung_holds
		WHERE project = ? AND environment = ? ORDER BY id DESC LIMIT 1`, project, environment).
		Scan(&h.ID, &h.Project, &h.Environment, &h.Kind, &h.Reason, &h.At, &h.By)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// ---------------------------------------------------------------------------
// Instance pulls.

// InstancePull is when a nested hz last asked for one of its rungs' desired.
type InstancePull struct {
	Machine     string
	Project     string
	Environment string
	At          time.Time
}

// RecordInstancePull overwrites the machine's last-pull row.
func (d *DB) RecordInstancePull(ctx context.Context, machine, project, environment string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO instance_pulls (machine, project, environment, last_pull_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (machine) DO UPDATE SET project = excluded.project, environment = excluded.environment,
		    last_pull_at = excluded.last_pull_at`, machine, project, environment)
	return err
}

// InstancePulls is every machine's last pull, by machine.
func (d *DB) InstancePulls(ctx context.Context) (map[string]InstancePull, error) {
	rows, err := d.QueryContext(ctx, `SELECT machine, project, environment, last_pull_at FROM instance_pulls`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]InstancePull{}
	for rows.Next() {
		var p InstancePull
		if err := rows.Scan(&p.Machine, &p.Project, &p.Environment, &p.At); err != nil {
			return nil, err
		}
		out[p.Machine] = p
	}
	return out, rows.Err()
}
