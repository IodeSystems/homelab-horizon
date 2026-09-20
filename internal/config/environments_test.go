package config

import "testing"

// TestValidateEnvironments covers the rung rules, and the deliberate non-rule that
// mirrors ValidateProjects: a service naming an environment is only checked when it also
// names a project. Tightening that would turn an additive change into a migration.
func TestValidateEnvironments(t *testing.T) {
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
			name: "a service naming an environment but no project is not checked",
			cfg:  Config{Services: []Service{{Name: "beta", Environment: "beta"}}},
		},
		{
			name: "a service in a project but no environment is fine",
			cfg: Config{
				Projects: []Project{{Name: "veliode"}},
				Services: []Service{{Name: "beta", Project: "veliode"}},
			},
		},
		{
			name: "declared environment, service in it",
			cfg: Config{
				Projects:     []Project{{Name: "veliode"}},
				Environments: []Environment{{Project: "veliode", Name: "beta", Posture: "staging"}},
				Services:     []Service{{Name: "beta", Project: "veliode", Environment: "beta"}},
			},
		},
		{
			name: "service naming an environment that does not exist",
			cfg: Config{
				Projects: []Project{{Name: "veliode"}},
				Services: []Service{{Name: "beta", Project: "veliode", Environment: "ghost"}},
			},
			wantErr: `service "beta" names environment "ghost" in project "veliode", which does not exist`,
		},
		{
			name: "an environment in another project does not satisfy a service",
			cfg: Config{
				Projects:     []Project{{Name: "veliode"}, {Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "prod", Posture: "prod"}},
				Services:     []Service{{Name: "app", Project: "veliode", Environment: "prod"}},
			},
			wantErr: `service "app" names environment "prod" in project "veliode", which does not exist`,
		},
		{
			name:    "environment with no name",
			cfg:     Config{Environments: []Environment{{Project: "veliode", Name: " ", Posture: "dev"}}},
			wantErr: "an environment has no name",
		},
		{
			name:    "environment with no project",
			cfg:     Config{Environments: []Environment{{Name: "prod", Posture: "prod"}}},
			wantErr: `environment "prod" has no project`,
		},
		{
			name: "environment naming a project that does not exist",
			cfg: Config{
				Environments: []Environment{{Project: "ghost", Name: "prod", Posture: "prod"}},
			},
			wantErr: `environment "prod" names project "ghost", which does not exist`,
		},
		{
			name: "duplicate environment within one project",
			cfg: Config{
				Projects: []Project{{Name: "redline"}},
				Environments: []Environment{
					{Project: "redline", Name: "prod", Posture: "prod"},
					{Project: "redline", Name: "prod", Posture: "staging"},
				},
			},
			wantErr: `environment "prod" is declared twice in project "redline"`,
		},
		{
			name: "the same environment name in two projects is fine — every project gets a prod",
			cfg: Config{
				Projects: []Project{{Name: "redline"}, {Name: "veliode"}},
				Environments: []Environment{
					{Project: "redline", Name: "prod", Posture: "prod"},
					{Project: "veliode", Name: "prod", Posture: "staging"},
				},
			},
		},
		{
			name: "unknown posture",
			cfg: Config{
				Projects:     []Project{{Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "loadtest", Posture: "qa"}},
			},
			wantErr: `has posture "qa", which is not one of dev, staging, prod`,
		},
		{
			name: "empty posture is not a posture",
			cfg: Config{
				Projects:     []Project{{Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "prod"}},
			},
			wantErr: `has posture "", which is not one of dev, staging, prod`,
		},
		{
			name: "a name at an honest posture — prod called prod, sitting at staging",
			cfg: Config{
				Projects:     []Project{{Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "prod", Posture: "staging"}},
			},
		},
		{
			name: "promotes from an environment that does not exist",
			cfg: Config{
				Projects:     []Project{{Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "prod", Posture: "prod", From: "ghost"}},
			},
			wantErr: `environment "prod" in project "redline" promotes from "ghost", which does not exist in that project`,
		},
		{
			name: "promotes from an environment in a different project",
			cfg: Config{
				Projects: []Project{{Name: "redline"}, {Name: "veliode"}},
				Environments: []Environment{
					{Project: "veliode", Name: "staging", Posture: "staging"},
					{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
				},
			},
			wantErr: `environment "prod" in project "redline" promotes from "staging", which does not exist in that project`,
		},
		{
			name: "promotion cycle",
			cfg: Config{
				Projects: []Project{{Name: "redline"}},
				Environments: []Environment{
					{Project: "redline", Name: "a", Posture: "staging", From: "b"},
					{Project: "redline", Name: "b", Posture: "staging", From: "a"},
				},
			},
			wantErr: "promotion cycle",
		},
		{
			name: "a self-promotion is a cycle",
			cfg: Config{
				Projects:     []Project{{Name: "redline"}},
				Environments: []Environment{{Project: "redline", Name: "a", Posture: "dev", From: "a"}},
			},
			wantErr: "promotion cycle",
		},
		{
			name: "the ladder is not a cycle",
			cfg: Config{
				Projects: []Project{{Name: "redline"}},
				Environments: []Environment{
					{Project: "redline", Name: "dev", Posture: "dev"},
					{Project: "redline", Name: "staging", Posture: "staging", From: "dev", Version: "1.2.3"},
					{Project: "redline", Name: "prod", Posture: "prod", From: "staging", Version: "1.2.1"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateEnvironments()
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

// TestPostureIsOrdered pins the ladder. String comparison would sort dev < prod <
// staging, which makes a promotion to prod look like a step down — the reason the order
// is a slice and not an assumption.
func TestPostureIsOrdered(t *testing.T) {
	if PostureRank("dev") >= PostureRank("staging") || PostureRank("staging") >= PostureRank("prod") {
		t.Fatalf("postures are not ordered dev < staging < prod: %d %d %d",
			PostureRank("dev"), PostureRank("staging"), PostureRank("prod"))
	}
	if PostureRank("qa") != -1 {
		t.Fatalf("an unknown posture must rank -1, got %d", PostureRank("qa"))
	}
	if PostureRank("") != -1 {
		t.Fatalf("an empty posture must rank -1, got %d", PostureRank(""))
	}
}

// TestSaveRefusesAnUndeclaredEnvironment pins that Save is the chokepoint for rungs too.
// Without it a service points at an environment nobody declared and the environment
// reads as empty rather than wrong.
func TestSaveRefusesAnUndeclaredEnvironment(t *testing.T) {
	cfg := &Config{
		Projects: []Project{{Name: "redline"}},
		Services: []Service{{Name: "app", Project: "redline", Environment: "ghost"}},
	}
	if err := Save(t.TempDir()+"/hz.json", cfg); err == nil {
		t.Fatal("Save accepted a service naming an environment that does not exist")
	}
}
