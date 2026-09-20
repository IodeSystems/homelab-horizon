package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func listProjects(t *testing.T, s *Server) []apitypes.ProjectResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIProjects(w, asAdmin(s, http.MethodGet, "/api/v1/projects", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.ProjectResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func projectByName(t *testing.T, list []apitypes.ProjectResp, name string) apitypes.ProjectResp {
	t.Helper()
	for _, p := range list {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no project %q in %+v", name, list)
	return apitypes.ProjectResp{}
}

// TestProjectsResolveTheFeedServerSide pins the wire contract the CLI depends
// on: the resolved feed travels with the name of the project that supplied it,
// so no client has to re-walk the tree to answer "where did this come from".
func TestProjectsResolveTheFeedServerSide(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Projects: []config.Project{
			{Name: "acme-co", Feed: &config.Feed{
				URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>",
			}},
			{Name: "intern", Parent: "acme-co"},
			{Name: "storefront", Parent: "acme-co", Feed: &config.Feed{
				URL: "https://<other-registry-host>/debian", Suite: "jammy", Component: "contrib",
			}},
			{Name: "orphan"},
		},
		Services: []config.Service{
			{Name: "git", Project: "intern", Environment: "prod"},
			{Name: "idp", Project: "intern", Environment: "prod"},
			{Name: "legacy"},
		},
	})

	list := listProjects(t, s)
	if len(list) != 4 {
		t.Fatalf("want 4 projects, got %d: %+v", len(list), list)
	}
	if list[0].Name != "acme-co" {
		t.Fatalf("projects should sort by name, got %q first", list[0].Name)
	}

	root := projectByName(t, list, "acme-co")
	if root.Feed == nil || root.ResolvedFeed == nil || root.FeedFrom != "acme-co" {
		t.Fatalf("root should declare its own feed: %+v", root)
	}

	// The inheriting child declares nothing and resolves to the root's feed.
	// Both halves matter: a client that showed Feed would show nothing here,
	// and one that showed ResolvedFeed without FeedFrom could not say where to
	// go and change it.
	child := projectByName(t, list, "intern")
	if child.Feed != nil {
		t.Fatalf("intern declares no feed, got %+v", child.Feed)
	}
	if child.ResolvedFeed == nil || child.ResolvedFeed.Suite != "noble" {
		t.Fatalf("intern should inherit the root feed, got %+v", child.ResolvedFeed)
	}
	if child.FeedFrom != "acme-co" {
		t.Fatalf("intern's feed came from %q, want acme-co", child.FeedFrom)
	}
	if len(child.Services) != 2 || child.Services[0] != "git" {
		t.Fatalf("intern's services = %+v, want [git idp]", child.Services)
	}

	over := projectByName(t, list, "storefront")
	if over.FeedFrom != "storefront" || over.ResolvedFeed.Suite != "jammy" {
		t.Fatalf("storefront should override whole, got %q / %+v", over.FeedFrom, over.ResolvedFeed)
	}
	if over.ResolvedFeed.KeyID != "" {
		t.Fatalf("an override must not pick up the ancestor's key: %+v", over.ResolvedFeed)
	}

	none := projectByName(t, list, "orphan")
	if none.ResolvedFeed != nil || none.FeedFrom != "" {
		t.Fatalf("a project with no feed anywhere must report none, got %q / %+v", none.FeedFrom, none.ResolvedFeed)
	}
}

// TestProjectsNeedsAdmin pins the same gate every other data route has. The
// feed is not a secret, but the project tree names every project hz knows.
func TestProjectsNeedsAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{Projects: []config.Project{{Name: "acme-co"}}})
	w := httptest.NewRecorder()
	s.handleAPIProjects(w, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request returned %d, want 401", w.Code)
	}
}

// TestProjectsWithNoProjects pins the empty case: an existing config declares
// none, and the route must answer with a list rather than null or an error.
func TestProjectsWithNoProjects(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: []config.Service{{Name: "grafana"}}})
	if got := listProjects(t, s); len(got) != 0 {
		t.Fatalf("want no projects, got %+v", got)
	}
}
