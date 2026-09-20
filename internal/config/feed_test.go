package config

import "testing"

// The tree every resolution case below runs against:
//
//	acme-co            declares the feed
//	├── intern         declares nothing        → inherits from acme-co
//	│   └── intern-ci  declares nothing        → inherits from acme-co (grandparent)
//	├── storefront     declares its own        → overrides
//	└── (orphan)       no parent, no feed      → legal, resolves to nothing
func feedTree() Config {
	return Config{Projects: []Project{
		{Name: "acme-co", Feed: &Feed{
			URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>",
		}},
		{Name: "intern", Parent: "acme-co"},
		{Name: "intern-ci", Parent: "intern"},
		{Name: "storefront", Parent: "acme-co", Feed: &Feed{
			URL: "https://<other-registry-host>/debian", Suite: "jammy", Component: "contrib", KeyID: "<other-fingerprint>",
		}},
		{Name: "orphan"},
	}}
}

// TestResolveFeed pins the whole of the resolution contract: nearest wins, it
// wins whole (no field-level merge), the provenance is reported, and "no feed
// anywhere" is a legal answer rather than an error.
func TestResolveFeed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      Config
		project  string
		wantFeed *Feed
		wantFrom string
		wantErr  string
	}{
		{
			name:     "declared here",
			cfg:      feedTree(),
			project:  "acme-co",
			wantFeed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>"},
			wantFrom: "acme-co",
		},
		{
			name:     "inherited from parent",
			cfg:      feedTree(),
			project:  "intern",
			wantFeed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>"},
			wantFrom: "acme-co",
		},
		{
			name:     "inherited from grandparent",
			cfg:      feedTree(),
			project:  "intern-ci",
			wantFeed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>"},
			wantFrom: "acme-co",
		},
		{
			// The override replaces all four fields. If resolution ever merged
			// field-by-field this case would come back with acme-co's suite.
			name:     "child overrides parent, whole",
			cfg:      feedTree(),
			project:  "storefront",
			wantFeed: &Feed{URL: "https://<other-registry-host>/debian", Suite: "jammy", Component: "contrib", KeyID: "<other-fingerprint>"},
			wantFrom: "storefront",
		},
		{
			name:    "no feed anywhere up the chain is legal",
			cfg:     feedTree(),
			project: "orphan",
		},
		{
			name: "no feed anywhere, several levels up",
			cfg: Config{Projects: []Project{
				{Name: "root"},
				{Name: "mid", Parent: "root"},
				{Name: "leaf", Parent: "mid"},
			}},
			project: "leaf",
		},
		{
			name:    "unknown project",
			cfg:     feedTree(),
			project: "ghost",
			wantErr: `project "ghost" does not exist`,
		},
		{
			name:    "unknown project, empty config",
			cfg:     Config{},
			project: "anything",
			wantErr: `project "anything" does not exist`,
		},
		{
			// ValidateProjects rejects this, so it can only arrive from a
			// hand-edited file — which is exactly when a hang would be
			// undiagnosable. The bounded walk must name the cycle instead.
			name: "parent cycle is caught by the bounded walk, not a hang",
			cfg: Config{Projects: []Project{
				{Name: "a", Parent: "b"},
				{Name: "b", Parent: "a"},
			}},
			project: "a",
			wantErr: "has a cycle",
		},
		{
			name: "self-parent is a cycle too",
			cfg: Config{Projects: []Project{
				{Name: "loop", Parent: "loop"},
			}},
			project: "loop",
			wantErr: "has a cycle",
		},
		{
			// A dangling parent cannot be walked through. Reported rather than
			// treated as a root, because silently resolving to "no feed" would
			// hide a broken tree behind a legal-looking answer.
			name: "parent that does not exist",
			cfg: Config{Projects: []Project{
				{Name: "child", Parent: "ghost"},
			}},
			project: "child",
			wantErr: `names parent "ghost", which does not exist`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, from, err := tc.cfg.ResolveFeed(tc.project)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (feed %+v from %q)", tc.wantErr, got, from)
				}
				if !contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if tc.wantFeed == nil {
				if got != nil {
					t.Fatalf("want no feed, got %+v from %q", got, from)
				}
				if from != "" {
					t.Fatalf("no feed should report no source, got %q", from)
				}
				return
			}
			if got == nil {
				t.Fatalf("want feed %+v, got nil", tc.wantFeed)
			}
			if *got != *tc.wantFeed {
				t.Fatalf("feed = %+v, want %+v", *got, *tc.wantFeed)
			}
			if from != tc.wantFrom {
				t.Fatalf("feed came from %q, want %q", from, tc.wantFrom)
			}
		})
	}
}

// TestResolveFeedReturnsACopy pins that a caller holding the resolved feed
// cannot edit the config through it. Resolution is a read; every consumer
// (the CLI, the API, the projection) renders what it gets back.
func TestResolveFeedReturnsACopy(t *testing.T) {
	cfg := feedTree()
	got, _, err := cfg.ResolveFeed("intern")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got.Suite = "tampered"
	if cfg.Projects[0].Feed.Suite != "noble" {
		t.Fatalf("editing a resolved feed changed the config: suite is now %q", cfg.Projects[0].Feed.Suite)
	}
}

// TestValidateFeeds covers the three required fields and the one that is
// deliberately optional.
func TestValidateFeeds(t *testing.T) {
	full := Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>"}
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "no projects is fine",
			cfg:  Config{},
		},
		{
			name: "a project with no feed is fine — this is most projects",
			cfg:  Config{Projects: []Project{{Name: "acme-co"}}},
		},
		{
			name: "a complete feed",
			cfg:  Config{Projects: []Project{{Name: "acme-co", Feed: &full}}},
		},
		{
			name:    "feed with no url",
			cfg:     Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{Suite: "noble", Component: "main"}}}},
			wantErr: `project "acme-co": feed requires a url`,
		},
		{
			name:    "feed with no suite",
			cfg:     Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{URL: "https://<registry-host>/debian", Component: "main"}}}},
			wantErr: `project "acme-co": feed requires a suite`,
		},
		{
			name:    "feed with no component",
			cfg:     Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble"}}}},
			wantErr: `project "acme-co": feed requires a component`,
		},
		{
			name:    "a declared but empty feed is not the same as no feed",
			cfg:     Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{}}}},
			wantErr: `project "acme-co": feed requires a url`,
		},
		{
			// The warn path's case, and it must stay out of the error path.
			name: "feed with no key_id is legal",
			cfg: Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{
				URL: "https://<registry-host>/debian", Suite: "noble", Component: "main",
			}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateFeeds()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestFeedWarnings pins that an unsigned feed is reported and a signed one is
// silent. Same shape as PinnedIPWarnings: findings, not failures.
func TestFeedWarnings(t *testing.T) {
	cfg := Config{Projects: []Project{
		{Name: "signed", Feed: &Feed{URL: "https://<registry-host>/debian", Suite: "noble", Component: "main", KeyID: "<fingerprint>"}},
		{Name: "unsigned", Feed: &Feed{URL: "https://<lab-registry-host>/debian", Suite: "noble", Component: "main"}},
		{Name: "blank-key", Feed: &Feed{URL: "https://<other-registry-host>/debian", Suite: "noble", Component: "main", KeyID: "  "}},
		{Name: "no-feed"},
	}}
	got := cfg.FeedWarnings()
	if len(got) != 2 {
		t.Fatalf("want 2 warnings, got %d: %+v", len(got), got)
	}
	if got[0].Project != "unsigned" || got[0].Reason != FeedUnsigned {
		t.Fatalf("first warning = %+v", got[0])
	}
	if got[1].Project != "blank-key" {
		t.Fatalf("a whitespace key_id is no key at all; second warning = %+v", got[1])
	}
	if got[0].URL != "https://<lab-registry-host>/debian" {
		t.Fatalf("warning should name the feed's url, got %q", got[0].URL)
	}
}

// TestSaveRefusesAHalfDeclaredFeed pins that Save is the chokepoint for feeds
// as it already is for the tree. A feed with no suite reaches a machine as a
// sources entry that cannot fetch, and the machine is where that is hardest to
// see.
func TestSaveRefusesAHalfDeclaredFeed(t *testing.T) {
	cfg := &Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{URL: "https://<registry-host>/debian"}}}}
	if err := Save(t.TempDir()+"/hz.json", cfg); err == nil {
		t.Fatal("Save accepted a feed with no suite or component")
	}
}

// TestSaveAcceptsAnUnsignedFeed is the other half: the warn path must not have
// become an error path by accident.
func TestSaveAcceptsAnUnsignedFeed(t *testing.T) {
	cfg := &Config{Projects: []Project{{Name: "acme-co", Feed: &Feed{
		URL: "https://<registry-host>/debian", Suite: "noble", Component: "main",
	}}}}
	if err := Save(t.TempDir()+"/hz.json", cfg); err != nil {
		t.Fatalf("Save refused a legal unsigned feed: %v", err)
	}
}

// TestFeedRoundTripsThroughJSON pins the absent/empty distinction across the
// file, which is where it would be lost: a project with no feed must not come
// back carrying a zero one.
func TestFeedRoundTripsThroughJSON(t *testing.T) {
	src := `{"projects":[
	  {"name":"acme-co","feed":{"url":"https://<registry-host>/debian","suite":"noble","component":"main","key_id":"<fingerprint>"}},
	  {"name":"intern","parent":"acme-co"}
	]}`
	cfg, err := LoadFromJSON([]byte(src))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Projects[1].Feed != nil {
		t.Fatalf("a project declaring no feed loaded a %+v", cfg.Projects[1].Feed)
	}
	f, from, err := cfg.ResolveFeed("intern")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if f == nil || from != "acme-co" || f.Suite != "noble" {
		t.Fatalf("resolved %+v from %q", f, from)
	}
}
