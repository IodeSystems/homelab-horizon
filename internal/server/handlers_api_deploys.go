package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The tested staging -> prod release: plan/plan.md "Redline push to prod — the
// plan", steps H1 (report), H2 (evidence-gated promote) and H3 (deploy check).
//
//	POST /api/v1/deploys/report        a rung says what it now runs
//	POST /api/v1/environments/promote  move a version up a rung, on evidence
//	GET  /api/v1/deploys/check         may THIS artifact be deployed to THIS rung
//	GET  /api/v1/deploys/latest        newest report per rung (UI)
//	GET  /api/v1/promotions            the promotion record (UI)
//
// and, in handlers_api_lines.go, the release lines the promote's restore-test
// gate reads: kept backups, restore tests, the lines read and the pins.
//
// THE ONE AUTHORITY (CLAUDE.md). Promote and check are admin-gated, and an
// admin API token has no scope, so the evidence gate gates one authority
// against itself: it stops a version nobody tested, not a person. What it adds
// is the record — who promoted which artifact, when — attributed the way the
// audit log already attributes (adminActor).
//
// The one real scope here is the REPORT: a service token may report, but only
// for the rung its service is attributed to. That is what a staging box already
// holds as HZ_TOKEN, and it cannot speak for prod.
//
// Reports and promotions live in hz.db, which is node-local and NOT peer-synced;
// the declared Version they move is in config.json, which is. An HA peer
// answering a check therefore has the declaration but not the promotion row,
// and refuses — fail-closed, not fail-open.

// promoteMu serialises promotions: the gate reads the target's version and the
// write sets it, and two promotions interleaving between those would each pass
// a downgrade check the other invalidated.
var promoteMu sync.Mutex

// deployReporter authorises a report and returns who to record. An admin (an
// account, its API token, the admin cookie or a VPN admin) may report for any
// rung. Otherwise the bearer must be a SERVICE token, and the service must be
// attributed to exactly the rung being reported. A restore-test report is
// scoped the same way (handlers_api_lines.go).
func (s *Server) deployReporter(r *http.Request, req apitypes.DeployReportReq) (who string, status int, msg string) {
	if s.isAdmin(r) {
		return s.adminActor(r), 0, ""
	}
	svc, status, msg := s.reportingService(r)
	if status != 0 {
		return "", status, msg
	}
	if svc.Project == "" || svc.Environment == "" {
		return "", http.StatusForbidden, "service " + svc.Name + "'s token may report only for its own rung, and " + svc.Name +
			" is attributed to none — `hz service assign " + svc.Name + " <project>/<environment>`, or report with an admin API token"
	}
	if svc.Project != req.Project || svc.Environment != req.Environment {
		return "", http.StatusForbidden, "service " + svc.Name + "'s token may report only for " + svc.Project + "/" + svc.Environment +
			", not " + req.Project + "/" + req.Environment
	}
	return "service:" + svc.Name, 0, ""
}

// reportingService resolves a non-admin bearer to the service whose token it
// is, or says why not (401).
func (s *Server) reportingService(r *http.Request) (svc config.Service, status int, msg string) {
	tok := requestBearer(r)
	// An empty token must never reach findServiceByToken: a service with no
	// token has Token == "", and "" would match it.
	if tok == "" || strings.HasPrefix(tok, db.APITokenPrefix) {
		return config.Service{}, http.StatusUnauthorized, "Unauthorized"
	}
	idx := s.findServiceByToken(tok)
	if idx < 0 {
		return config.Service{}, http.StatusUnauthorized, "Unauthorized: unknown token"
	}
	return s.cfg().Services[idx], 0, ""
}

// handleAPIDeployReport is H1. Append-only: every call is a new row, a
// repeat of the same version with a different artifact included — a redeploy of
// one commit can rebuild, and the newest report is the one the gate reads.
// POST /api/v1/deploys/report
func (s *Server) handleAPIDeployReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.DeployReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Decoded before the scope check, because the scope IS the rung in the
		// body. An unauthenticated caller learns only that its JSON is bad.
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	who, status, msg := s.deployReporter(r, req)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — reports are kept in hz.db")
		return
	}
	if _, err := s.cfg().LookupEnvironment(req.Project, req.Environment); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error()+" — a report names a declared rung (`hz env ls`)")
		return
	}
	if strings.TrimSpace(req.App) == "" {
		writeJSONError(w, http.StatusBadRequest, "app is required")
		return
	}
	if strings.TrimSpace(req.Host) == "" {
		writeJSONError(w, http.StatusBadRequest, "host is required — the report says WHERE it runs")
		return
	}
	id, err := s.users.RecordDeployReport(r.Context(), db.DeployReport{
		Project: req.Project, Environment: req.Environment, App: req.App,
		Version: req.Version, Describe: req.Describe, ArtifactSHA256: req.ArtifactSHA256,
		Host: req.Host, BuildURL: req.BuildURL, ReportedBy: who,
	})
	if errors.Is(err, db.ErrInvalidVersion) || errors.Is(err, db.ErrInvalidSHA256) || errors.Is(err, db.ErrInvalidLocator) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, apitypes.DeployReportResultResp{Recorded: true, ID: id})
}

// promoteRefusal is a 409 with the gate's reason.
func promoteRefusal(w http.ResponseWriter, msg string) {
	writeJSONError(w, http.StatusConflict, msg)
}

// handleAPIEnvironmentPromote is H2. Refused unless ALL of:
//
//  1. config.CheckPromotion(from, to) — same project, a declared edge, upward.
//     Structural only; it checks no evidence.
//  2. EVIDENCE: the newest report from `from` is this version. "staging ran
//     1.4.0 once, last month" is not evidence that 1.4.0 is what was tested.
//  3. Not a downgrade (db.CompareVersions, hz's one semver comparator) of the
//     target's declared version, unless allowDowngrade. A target whose
//     declared version cannot be compared counts as needing the flag.
//  4. THE RESTORE-TEST GATE (handlers_api_lines.go restoreGate): for every
//     supported line of the target — derived BEFORE the promotion — a kept
//     backup exists and X's newest restore test on `from` against it passed.
//     No supported line (the first release) requires nothing, and says so.
//     A downgrade is gated too, UNLESS skipRestoreTests (the operator's call,
//     2026-10-01): then the gate is skipped only when the target was promoted
//     to this exact version AND artifact before — it ran there. Any other
//     version or build is refused; the skip is recorded on the promotion.
//
// On success the target's Version is set through the env-set writer
// (SetEnvironment + updateConfig, so Save validates it), and a promotions row
// pins the artifact the source reported. The config is written FIRST: if the
// row then fails, the rung declares a version no promotion recorded, and the
// deploy check refuses it — fail-closed. Re-running the promote repairs it.
// POST /api/v1/environments/promote
func (s *Server) handleAPIEnvironmentPromote(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — the evidence and the promotion record are in hz.db")
		return
	}
	var req apitypes.PromoteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.Project == "" || req.From == "" || req.To == "" {
		writeJSONError(w, http.StatusBadRequest, "project, from and to are required")
		return
	}
	if err := db.CheckDeployVersion(req.Version); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	promoteMu.Lock()
	defer promoteMu.Unlock()

	cfg := s.cfg()
	from, err := cfg.LookupEnvironment(req.Project, req.From)
	if err != nil {
		promoteRefusal(w, err.Error())
		return
	}
	to, err := cfg.LookupEnvironment(req.Project, req.To)
	if err != nil {
		promoteRefusal(w, err.Error())
		return
	}
	if err := config.CheckPromotion(from, to); err != nil {
		promoteRefusal(w, err.Error())
		return
	}

	// The evidence. Its artifact is the one pinned.
	latest, err := s.users.LatestDeployReport(r.Context(), req.Project, req.From)
	if errors.Is(err, db.ErrNotFound) {
		promoteRefusal(w, req.From+" has not reported running "+req.Version+"; it has never reported a deploy"+
			" — the deploy to "+req.From+" posts /api/v1/deploys/report after it flips")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if latest.Version != req.Version {
		promoteRefusal(w, req.From+" has not reported running "+req.Version+"; it last reported "+latest.Version+
			" at "+latest.ReportedAt.UTC().Format(time.RFC3339)+" on "+latest.Host)
		return
	}

	downgrade := false
	if to.Version != "" {
		cmp, err := db.CompareVersions(req.Version, to.Version)
		switch {
		case err != nil:
			if !req.AllowDowngrade {
				promoteRefusal(w, to.Name+" declares "+to.Version+", which hz cannot order against "+req.Version+
					" ("+err.Error()+") — pass allowDowngrade to replace it anyway; it is recorded as a downgrade")
				return
			}
			downgrade = true
		case cmp < 0:
			if !req.AllowDowngrade {
				promoteRefusal(w, req.Version+" is lower than "+to.Name+"'s declared "+to.Version+
					" — a rollback is allowed, explicitly: pass allowDowngrade (--allow-downgrade); it is recorded as a downgrade")
				return
			}
			downgrade = true
		}
	}

	var gate gateResult
	gateRecord := ""
	if req.SkipRestoreTests {
		ran, err := s.users.LatestPromotionOf(r.Context(), req.Project, req.To, req.Version)
		if errors.Is(err, db.ErrNotFound) {
			promoteRefusal(w, "skipRestoreTests is for a rollback to a version "+req.Project+"/"+req.To+
				" already ran; "+req.Version+" was never promoted into it")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ran.ArtifactSHA256 != latest.ArtifactSHA256 {
			promoteRefusal(w, req.From+"'s newest report of "+req.Version+" is artifact "+latest.ArtifactSHA256[:12]+
				", not the "+ran.ArtifactSHA256[:12]+" "+req.Project+"/"+req.To+" ran (promotion #"+
				strconv.FormatInt(ran.ID, 10)+") — skipRestoreTests covers only the build that ran there")
			return
		}
		gate = gateResult{
			checked: []apitypes.LineCheckedResp{},
			summary: "skipped: " + req.Project + "/" + req.To + " ran " + req.Version + " (this artifact) before, promotion #" +
				strconv.FormatInt(ran.ID, 10) + " — no restore test was run (skipRestoreTests)",
		}
		gateRecord = db.RestoreGateSkipped
	} else {
		gate, err = s.restoreGate(r.Context(), cfg, req.Project, req.From, to, req.Version)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(gate.refusals) > 0 {
		promoteRefusal(w, strings.Join(gate.refusals, "\n"))
		return
	}

	next := *cfg
	version := req.Version
	if _, err := next.SetEnvironment(req.Project, req.To, config.EnvironmentPatch{Version: &version}); err != nil {
		promoteRefusal(w, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	id, err := s.users.RecordPromotion(r.Context(), db.Promotion{
		Project: req.Project, FromEnv: req.From, ToEnv: req.To, Version: req.Version,
		ArtifactSHA256: latest.ArtifactSHA256, PromotedBy: s.adminActor(r), Downgrade: downgrade,
		BuildURL: latest.BuildURL, Lines: gate.lines, RestoreGate: gateRecord,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, req.To+" now declares "+req.Version+
			" but the promotion was NOT recorded ("+err.Error()+"); the deploy check will refuse it until the promote is re-run")
		return
	}
	writeJSON(w, apitypes.PromoteResp{
		Promoted: true, Version: req.Version, ArtifactSHA256: latest.ArtifactSHA256, BuildURL: latest.BuildURL,
		Downgrade: downgrade, ID: id, RestoreTests: gate.summary, LinesChecked: gate.checked,
	})
}

// checkRefusal answers 409 {"ok":false,"reason":...}.
func checkRefusal(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apitypes.DeployCheckResp{OK: false, Reason: reason})
}

// handleAPIDeployCheck is H3: may this artifact, at this version, be deployed
// to this rung. ok only when the rung DECLARES this version AND the artifact is
// the one pinned by the newest promotion of that version into it — so a bundle
// rebuilt under the same version string is refused, and so is a version set by
// hand with `hz env set` that no promotion recorded.
//
// A rung with an Upstream (a nested hz holds its placements) is still answered
// HERE: the declaration — version and promotion edge — lives in this hz, and
// the upstream hz holds only where it runs. Asking the child would ask the
// party being gated.
// GET /api/v1/deploys/check?project=&environment=&version=&artifact_sha256=
func (s *Server) handleAPIDeployCheck(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if s.users == nil {
		checkRefusal(w, http.StatusServiceUnavailable, "identity store unavailable — the promotion record is in hz.db")
		return
	}
	q := r.URL.Query()
	project, env, version := q.Get("project"), q.Get("environment"), q.Get("version")
	if project == "" || env == "" {
		checkRefusal(w, http.StatusBadRequest, "project and environment are required")
		return
	}
	if err := db.CheckDeployVersion(version); err != nil {
		checkRefusal(w, http.StatusBadRequest, err.Error())
		return
	}
	sha, err := db.NormalizeSHA256(q.Get("artifact_sha256"))
	if err != nil {
		checkRefusal(w, http.StatusBadRequest, err.Error())
		return
	}

	to, err := s.cfg().LookupEnvironment(project, env)
	if err != nil {
		checkRefusal(w, http.StatusConflict, "nothing declared: "+err.Error())
		return
	}
	if to.Version == "" {
		checkRefusal(w, http.StatusConflict, "nothing declared: "+project+"/"+env+" declares no version — promote one into it first")
		return
	}
	if to.Version != version {
		checkRefusal(w, http.StatusConflict, project+"/"+env+" declares "+to.Version+", not "+version)
		return
	}
	promo, err := s.users.LatestPromotionOf(r.Context(), project, env, version)
	if errors.Is(err, db.ErrNotFound) {
		checkRefusal(w, http.StatusConflict, project+"/"+env+" declares "+version+" but no promotion recorded it"+
			" — it was set by hand, not promoted; `hz env promote` pins the artifact")
		return
	}
	if err != nil {
		checkRefusal(w, http.StatusInternalServerError, err.Error())
		return
	}
	if promo.ArtifactSHA256 != sha {
		checkRefusal(w, http.StatusConflict, "artifact differs from the one promoted: "+version+" was promoted into "+env+
			" from "+promo.FromEnv+" with sha256 "+promo.ArtifactSHA256+" by "+promo.PromotedBy+
			" at "+promo.PromotedAt.UTC().Format(time.RFC3339)+"; this bundle is "+sha)
		return
	}
	writeJSON(w, apitypes.DeployCheckResp{OK: true})
}

func ageSeconds(now, at time.Time) int64 {
	if d := now.Sub(at); d > 0 {
		return int64(d.Seconds())
	}
	return 0
}

func deployReportResp(now time.Time, r db.DeployReport) apitypes.DeployReportResp {
	return apitypes.DeployReportResp{
		ID: r.ID, Project: r.Project, Environment: r.Environment, App: r.App,
		Version: r.Version, Describe: r.Describe, ArtifactSHA256: r.ArtifactSHA256, Host: r.Host,
		BuildURL: r.BuildURL, ReportedAt: r.ReportedAt.UTC().Format(time.RFC3339), ReportedBy: r.ReportedBy,
		AgeSeconds: ageSeconds(now, r.ReportedAt),
	}
}

// handleAPIDeployLatest is the newest report per rung, for the Overview's
// Reported column.
// GET /api/v1/deploys/latest
func (s *Server) handleAPIDeployLatest(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	reports, err := s.users.LatestDeployReports(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]apitypes.DeployReportResp, 0, len(reports))
	for _, rep := range reports {
		out = append(out, deployReportResp(now, rep))
	}
	writeJSON(w, out)
}

// handleAPIPromotions is the promotion record, newest first. project="" is
// every project. limit defaults to 50.
// GET /api/v1/promotions?project=&limit=
func (s *Server) handleAPIPromotions(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.users.Promotions(r.Context(), r.URL.Query().Get("project"), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]apitypes.PromotionResp, 0, len(list))
	for _, p := range list {
		out = append(out, apitypes.PromotionResp{
			ID: p.ID, Project: p.Project, From: p.FromEnv, To: p.ToEnv, Version: p.Version,
			ArtifactSHA256: p.ArtifactSHA256, PromotedAt: p.PromotedAt.UTC().Format(time.RFC3339),
			PromotedBy: p.PromotedBy, Downgrade: p.Downgrade, AgeSeconds: ageSeconds(now, p.PromotedAt),
			BuildURL: p.BuildURL, RestoreGate: p.RestoreGate,
		})
	}
	writeJSON(w, out)
}
