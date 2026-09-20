package config

import "testing"

// TestValidateProjects covers the tree rules, and one rule that is a deliberate
// non-rule: a service may name no project. Every service is in that state today, so
// rejecting it would turn an additive change into a migration.
func TestValidateProjects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "empty config is fine",
			cfg:  Config{},
		},
		{
			name: "a service with no project is fine — this is every service today",
			cfg:  Config{Services: []Service{{Name: "grafana"}}},
		},
		{
			name: "assigned service, declared project",
			cfg: Config{
				Projects: []Project{{Name: "veliode"}},
				Services: []Service{{Name: "beta", Project: "veliode", Environment: "beta"}},
			},
		},
		{
			name: "service naming a project that does not exist",
			cfg: Config{
				Services: []Service{{Name: "beta", Project: "ghost"}},
			},
			wantErr: `service "beta" names project "ghost", which does not exist`,
		},
		{
			name:    "project with no name",
			cfg:     Config{Projects: []Project{{Name: " "}}},
			wantErr: "a project has no name",
		},
		{
			name:    "duplicate project",
			cfg:     Config{Projects: []Project{{Name: "a"}, {Name: "a"}}},
			wantErr: `project "a" is declared twice`,
		},
		{
			name:    "parent that does not exist",
			cfg:     Config{Projects: []Project{{Name: "child", Parent: "ghost"}}},
			wantErr: `names parent "ghost", which does not exist`,
		},
		{
			name: "parent cycle",
			cfg: Config{Projects: []Project{
				{Name: "a", Parent: "b"},
				{Name: "b", Parent: "a"},
			}},
			wantErr: "parent cycle",
		},
		{
			name: "deep chain is not a cycle",
			cfg: Config{Projects: []Project{
				{Name: "root"},
				{Name: "mid", Parent: "root"},
				{Name: "leaf", Parent: "mid"},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateProjects()
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

// TestSaveRefusesABrokenTree pins that Save is the chokepoint. Without it a bad tree
// reaches disk and surfaces later as a project that looks empty rather than wrong.
func TestSaveRefusesABrokenTree(t *testing.T) {
	cfg := &Config{Services: []Service{{Name: "x", Project: "ghost"}}}
	if err := Save(t.TempDir()+"/hz.json", cfg); err == nil {
		t.Fatal("Save accepted a service naming a project that does not exist")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
