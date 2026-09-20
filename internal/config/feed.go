package config

import (
	"errors"
	"fmt"
	"strings"
)

// The package feed is the first thing a project inherits.
//
// Until now `Parent` recorded the tree and conferred nothing — a deliberate
// choice, because inheritance semantics are the expensive half and nothing
// was being shared. The feed is what changes that, and it is allowed to under
// the rule architecture.md already set: **shape inherits, secrets do not**. A
// registry URL, a suite and a component are non-secret shape — a box that
// installs from them still needs its own sealed config and its own key. So one
// feed declared at the company root serves every project's machines, and no
// leaf has to unwrap anything belonging to an ancestor to use it.

// Feed is an apt-style package repository a project's machines install from.
//
// hz does not know what a .deb is (architecture.md, "Versions"): it carries
// these four strings to the agent and the agent writes a sources entry. That
// is the whole seam, and it is what keeps hz from mandating a packaging
// strategy.
type Feed struct {
	URL       string `json:"url"`              // registry base
	Suite     string `json:"suite"`            // e.g. "noble"
	Component string `json:"component"`        // e.g. "main"
	KeyID     string `json:"key_id,omitempty"` // signing key fingerprint
}

// Validate checks a declared feed names a repository that can actually be
// written into a sources entry. KeyID is deliberately not required here — see
// FeedWarnings.
func (f *Feed) Validate() error {
	if f == nil {
		return nil
	}
	if strings.TrimSpace(f.URL) == "" {
		return errors.New("feed requires a url")
	}
	if strings.TrimSpace(f.Suite) == "" {
		return errors.New("feed requires a suite")
	}
	if strings.TrimSpace(f.Component) == "" {
		return errors.New("feed requires a component")
	}
	return nil
}

// ValidateFeeds checks every declared feed is usable.
//
// Separate from ValidateProjects, and called beside it from Save, because they
// answer different questions and fail for different reasons. ValidateProjects
// is about the tree itself — names unique, parents real, no cycles, services
// pointing at projects that exist — and ResolveFeed's walk is only meaningful
// once that holds. ValidateFeeds is about a payload hanging off one node, and
// a project may carry more such payloads later. Folding it in would make a
// single function whose error message no longer tells you which kind of thing
// is wrong.
func (c *Config) ValidateFeeds() error {
	for _, p := range c.Projects {
		if p.Feed == nil {
			continue
		}
		if err := p.Feed.Validate(); err != nil {
			return fmt.Errorf("project %q: %w", p.Name, err)
		}
	}
	return nil
}

// FeedWarning is a declared feed that is legal and probably not what was
// meant.
//
// The warn path is PinnedIPWarnings' (internal/config/pinned_ips.go): a slice
// of findings returned from config, printed by the startup checker in
// cmd/homelab-horizon and never counted against a green run. An unsigned feed
// is exactly that shape of problem — it validates, it installs, and the
// failure it invites is silent.
type FeedWarning struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	Reason  string `json:"reason"`
}

// FeedUnsigned is the reason string for a feed declared without a signing key.
const FeedUnsigned = "no signing key"

// FeedWarnings reports declared feeds with no KeyID.
//
// Not an error: a feed may genuinely be unsigned on a lab box, or the
// fingerprint may not be known yet when the project is first declared, and
// refusing to save would make the common first step fail. It is reported
// because an unsigned repository is one an attacker on the path can rewrite,
// and nothing about the resulting install looks wrong.
func (c *Config) FeedWarnings() []FeedWarning {
	var out []FeedWarning
	for _, p := range c.Projects {
		if p.Feed == nil || strings.TrimSpace(p.Feed.KeyID) != "" {
			continue
		}
		out = append(out, FeedWarning{Project: p.Name, URL: p.Feed.URL, Reason: FeedUnsigned})
	}
	return out
}

// ResolveFeed returns the feed a project installs from, the project that
// supplied it, and an error only when the project does not exist.
//
// Nearest declaration wins, and it wins whole: the walk goes up through Parent
// and the first project carrying a non-nil Feed supplies all four fields. A
// child that declares a feed replaces its ancestor's entirely rather than
// inheriting three fields and overriding one. Field-level merge would make
// "where did this value come from" unanswerable, and answering it is the
// entire job of the second return value.
//
// A project with no feed anywhere up the chain resolves to (nil, "", nil).
// That is not an error: most projects will not have a feed for a while, and
// the root is the only one expected to declare one.
//
// The returned Feed is a copy. Callers render it and hand it to a projection;
// none of them should be able to edit the config by holding it.
func (c *Config) ResolveFeed(project string) (*Feed, string, error) {
	byName := make(map[string]Project, len(c.Projects))
	for _, p := range c.Projects {
		byName[p.Name] = p
	}
	cur, ok := byName[project]
	if !ok {
		return nil, "", fmt.Errorf("project %q does not exist", project)
	}

	// ValidateProjects rejects cycles, so a cycle should be impossible here.
	// Bound the walk anyway and fail loudly rather than spin: ResolveFeed is
	// reachable from a config that was written before that validation existed,
	// or edited on disk by hand, and a hang is the one failure mode nobody can
	// diagnose from the outside.
	for steps := 0; steps <= len(c.Projects); steps++ {
		if cur.Feed != nil {
			f := *cur.Feed
			return &f, cur.Name, nil
		}
		if cur.Parent == "" {
			return nil, "", nil
		}
		parent, ok := byName[cur.Parent]
		if !ok {
			return nil, "", fmt.Errorf("project %q names parent %q, which does not exist", cur.Name, cur.Parent)
		}
		cur = parent
	}
	return nil, "", fmt.Errorf("resolving the feed for project %q walked more than %d parents: the project tree has a cycle", project, len(c.Projects))
}
