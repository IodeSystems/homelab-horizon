package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/nested"
)

// The CHILD side of a nested hz, as served (plan/plan.md N4a). This hz is
// configured with `upstream` in its config.json and answers, for each rung
// listed there, FROM ITS CACHE:
//
//	GET  /api/v1/deploys/desired   the cached desired state; X-HZ-Upstream when the parent is not answering
//	GET  /api/v1/artifacts/{sha}   the pulled, verified artifact
//	POST /api/v1/deploys/report    recorded here, queued for the parent, never waiting on it
//
// Auth for all three: this hz's own admin, or a LOCAL service token attributed
// to that rung (the service declared in THIS hz's config with project and
// environment set). The parent's instance token is not accepted here — it is
// this hz's credential TO the parent, held in upstream.token_file.
//
// The puller (internal/nested) runs in the background; these handlers never
// dial the parent, so a parent that is down costs a local request nothing.

// childCaller authorises a local caller for one upstream rung.
func (s *Server) childCaller(r *http.Request, rungs []config.UpstreamRung) (who string, status int, msg string) {
	if s.isAdmin(r) {
		return s.adminActor(r), 0, ""
	}
	if _, ok := s.instanceCaller(r); ok {
		return "", http.StatusForbidden, "an instance token is for the parent hz; this hz serves its upstream rungs to its own admin and local service tokens"
	}
	svc, status, msg := s.reportingService(r)
	if status != 0 {
		return "", status, msg
	}
	for _, rg := range rungs {
		if svc.Project == rg.Project && svc.Environment == rg.Environment {
			return "service:" + svc.Name, 0, ""
		}
	}
	names := make([]string, 0, len(rungs))
	for _, rg := range rungs {
		names = append(names, rg.String())
	}
	return "", http.StatusForbidden, "service " + svc.Name + "'s token may read only its own rung (" + svc.Project + "/" + svc.Environment +
		"), not " + strings.Join(names, ", ")
}

func (s *Server) handleChildDesired(w http.ResponseWriter, r *http.Request, rung config.UpstreamRung) {
	if _, status, msg := s.childCaller(r, []config.UpstreamRung{rung}); status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	resp := s.child.Answer(rung, r.Header.Get("If-None-Match"))
	if resp.Upstream != "" {
		w.Header().Set(nested.HeaderUpstream, resp.Upstream)
	}
	if resp.ETag != "" {
		w.Header().Set("ETag", resp.ETag)
	}
	switch resp.Status {
	case http.StatusOK:
		writeJSON(w, resp.Desired)
	case http.StatusNotModified:
		w.WriteHeader(http.StatusNotModified)
	default:
		writeJSONError(w, resp.Status, resp.Error)
	}
}

func (s *Server) handleChildArtifact(w http.ResponseWriter, r *http.Request, sha string, rungs []config.UpstreamRung) {
	if _, status, msg := s.childCaller(r, rungs); status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	f, size, err := s.child.Store.Open(sha)
	if errors.Is(err, os.ErrNotExist) {
		writeJSONError(w, http.StatusNotFound, "artifact "+artifactSHA12(sha)+" is named by this hz's cache but its file is gone from "+s.child.Store.Dir)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	serveArtifact(w, r, sha, f, size)
}

// handleChildReport records a local report and queues it for the parent. The
// answer never waits on the parent.
func (s *Server) handleChildReport(w http.ResponseWriter, r *http.Request, req apitypes.DeployReportReq) {
	rung := config.UpstreamRung{Project: req.Project, Environment: req.Environment}
	who, status, msg := s.childCaller(r, []config.UpstreamRung{rung})
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if req.ForwardedFor != "" {
		writeJSONError(w, http.StatusBadRequest, "forwarded_for is set by this hz when it forwards; a local report does not send it")
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
	if err := db.CheckDeployVersion(req.Version); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sha, err := db.NormalizeSHA256(req.ArtifactSHA256)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ArtifactSHA256 = sha
	if err := db.CheckLocator("build_url", req.BuildURL); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var id int64
	if s.users != nil {
		id, err = s.users.RecordDeployReport(r.Context(), db.DeployReport{
			Project: req.Project, Environment: req.Environment, App: req.App,
			Version: req.Version, Describe: req.Describe, ArtifactSHA256: sha,
			Host: req.Host, BuildURL: req.BuildURL, ReportedBy: who,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	fwd := req
	fwd.ForwardedFor = who
	if err := s.child.Enqueue(fwd); err != nil {
		// The one failure that would drop a report: say so, loudly, as a 500.
		writeJSONError(w, http.StatusInternalServerError, "recorded here but NOT queued for the parent hz: "+err.Error())
		return
	}
	writeJSON(w, apitypes.DeployReportResultResp{Recorded: true, ID: id, Upstream: "queued"})
}

// startChild runs the puller and the report drainer when this hz is nested.
func (s *Server) startChild(done <-chan struct{}) {
	if s.child == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-done
		cancel()
	}()
	go s.child.Run(ctx)
}

// childFromConfig builds the child when config.json has an upstream block.
func childFromConfig(cfg *config.Config) *nested.Child {
	if cfg.Upstream == nil {
		return nil
	}
	c := nested.New(*cfg.Upstream, cfg.DataDir())
	c.MaxArtifactBytes = cfg.ArtifactCap()
	return c
}
