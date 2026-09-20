package server

import (
	"net/http"
	"sort"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// handleAPIProjects serves the project tree with each project's feed already
// resolved.
//
// Resolution happens here, not in the client. `hz feed show` and `hz project
// show` both need the same answer to "which project supplied this feed", and
// two walkers of the same tree is two chances to disagree about it. The server
// holds the config; it does the walk once and sends the provenance along with
// the value.
func (s *Server) handleAPIProjects(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	cfg := s.cfg()

	byProject := map[string][]string{}
	for _, svc := range cfg.Services {
		if svc.Project == "" {
			continue
		}
		byProject[svc.Project] = append(byProject[svc.Project], svc.Name)
	}

	out := make([]apitypes.ProjectResp, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		pr := apitypes.ProjectResp{
			Name:     p.Name,
			Parent:   p.Parent,
			Feed:     feedResp(p.Feed),
			Services: byProject[p.Name],
		}
		// A tree bad enough to fail resolution is reported by Save and by the
		// startup checker. Here it means one project cannot answer the feed
		// question; the rest still can, so the row goes out without one rather
		// than failing the whole listing.
		if resolved, from, err := cfg.ResolveFeed(p.Name); err == nil {
			pr.ResolvedFeed = feedResp(resolved)
			pr.FeedFrom = from
		}
		sort.Strings(pr.Services)
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	writeJSON(w, out)
}

// feedResp converts a config feed for the wire, preserving nil: absent and
// empty are different answers, and flattening them here would undo the
// distinction the pointer exists for.
func feedResp(f *config.Feed) *apitypes.FeedResp {
	if f == nil {
		return nil
	}
	return &apitypes.FeedResp{URL: f.URL, Suite: f.Suite, Component: f.Component, KeyID: f.KeyID}
}
