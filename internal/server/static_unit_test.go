package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func staticCfg(roots ...config.Service) *config.Config {
	return &config.Config{StaticServePort: 8091, Services: roots}
}

func staticSvc(name, domain, root string, spa bool) config.Service {
	return config.Service{
		Name:    name,
		Domains: []string{domain},
		Proxy:   &config.ProxyConfig{StaticRoot: root, SPA: spa},
	}
}

func declaredPaths() staticPaths {
	return staticPaths{SiteMap: "/etc/homelab-horizon/config.json.sites", Unit: "/etc/systemd/system/hz-static.service"}
}

// unitDirectives returns the unit's real lines — section headers and
// directives — with comments and blank lines dropped.
//
// It parses rather than greps because this unit EXPLAINS ITSELF in comments:
// a naive strings.Contains for "[Install]" or "ReadWritePaths" matches the
// paragraph saying why neither is there, so the grep version of these tests
// failed on a correct unit and would have passed on one that carried the
// directive plus a comment denying it.
func unitDirectives(contents string) []string {
	var out []string
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// hasDirective reports whether the unit really sets key.
func hasDirective(contents, key string) bool {
	for _, line := range unitDirectives(contents) {
		if strings.HasPrefix(line, key) {
			return true
		}
	}
	return false
}

// fileNamed returns the declared file at path, or fails.
func fileNamed(t *testing.T, sec *agent.FilesSection, path string) agent.File {
	t.Helper()
	if sec == nil {
		t.Fatalf("no files section at all; %s is not declared", path)
	}
	for _, f := range sec.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no declared file at %s (have %d)", path, len(sec.Files))
	return agent.File{}
}

// THE DECLARATION. hz names the unit, the map it reads, and nothing else.
func TestHZDeclaresTheStaticUnitAndTheMapItReads(t *testing.T) {
	cfg := staticCfg(
		staticSvc("shop", "shop.example.com", "/srv/shop", true),
		staticSvc("docs", "docs.example.com", "/srv/docs", false),
	)
	p := declaredPaths()
	sec := staticFilesSection(cfg, "/usr/local/bin/homelab-horizon", p)

	unit := fileNamed(t, sec, p.Unit)
	mapFile := fileNamed(t, sec, p.SiteMap)
	if len(sec.Files) != 2 {
		t.Fatalf("declared %d files, want exactly the unit and the map", len(sec.Files))
	}

	// ONE DERIVER, TWO TRANSPORTS. The declared map must be the map the
	// supervisor pushes down its pipe, or the unit and the fork would serve
	// different sites from the same config.
	var got map[string]staticSite
	if err := json.Unmarshal([]byte(mapFile.Contents), &got); err != nil {
		t.Fatalf("the declared map is not JSON: %v", err)
	}
	want := deriveStaticSites(cfg)
	if len(got) != len(want) {
		t.Fatalf("declared %d sites, deriveStaticSites says %d", len(got), len(want))
	}
	for host, site := range want {
		if got[host] != site {
			t.Fatalf("host %s declared as %+v, derived as %+v", host, got[host], site)
		}
	}

	// The unit has to name the binary, the address HAProxy routes to, and the
	// map. A unit missing any of the three starts a server serving nothing.
	for _, want := range []string{
		"ExecStart=/usr/local/bin/homelab-horizon",
		"Environment=" + StaticServerEnvAddr + "=" + cfg.StaticServeAddr(),
		"Environment=" + StaticServerEnvSites + "=" + p.SiteMap,
	} {
		if !hasDirective(unit.Contents, want) {
			t.Fatalf("the declared unit does not carry %q:\n%s", want, unit.Contents)
		}
	}
	if mapFile.Secret || unit.Secret {
		t.Error("marked secret: document roots and hostnames are already on hz's own screens, and a secret file is redacted out of the diff a human reviews")
	}
	if mapFile.Mode != 0o644 {
		t.Errorf("map mode %o: the unit's unprivileged user has to read it", mapFile.Mode)
	}
}

// THE ROUND TRIP. What hz declares is what the server accepts — checked by
// handing the declared bytes to the loader the unit actually calls, rather than
// by two structs that agree with each other.
func TestTheDeclaredMapIsOneTheServerCanLoad(t *testing.T) {
	cfg := staticCfg(staticSvc("shop", "shop.example.com", "/srv/shop", true))
	sec := staticFilesSection(cfg, "/usr/local/bin/homelab-horizon", declaredPaths())
	declared := fileNamed(t, sec, declaredPaths().SiteMap)

	path := filepath.Join(t.TempDir(), "sites.json")
	if err := os.WriteFile(path, []byte(declared.Contents), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, err := loadSiteMap(path)
	if err != nil {
		t.Fatalf("the server refuses the map hz declares: %v", err)
	}
	if got := sites["shop.example.com"]; got.Root != "/srv/shop" || !got.SPA {
		t.Fatalf("round trip lost the site: %+v", got)
	}
}

// EMPTY IS NOT UNKNOWN, on the producing side. A machine with no static
// services gets a section with an empty map, not a nil section: nil is what a
// machine hz cannot see gets, and the two must not look the same.
func TestNoStaticSitesDeclaresAnEmptyMapRatherThanNoSection(t *testing.T) {
	sec := staticFilesSection(staticCfg(), "/usr/local/bin/homelab-horizon", declaredPaths())
	if sec == nil {
		t.Fatal("no section at all; that is what hz says about a machine it cannot see, not about one with no static sites")
	}
	declared := fileNamed(t, sec, declaredPaths().SiteMap)

	// And the empty map must be the one the server SERVES rather than the one
	// it refuses — the distinction loadSiteMap draws.
	path := filepath.Join(t.TempDir(), "sites.json")
	if err := os.WriteFile(path, []byte(declared.Contents), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, err := loadSiteMap(path)
	if err != nil {
		t.Fatalf("an empty declaration reads as unreadable: %v", err)
	}
	if len(sites) != 0 {
		t.Fatalf("declared %d sites for a config with none", len(sites))
	}
}

// THE BRAKE. Until the supervisor is deleted, hz is still forking a child onto
// the same loopback port, so an armed agent must PLACE this unit and start
// nothing. Two things hold that: the unit has no [Install] section, and the
// payload's Unit entry carries no action.
//
// Both come off in the commit that deletes the supervisor. This test is what
// fails if one of them moves alone.
func TestTheDeclaredStaticUnitIsInert(t *testing.T) {
	cfg := staticCfg(staticSvc("shop", "shop.example.com", "/srv/shop", true))
	sec := staticFilesSection(cfg, "/usr/local/bin/homelab-horizon", declaredPaths())
	unit := fileNamed(t, sec, declaredPaths().Unit)

	for _, line := range unitDirectives(unit.Contents) {
		if line == "[Install]" || strings.HasPrefix(line, "WantedBy=") || strings.HasPrefix(line, "RequiredBy=") {
			t.Errorf("the declared unit carries %q: `systemctl enable hz-static` would work while hz is still forking its own child on the same port", line)
		}
	}
	if len(sec.Units) != 1 || sec.Units[0].Name != staticUnitName {
		t.Fatalf("units %+v, want exactly %s named", sec.Units, staticUnitName)
	}
	if sec.Units[0].Action != "" {
		t.Errorf("unit action %q: the agent would START this unit against hz's own forked child. Delete the supervisor in the same commit that sets an action.", sec.Units[0].Action)
	}
}

// THE PRIVILEGE BOUNDARY THE UNIT IS SUPPOSED TO REPLACE THE FORK WITH.
//
// Today's separation is one thing: the forked child runs as uid 65534. If the
// unit does not at least keep that, deleting the supervisor is a privilege
// INCREASE wearing a cleanup's clothes. These are the lines that make it not.
func TestTheDeclaredStaticUnitDropsPrivilegeAndCannotWrite(t *testing.T) {
	cfg := staticCfg(staticSvc("shop", "shop.example.com", "/srv/shop", true))
	unit := fileNamed(t, staticFilesSection(cfg, "/usr/local/bin/homelab-horizon", declaredPaths()), declaredPaths().Unit)

	for _, line := range []string{
		"User=nobody",            // the uid drop the fork does today
		"NoNewPrivileges=true",   // and it cannot be undone
		"CapabilityBoundingSet=", // no capability at all, empty on purpose
		"ProtectSystem=strict",   // read-only filesystem: it serves, it does not write
		"ProtectHome=read-only",
		"RestrictAddressFamilies=AF_INET AF_INET6",
	} {
		if !hasDirective(unit.Contents, line) {
			t.Errorf("the declared unit is missing %q — the unit boundary has to carry at least what the fork does", line)
		}
	}
	// A ReadWritePaths here would hand the file server write access to the
	// site it serves, which is the thing sitedeploy's chown does today and the
	// thing this boundary is meant to take away.
	if hasDirective(unit.Contents, "ReadWritePaths") {
		t.Error("the declared unit grants a writable path: a static file server writes nothing")
	}
	if hasDirective(unit.Contents, "User=root") {
		t.Error("the declared unit runs as root: that is strictly more privilege than the fork it replaces")
	}
}

// The payload hz serves for the box it is on carries the declaration; the
// payload for a machine hz cannot see does not, and says why. Same rule as the
// other five sections, checked here because this one is new.
func TestTheStaticDeclarationIsLocalOnlyAndSaysSoForEveryoneElse(t *testing.T) {
	s := projectionServer(t)

	local := s.buildAgentDesired()
	if local.Files == nil {
		t.Fatal("the local payload carries no static declaration")
	}
	if local.Model != nil && local.Model.Unresolvable(agentSectionFiles) {
		t.Error("the local box reports its own files section unresolvable while hz produced one")
	}

	remote := s.desiredFor("app-1")
	if remote.Files != nil {
		t.Fatal("hz declared a static unit for a machine whose service list it cannot see")
	}
	if remote.Model == nil || !remote.Model.Unresolvable(agentSectionFiles) {
		t.Fatal("the remote payload's files section is absent with nothing saying why — indistinguishable from hz managing no files there")
	}
}
