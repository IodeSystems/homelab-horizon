package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/artifact"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The build registry (operator, 2026-10-02): build numbers come FROM hz.
//
//	POST   /api/v1/builders               {machine, projects[]}   admin   mint (or rotate) a builder token
//	GET    /api/v1/builders                                       admin
//	DELETE /api/v1/builders/{machine}                             admin   revoke
//	POST   /api/v1/builds                 start                   builder
//	GET    /api/v1/builds?project=&number=&limit=                 admin
//	GET    /api/v1/builds/{id}                                    admin
//	POST   /api/v1/builds/{id}/log        text/plain chunk        builder
//	GET    /api/v1/builds/{id}/log?offset=&tail=                  admin
//	PUT    /api/v1/builds/{id}/state                              builder
//	POST   /api/v1/builds/{id}/finish                             builder
//	POST   /api/v1/builds/seed            {project, next}         admin   raise the next number
//
// THE BUILDER TOKEN authenticates as builder:<machine> for exactly five calls —
// start, log, state, finish, and an artifact upload — and only for the
// projects its grant names, and only for builds that machine started. isAdmin
// never accepts it, so every admin route refuses it without knowing it exists;
// TestBuilderTokenIsRefusedOnAdminRoutes enumerates them.
//
// THE ONE AUTHORITY (CLAUDE.md). Minting a builder is admin-gated, so this
// gates one authority against itself too. What the registry adds is not a
// second person: it is ONE source of numbers, and a record — which machine
// built which commit into which artifact, with its log — that a promotion now
// has to point at.
//
// A builder on the dev box makes builds TRACEABLE, not independent: the box
// that writes the code also runs the build that vouches for it.

// builderTokenPrefix marks a builder token, so it never reaches the
// service-token or personal-token lookups by accident.
const builderTokenPrefix = "hzb_"

// BuildLogCap bounds one build's log. Past it, one truncation line is
// appended and every later chunk is a 413.
const BuildLogCap = 20 << 20

// buildLogReadMax bounds one GET of a log; X-Log-Next-Offset continues it.
const buildLogReadMax = 4 << 20

// FailedBuildLogKeep is how long a failed or abandoned build's log is kept.
const FailedBuildLogKeep = 30 * 24 * time.Hour

func newBuilderToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return builderTokenPrefix + hex.EncodeToString(raw), nil
}

// builderCaller resolves a builder token to its builder.
func (s *Server) builderCaller(r *http.Request) (config.Builder, bool) {
	tok := requestBearer(r)
	if !strings.HasPrefix(tok, builderTokenPrefix) {
		return config.Builder{}, false
	}
	sum := tokenDigest(tok)
	for _, b := range s.cfg().Builders {
		if subtle.ConstantTimeCompare([]byte(b.TokenSHA256), []byte(sum)) == 1 {
			return b, true
		}
	}
	return config.Builder{}, false
}

// builderFor authorises a builder token for one project.
func (s *Server) builderFor(r *http.Request, project string) (config.Builder, int, string) {
	b, ok := s.builderCaller(r)
	if !ok {
		if strings.HasPrefix(requestBearer(r), builderTokenPrefix) {
			return b, http.StatusUnauthorized, "Unauthorized: unknown or revoked builder token"
		}
		return b, http.StatusUnauthorized, "Unauthorized: a build is started, logged and finished with a builder token (`hz builder add <machine> --project P`)"
	}
	if !b.Grants(project) {
		return b, http.StatusForbidden, "builder " + b.Machine + " is granted builds of " + strings.Join(b.Projects, ", ") + ", not " + project
	}
	return b, 0, ""
}

// ---------------------------------------------------------------------------
// Builders.

func builderResp(b config.Builder) apitypes.BuilderResp {
	return apitypes.BuilderResp{Machine: b.Machine, Projects: append([]string{}, b.Projects...)}
}

// handleAPIBuilders lists builders (GET) or mints a builder token (POST).
func (s *Server) handleAPIBuilders(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := []apitypes.BuilderResp{}
		for _, b := range s.cfg().Builders {
			out = append(out, builderResp(b))
		}
		writeJSON(w, out)
	case http.MethodPost:
		s.mintBuilder(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET lists builders, POST mints one")
	}
}

func (s *Server) mintBuilder(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BuilderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	req.Machine = strings.TrimSpace(req.Machine)
	if req.Machine == "" {
		writeJSONError(w, http.StatusBadRequest, "machine is required — a builder is a declared machine")
		return
	}
	tok, err := newBuilderToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := *s.cfg()
	rotated, err := next.SetBuilder(req.Machine, req.Projects, tokenDigest(tok))
	if err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	b, _ := next.FindBuilder(req.Machine)
	slog.Info("builder token minted", "machine", b.Machine, "projects", b.Projects, "rotated", rotated, "by", s.adminActor(r))
	writeJSON(w, apitypes.BuilderTokenResp{Machine: b.Machine, Projects: b.Projects, Token: tok, Rotated: rotated})
}

// handleAPIBuilder revokes a builder: DELETE /api/v1/builders/{machine}.
func (s *Server) handleAPIBuilder(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "DELETE required")
		return
	}
	machine := strings.TrimPrefix(r.URL.Path, "/api/v1/builders/")
	if machine == "" || strings.Contains(machine, "/") {
		writeJSONError(w, http.StatusBadRequest, "the path names a builder by its machine: /api/v1/builders/<machine>")
		return
	}
	next := *s.cfg()
	if err := next.RemoveBuilder(machine); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) { *c = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	slog.Info("builder revoked", "machine", machine, "by", s.adminActor(r))
	writeJSON(w, apitypes.BuilderRemovedResp{Removed: true, Machine: machine})
}

// ---------------------------------------------------------------------------
// Builds.

// declaredVersions is every version a rung of project declares in config.json:
// recorded outside hz.db, and counted by the seed and the in-use check.
func declaredVersions(cfg *config.Config, project string) []string {
	var out []string
	for _, e := range cfg.Environments {
		if e.Project == project && e.Version != "" {
			out = append(out, e.Version)
		}
	}
	return out
}

// buildURL is hz's page for a build: admin_url when set, else the address this
// request reached hz at.
func (s *Server) buildURL(r *http.Request, project string, number int64) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg().AdminURL), "/")
	if base == "" {
		base = s.requestBaseURL(r)
	}
	return base + "/p/" + project + "/builds/" + strconv.FormatInt(number, 10)
}

// buildLogPath is <buildDir>/<project>/<number>.log. A project name that would
// leave the directory is refused rather than cleaned.
func (s *Server) buildLogPath(project string, number int64) (string, error) {
	if s.buildDir == "" {
		return "", errors.New("build log store unavailable")
	}
	if project == "" || project == "." || project == ".." || strings.ContainsAny(project, "/\\\x00") || strings.HasPrefix(project, ".") {
		return "", fmt.Errorf("project %q cannot name a log directory", project)
	}
	return filepath.Join(s.buildDir, project, strconv.FormatInt(number, 10)+".log"), nil
}

// logState is a log file's size and whether it hit the cap. A missing file is
// size 0.
func logState(path string) (size int64, truncated bool, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return fi.Size(), fi.Size() > BuildLogCap, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Server) buildResp(r *http.Request, b db.Build, now time.Time) apitypes.BuildResp {
	out := apitypes.BuildResp{
		ID: b.ID, Project: b.Project, Number: b.Number, Version: b.Version, VersionCore: b.VersionCore,
		Commit: b.Commit, Describe: b.Describe, Machine: b.Machine,
		StartedAt: stamp(b.StartedAt), LastActivityAt: stamp(b.LastActivityAt),
		Result: b.Outcome(now), Step: b.Step, Steps: []apitypes.BuildStepResp{},
		ArtifactSHA256: b.ArtifactSHA256, FailedStep: b.FailedStep,
		LogDeletedWhy: b.LogDeletedWhy, BuildURL: s.buildURL(r, b.Project, b.Number),
	}
	for _, st := range b.Steps {
		out.Steps = append(out.Steps, apitypes.BuildStepResp(st))
	}
	end := now
	switch {
	case b.FinishedAt != nil:
		out.FinishedAt = stamp(*b.FinishedAt)
		end = *b.FinishedAt
	case out.Result == db.BuildAbandoned:
		end = b.LastActivityAt
	}
	out.DurationSeconds = ageSeconds(end, b.StartedAt)
	if b.LogDeletedAt != nil {
		out.LogDeletedAt = stamp(*b.LogDeletedAt)
	} else if path, err := s.buildLogPath(b.Project, b.Number); err == nil {
		out.LogSize, out.LogTruncated, _ = logState(path)
	}
	return out
}

// handleAPIBuilds starts a build (POST, builder) or lists builds (GET, admin).
func (s *Server) handleAPIBuilds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.startBuild(w, r)
	case http.MethodGet:
		s.listBuilds(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "POST starts a build, GET lists them")
	}
}

func (s *Server) startBuild(w http.ResponseWriter, r *http.Request) {
	var req apitypes.BuildStartReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Decoded before the scope check, because the scope IS the project in
		// the body — the deploy report's order.
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	b, status, msg := s.builderFor(r, req.Project)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if req.Machine != b.Machine {
		writeJSONError(w, http.StatusForbidden, "this token is builder "+b.Machine+"'s; a build names the machine that runs it, and "+
			strconv.Quote(req.Machine)+" is not "+b.Machine)
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable — builds are numbered in hz.db")
		return
	}
	cfg := s.cfg()
	if err := cfg.CheckProjectRef(req.Project); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.buildLogPath(req.Project, 1); err != nil && s.buildDir != "" {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	build, skipped, err := s.users.StartBuild(r.Context(), db.BuildStart{
		Project: req.Project, Commit: req.Commit, VersionCore: req.VersionCore, Describe: req.Describe, Machine: b.Machine,
	}, declaredVersions(cfg, req.Project))
	if errors.Is(err, db.ErrInvalidBuild) || errors.Is(err, db.ErrInvalidVersion) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("build started", "project", build.Project, "number", build.Number, "version", build.Version,
		"commit", build.Commit, "machine", build.Machine, "skipped", skipped)
	writeJSON(w, apitypes.BuildStartResp{ID: build.ID, Number: build.Number, Version: build.Version, BuildURL: s.buildURL(r, build.Project, build.Number)})
}

func (s *Server) listBuilds(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	q := r.URL.Query()
	now := time.Now()
	out := []apitypes.BuildResp{}
	if n := q.Get("number"); n != "" {
		num, err := strconv.ParseInt(n, 10, 64)
		if err != nil || q.Get("project") == "" {
			writeJSONError(w, http.StatusBadRequest, "?number= needs a ?project= and a positive integer")
			return
		}
		b, err := s.users.BuildByNumber(r.Context(), q.Get("project"), num)
		switch {
		case errors.Is(err, db.ErrNotFound):
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		default:
			out = append(out, s.buildResp(r, *b, now))
		}
		writeJSON(w, out)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.users.Builds(r.Context(), q.Get("project"), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, b := range list {
		out = append(out, s.buildResp(r, b, now))
	}
	writeJSON(w, out)
}

// handleAPIBuildSeed raises a project's next build number.
// POST /api/v1/builds/seed {project, next}
func (s *Server) handleAPIBuildSeed(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return
	}
	var req apitypes.BuildSeedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	cfg := s.cfg()
	if err := cfg.CheckProjectRef(req.Project); err != nil || req.Project == "" {
		writeJSONError(w, http.StatusBadRequest, "project must be a declared project")
		return
	}
	prev, err := s.users.RaiseBuildNumber(r.Context(), req.Project, req.Next, declaredVersions(cfg, req.Project))
	switch {
	case errors.Is(err, db.ErrSeedLower):
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, db.ErrInvalidBuild):
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("build number raised", "project", req.Project, "from", prev, "to", req.Next, "by", s.adminActor(r))
	writeJSON(w, apitypes.BuildSeedResp{Project: req.Project, Previous: prev, Next: req.Next})
}

// handleAPIBuild routes /api/v1/builds/{id}[/log|/state|/finish].
func (s *Server) handleAPIBuild(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/builds/")
	idPart, verb, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 || strings.Contains(verb, "/") {
		writeJSONError(w, http.StatusBadRequest, "the path names a build by its id: /api/v1/builds/<id>[/log|/state|/finish]")
		return
	}
	switch {
	case verb == "" && r.Method == http.MethodGet:
		s.getBuild(w, r, id)
	case verb == "log" && r.Method == http.MethodGet:
		s.readBuildLog(w, r, id)
	case verb == "log" && r.Method == http.MethodPost:
		s.appendBuildLog(w, r, id)
	case verb == "state" && r.Method == http.MethodPut:
		s.putBuildState(w, r, id)
	case verb == "finish" && r.Method == http.MethodPost:
		s.finishBuild(w, r, id)
	case verb == "" || verb == "log" || verb == "state" || verb == "finish":
		writeJSONError(w, http.StatusMethodNotAllowed, "GET /builds/{id}; GET or POST /log; PUT /state; POST /finish")
	default:
		writeJSONError(w, http.StatusNotFound, "no such build call: "+verb)
	}
}

// adminBuild loads a build for an admin read.
func (s *Server) adminBuild(w http.ResponseWriter, r *http.Request, id int64) (*db.Build, bool) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return nil, false
	}
	return s.loadBuild(w, r, id)
}

func (s *Server) loadBuild(w http.ResponseWriter, r *http.Request, id int64) (*db.Build, bool) {
	if s.users == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "identity store unavailable")
		return nil, false
	}
	b, err := s.users.BuildByID(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "no build "+strconv.FormatInt(id, 10))
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return b, true
}

// builderBuild authorises a builder write to one build: a valid builder token,
// granted for the build's project, of the machine that started it. The token
// is checked before the build is looked up, so an unauthenticated caller
// learns nothing about which ids exist.
func (s *Server) builderBuild(w http.ResponseWriter, r *http.Request, id int64) (*db.Build, bool) {
	if _, ok := s.builderCaller(r); !ok {
		_, status, msg := s.builderFor(r, "")
		writeJSONError(w, status, msg)
		return nil, false
	}
	b, ok := s.loadBuild(w, r, id)
	if !ok {
		return nil, false
	}
	caller, status, msg := s.builderFor(r, b.Project)
	if status != 0 {
		writeJSONError(w, status, msg)
		return nil, false
	}
	if caller.Machine != b.Machine {
		writeJSONError(w, http.StatusForbidden, "build #"+strconv.FormatInt(b.Number, 10)+" was started by builder "+b.Machine+
			"; builder "+caller.Machine+" cannot write to it")
		return nil, false
	}
	return b, true
}

func finishedRefusal(w http.ResponseWriter, b *db.Build) {
	writeJSONError(w, http.StatusConflict, "build #"+strconv.FormatInt(b.Number, 10)+" is finished ("+b.Result+
		") — a finish is final; start a new build")
}

func (s *Server) getBuild(w http.ResponseWriter, r *http.Request, id int64) {
	b, ok := s.adminBuild(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, s.buildResp(r, *b, time.Now()))
}

// appendBuildLog appends one text/plain chunk to the build's log, 0600, under
// the 20 MiB cap.
func (s *Server) appendBuildLog(w http.ResponseWriter, r *http.Request, id int64) {
	b, ok := s.builderBuild(w, r, id)
	if !ok {
		return
	}
	if b.FinishedAt != nil {
		finishedRefusal(w, b)
		return
	}
	path, err := s.buildLogPath(b.Project, b.Number)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	chunk, err := io.ReadAll(http.MaxBytesReader(w, r.Body, BuildLogCap+1))
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a log chunk is at most %d bytes (the whole log's cap)", BuildLogCap))
		return
	}

	s.buildLogMu.Lock()
	defer s.buildLogMu.Unlock()
	size, truncated, err := logState(path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if truncated {
		writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"build #%d's log reached its cap of %d bytes and was truncated; hz keeps no more of it", b.Number, BuildLogCap))
		return
	}
	write := chunk
	cut := false
	if size+int64(len(chunk)) > BuildLogCap {
		write = chunk[:BuildLogCap-size]
		cut = true
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = f.Chmod(0o600)
	_, werr := f.Write(write)
	if werr == nil && cut {
		_, werr = fmt.Fprintf(f, "\n[hz] log truncated: build #%d's log reached the %d-byte cap; later output was not kept\n", b.Number, BuildLogCap)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		writeJSONError(w, http.StatusInternalServerError, werr.Error())
		return
	}
	if err := s.users.TouchBuild(r.Context(), b.ID); err != nil && !errors.Is(err, db.ErrBuildFinished) {
		slog.Warn("build log appended but its activity time was not recorded", "build", b.ID, "error", err)
	}
	newSize, _, _ := logState(path)
	writeJSON(w, apitypes.BuildLogAppendResp{Appended: int64(len(write)), Size: newSize, Truncated: cut})
}

// readBuildLog answers the log as text/plain from ?offset= (default 0), or the
// last ?tail= bytes, at most 4 MiB per call. X-Log-Next-Offset is where to ask
// next; X-Log-Size is the whole log's size.
func (s *Server) readBuildLog(w http.ResponseWriter, r *http.Request, id int64) {
	b, ok := s.adminBuild(w, r, id)
	if !ok {
		return
	}
	if b.LogDeletedAt != nil {
		writeJSONError(w, http.StatusGone, "build #"+strconv.FormatInt(b.Number, 10)+"'s log was deleted by retention at "+
			stamp(*b.LogDeletedAt)+" ("+b.LogDeletedWhy+")")
		return
	}
	path, err := s.buildLogPath(b.Project, b.Number)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	q := r.URL.Query()
	var offset, tail int64
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.ParseInt(v, 10, 64); err != nil || offset < 0 {
			writeJSONError(w, http.StatusBadRequest, "?offset= is a non-negative byte offset")
			return
		}
	}
	if v := q.Get("tail"); v != "" {
		if tail, err = strconv.ParseInt(v, 10, 64); err != nil || tail < 0 {
			writeJSONError(w, http.StatusBadRequest, "?tail= is a non-negative byte count")
			return
		}
	}
	size, truncated, err := logState(path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tail > 0 {
		offset = max(size-tail, 0)
	}
	offset = min(offset, size)
	var body []byte
	if size > offset {
		f, err := os.Open(path)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		body = make([]byte, min(size-offset, buildLogReadMax))
		n, err := f.ReadAt(body, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		body = body[:n]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Log-Offset", strconv.FormatInt(offset, 10))
	w.Header().Set("X-Log-Next-Offset", strconv.FormatInt(offset+int64(len(body)), 10))
	w.Header().Set("X-Log-Size", strconv.FormatInt(size, 10))
	w.Header().Set("X-Log-Truncated", strconv.FormatBool(truncated))
	w.Header().Set("X-Build-Result", b.Outcome(time.Now()))
	_, _ = w.Write(body)
}

func (s *Server) putBuildState(w http.ResponseWriter, r *http.Request, id int64) {
	b, ok := s.builderBuild(w, r, id)
	if !ok {
		return
	}
	var req apitypes.BuildStateReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	steps := make([]db.BuildStep, 0, len(req.Steps))
	for _, st := range req.Steps {
		steps = append(steps, db.BuildStep(st))
	}
	err := s.users.UpdateBuildState(r.Context(), b.ID, req.Step, req.Status, steps)
	switch {
	case errors.Is(err, db.ErrBuildFinished):
		finishedRefusal(w, b)
		return
	case errors.Is(err, db.ErrInvalidBuild):
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after, err := s.users.BuildByID(r.Context(), b.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.buildResp(r, *after, time.Now()))
}

// finishBuild records the build's one finish. passed names an artifact already
// uploaded to the build's project; failed names none.
func (s *Server) finishBuild(w http.ResponseWriter, r *http.Request, id int64) {
	b, ok := s.builderBuild(w, r, id)
	if !ok {
		return
	}
	if b.FinishedAt != nil {
		finishedRefusal(w, b)
		return
	}
	var req apitypes.BuildFinishReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	sha := ""
	switch req.Result {
	case db.BuildPassed:
		var err error
		sha, err = db.NormalizeSHA256(req.ArtifactSHA256)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "a passed build names its artifact: "+err.Error())
			return
		}
		art, err := s.users.LookupArtifact(r.Context(), sha)
		switch {
		case errors.Is(err, db.ErrNotFound):
			writeJSONError(w, http.StatusConflict, "artifact "+artifactSHA12(sha)+" is not uploaded to hz — upload it first (PUT /api/v1/artifacts/"+
				sha+"?project="+b.Project+"), then finish")
			return
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		case art.Project != b.Project:
			writeJSONError(w, http.StatusConflict, "artifact "+artifactSHA12(sha)+" is uploaded for project "+art.Project+
				", not "+b.Project+" — a build vouches only for its own project's artifact")
			return
		case art.Deleted():
			writeJSONError(w, http.StatusConflict, "artifact "+artifactSHA12(sha)+" was deleted by retention — re-upload it, then finish")
			return
		}
	case db.BuildFailed:
		sha = req.ArtifactSHA256
	}
	err := s.users.FinishBuild(r.Context(), b.ID, req.Result, sha, req.FailedStep)
	switch {
	case errors.Is(err, db.ErrBuildFinished):
		finishedRefusal(w, b)
		return
	case errors.Is(err, db.ErrInvalidBuild) || errors.Is(err, db.ErrInvalidSHA256):
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after, err := s.users.BuildByID(r.Context(), b.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("build finished", "project", after.Project, "number", after.Number, "result", after.Result, "artifact", after.ArtifactSHA256)
	writeJSON(w, s.buildResp(r, *after, time.Now()))
}

// ---------------------------------------------------------------------------
// Provenance — the promote gate's question.

// provenance is where an artifact came from, as the promote gate reads it.
type provenance struct {
	kind  string // db.ProvenanceBuild, db.ProvenanceGrandfathered, or "" (none)
	build *db.Build
	// refusal is set when kind is "": the sentence naming the rule.
	refusal string
}

// artifactProvenance answers whether sha may be promoted in project: the
// artifact of a passed build by a builder granted for project NOW, or
// grandfathered (uploaded before migration 0018).
func (s *Server) artifactProvenance(ctx context.Context, cfg *config.Config, project, sha string) (provenance, error) {
	b, err := s.users.PassedBuildOf(ctx, project, sha)
	switch {
	case err == nil:
		if bl, ok := cfg.FindBuilder(b.Machine); ok && bl.Grants(project) {
			return provenance{kind: db.ProvenanceBuild, build: b}, nil
		}
	case !errors.Is(err, db.ErrNotFound):
		return provenance{}, err
	}
	old, err := s.users.IsGrandfathered(ctx, sha)
	if err != nil {
		return provenance{}, err
	}
	if old {
		return provenance{kind: db.ProvenanceGrandfathered}, nil
	}
	if b != nil {
		return provenance{refusal: "artifact " + artifactSHA12(sha) + " is build #" + strconv.FormatInt(b.Number, 10) + "'s, by builder " + b.Machine +
			", which is no longer granted builds of " + project + " — a promotion needs the artifact of a passed build by a builder granted for the project"}, nil
	}
	return provenance{refusal: "artifact " + artifactSHA12(sha) + " has no build record: no passed build of " + project +
		" names it, and it was uploaded after hz began numbering builds — a promotion needs the artifact of a passed build by a builder granted for the project" +
		" (POST /api/v1/builds, then /finish {\"result\":\"passed\",\"artifact_sha256\":…})"}, nil
}

// buildNumberOf is the number of the build that minted version in project, or
// 0 when hz did not number it.
func (s *Server) buildNumberOf(ctx context.Context, project, version string) int64 {
	if s.users == nil {
		return 0
	}
	b, err := s.users.BuildByVersion(ctx, project, version)
	if err != nil {
		return 0
	}
	return b.Number
}

// artifactProvenanceResp fills an ArtifactResp's provenance.
func (s *Server) artifactProvenanceResp(ctx context.Context, out *apitypes.ArtifactResp) error {
	if b, err := s.users.PassedBuildOf(ctx, out.Project, out.SHA256); err == nil {
		out.Provenance, out.BuildID, out.BuildNumber = apitypes.ProvenanceBuild, b.ID, b.Number
		return nil
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}
	old, err := s.users.IsGrandfathered(ctx, out.SHA256)
	if err != nil {
		return err
	}
	if old {
		out.Provenance = apitypes.ProvenanceNoBuildRecord
	} else {
		out.Provenance = apitypes.ProvenanceUnproven
	}
	return nil
}

// ---------------------------------------------------------------------------
// Log retention — the impure half; artifact.DecideBuildLogs decides.

// runBuildLogRetention deletes the logs DecideBuildLogs names and records each
// deletion on its build. Errors are logged, never returned.
func (s *Server) runBuildLogRetention(ctx context.Context, trigger string) []artifact.LogDeletion {
	if s.users == nil || s.buildDir == "" {
		return nil
	}
	builds, err := s.users.Builds(ctx, "", 1<<30)
	if err != nil {
		slog.Error("build log retention skipped: could not read the builds", "error", err)
		return nil
	}
	arts, err := s.users.Artifacts(ctx)
	if err != nil {
		slog.Error("build log retention skipped: could not read the artifacts", "error", err)
		return nil
	}
	now := time.Now()
	in := artifact.BuildLogInput{Now: now.Unix(), FailedKeepSeconds: int64(FailedBuildLogKeep / time.Second), DeletedArtifacts: map[string]bool{}}
	for _, a := range arts {
		if a.Deleted() {
			in.DeletedArtifacts[a.SHA256] = true
		}
	}
	paths := map[int64]string{}
	for _, b := range builds {
		path, err := s.buildLogPath(b.Project, b.Number)
		if err != nil || b.LogDeletedAt != nil {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		paths[b.ID] = path
		ended := b.LastActivityAt
		if b.FinishedAt != nil {
			ended = *b.FinishedAt
		}
		in.Builds = append(in.Builds, artifact.BuildLog{ID: b.ID, Project: b.Project, Number: b.Number,
			Result: b.Outcome(now), ArtifactSHA: b.ArtifactSHA256, EndedAt: ended.Unix()})
	}
	var done []artifact.LogDeletion
	for _, d := range artifact.DecideBuildLogs(in) {
		if err := os.Remove(paths[d.ID]); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Error("build log retention could not delete a log", "build", d.ID, "error", err)
			continue
		}
		if err := s.users.RecordBuildLogDeleted(ctx, d.ID, d.Why); err != nil {
			slog.Error("LOUD: build log retention deleted a log but could not record it", "build", d.ID, "error", err)
		}
		slog.Warn("build log deleted by retention", "project", d.Project, "number", d.Number, "why", d.Why, "trigger", trigger)
		done = append(done, d)
	}
	return done
}

// buildNumberOfID is the number of build id, or 0.
func (s *Server) buildNumberOfID(ctx context.Context, id int64) int64 {
	if s.users == nil || id == 0 {
		return 0
	}
	b, err := s.users.BuildByID(ctx, id)
	if err != nil {
		return 0
	}
	return b.Number
}
