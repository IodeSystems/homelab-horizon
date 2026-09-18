package haproxy

import (
	"strings"
	"testing"
)

// "Internal, except this path." The case that drove it: a package repository a
// cloud box must reach, on a service whose web UI and git endpoints must stay
// on the LAN.

func TestInternalOnlyDeniesEverythingWithoutExemptions(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "index", DomainMatches: []string{"index.example.com"},
		Server: "127.0.0.1:3000", InternalOnly: true,
	}})
	if !strings.Contains(cfg, "deny deny_status 403 if host_index !local_access\n") {
		t.Fatalf("plain internal-only rule missing:\n%s", cfg)
	}
	if strings.Contains(cfg, "path_beg") {
		t.Error("no exemptions configured, but a path condition was emitted")
	}
}

func TestPublicPathCarvesOneHole(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "index", DomainMatches: []string{"index.example.com"},
		Server: "127.0.0.1:3000", InternalOnly: true,
		PublicPaths: []string{"/api/packages/iodesystems/debian/"},
	}})
	want := "deny deny_status 403 if host_index !local_access !{ path_beg /api/packages/iodesystems/debian/ }"
	if !strings.Contains(cfg, want) {
		t.Fatalf("want %q in:\n%s", want, cfg)
	}
}

// Several exemptions must AND as negated terms: denied unless local, or one of
// them matched. An OR here would deny everything the moment there were two.
func TestSeveralPublicPathsAllAppear(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "index", DomainMatches: []string{"index.example.com"},
		Server: "127.0.0.1:3000", InternalOnly: true,
		PublicPaths: []string{"/api/packages/iodesystems/debian/", "/hooks/"},
	}})
	line := ""
	for _, l := range strings.Split(cfg, "\n") {
		if strings.Contains(l, "deny deny_status 403 if host_index") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("no deny rule emitted")
	}
	for _, want := range []string{"!local_access", "!{ path_beg /api/packages/iodesystems/debian/ }", "!{ path_beg /hooks/ }"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in: %s", want, line)
		}
	}
}

// A public service has no deny rule at all, so an exemption would be noise.
// Validation rejects the combination; the generator simply never sees it.
func TestPublicServiceGetsNoRule(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "site", DomainMatches: []string{"site.example.com"},
		Server: "127.0.0.1:3000",
	}})
	if strings.Contains(cfg, "deny deny_status 403 if host_site") {
		t.Errorf("public service got an internal-only rule:\n%s", cfg)
	}
}

func TestEmptyPathsAreDropped(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "index", DomainMatches: []string{"index.example.com"},
		Server: "127.0.0.1:3000", InternalOnly: true,
		PublicPaths: []string{"", "  ", "/ok/"},
	}})
	if strings.Contains(cfg, "path_beg }") || strings.Contains(cfg, "path_beg  }") {
		t.Errorf("an empty path produced a broken condition:\n%s", cfg)
	}
	if !strings.Contains(cfg, "!{ path_beg /ok/ }") {
		t.Errorf("the real path was lost:\n%s", cfg)
	}
}
