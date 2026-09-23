package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Importing a gateway into the project tree, and the feed writer it needs.
//
// The plan is computed HERE rather than in the CLI, for the reason
// handlers_api_projects.go gives about feed resolution: the server holds the
// config, so it reads it once and the client renders what it is told. A client
// that re-derived the proposal from /api/v1/services would be a second reader of
// the same facts, free to disagree with the one that writes them — and it would
// see less, because ServiceResp does not carry everything config.Service does.
//
// The write takes the plan from ONE OF TWO PLACES.
//
// With no `plan` in the body it recomputes the proposal and applies THAT, with
// the client's fingerprint as the guard: a config that changed between the dry
// run and the execute is refused rather than quietly importing something nobody
// read. That is what `hz import --execute` has always meant and it is unchanged.
//
// With a `plan` the operator has edited the proposal, which is the case a dry
// run against a real flat estate proved the heuristic cannot handle: thirty
// services under one domain suffix collapse into one project, and the operator
// is the only party that knows they are eight applications. The proposal is a
// starting point and not a verdict, so an edited one has to be applicable.
//
// A client-supplied plan IS a larger thing to have authorised than "apply your
// own proposal", and it is paid for with validation rather than refusal:
// config.PlanFromImportFile checks the file against THIS config before anything
// is written — every service named exists, every service in the config is
// accounted for by name (which is also how a gateway that changed since the file
// was written is caught), every project and rung a row refers to is declared in
// the same file, a posture is one of the three, and an environment may not be
// spread across projects. A fingerprint is refused alongside a plan: a
// hand-edited plan is by definition not the one hz computed, so a fingerprint
// there could only be stale or ignored.

// handleAPIImport serves the proposal (GET) and applies it (POST).
// GET  /api/v1/import
// POST /api/v1/import
func (s *Server) handleAPIImport(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfg()
		writeJSON(w, importPlanResp(cfg.ProposeImport(), len(cfg.Projects)))
	case http.MethodPost:
		s.applyImport(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST required")
	}
}

func (s *Server) applyImport(w http.ResponseWriter, r *http.Request) {
	var req apitypes.ImportApplyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Build and validate the whole result BEFORE anything is published.
	// updateConfig stores the new config and then saves it, so a config that
	// fails validation would already be live by the time Save says so.
	next := *s.cfg()
	plan, ok := importPlanFor(w, &next, req)
	if !ok {
		return
	}
	before := assignedCount(&next)
	projectsBefore, envsBefore := len(next.Projects), len(next.Environments)
	if err := next.ApplyImport(plan, req.Merge); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	writeJSON(w, apitypes.ImportApplyResp{
		OK:                true,
		ProjectsAdded:     len(next.Projects) - projectsBefore,
		EnvironmentsAdded: len(next.Environments) - envsBefore,
		ServicesAssigned:  assignedCount(&next) - before,
	})
}

// importPlanFor resolves the request's plan against this config, writing the
// refusal itself when there is one. Both sources end in the same ImportPlan, so
// ApplyImport never learns which one it got.
func importPlanFor(w http.ResponseWriter, cfg *config.Config, req apitypes.ImportApplyReq) (config.ImportPlan, bool) {
	if req.Plan == nil {
		plan := cfg.ProposeImport()
		if req.Fingerprint != "" && req.Fingerprint != plan.Fingerprint() {
			writeJSONError(w, http.StatusConflict,
				"the config changed since that plan was read (plan "+req.Fingerprint+", now "+plan.Fingerprint()+"); re-run the dry run")
			return config.ImportPlan{}, false
		}
		return plan, true
	}
	if req.Fingerprint != "" {
		writeJSONError(w, http.StatusBadRequest,
			"a plan file and a fingerprint were both sent. A fingerprint identifies a plan hz computed; an edited file is a different plan, so the two cannot both be the authority")
		return config.ImportPlan{}, false
	}
	plan, err := cfg.PlanFromImportFile(importFileFromReq(*req.Plan))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return config.ImportPlan{}, false
	}
	return plan, true
}

// importFileFromReq converts the wire form. One field per field, dull on
// purpose, for the reason importPlanResp gives in the other direction.
func importFileFromReq(r apitypes.ImportFileReq) config.ImportFile {
	f := config.ImportFile{
		Version:      r.Version,
		Projects:     make([]config.ImportFileProject, 0, len(r.Projects)),
		Environments: make([]config.ImportFileEnvironment, 0, len(r.Environments)),
		Assign:       make([]config.ImportFilePlacement, 0, len(r.Assign)),
		Unassigned:   append([]string(nil), r.Unassigned...),
	}
	for _, x := range r.Projects {
		f.Projects = append(f.Projects, config.ImportFileProject{Name: x.Name, Parent: x.Parent})
	}
	for _, x := range r.Environments {
		f.Environments = append(f.Environments, config.ImportFileEnvironment{
			Project: x.Project, Name: x.Name, Posture: x.Posture,
		})
	}
	for _, x := range r.Assign {
		f.Assign = append(f.Assign, config.ImportFilePlacement{
			Service: x.Service, Project: x.Project, Environment: x.Environment,
		})
	}
	return f
}

// assignedCount counts services that name a project, so the response reports
// what the write DID rather than what the plan proposed. Under merge the two
// differ: anything already declared or already filed is skipped.
func assignedCount(cfg *config.Config) int {
	n := 0
	for _, svc := range cfg.Services {
		if svc.Project != "" {
			n++
		}
	}
	return n
}

// handleAPIProjectFeed declares the package repository a project's machines
// install from.
//
// POST /api/v1/projects/feed
func (s *Server) handleAPIProjectFeed(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.FeedSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	feed := config.Feed{URL: req.URL, Suite: req.Suite, Component: req.Component, KeyID: req.KeyID}
	if err := next.SetFeed(req.Project, feed); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}

	// Answer with the project as it now reads, resolved feed and all, so the
	// caller shows what hz holds rather than what it sent.
	out := apitypes.ProjectResp{Name: req.Project}
	for _, p := range next.Projects {
		if p.Name == req.Project {
			out.Parent = p.Parent
			out.Feed = feedResp(p.Feed)
		}
	}
	if resolved, from, err := next.ResolveFeed(req.Project); err == nil {
		out.ResolvedFeed = feedResp(resolved)
		out.FeedFrom = from
	}
	writeJSON(w, out)
}

// importPlanResp converts the plan for the wire. One field per field: the
// conversion is dull on purpose, because the alternative is serialising an
// internal type and having the wire contract change whenever it does.
func importPlanResp(p config.ImportPlan, existingProjects int) apitypes.ImportPlanResp {
	out := apitypes.ImportPlanResp{
		Fingerprint:      p.Fingerprint(),
		Projects:         make([]apitypes.ImportProjectResp, 0, len(p.Projects)),
		Environments:     make([]apitypes.ImportEnvironmentResp, 0, len(p.Environments)),
		Assignments:      make([]apitypes.ImportAssignmentResp, 0, len(p.Assignments)),
		Unassigned:       make([]apitypes.ImportUnassignedResp, 0, len(p.Unassigned)),
		ExistingProjects: existingProjects,
	}
	for _, x := range p.Projects {
		out.Projects = append(out.Projects, apitypes.ImportProjectResp{Name: x.Name, Parent: x.Parent, Reason: x.Reason})
	}
	for _, x := range p.Environments {
		out.Environments = append(out.Environments, apitypes.ImportEnvironmentResp{
			Project: x.Project, Name: x.Name, Posture: x.Posture, Reason: x.Reason,
		})
	}
	for _, x := range p.Assignments {
		out.Assignments = append(out.Assignments, apitypes.ImportAssignmentResp{
			Service: x.Service, Project: x.Project, Environment: x.Environment,
			ProjectReason: x.ProjectReason, EnvironmentReason: x.EnvironmentReason,
		})
	}
	for _, x := range p.Unassigned {
		out.Unassigned = append(out.Unassigned, apitypes.ImportUnassignedResp{Service: x.Service, Reason: x.Reason, Note: x.Note})
	}
	for _, x := range p.Signals {
		out.Signals = append(out.Signals, apitypes.ImportSignalResp{Name: x.Name, Detail: x.Detail})
	}
	return out
}
