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

// postDeclare runs one write handler and decodes its body, failing on a non-2xx
// so a test cannot pass by asserting on an error page.
func postDeclare(t *testing.T, s *Server, h http.HandlerFunc, path string, req, out any) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h(w, asAdmin(s, http.MethodPost, path, string(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
	}
	if out != nil {
		if err := json.NewDecoder(w.Body).Decode(out); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
	}
}

// postDeclareErr runs one write handler expecting a refusal, and returns the
// message so the test can assert it names what is wrong.
func postDeclareErr(t *testing.T, s *Server, h http.HandlerFunc, path string, req any) string {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h(w, asAdmin(s, http.MethodPost, path, string(body)))
	if w.Code == http.StatusOK {
		t.Fatalf("%s should have been refused, got 200: %s", path, w.Body.String())
	}
	return w.Body.String()
}

// TestDeclareWalkthroughOverTheAPI is step 1 and 2 of the acceptance
// walkthrough driven entirely over the write surface, with no hand-edited JSON
// anywhere: declare the root, declare the child under it, declare the child's
// rungs, and then read the whole thing back off the READ endpoints.
//
// Every one of these handlers goes through updateConfig, which calls Save, so a
// green run is also proof that each intermediate state was saveable.
func TestDeclareWalkthroughOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Services: []config.Service{{Name: "app", Domains: []string{"app.<our-domain>"}}},
	})

	var root apitypes.ProjectResp
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "iodesystems"}, &root)
	if root.Name != "iodesystems" || root.Parent != "" {
		t.Fatalf("root came back %+v", root)
	}

	// The feed on the root, through the writer that already existed — the point
	// of the root at step 1 is that it carries the feed and holds nothing.
	postDeclare(t, s, s.handleAPIProjectFeed, "/api/v1/projects/feed",
		apitypes.FeedSetReq{
			Project: "iodesystems", URL: "https://<registry-host>/debian",
			Suite: "noble", Component: "main", KeyID: "<fingerprint>",
		}, nil)

	var child apitypes.ProjectResp
	postDeclare(t, s, s.handleAPIProjectAdd, "/api/v1/projects/add",
		apitypes.ProjectAddReq{Name: "redline", Parent: "iodesystems"}, &child)
	if child.Parent != "iodesystems" {
		t.Fatalf("child's parent came back %q", child.Parent)
	}
	// The write answers with the resolved feed, not an echo of the request: a
	// project declared under a root inherits it immediately.
	if child.Feed != nil {
		t.Fatalf("redline declares no feed of its own, got %+v", child.Feed)
	}
	if child.ResolvedFeed == nil || child.FeedFrom != "iodesystems" {
		t.Fatalf("redline should inherit the root feed, got %q / %+v", child.FeedFrom, child.ResolvedFeed)
	}

	var staging, prod apitypes.EnvironmentResp
	postDeclare(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add",
		apitypes.EnvironmentAddReq{Project: "redline", Name: "staging", Posture: "staging", Version: "1.2.3"}, &staging)
	postDeclare(t, s, s.handleAPIEnvironmentAdd, "/api/v1/environments/add",
		apitypes.EnvironmentAddReq{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Version: "1.2.1"}, &prod)
	if staging.Posture != "staging" || staging.Version != "1.2.3" {
		t.Fatalf("staging came back %+v", staging)
	}
	if prod.From != "staging" {
		t.Fatalf("prod's promotion edge came back %q", prod.From)
	}

	// Read it back off the endpoints the read side uses, so this is the whole
	// round trip and not a handler talking to itself.
	envs := listEnvironments(t, s)
	if len(envs) != 2 || envs[0].Name != "prod" || envs[1].Name != "staging" {
		t.Fatalf("environments came back %+v", envs)
	}
	list := listProjects(t, s)
	if len(list) != 2 {
		t.Fatalf("want 2 projects, got %+v", list)
	}
	if projectByName(t, list, "redline").FeedFrom != "iodesystems" {
		t.Fatal("the read endpoint disagrees with the write about the feed's provenance")
	}
}

func listEnvironments(t *testing.T, s *Server) []apitypes.EnvironmentResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIEnvironments(w, asAdmin(s, http.MethodGet, "/api/v1/environments", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.EnvironmentResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func declaredTree(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, &config.Config{
		Projects: []config.Project{
			{Name: "iodesystems", Feed: &config.Feed{
				URL: "https://<registry-host>/debian", Suite: "noble", Component: "main",
			}},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []config.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
		},
		Services: []config.Service{{Name: "app", Project: "redline", Environment: "staging"}},
	})
}

// TestProjectRmIsADryRunUntilConfirmed pins the per-command dry-run decision:
// `add` writes immediately, `rm` does not. A removal is the one irreversible
// command in this surface — the project takes its feed with it, and
// `project add` cannot put a feed back.
func TestProjectRmIsADryRunUntilConfirmed(t *testing.T) {
	s := declaredTree(t)

	var out apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIProjectRm, "/api/v1/projects/rm",
		apitypes.ProjectRmReq{Name: "iodesystems", Cascade: true}, &out)
	if out.OK {
		t.Fatal("an unconfirmed removal must not report a write")
	}
	if out.Feed == nil || out.Feed.Suite != "noble" {
		t.Fatalf("the dry run must name the feed that goes with the project, got %+v", out.Feed)
	}
	if len(listProjects(t, s)) != 2 {
		t.Fatal("an unconfirmed removal wrote anyway")
	}
	// It has to say what it would take, or there is nothing to confirm.
	names := map[string]bool{}
	for _, d := range out.Removes {
		names[d.Name] = true
	}
	for _, want := range []string{"iodesystems", "redline", "redline/staging", "redline/prod", "app"} {
		if !names[want] {
			t.Fatalf("the dry run did not name %q; it listed %+v", want, out.Removes)
		}
	}

	postDeclare(t, s, s.handleAPIProjectRm, "/api/v1/projects/rm",
		apitypes.ProjectRmReq{Name: "iodesystems", Cascade: true, Confirm: true}, &out)
	if !out.OK {
		t.Fatalf("a confirmed removal must write: %+v", out)
	}
	if len(listProjects(t, s)) != 0 {
		t.Fatalf("projects survived a confirmed cascade: %+v", listProjects(t, s))
	}
	if len(listEnvironments(t, s)) != 0 {
		t.Fatal("rungs survived a confirmed cascade")
	}
	svc := s.cfg().Services[0]
	if svc.Project != "" || svc.Environment != "" {
		t.Fatalf("app was left assigned to %q/%q", svc.Project, svc.Environment)
	}
}

// TestRemovalBlockedAnswersWithTheDependants is why the refusal is a 200 with a
// body rather than an error string: the list of what is in the way is the whole
// value of refusing, and it survives as a list only if it is not flattened into
// a message.
func TestRemovalBlockedAnswersWithTheDependants(t *testing.T) {
	s := declaredTree(t)

	var out apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIProjectRm, "/api/v1/projects/rm",
		apitypes.ProjectRmReq{Name: "redline", Confirm: true}, &out)
	if out.OK {
		t.Fatal("a blocked removal must not write, even with confirm")
	}
	if len(out.Blocked) != 3 {
		t.Fatalf("want 3 blockers (two rungs and a service), got %+v", out.Blocked)
	}
	kinds := map[string]string{}
	for _, d := range out.Blocked {
		kinds[d.Name] = d.Kind
		if d.How == "" {
			t.Fatalf("blocker %q has no explanation", d.Name)
		}
	}
	if kinds["redline/staging"] != "environment" || kinds["app"] != "service" {
		t.Fatalf("blockers came back %+v", out.Blocked)
	}
	if len(listProjects(t, s)) != 2 {
		t.Fatal("a blocked removal wrote anyway")
	}

	// The same for a rung: the service on it and the rung promoting from it.
	postDeclare(t, s, s.handleAPIEnvironmentRm, "/api/v1/environments/rm",
		apitypes.EnvironmentRmReq{Project: "redline", Name: "staging", Confirm: true}, &out)
	if out.OK || len(out.Blocked) != 2 {
		t.Fatalf("staging should be blocked by the service and the edge, got %+v", out)
	}
}

func TestEnvironmentSetPatchesOverTheWire(t *testing.T) {
	s := declaredTree(t)

	// A version bump naming only --version must not drop the promotion edge.
	var got apitypes.EnvironmentResp
	postDeclare(t, s, s.handleAPIEnvironmentSet, "/api/v1/environments/set",
		apitypes.EnvironmentSetReq{Project: "redline", Name: "prod", Version: strptr("2.0.0")}, &got)
	if got.Version != "2.0.0" {
		t.Fatalf("version came back %q", got.Version)
	}
	if got.From != "staging" || got.Posture != "prod" {
		t.Fatalf("a version bump changed something else: %+v", got)
	}

	// A non-nil empty string clears; absent leaves alone. Both halves are the
	// contract, and without the first an edge could never be removed.
	//
	// A FRESH target, deliberately: EnvironmentResp omits an empty `from`, so
	// decoding into the struct above would leave the previous "staging" in place
	// and the assertion would pass on a stale value rather than on the response.
	var cleared apitypes.EnvironmentResp
	postDeclare(t, s, s.handleAPIEnvironmentSet, "/api/v1/environments/set",
		apitypes.EnvironmentSetReq{Project: "redline", Name: "prod", From: strptr("")}, &cleared)
	if cleared.From != "" {
		t.Fatalf("an empty --from left the edge as %q", cleared.From)
	}
	if cleared.Version != "2.0.0" {
		t.Fatalf("clearing the edge lost the version: %q", cleared.Version)
	}
	// And in the config, not only in the answer.
	if s.cfg().Environments[1].From != "" {
		t.Fatalf("prod still promotes from %q in the stored config", s.cfg().Environments[1].From)
	}

	body := postDeclareErr(t, s, s.handleAPIEnvironmentSet, "/api/v1/environments/set",
		apitypes.EnvironmentSetReq{Project: "redline", Name: "prod", Posture: strptr("production")})
	for _, p := range config.Postures {
		if !strings.Contains(body, p) {
			t.Fatalf("an unranked posture must be refused listing %q; got %s", p, body)
		}
	}
	if s.cfg().Environments[1].Posture != "prod" {
		t.Fatal("a refused posture was stored anyway")
	}
}

func strptr(s string) *string { return &s }

// TestDeclareRoutesNeedAdmin pins the same gate every other write route has.
// A write surface reachable anonymously would let anybody restructure the tree
// the whole model rests on.
func TestDeclareRoutesNeedAdmin(t *testing.T) {
	s := declaredTree(t)
	for name, h := range map[string]http.HandlerFunc{
		"/api/v1/projects/add":     s.handleAPIProjectAdd,
		"/api/v1/projects/rm":      s.handleAPIProjectRm,
		"/api/v1/environments/add": s.handleAPIEnvironmentAdd,
		"/api/v1/environments/set": s.handleAPIEnvironmentSet,
		"/api/v1/environments/rm":  s.handleAPIEnvironmentRm,
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, name, strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous POST %s returned %d, want 401", name, w.Code)
		}
		w = httptest.NewRecorder()
		h(w, asAdmin(s, http.MethodGet, name, ""))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s returned %d, want 405", name, w.Code)
		}
	}
}

// TestDeclareRoutesAreRegistered catches the failure a handler test cannot: a
// handler that works perfectly and is reachable at no URL.
func TestDeclareRoutesAreRegistered(t *testing.T) {
	s := declaredTree(t)
	mux := s.setupRoutes()
	for _, path := range []string{
		"/api/v1/projects/add", "/api/v1/projects/rm",
		"/api/v1/environments/add", "/api/v1/environments/set", "/api/v1/environments/rm",
	} {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, path, nil))
		if pattern != path {
			t.Fatalf("POST %s routes to %q, not to its own handler", path, pattern)
		}
	}
}
