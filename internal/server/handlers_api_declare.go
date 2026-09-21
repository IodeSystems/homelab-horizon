package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The write half of the project tree.
//
// Same layering as handlers_api_import.go, and for the same reason: the SERVER
// owns the config. It holds it in an atomic pointer, it is probably serving it
// to another request right now, and it is the only thing that writes
// config.json. A CLI that edited the file directly would race that, so every one
// of these is a request and the decisions all live in internal/config
// (AddProject, RemoveProject, AddEnvironment, SetEnvironment, RemoveEnvironment)
// beside the validators they have to satisfy.
//
// One shape is repeated deliberately: every handler builds the WHOLE next config
// and validates it BEFORE publishing. updateConfig stores first and saves
// second, so a config that fails Save is already live by the time Save says so.
// The config writers validate on their own copy, which is what makes that safe.

// handleAPIProjectAdd declares a project.
// POST /api/v1/projects/add
func (s *Server) handleAPIProjectAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.ProjectAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	if err := next.AddProject(req.Name, req.Parent); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	writeJSON(w, projectResp(&next, req.Name))
}

// handleAPIProjectRm removes a project, or — without confirm — says what that
// would take.
// POST /api/v1/projects/rm
func (s *Server) handleAPIProjectRm(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.ProjectRmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	removes, blocked, err := next.ProjectRemoval(req.Name, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The feed goes with the project and `hz project add` cannot put it back, so
	// it is read off the config BEFORE the removal and reported either way.
	var feed *apitypes.FeedResp
	for _, p := range next.Projects {
		if p.Name == req.Name {
			feed = feedResp(p.Feed)
		}
	}
	out := apitypes.RemovalResp{Removes: dependantsResp(removes), Blocked: dependantsResp(blocked), Feed: feed}

	// A blocked removal and an unconfirmed one both write nothing, and both
	// answer 200 with the reason in the body rather than an error string: the
	// list of dependants is the whole point of refusing, and it survives as a
	// list only if it is not flattened into a message.
	if len(blocked) > 0 || !req.Confirm {
		writeJSON(w, out)
		return
	}
	if _, err := next.RemoveProject(req.Name, req.Cascade); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	out.OK = true
	writeJSON(w, out)
}

// handleAPIEnvironmentAdd declares a rung.
// POST /api/v1/environments/add
func (s *Server) handleAPIEnvironmentAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.EnvironmentAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	env := config.Environment{
		Project: req.Project, Name: req.Name, Posture: req.Posture,
		From: req.From, Version: req.Version,
	}
	if err := next.AddEnvironment(env); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	writeJSON(w, environmentResp(&next, req.Project, req.Name))
}

// handleAPIEnvironmentSet patches a rung. Absent fields are left alone; see
// apitypes.EnvironmentSetReq for why this one is a patch and feed set is not.
// POST /api/v1/environments/set
func (s *Server) handleAPIEnvironmentSet(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.EnvironmentSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	patch := config.EnvironmentPatch{Posture: req.Posture, From: req.From, Version: req.Version}
	if _, err := next.SetEnvironment(req.Project, req.Name, patch); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	writeJSON(w, environmentResp(&next, req.Project, req.Name))
}

// handleAPIEnvironmentRm removes a rung, or — without confirm — says what that
// would take.
// POST /api/v1/environments/rm
func (s *Server) handleAPIEnvironmentRm(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.EnvironmentRmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	removes, blocked, err := next.EnvironmentRemoval(req.Project, req.Name, req.Cascade)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := apitypes.RemovalResp{Removes: dependantsResp(removes), Blocked: dependantsResp(blocked)}
	if len(blocked) > 0 || !req.Confirm {
		writeJSON(w, out)
		return
	}
	if _, err := next.RemoveEnvironment(req.Project, req.Name, req.Cascade); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	out.OK = true
	writeJSON(w, out)
}

// handleAPIServiceAssign places one service in the project tree, or — with both
// placement fields empty — takes it out of it.
//
// Narrow on purpose: /services/edit is full-replace across domains, proxy, DNS
// and forwards, so an assign routed through it would have to round-trip the
// whole service and could drop whatever changed between the read and the write.
// Moving a service between rungs touches two fields, so this writes two fields.
// POST /api/v1/services/assign
func (s *Server) handleAPIServiceAssign(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.ServiceAssignReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	next := *s.cfg()
	svc, err := next.AssignService(req.Service, req.Project, req.Environment)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.updateConfig(func(cfg *config.Config) { *cfg = next }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
		return
	}
	// Assignment renders nothing — no DNS record, no HAProxy backend — so unlike
	// the service mutations this does not trigger a sync. `hz pending` will show
	// the config change; there is nothing on a box to push it to.
	writeJSON(w, apitypes.ServiceAssignResp{
		Service: svc.Name, Project: svc.Project, Environment: svc.Environment,
	})
}

// projectResp renders one project as the read surface renders it — resolved
// feed and provenance included — so a write answers with what hz now holds
// rather than an echo of what was sent.
func projectResp(cfg *config.Config, name string) apitypes.ProjectResp {
	out := apitypes.ProjectResp{Name: name}
	for _, p := range cfg.Projects {
		if p.Name == name {
			out.Parent = p.Parent
			out.Feed = feedResp(p.Feed)
		}
	}
	for _, svc := range cfg.Services {
		if svc.Project == name {
			out.Services = append(out.Services, svc.Name)
		}
	}
	if resolved, from, err := cfg.ResolveFeed(name); err == nil {
		out.ResolvedFeed = feedResp(resolved)
		out.FeedFrom = from
	}
	return out
}

// environmentResp renders one rung as /api/v1/environments renders it.
func environmentResp(cfg *config.Config, project, name string) apitypes.EnvironmentResp {
	for _, e := range cfg.Environments {
		if e.Project == project && e.Name == name {
			return apitypes.EnvironmentResp{
				Project: e.Project, Name: e.Name, Posture: e.Posture,
				From: e.From, Version: e.Version,
			}
		}
	}
	return apitypes.EnvironmentResp{Project: project, Name: name}
}

// dependantsResp converts the config records for the wire, one field per field,
// for the reason importPlanResp gives: serialising an internal type would make
// the wire contract change whenever it does.
func dependantsResp(list []config.Dependant) []apitypes.DependantResp {
	if len(list) == 0 {
		return nil
	}
	out := make([]apitypes.DependantResp, 0, len(list))
	for _, d := range list {
		out = append(out, apitypes.DependantResp{Kind: d.Kind, Name: d.Name, How: d.How})
	}
	return out
}
