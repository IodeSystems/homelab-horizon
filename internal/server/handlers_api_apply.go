package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Apply, hold, desired and the instance token (plan/plan.md N4a, decided
// 2026-10-01).
//
//	POST /api/v1/environments/apply    {project, environment, version}   admin
//	POST /api/v1/environments/hold     {project, environment, reason}    admin
//	POST /api/v1/environments/unhold   {project, environment}            admin
//	GET  /api/v1/deploys/desired       ?project&environment   admin | instance token
//	GET  /api/v1/deploys/applied       ?project= (UI)                    admin
//	POST /api/v1/machines/hz-token     {machine}                         admin
//
// A PROMOTION APPROVES; AN APPLY IS WHAT RUNS. desired answers the newest
// APPLY, so a rung is held by default — a promote alone changes nothing a box
// pulls. An apply may apply only the newest promotion into the rung: it never
// picks a build. A hold is the emergency stop on top, and rides desired.
//
// THE ONE AUTHORITY (CLAUDE.md). Apply and hold are admin-gated like promote,
// so the apply step gates one authority against itself: it separates "approved"
// from "running" in time and in the record, not between two people.
//
// THE INSTANCE TOKEN is the one credential a nested hz holds for this one. It
// authenticates as instance:<machine> for exactly three calls — desired, an
// artifact download, and a forwarded deploy report — and only for rungs whose
// Upstream names that machine. isAdmin never accepts it, so every admin route
// refuses it without knowing it exists; TestInstanceTokenIsRefusedOnAdminRoutes
// enumerates them.

// instanceTokenPrefix marks an instance token, so it never reaches the
// service-token or personal-token lookups by accident.
const instanceTokenPrefix = "hzi_"

func tokenDigest(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// instanceCaller resolves an instance token to its machine.
func (s *Server) instanceCaller(r *http.Request) (machine string, ok bool) {
	tok := requestBearer(r)
	if !strings.HasPrefix(tok, instanceTokenPrefix) {
		return "", false
	}
	sum := tokenDigest(tok)
	for _, m := range s.cfg().Machines {
		if m.HZ == nil || m.HZ.TokenSHA256 == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(m.HZ.TokenSHA256), []byte(sum)) == 1 {
			return m.Name, true
		}
	}
	return "", false
}

// instanceForRung authorises an instance token for one rung: the rung's
// Upstream must be the token's machine.
func (s *Server) instanceForRung(r *http.Request, env config.Environment) (machine string, status int, msg string) {
	machine, ok := s.instanceCaller(r)
	if !ok {
		return "", http.StatusUnauthorized, "Unauthorized"
	}
	if env.Upstream != machine {
		where := "is placed here"
		if env.Upstream != "" {
			where = "names " + env.Upstream + " as its upstream"
		}
		return "", http.StatusForbidden, "instance:" + machine + " may act only for rungs whose upstream is " + machine + "; " +
			env.Project + "/" + env.Name + " " + where
	}
	return machine, 0, ""
}

func holdResp(h *db.HoldEvent) *apitypes.HoldResp {
	if h == nil || !h.Held() {
		return nil
	}
	return &apitypes.HoldResp{By: h.By, Reason: h.Reason, At: h.At.UTC().Format(time.RFC3339)}
}

// latestHold is the rung's newest hold event, nil for never held.
func (s *Server) latestHold(r *http.Request, project, env string) (*db.HoldEvent, error) {
	h, err := s.users.LatestHold(r.Context(), project, env)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// desiredETag hashes (apply id, hold state). The newest hold EVENT's id is
// part of it, so a hold, an unhold and a re-hold each change the tag.
func desiredETag(applyID int64, hold *db.HoldEvent) string {
	state := "never-held"
	if hold != nil {
		state = strconv.FormatInt(hold.ID, 10) + ":" + hold.Kind
	}
	sum := sha256.Sum256([]byte("apply:" + strconv.FormatInt(applyID, 10) + "|hold:" + state))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// handleAPIDeployDesired answers what a rung should run: the NEWEST APPLY.
// GET /api/v1/deploys/desired?project=&environment=
func (s *Server) handleAPIDeployDesired(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	q := r.URL.Query()
	project, envName := q.Get("project"), q.Get("environment")
	if project == "" || envName == "" {
		writeJSONError(w, http.StatusBadRequest, "project and environment are required")
		return
	}
	cfg := s.cfg()
	if s.child != nil && cfg.ServesUpstream(project, envName) {
		s.handleChildDesired(w, r, config.UpstreamRung{Project: project, Environment: envName})
		return
	}
	env, lookupErr := cfg.LookupEnvironment(project, envName)
	instance := ""
	if !s.isAdmin(r) {
		if lookupErr != nil {
			// The rung decides the scope, so an unknown rung cannot be
			// answered to a non-admin beyond "not yours".
			if _, ok := s.instanceCaller(r); ok {
				writeJSONError(w, http.StatusForbidden, "no rung "+project+"/"+envName+" names this instance as its upstream")
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		m, status, msg := s.instanceForRung(r, env)
		if status != 0 {
			writeJSONError(w, status, msg)
			return
		}
		instance = m
	}
	if lookupErr != nil {
		writeJSONError(w, http.StatusNotFound, lookupErr.Error())
		return
	}
	if !cfg.ConfigPrimary && cfg.PeerID != "" {
		// The apply record is in the PRIMARY's hz.db; a replica has none, and
		// "nothing applied" from one would be a wrong answer, not an empty one.
		writeJSONError(w, http.StatusServiceUnavailable, "this hz is an HA replica; applies are recorded in the config primary's hz.db — ask the primary")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — the apply record is in hz.db")
		return
	}
	if instance != "" {
		if err := s.users.RecordInstancePull(r.Context(), instance, project, envName); err != nil {
			slog.Warn("could not record a nested hz's pull", "machine", instance, "error", err)
		}
	}
	apply, err := s.users.LatestApply(r.Context(), project, envName)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "nothing applied to "+project+"/"+envName)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hold, err := s.latestHold(r, project, envName)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	etag := desiredETag(apply.ID, hold)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	buildURL := ""
	if p, err := s.users.PromotionByID(r.Context(), apply.PromotionID); err == nil {
		buildURL = p.BuildURL
	}
	writeJSON(w, apitypes.DesiredResp{
		Version: apply.Version, ArtifactSHA256: apply.ArtifactSHA256, PromotionID: apply.PromotionID,
		ApplyID: apply.ID, AppliedBy: apply.AppliedBy, AppliedAt: apply.AppliedAt.UTC().Format(time.RFC3339),
		BuildURL: buildURL, Hold: holdResp(hold),
	})
}

// handleAPIEnvironmentApply applies the newest promotion into a rung.
// POST /api/v1/environments/apply
func (s *Server) handleAPIEnvironmentApply(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — promotions and applies are in hz.db")
		return
	}
	var req apitypes.ApplyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.Project == "" || req.Environment == "" {
		writeJSONError(w, http.StatusBadRequest, "project and environment are required")
		return
	}
	if err := db.CheckDeployVersion(req.Version); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	promoteMu.Lock()
	defer promoteMu.Unlock()

	ctx := r.Context()
	rung := req.Project + "/" + req.Environment
	if _, err := s.cfg().LookupEnvironment(req.Project, req.Environment); err != nil {
		promoteRefusal(w, err.Error())
		return
	}
	promo, err := s.users.LatestPromotionInto(ctx, req.Project, req.Environment)
	if errors.Is(err, db.ErrNotFound) {
		promoteRefusal(w, "nothing was promoted into "+rung+" — an apply applies the newest promotion; `hz env promote` first")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if promo.Version != req.Version {
		promoteRefusal(w, fmt.Sprintf("the newest promotion into %s is %s (promotion #%d by %s at %s); an apply applies only that — "+
			"`hz env apply %s %s --version %s`, or promote %s first",
			rung, promo.Version, promo.ID, promo.PromotedBy, promo.PromotedAt.UTC().Format(time.RFC3339),
			req.Project, req.Environment, promo.Version, req.Version))
		return
	}
	art, err := s.users.LookupArtifact(ctx, promo.ArtifactSHA256)
	switch {
	case errors.Is(err, db.ErrNotFound):
		promoteRefusal(w, "artifact "+artifactSHA12(promo.ArtifactSHA256)+" is not uploaded to hz; "+req.Environment+
			" cannot pull it — upload it (PUT /api/v1/artifacts/"+promo.ArtifactSHA256+"?project="+req.Project+") before applying")
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	case art.Deleted():
		promoteRefusal(w, "artifact "+artifactSHA12(promo.ArtifactSHA256)+" was deleted by retention at "+
			art.DeletedAt.UTC().Format(time.RFC3339)+"; "+req.Environment+" cannot pull it — re-upload it before applying")
		return
	}
	if cur, err := s.users.LatestApply(ctx, req.Project, req.Environment); err == nil && cur.PromotionID == promo.ID {
		writeJSON(w, apitypes.ApplyResp{Applied: true, ID: cur.ID, Version: cur.Version, ArtifactSHA256: cur.ArtifactSHA256,
			PromotionID: cur.PromotionID, Existing: true})
		return
	} else if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, err := s.users.RecordApply(ctx, db.Apply{
		Project: req.Project, Environment: req.Environment, Version: promo.Version,
		ArtifactSHA256: promo.ArtifactSHA256, PromotionID: promo.ID, AppliedBy: s.adminActor(r),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("applied", "rung", rung, "version", promo.Version, "promotion_id", promo.ID, "apply_id", id, "by", s.adminActor(r))
	writeJSON(w, apitypes.ApplyResp{Applied: true, ID: id, Version: promo.Version, ArtifactSHA256: promo.ArtifactSHA256, PromotionID: promo.ID})
}

// handleAPIEnvironmentHold is hold (on) and unhold (!on).
// POST /api/v1/environments/hold, /api/v1/environments/unhold
func (s *Server) handleAPIEnvironmentHold(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if s.users == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — holds are recorded in hz.db")
			return
		}
		var req apitypes.HoldReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
			return
		}
		if req.Project == "" || req.Environment == "" {
			writeJSONError(w, http.StatusBadRequest, "project and environment are required")
			return
		}
		if _, err := s.cfg().LookupEnvironment(req.Project, req.Environment); err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		rung := req.Project + "/" + req.Environment
		promoteMu.Lock()
		defer promoteMu.Unlock()
		cur, err := s.latestHold(r, req.Project, req.Environment)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ev := db.HoldEvent{Project: req.Project, Environment: req.Environment, By: s.adminActor(r)}
		if on {
			if strings.TrimSpace(req.Reason) == "" {
				writeJSONError(w, http.StatusBadRequest, "a hold needs a reason — it stops "+rung+", and whoever lifts it needs to know why")
				return
			}
			ev.Kind, ev.Reason = db.HoldOn, req.Reason
		} else {
			if cur == nil || !cur.Held() {
				writeJSONError(w, http.StatusConflict, rung+" is not held")
				return
			}
			ev.Kind = db.HoldOff
		}
		if _, err := s.users.RecordHold(r.Context(), ev); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		now, err := s.latestHold(r, req.Project, req.Environment)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.Info("rung hold changed", "rung", rung, "held", on, "reason", req.Reason, "by", ev.By)
		writeJSON(w, apitypes.HoldStateResp{Project: req.Project, Environment: req.Environment, Hold: holdResp(now)})
	}
}

// handleAPIDeploysApplied is each declared rung's apply and hold, for the
// Overview. A rung nothing was applied to has applied:null.
// GET /api/v1/deploys/applied?project=
func (s *Server) handleAPIDeploysApplied(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — applies and holds are in hz.db")
		return
	}
	project := r.URL.Query().Get("project")
	now := time.Now()
	out := make([]apitypes.RungDeployStateResp, 0)
	for _, env := range s.cfg().Environments {
		if project != "" && env.Project != project {
			continue
		}
		row := apitypes.RungDeployStateResp{Project: env.Project, Environment: env.Name}
		a, err := s.users.LatestApply(r.Context(), env.Project, env.Name)
		switch {
		case err == nil:
			row.Applied = &apitypes.AppliedResp{ID: a.ID, Version: a.Version, ArtifactSHA256: a.ArtifactSHA256,
				PromotionID: a.PromotionID, AppliedBy: a.AppliedBy, AppliedAt: a.AppliedAt.UTC().Format(time.RFC3339),
				AgeSeconds: ageSeconds(now, a.AppliedAt)}
			if art, err := s.users.LookupArtifact(r.Context(), a.ArtifactSHA256); err == nil {
				ar := artifactResp(*art)
				row.Artifact = &ar
			}
		case !errors.Is(err, db.ErrNotFound):
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h, err := s.latestHold(r, env.Project, env.Name)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		row.Hold = holdResp(h)
		out = append(out, row)
	}
	writeJSON(w, out)
}

// handleAPIMachineHZToken mints a nested hz's instance token: shown once, its
// sha256 stored on MachineHZ, a re-mint replacing (and so revoking) the old.
// POST /api/v1/machines/hz-token
func (s *Server) handleAPIMachineHZToken(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.InstanceTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	req.Machine = strings.TrimSpace(req.Machine)
	if req.Machine == "" {
		writeJSONError(w, http.StatusBadRequest, "machine is required")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := instanceTokenPrefix + hex.EncodeToString(raw)
	next := *s.cfg()
	if err := next.SetInstanceTokenHash(req.Machine, tokenDigest(tok)); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	slog.Info("instance token minted", "machine", req.Machine, "by", s.adminActor(r))
	writeJSON(w, apitypes.InstanceTokenResp{Machine: req.Machine, Token: tok})
}
