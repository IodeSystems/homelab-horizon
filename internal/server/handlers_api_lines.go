package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Release lines, kept backups and the restore-test gate (plan/plan.md
// "Versions, lines and the restore test", decided 2026-09-30).
//
//	POST /api/v1/backups/kept             the app keeps a backup for a line
//	POST /api/v1/restore-tests/report     version V restored line L's kept backup — passed or not
//	GET  /api/v1/projects/lines?project=  supported lines per rung, kept backups, retired lines
//	POST /api/v1/projects/lines/pin       keep a line supported, with a reason (config.json)
//	POST /api/v1/projects/lines/unpin
//
// SUPPORTED LINES ARE DERIVED on every read and every promote
// (db.DeriveSupportedLines), never stored: the declared version's line, the
// most recent different line promoted into the rung, and the project's pins.
// A stored list would be a second answer free to disagree (CLAUDE.md #8).
//
// The restore-test gate (restoreGate) is evaluated by the SAME function the
// lines read uses (lineEvidence), so the UI's "missing" and the promote's
// refusal are one sentence, not two that can drift.
//
// Kept backups and restore tests live in hz.db beside reports and promotions:
// observations, append-only, node-local, NOT peer-synced, no backup (the
// icebox's caveat on H1–H3 applies unchanged). Pins are declarations and live
// in config.json.

// rungLines derives one rung's supported lines from config.json and hz.db.
func (s *Server) rungLines(ctx context.Context, cfg *config.Config, project string, env config.Environment) (db.RungLines, error) {
	into, err := s.users.PromotionsInto(ctx, project, env.Name)
	if err != nil {
		return db.RungLines{}, err
	}
	var pins []db.LinePin
	for _, p := range cfg.ProjectPins(project) {
		pins = append(pins, db.LinePin{Line: p.Line, Reason: p.Reason})
	}
	return db.DeriveSupportedLines(project, env.Name, env.Version, into, pins), nil
}

// The statuses a line's restore evidence can have (apitypes.SupportedLineResp).
const (
	restorePassed         = "passed"
	restoreFailed         = "failed"
	restoreMissing        = "missing"
	restoreSuperseded     = "superseded"
	restoreNoKeptBackup   = "no-kept-backup"
	restoreNoSourceReport = "no-source-report"
	restoreNoSource       = "no-source"
)

type lineVerdict struct {
	status   string
	sentence string
	kept     *db.KeptBackup
	test     *db.RestoreTest
}

func sha12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// lineEvidence answers: may `version`, tested on `source`, be promoted past
// `line`, which `project/target` supports? The newest kept backup of the line
// is the one that counts, and the newest restore test of version on source
// against THAT backup's digest is the evidence — a test against an older
// backup of the line is superseded, not evidence.
//
// source "" (no edge) and version "" (the source reported nothing) are for
// the lines read; the gate always passes both.
func (s *Server) lineEvidence(ctx context.Context, project, target, source, version, line string) (lineVerdict, error) {
	kept, err := s.users.LatestKeptBackup(ctx, project, line)
	if errors.Is(err, db.ErrNotFound) {
		return lineVerdict{status: restoreNoKeptBackup,
			sentence: "line " + line + " is supported on " + project + "/" + target + " but has no kept backup"}, nil
	}
	if err != nil {
		return lineVerdict{}, err
	}
	v := lineVerdict{kept: kept}
	if source == "" {
		v.status, v.sentence = restoreNoSource, project+"/"+target+" has no `from` edge — nothing is promoted into it, so no restore test is asked of it"
		return v, nil
	}
	if version == "" {
		v.status, v.sentence = restoreNoSourceReport, source+" has reported no deploy, so there is no version to restore-test against line "+line
		return v, nil
	}
	test, err := s.users.LatestRestoreTest(ctx, project, source, version, line, kept.BackupSHA256)
	switch {
	case err == nil:
		v.test = test
		if test.Passed {
			v.status, v.sentence = restorePassed, fmt.Sprintf("%s passed its restore test against line %s's kept backup %s (restore test #%d)",
				version, line, sha12(kept.BackupSHA256), test.ID)
			return v, nil
		}
		where := test.BuildURL
		if where == "" {
			where = "no build_url"
		}
		v.status, v.sentence = restoreFailed, fmt.Sprintf("%s failed its restore test against line %s (%s)", version, line, where)
		return v, nil
	case !errors.Is(err, db.ErrNotFound):
		return lineVerdict{}, err
	}
	v.sentence = fmt.Sprintf("%s has no restore test against line %s's kept backup %s", version, line, sha12(kept.BackupSHA256))
	older, err := s.users.LatestRestoreTest(ctx, project, source, version, line, "")
	switch {
	case err == nil:
		v.test = older
		v.status = restoreSuperseded
		v.sentence += fmt.Sprintf(" — its newest test of line %s (#%d) was against %s, which a newer kept backup superseded",
			line, older.ID, sha12(older.BackupSHA256))
	case errors.Is(err, db.ErrNotFound):
		v.status = restoreMissing
	default:
		return lineVerdict{}, err
	}
	return v, nil
}

// gateResult is the restore-test gate's answer for one promote.
type gateResult struct {
	refusals []string // one sentence per failure; any refuses the promote
	lines    []db.PromotionLine
	checked  []apitypes.LineCheckedResp
	summary  string // PromoteResp.RestoreTests — always set on success
}

// restoreGate checks every supported line of the target AS IT IS NOW — before
// the promotion moves its declared version — against version's restore tests
// on `from`. Computing it after the write would let a release qualify itself:
// its own line would be "current" and the line it replaces would become a
// "prior" the test was never asked of.
func (s *Server) restoreGate(ctx context.Context, cfg *config.Config, project, from string, to config.Environment, version string) (gateResult, error) {
	var g gateResult
	rl, err := s.rungLines(ctx, cfg, project, to)
	if err != nil {
		return g, err
	}
	for _, gap := range rl.Gaps {
		g.refusals = append(g.refusals, "hz cannot say which lines "+project+"/"+to.Name+" supports: "+gap)
	}
	if len(rl.Supported) == 0 && len(rl.Gaps) == 0 {
		g.summary = "none required: " + project + "/" + to.Name + " has no supported line yet"
		g.checked = []apitypes.LineCheckedResp{}
		return g, nil
	}
	var done []string
	for _, sl := range rl.Supported {
		v, err := s.lineEvidence(ctx, project, to.Name, from, version, sl.Line)
		if err != nil {
			return g, err
		}
		if v.status != restorePassed {
			g.refusals = append(g.refusals, v.sentence)
			continue
		}
		g.lines = append(g.lines, db.PromotionLine{
			Line: sl.Line, Why: sl.Kinds(), KeptBackupID: v.kept.ID, RestoreTestID: v.test.ID,
		})
		g.checked = append(g.checked, apitypes.LineCheckedResp{
			Line: sl.Line, Why: sl.Kinds(), KeptBackupID: v.kept.ID,
			BackupSHA256: v.kept.BackupSHA256, RestoreTestID: v.test.ID,
		})
		done = append(done, fmt.Sprintf("%s (restore test #%d)", sl.Line, v.test.ID))
	}
	g.summary = fmt.Sprintf("checked %d line(s) of %s/%s: %s", len(done), project, to.Name, strings.Join(done, ", "))
	return g, nil
}

// ---------------------------------------------------------------------------
// Writers.

// keptBackupRecorder authorises a kept-backup record: an admin, or a service
// token of a service attributed to that PROJECT — any rung of it, since a
// backup belongs to a line, not a rung, and is usually taken from prod.
func (s *Server) keptBackupRecorder(r *http.Request, project string) (who string, status int, msg string) {
	if s.isAdmin(r) {
		return s.adminActor(r), 0, ""
	}
	svc, status, msg := s.reportingService(r)
	if status != 0 {
		return "", status, msg
	}
	if svc.Project == "" {
		return "", http.StatusForbidden, "service " + svc.Name + "'s token may record kept backups only for its own project, and " +
			svc.Name + " is attributed to none — `hz service assign " + svc.Name + " <project>/<environment>`, or use an admin API token"
	}
	if svc.Project != project {
		return "", http.StatusForbidden, "service " + svc.Name + "'s token may record kept backups only for " + svc.Project + ", not " + project
	}
	return "service:" + svc.Name, 0, ""
}

func recordError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrInvalidVersion) || errors.Is(err, db.ErrInvalidSHA256) || errors.Is(err, db.ErrInvalidLocator) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONError(w, http.StatusInternalServerError, err.Error())
}

// handleAPIKeptBackup records that the app keeps a backup for a line.
// POST /api/v1/backups/kept
func (s *Server) handleAPIKeptBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.KeptBackupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	who, status, msg := s.keptBackupRecorder(r, req.Project)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — kept backups are recorded in hz.db")
		return
	}
	if req.Project == "" {
		writeJSONError(w, http.StatusBadRequest, "project is required")
		return
	}
	if err := s.cfg().CheckProjectRef(req.Project); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.users.RecordKeptBackup(r.Context(), db.KeptBackup{
		Project: req.Project, Line: req.Line, BackupSHA256: req.BackupSHA256, Location: req.Location,
		TakenByVersion: req.TakenByVersion, BuildURL: req.BuildURL, RecordedBy: who,
	})
	if err != nil {
		recordError(w, err)
		return
	}
	writeJSON(w, apitypes.RecordedResp{Recorded: true, ID: id})
}

// handleAPIRestoreTestReport records a restore test. Scoped like a deploy
// report: a service token reports only for its own rung.
// POST /api/v1/restore-tests/report
func (s *Server) handleAPIRestoreTestReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.RestoreTestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	who, status, msg := s.deployReporter(r, apitypes.DeployReportReq{Project: req.Project, Environment: req.Environment})
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — restore tests are recorded in hz.db")
		return
	}
	if _, err := s.cfg().LookupEnvironment(req.Project, req.Environment); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error()+" — a restore test names the declared rung it ran on (`hz env ls`)")
		return
	}
	if req.Passed == nil {
		writeJSONError(w, http.StatusBadRequest, "passed is required — true or false; an absent result is not a failure and not a pass")
		return
	}
	id, err := s.users.RecordRestoreTest(r.Context(), db.RestoreTest{
		Project: req.Project, Environment: req.Environment, Version: req.Version, Line: req.Line,
		BackupSHA256: req.BackupSHA256, Passed: *req.Passed, BuildURL: req.BuildURL, ReportedBy: who,
	})
	if err != nil {
		recordError(w, err)
		return
	}
	writeJSON(w, apitypes.RecordedResp{Recorded: true, ID: id})
}

// ---------------------------------------------------------------------------
// The lines read.

func keptBackupResp(now time.Time, b *db.KeptBackup) *apitypes.KeptBackupResp {
	if b == nil {
		return nil
	}
	return &apitypes.KeptBackupResp{
		ID: b.ID, Line: b.Line, BackupSHA256: b.BackupSHA256, Location: b.Location,
		TakenByVersion: b.TakenByVersion, BuildURL: b.BuildURL,
		RecordedAt: b.RecordedAt.UTC().Format(time.RFC3339), RecordedBy: b.RecordedBy,
		AgeSeconds: ageSeconds(now, b.RecordedAt),
	}
}

func restoreTestResp(now time.Time, t *db.RestoreTest) *apitypes.RestoreTestResp {
	if t == nil {
		return nil
	}
	return &apitypes.RestoreTestResp{
		ID: t.ID, Environment: t.Environment, Version: t.Version, Line: t.Line,
		BackupSHA256: t.BackupSHA256, Passed: t.Passed, BuildURL: t.BuildURL,
		ReportedAt: t.ReportedAt.UTC().Format(time.RFC3339), ReportedBy: t.ReportedBy,
		AgeSeconds: ageSeconds(now, t.ReportedAt),
	}
}

func pinsResp(pins []config.PinnedLine) []apitypes.PinnedLineResp {
	out := make([]apitypes.PinnedLineResp, 0, len(pins))
	for _, p := range pins {
		out = append(out, apitypes.PinnedLineResp{Line: p.Line, Reason: p.Reason})
	}
	return out
}

// projectLines assembles the lines read for one project.
func (s *Server) projectLines(ctx context.Context, cfg *config.Config, project string) (apitypes.ProjectLinesResp, error) {
	now := time.Now()
	out := apitypes.ProjectLinesResp{
		Project: project, Pins: pinsResp(cfg.ProjectPins(project)),
		Rungs: []apitypes.RungLinesResp{}, Retired: []apitypes.KeptBackupResp{},
	}
	supported := map[string]bool{}
	var gaps []string
	for _, env := range cfg.Environments {
		if env.Project != project {
			continue
		}
		rl, err := s.rungLines(ctx, cfg, project, env)
		if err != nil {
			return out, err
		}
		rung := apitypes.RungLinesResp{
			Environment: env.Name, Posture: env.Posture, From: env.From, Declared: env.Version,
			Supported: []apitypes.SupportedLineResp{}, Gaps: append([]string{}, rl.Gaps...),
		}
		gaps = append(gaps, rl.Gaps...)
		if len(rl.Supported) == 0 && len(rl.Gaps) == 0 {
			rung.NoneRequired = "none required: " + project + "/" + env.Name + " has no supported line yet"
		}
		// The version the rung's NEXT promotion would carry: its source's newest report.
		version := ""
		if env.From != "" {
			rep, err := s.users.LatestDeployReport(ctx, project, env.From)
			switch {
			case err == nil:
				version = rep.Version
			case !errors.Is(err, db.ErrNotFound):
				return out, err
			}
		}
		for _, sl := range rl.Supported {
			supported[sl.Line] = true
			v, err := s.lineEvidence(ctx, project, env.Name, env.From, version, sl.Line)
			if err != nil {
				return out, err
			}
			why := make([]apitypes.LineWhyResp, 0, len(sl.Why))
			for _, w := range sl.Why {
				why = append(why, apitypes.LineWhyResp{Kind: w.Kind, Detail: w.Detail})
			}
			rung.Supported = append(rung.Supported, apitypes.SupportedLineResp{
				Line: sl.Line, Why: why, KeptBackup: keptBackupResp(now, v.kept),
				Restore: apitypes.LineRestoreResp{
					Status: v.status, Version: version, Sentence: v.sentence, Test: restoreTestResp(now, v.test),
				},
			})
		}
		out.Rungs = append(out.Rungs, rung)
	}
	if len(gaps) > 0 {
		// A rung hz cannot read may support any line, so none can be called retired.
		out.RetiredUnknown = "hz cannot say which lines are retired: " + strings.Join(gaps, "; ")
		return out, nil
	}
	kept, err := s.users.LatestKeptBackups(ctx, project)
	if err != nil {
		return out, err
	}
	for i := range kept {
		if !supported[kept[i].Line] {
			out.Retired = append(out.Retired, *keptBackupResp(now, &kept[i]))
		}
	}
	return out, nil
}

// handleAPIProjectLines is the lines read.
// GET /api/v1/projects/lines?project=
func (s *Server) handleAPIProjectLines(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — promotions, kept backups and restore tests are in hz.db")
		return
	}
	project := r.URL.Query().Get("project")
	cfg := s.cfg()
	if project == "" {
		writeJSONError(w, http.StatusBadRequest, "project is required")
		return
	}
	if err := cfg.CheckProjectRef(project); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	out, err := s.projectLines(r.Context(), cfg, project)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, out)
}

// handleAPILinePin pins (pin=true) or unpins a line on a project. A pin's line
// is checked with db.CheckLine here, because config cannot (config/lines.go);
// an unpin is not, so a hand-edited pin that is not a line can be removed.
// POST /api/v1/projects/lines/pin, /api/v1/projects/lines/unpin
func (s *Server) handleAPILinePin(pin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		var req apitypes.LinePinReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
			return
		}
		next := *s.cfg()
		var err error
		if pin {
			if err = db.CheckLine(req.Line); err == nil {
				err = next.PinLine(req.Project, req.Line, req.Reason)
			}
		} else {
			err = next.UnpinLine(req.Project, req.Line)
		}
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
			return
		}
		writeJSON(w, pinsResp(next.ProjectPins(req.Project)))
	}
}
