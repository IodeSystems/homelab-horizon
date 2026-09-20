package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The import endpoints as a client meets them: a GET that proposes and changes
// nothing, and a POST that writes only the proposal the caller actually read.

// importable is a gateway with two projects spelled into its hostnames and one
// service nothing explains. Placeholders only — this repo is public.
func importable() []config.Service {
	return []config.Service{
		{Name: "git", Domains: []string{"git.intern.<our-co>.<tld>"}, Proxy: &config.ProxyConfig{Backend: "<gw>:3000"}},
		{Name: "idp", Domains: []string{"idp.intern.<our-co>.<tld>"}, Proxy: &config.ProxyConfig{Backend: "<gw>:8080"}},
		{Name: "web", Domains: []string{"web.shop.<our-co>.<tld>"}, Proxy: &config.ProxyConfig{Backend: "<gw>:6400"}},
		{Name: "web-staging", Domains: []string{"staging.shop.<our-co>.<tld>"}, Proxy: &config.ProxyConfig{Backend: "<gw>:6402"}},
		{Name: "grafana", Domains: []string{"grafana.<our-co>.<tld>"}, Proxy: &config.ProxyConfig{Backend: "<gw>:3001"}},
	}
}

func getImportPlan(t *testing.T, s *Server) apitypes.ImportPlanResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIImport(w, asAdmin(s, http.MethodGet, "/api/v1/import", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("GET returned %d: %s", w.Code, w.Body.String())
	}
	var plan apitypes.ImportPlanResp
	if err := json.NewDecoder(w.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestImportGETProposesAndWritesNothing: the dry run is the default all the way
// down. A GET that quietly persisted its proposal would make the CLI's "nothing
// was written" line a lie no flag could fix.
func TestImportGETProposesAndWritesNothing(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: importable()})

	plan := getImportPlan(t, s)
	if len(plan.Projects) != 3 {
		t.Fatalf("want root + intern + shop, got %+v", plan.Projects)
	}
	if plan.Fingerprint == "" {
		t.Fatal("a plan must carry a fingerprint; --execute has nothing to check against otherwise")
	}
	if plan.ExistingProjects != 0 {
		t.Errorf("existingProjects = %d on a config with none", plan.ExistingProjects)
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].Service != "grafana" {
		t.Errorf("grafana should be left alone, got %+v", plan.Unassigned)
	}

	if got := len(s.cfg().Projects); got != 0 {
		t.Fatalf("a GET wrote %d project(s) into the config", got)
	}
	for _, svc := range s.cfg().Services {
		if svc.Project != "" {
			t.Fatalf("a GET assigned %q to %q", svc.Name, svc.Project)
		}
	}
}

// TestImportPOSTWritesTheTreeAndTheAssignmentsTogether is the deploy-gate half:
// Save refuses a service naming a rung nobody declared, so both halves go in one
// write or the write fails.
func TestImportPOSTWritesTheTreeAndTheAssignmentsTogether(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: importable()})
	plan := getImportPlan(t, s)

	body, _ := json.Marshal(apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("POST returned %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.ImportApplyResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ProjectsAdded != 3 || resp.EnvironmentsAdded != 1 || resp.ServicesAssigned != 4 {
		t.Fatalf("applied %+v, want 3 projects, 1 environment, 4 services", resp)
	}

	// The config hz now serves, and the one on disk, both say so.
	cfg := s.cfg()
	if len(cfg.Projects) != 3 || len(cfg.Environments) != 1 {
		t.Fatalf("in-memory config: projects=%+v environments=%+v", cfg.Projects, cfg.Environments)
	}
	saved, err := config.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range saved.Services {
		switch svc.Name {
		case "web-staging":
			if svc.Project != "shop" || svc.Environment != "staging" {
				t.Errorf("web-staging persisted as %q/%q", svc.Project, svc.Environment)
			}
		case "grafana":
			if svc.Project != "" {
				t.Errorf("grafana was left unassigned by the plan and persisted in %q", svc.Project)
			}
		}
	}
}

// TestImportRefusesAStalePlan: the plan is read at one moment and written at
// another. Applying a plan the caller never saw is the failure a dry run exists
// to prevent, so a changed config is a refusal rather than a different import.
func TestImportRefusesAStalePlan(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: importable()})
	body, _ := json.Marshal(apitypes.ImportApplyReq{Fingerprint: "not-the-plan"})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("POST returned %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "re-run the dry run") {
		t.Errorf("the refusal should say what to do, got %s", w.Body.String())
	}
	if len(s.cfg().Projects) != 0 {
		t.Error("a refused import wrote something")
	}
}

// TestImportRefusesAnExistingTreeWithoutMerge: a second --execute must not
// silently reorganise what the first one built, and --merge must add to it
// rather than rewrite it.
func TestImportRefusesAnExistingTreeWithoutMerge(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: importable()})
	plan := getImportPlan(t, s)
	body, _ := json.Marshal(apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint})
	if w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body)); w.Code != http.StatusOK {
		t.Fatalf("first import: %d %s", w.Code, w.Body.String())
	}

	// Second run, same plan, no merge.
	plan = getImportPlan(t, s)
	if plan.ExistingProjects != 3 {
		t.Fatalf("the plan must tell the client a tree exists BEFORE it types --execute, got %d", plan.ExistingProjects)
	}
	body, _ = json.Marshal(apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST over an existing tree returned %d, want 400: %s", w.Code, w.Body.String())
	}

	// With merge it is a no-op, because everything the plan proposes is already
	// there. Additive, never a rewrite.
	body, _ = json.Marshal(apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint, Merge: true})
	w = postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("merge returned %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.ImportApplyResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ProjectsAdded != 0 || resp.ServicesAssigned != 0 {
		t.Errorf("re-importing an already-imported config added %+v", resp)
	}
	if len(s.cfg().Projects) != 3 {
		t.Errorf("merge changed the tree size: %+v", s.cfg().Projects)
	}
}

// TestImportNeedsAdmin pins the same gate every other config route has: the plan
// names every service and every domain hz knows.
func TestImportNeedsAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: importable()})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		s.handleAPIImport(w, httptest.NewRequest(method, "/api/v1/import", strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s returned %d, want 401", method, w.Code)
		}
	}
	if len(s.cfg().Projects) != 0 {
		t.Error("an unauthorised request wrote something")
	}
}

// TestFeedSetDeclaresAndTheChildInherits is the writer the feed never had, and
// the half that proves it is worth having: a feed declared on the root resolves
// for every descendant, which is step 1 of the acceptance walkthrough.
func TestFeedSetDeclaresAndTheChildInherits(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Projects: []config.Project{{Name: "acme"}, {Name: "shop", Parent: "acme"}},
	})
	body, _ := json.Marshal(apitypes.FeedSetReq{
		Project: "acme", URL: "<registry>", Suite: "noble", Component: "main", KeyID: "<fingerprint>",
	})
	w := postRemote(t, s, s.handleAPIProjectFeed, "/api/v1/projects/feed", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("POST returned %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.ProjectResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Feed == nil || resp.Feed.Suite != "noble" || resp.FeedFrom != "acme" {
		t.Fatalf("the response should read back what hz now holds, got %+v", resp)
	}

	saved, err := config.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, from, err := saved.ResolveFeed("shop")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Suite != "noble" || from != "acme" {
		t.Fatalf("shop resolved %+v from %q", resolved, from)
	}
}

// TestFeedSetRefusesWhatItCannotWrite: an unusable feed and an unknown project
// both fail by name, before anything is stored. hz carries these four strings to
// an agent that writes a sources entry; a feed missing one cannot become one.
func TestFeedSetRefusesWhatItCannotWrite(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "acme"}}})

	for _, tc := range []struct {
		name string
		req  apitypes.FeedSetReq
		want string
	}{
		{"unknown project", apitypes.FeedSetReq{Project: "nope", URL: "u", Suite: "s", Component: "c"}, "no project"},
		{"no suite", apitypes.FeedSetReq{Project: "acme", URL: "u", Component: "c"}, "suite"},
		{"no url", apitypes.FeedSetReq{Project: "acme", Suite: "s", Component: "c"}, "url"},
		{"no component", apitypes.FeedSetReq{Project: "acme", URL: "u", Suite: "s"}, "component"},
	} {
		body, _ := json.Marshal(tc.req)
		w := postRemote(t, s, s.handleAPIProjectFeed, "/api/v1/projects/feed", string(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400: %s", tc.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: error should name %q, got %s", tc.name, tc.want, w.Body.String())
		}
	}
	if s.cfg().Projects[0].Feed != nil {
		t.Errorf("a refused feed was written: %+v", s.cfg().Projects[0].Feed)
	}
}

// TestFeedSetNeedsAdmin: it writes the config.
func TestFeedSetNeedsAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "acme"}}})
	w := httptest.NewRecorder()
	s.handleAPIProjectFeed(w, httptest.NewRequest(http.MethodPost, "/api/v1/projects/feed",
		strings.NewReader(`{"project":"acme","url":"u","suite":"s","component":"c"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST returned %d, want 401", w.Code)
	}
	if s.cfg().Projects[0].Feed != nil {
		t.Error("an unauthorised POST wrote a feed")
	}
}
