package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyConfigJSON is a config as hz wrote them BEFORE projects, environments
// and feeds existed: services with no project, no environment, no tree, and no
// feed anywhere. This is the shape sitting on the live gateway right now.
//
// It is a literal rather than a fixture file so that a future change to the
// Config struct cannot quietly "fix" it — the whole point is that these bytes
// do not change when the code does.
//
// The first draft of this fixture used "target_host"/"target_port", which do not
// exist on Service and were therefore dropped on load as unknown JSON. Nothing
// failed, because nothing asserted on them — a fixture can lie about the shape
// it claims to represent and still pass. The backend lives on Proxy, and
// TestLegacyFixtureHasNoUnknownFields below now refuses to let that happen
// again.
const legacyConfigJSON = `{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "vpn_range": "10.216.34.0/24",
  "server_endpoint": "<gateway-host>:51820",
  "dnsmasq_enabled": true,
  "haproxy_enabled": true,
  "ssl_enabled": true,
  "local_dns_domain": "lan",
  "services": [
    {"name": "git",     "domains": ["git.<our-domain>"],     "proxy": {"backend": "127.0.0.1:3000"}},
    {"name": "idp",     "domains": ["idp.<our-domain>"],     "proxy": {"backend": "127.0.0.1:8080"}},
    {"name": "app",     "domains": ["app.<our-domain>"],     "proxy": {"backend": "10.10.2.11:6400"}},
    {"name": "staging", "domains": ["staging.<our-domain>"], "proxy": {"backend": "127.0.0.1:6400", "internal_only": true}}
  ]
}`

// TestLegacyConfigLoadsAndSaves is the deploy gate for the whole project /
// environment / feed tree: a config written before any of it existed must still
// load, and — the part that actually bites — must still SAVE.
//
// Save() gained three validators (ValidateProjects, ValidateEnvironments,
// ValidateFeeds) and it is the one chokepoint every writer goes through. If any
// of them refuses a config that has none of those records, then the first write
// after this deploys fails on the live gateway, and every subsequent UI action
// that persists anything fails with it.
func TestLegacyConfigLoadsAndSaves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(legacyConfigJSON), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a legacy config must still load: %v", err)
	}

	// Positive control on the loader itself: if this value is not what the file
	// said, Load fell back to defaults and every assertion below is vacuous.
	if cfg.VPNRange != "10.216.34.0/24" {
		t.Fatalf("Load did not read the file: VPNRange is %q", cfg.VPNRange)
	}
	if len(cfg.Services) != 4 {
		t.Fatalf("want 4 services from the file, got %d", len(cfg.Services))
	}
	// Second positive control, on the SERVICE shape rather than the top level.
	// The first draft of this fixture spelled the backend as a field that does
	// not exist, so every service loaded with a nil Proxy and nothing noticed.
	for _, s := range cfg.Services {
		if s.Proxy == nil || s.Proxy.Backend == "" {
			t.Fatalf("service %q loaded with no backend — the fixture is not the shape it claims", s.Name)
		}
	}

	// The new records are absent, not empty-but-present. Absent is the state the
	// live gateway is in, and the distinction is goal property 6.
	if len(cfg.Projects) != 0 {
		t.Errorf("a legacy config declares no projects, got %+v", cfg.Projects)
	}
	if len(cfg.Environments) != 0 {
		t.Errorf("a legacy config declares no environments, got %+v", cfg.Environments)
	}
	for _, s := range cfg.Services {
		if s.Project != "" || s.Environment != "" {
			t.Errorf("service %q came back assigned (%q/%q); nothing wrote those fields",
				s.Name, s.Project, s.Environment)
		}
	}

	// The one that actually breaks a box. Save runs all three validators.
	if err := Save(path, cfg); err != nil {
		t.Fatalf("a legacy config must still SAVE — this is the deploy gate: %v", err)
	}

	// And it round-trips: saving must not have invented records.
	again, err := Load(path)
	if err != nil {
		t.Fatalf("reload after save: %v", err)
	}
	if len(again.Projects) != 0 || len(again.Environments) != 0 {
		t.Errorf("Save invented records: projects=%+v environments=%+v",
			again.Projects, again.Environments)
	}
	if len(again.Services) != 4 {
		t.Errorf("Save lost services: want 4, got %d", len(again.Services))
	}
}

// TestLegacyConfigWithOneAssignedServiceIsRefused pins the deploy-gate rule
// from the other side, so the order of operations is enforced rather than
// merely documented: assigning a service to a rung nobody declared must fail
// LOUDLY at Save, not resolve later to an empty environment that looks like
// "nothing is in that project".
func TestLegacyConfigWithOneAssignedServiceIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(legacyConfigJSON), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Someone assigns a service before declaring the project or the rung.
	cfg.Services[0].Project = "intern"
	cfg.Services[0].Environment = "prod"

	if err := Save(path, cfg); err == nil {
		t.Fatal("Save accepted a service naming a project and an environment that do not exist")
	}

	// Declaring both makes it legal — declare, THEN assign.
	cfg.Projects = append(cfg.Projects, Project{Name: "intern"})
	cfg.Environments = append(cfg.Environments, Environment{
		Project: "intern", Name: "prod", Posture: "prod",
	})
	if err := Save(path, cfg); err != nil {
		t.Fatalf("declaring the project and the rung should make the assignment legal: %v", err)
	}
}

// TestLegacyFixtureHasNoUnknownFields refuses to let the fixture above drift
// away from the struct it claims to represent.
//
// encoding/json ignores fields it does not recognise, so a fixture can name
// something that does not exist and still load, still save, and still pass
// every assertion — it simply loads as a zero value. That is how
// "target_host"/"target_port" survived in here: no test asserted on them, so
// nothing failed. A fixture whose whole job is to be "what the live gateway
// looks like" is worth nothing if the code cannot see half of it.
func TestLegacyFixtureHasNoUnknownFields(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(legacyConfigJSON))
	dec.DisallowUnknownFields()
	var probe Config
	if err := dec.Decode(&probe); err != nil {
		t.Fatalf("the legacy fixture names a field Config does not have: %v", err)
	}
}
