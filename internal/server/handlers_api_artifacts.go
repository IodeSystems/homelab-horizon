package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/artifact"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The artifact store, parent side (plan/plan.md N4a).
//
//	PUT /api/v1/artifacts/{sha256}?project=P   the staging deploy uploads its bundle
//	GET /api/v1/artifacts/{sha256}             a nested hz (or an admin) downloads it
//	GET /api/v1/artifacts                      every artifact record (UI)
//
// The bytes live in <data dir>/artifacts/<sha256> — the directory hz.db is in
// (config.DataDir) — and the record in hz.db. The record outlives the file:
// "never uploaded" and "uploaded, then deleted by retention" are different
// answers (#2), and a download says which.
//
// THE ARTIFACT IS NOT A SECRET, and must not carry one (#3): it is the code a
// nested hz serves to the box it runs on. Secrets travel the sealed-config
// channel. It is, however, code that runs AS ROOT there — whoever controls this
// hz controls that box.

// artifactSHA12 is the short form a refusal names.
func artifactSHA12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (s *Server) artifactStore() (artifact.Store, bool) {
	if s.artifactDir == "" {
		return artifact.Store{}, false
	}
	return artifact.Store{Dir: s.artifactDir}, true
}

// handleAPIArtifact routes the subtree: PUT uploads, GET downloads.
func (s *Server) handleAPIArtifact(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/")
	sha, err := db.NormalizeSHA256(raw)
	if err != nil || strings.Contains(raw, "/") {
		writeJSONError(w, http.StatusBadRequest, "the path names an artifact by its sha256: /api/v1/artifacts/<64 hex characters>")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.handleArtifactUpload(w, r, sha)
	case http.MethodGet, http.MethodHead:
		s.handleArtifactDownload(w, r, sha)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "PUT uploads an artifact, GET downloads one")
	}
}

// artifactUploader authorises an upload into project: an admin, or a service
// token of a service in that project. Returns who to record.
func (s *Server) artifactUploader(r *http.Request, project string) (who string, status int, msg string) {
	if s.isAdmin(r) {
		return s.adminActor(r), 0, ""
	}
	if _, ok := s.instanceCaller(r); ok {
		return "", http.StatusForbidden, "an instance token downloads artifacts; it cannot upload one"
	}
	svc, status, msg := s.reportingService(r)
	if status != 0 {
		return "", status, msg
	}
	if svc.Project != project {
		where := svc.Project
		if where == "" {
			where = "no project"
		}
		return "", http.StatusForbidden, "service " + svc.Name + "'s token may upload only into its own project (" + where + "), not " + project
	}
	return "service:" + svc.Name, 0, ""
}

// handleArtifactUpload streams the body to a temp file, hashing as it goes.
// A hash that is not the path's is a 400 and the temp file is removed; a
// stored sha is answered existing without reading the body.
func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request, sha string) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeJSONError(w, http.StatusBadRequest, "?project= is required — an artifact is recorded under the project it belongs to, and a service token may upload only into its own")
		return
	}
	who, status, msg := s.artifactUploader(r, project)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	store, ok := s.artifactStore()
	if !ok || s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artifact store unavailable — artifacts are recorded in hz.db and kept beside it")
		return
	}
	if err := s.cfg().CheckProjectRef(project); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()

	existing, err := s.users.LookupArtifact(ctx, sha)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil && existing.Project != project {
		writeJSONError(w, http.StatusConflict, "artifact "+artifactSHA12(sha)+" is stored for project "+existing.Project+", not "+project)
		return
	}
	if existing != nil && !existing.Deleted() {
		if has, _ := store.Has(sha); has {
			writeJSON(w, apitypes.ArtifactUploadResp{Stored: true, SHA256: sha, Size: existing.Size, Existing: true})
			return
		}
	}

	limit := s.cfg().ArtifactCap()
	tooLarge := func() {
		writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"artifact exceeds the size cap of %d bytes — artifact_max_bytes in config.json (default %d, 512 MiB)", limit, config.DefaultArtifactMaxBytes))
	}
	if r.ContentLength > limit {
		tooLarge()
		return
	}
	// The server's ReadTimeout is 30s, which a bundle over a VPN outlasts.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour))

	// Not under artifactMu: retention decides from the RECORDS, and a file
	// being written has no live record yet (a new sha) or a 'deleted' one
	// (a re-upload), neither of which retention touches.
	size, err := store.Put(r.Body, sha, limit)
	switch {
	case errors.Is(err, artifact.ErrTooLarge):
		tooLarge()
		return
	case errors.Is(err, artifact.ErrHashMismatch):
		writeJSONError(w, http.StatusBadRequest, err.Error()+" — nothing was stored")
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		if _, err := s.users.RecordArtifact(ctx, db.Artifact{SHA256: sha, Project: project, Size: size, UploadedBy: who}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "stored the file but could not record it: "+err.Error())
			return
		}
	} else if existing.Deleted() {
		if err := s.users.RecordArtifactEvent(ctx, sha, db.ArtifactRestored, "re-uploaded after retention deleted it", who); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	slog.Info("artifact stored", "sha256", sha, "project", project, "size", size, "by", who)
	s.runArtifactRetention(context.WithoutCancel(ctx), "after an upload")
	writeJSON(w, apitypes.ArtifactUploadResp{Stored: true, SHA256: sha, Size: size, Existing: false})
}

// handleArtifactDownload streams a stored artifact. An admin may fetch any;
// an instance token only one an apply into one of its rungs pinned. A nested
// hz answers for its configured rungs from its own pulled copy.
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request, sha string) {
	if s.child != nil {
		if rungs := s.child.ArtifactRungs(sha); len(rungs) > 0 {
			s.handleChildArtifact(w, r, sha, rungs)
			return
		}
	}
	if !s.isAdmin(r) {
		machine, ok := s.instanceCaller(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if s.users == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — the apply record is in hz.db")
			return
		}
		var rungs [][2]string
		for _, rg := range s.cfg().UpstreamRungsOf(machine) {
			rungs = append(rungs, [2]string{rg.Project, rg.Environment})
		}
		pinned, err := s.users.AppliedTo(r.Context(), sha, rungs)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !pinned {
			writeJSONError(w, http.StatusForbidden, "instance:"+machine+" may download only an artifact applied into a rung whose upstream is "+
				machine+"; no apply into one pinned "+artifactSHA12(sha))
			return
		}
	}
	store, ok := s.artifactStore()
	if !ok || s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artifact store unavailable")
		return
	}
	rec, err := s.users.LookupArtifact(r.Context(), sha)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "artifact "+artifactSHA12(sha)+" was never uploaded to hz")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	uploaded := "uploaded " + rec.UploadedAt.UTC().Format(time.RFC3339) + " by " + rec.UploadedBy
	if rec.Deleted() {
		writeJSONError(w, http.StatusNotFound, "artifact "+artifactSHA12(sha)+" was "+uploaded+", and its file is gone: retention deleted it at "+
			rec.DeletedAt.UTC().Format(time.RFC3339)+" ("+rec.DeletedWhy+") — re-upload it to serve it again")
		return
	}
	f, size, err := store.Open(sha)
	if errors.Is(err, os.ErrNotExist) {
		writeJSONError(w, http.StatusNotFound, "artifact "+artifactSHA12(sha)+" was "+uploaded+", but its file is missing from "+store.Dir+
			" and no deletion is recorded — the file is gone, it was never deleted by hz")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	serveArtifact(w, r, sha, f, size)
}

func serveArtifact(w http.ResponseWriter, r *http.Request, sha string, f io.Reader, size int64) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Hour))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Artifact-SHA256", sha)
	w.Header().Set("ETag", `"`+sha+`"`)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

func artifactResp(a db.Artifact) apitypes.ArtifactResp {
	out := apitypes.ArtifactResp{
		SHA256: a.SHA256, Project: a.Project, Size: a.Size,
		UploadedAt: a.UploadedAt.UTC().Format(time.RFC3339), UploadedBy: a.UploadedBy,
	}
	if a.DeletedAt != nil {
		out.DeletedAt = a.DeletedAt.UTC().Format(time.RFC3339)
		out.DeletedWhy = a.DeletedWhy
	}
	return out
}

// handleAPIArtifacts lists every artifact record, newest upload first.
// GET /api/v1/artifacts
func (s *Server) handleAPIArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	list, err := s.users.Artifacts(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apitypes.ArtifactResp, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, artifactResp(list[i]))
	}
	writeJSON(w, out)
}

// runArtifactRetention gathers retention's inputs, asks artifact.Decide which
// files to delete, and deletes them — logging each with its size and why it
// was not kept, and recording a 'deleted' event so the row says so. Errors are
// logged, never returned: retention failing must not fail an upload.
func (s *Server) runArtifactRetention(ctx context.Context, trigger string) []artifact.Deletion {
	store, ok := s.artifactStore()
	if !ok || s.users == nil {
		return nil
	}
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	in, err := s.retentionInput(ctx)
	if err != nil {
		slog.Error("artifact retention skipped: could not read its inputs", "trigger", trigger, "error", err)
		return nil
	}
	dels, _ := artifact.Decide(in)
	var done []artifact.Deletion
	for _, d := range dels {
		if err := store.Remove(d.SHA); err != nil {
			slog.Error("artifact retention could not delete a file", "sha256", d.SHA, "error", err)
			continue
		}
		if err := s.users.RecordArtifactEvent(ctx, d.SHA, db.ArtifactDeleted, d.Why, "retention"); err != nil {
			slog.Error("LOUD: artifact retention deleted a file but could not record it", "sha256", d.SHA, "error", err)
		}
		slog.Warn("artifact deleted by retention", "sha256", d.SHA, "size", d.Size, "why", d.Why, "trigger", trigger)
		done = append(done, d)
	}
	return done
}

func lineOrEmpty(version string) string {
	l, err := db.LineOf(version)
	if err != nil {
		return ""
	}
	return l
}

func (s *Server) retentionInput(ctx context.Context) (artifact.Input, error) {
	in := artifact.Input{
		Now: time.Now().Unix(), MaxAgeSeconds: artifact.DefaultMaxAgeSeconds, KeepLastPromoted: artifact.DefaultKeepLastPromoted,
		Supported: map[artifact.Rung][]string{}, LinesUnknown: map[artifact.Rung]string{},
	}
	arts, err := s.users.Artifacts(ctx)
	if err != nil {
		return in, err
	}
	for _, a := range arts {
		in.Artifacts = append(in.Artifacts, artifact.Record{SHA: a.SHA256, Size: a.Size, UploadedAt: a.StoredAt.Unix(), Deleted: a.Deleted()})
	}
	promos, err := s.users.Promotions(ctx, "", 1<<30)
	if err != nil {
		return in, err
	}
	for _, p := range promos {
		in.Promotions = append(in.Promotions, artifact.Pin{Rung: artifact.Rung{Project: p.Project, Environment: p.ToEnv},
			SHA: p.ArtifactSHA256, Line: lineOrEmpty(p.Version), ID: p.ID})
	}
	applies, err := s.users.Applies(ctx, "")
	if err != nil {
		return in, err
	}
	for _, a := range applies {
		in.Applies = append(in.Applies, artifact.Pin{Rung: artifact.Rung{Project: a.Project, Environment: a.Environment},
			SHA: a.ArtifactSHA256, Line: lineOrEmpty(a.Version), ID: a.ID})
	}
	reports, err := s.users.LatestDeployReports(ctx)
	if err != nil {
		return in, err
	}
	for _, r := range reports {
		in.NewestReports = append(in.NewestReports, artifact.Pin{Rung: artifact.Rung{Project: r.Project, Environment: r.Environment},
			SHA: r.ArtifactSHA256, Line: lineOrEmpty(r.Version), ID: r.ID})
	}
	cfg := s.cfg()
	for _, env := range cfg.Environments {
		rung := artifact.Rung{Project: env.Project, Environment: env.Name}
		lines, err := s.rungLines(ctx, cfg, env.Project, env)
		if err != nil {
			in.LinesUnknown[rung] = err.Error()
			continue
		}
		if len(lines.Gaps) > 0 {
			in.LinesUnknown[rung] = strings.Join(lines.Gaps, "; ")
			continue
		}
		for _, l := range lines.Supported {
			in.Supported[rung] = append(in.Supported[rung], l.Line)
		}
	}
	return in, nil
}

// startArtifactRetention runs retention once at start and then daily.
func (s *Server) startArtifactRetention(done <-chan struct{}) {
	if _, ok := s.artifactStore(); !ok || s.users == nil {
		return
	}
	go func() {
		s.runArtifactRetention(context.Background(), "at start")
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				s.runArtifactRetention(context.Background(), "daily")
			}
		}
	}()
}
