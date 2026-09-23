package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A config with one declared host, one nameless declaration, and records
// pointing at both @nas and @self through every kind the screen groups by.
func hostsViewFixture() *config.Config {
	return &config.Config{
		LocalInterface: "192.168.1.1",
		Hosts: []config.HostDecl{
			{Name: "nas", IP: "192.168.1.51", Labels: map[string]string{"rack": "a"}},
			{Name: "spare", IP: "192.168.1.60"},
			{IP: "192.168.1.99"}, // legal, nameless: nothing can reference it
		},
		Services: []config.Service{
			{
				Name:        "files",
				Proxy:       &config.ProxyConfig{Backend: "@nas:8080", Deploy: &config.DeployConfig{NextBackend: "@nas:8081"}},
				InternalDNS: &config.InternalDNS{IP: "@self"},
				Forwards:    []config.Forward{{Proto: "tcp", Port: 2222, Backend: "@nas:22"}},
			},
			{
				Name:        "admin",
				Proxy:       &config.ProxyConfig{Backend: "127.0.0.1:8080"}, // a literal: not a reference
				InternalDNS: &config.InternalDNS{IP: "@self"},
			},
		},
		LocalDNSRecords:  []config.LocalDNSRecord{{Name: "desktop", IP: "@nas"}},
		Exporters:        []config.Exporter{{Job: "node", Mode: "static", Targets: []string{"@nas:9100"}}},
		ScrapeExclusions: []string{"@nas"},
	}
}

func findHost(t *testing.T, resp apitypes.HostsViewResp, name string) apitypes.HostView {
	t.Helper()
	for _, h := range resp.Hosts {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("host %q not in view (%d hosts)", name, len(resp.Hosts))
	return apitypes.HostView{}
}

func TestHostsViewSelfLeadsAndIsNotEditable(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())

	if len(resp.Hosts) != 4 {
		t.Fatalf("want @self + 3 declarations, got %d", len(resp.Hosts))
	}
	self := resp.Hosts[0]
	if !self.Self || self.Name != config.HostRefSelfName {
		t.Fatalf("@self must be the first row, got %+v", self)
	}
	if self.Ref != config.SelfRef {
		t.Errorf("@self ref = %q, want %q", self.Ref, config.SelfRef)
	}
	if self.Editable {
		t.Error("@self must not be editable from this screen")
	}
	if !strings.Contains(self.NotEditableWhy, "local_interface") {
		t.Errorf("@self must say where it IS set, got %q", self.NotEditableWhy)
	}
	if !self.Addressable || self.IP != "192.168.1.1" {
		t.Errorf("@self should resolve to local_interface, got %q", self.IP)
	}
	if len(self.References) != 2 {
		t.Fatalf("both services' internal_dns.ip are @self, got %d", len(self.References))
	}
	for _, r := range self.References {
		if r.Resolved != "192.168.1.1" {
			t.Errorf("@self reference %s resolved to %q, want 192.168.1.1", r.Field, r.Resolved)
		}
	}
}

// The screen's whole point: the authored value and the resolved value are both
// carried, per record. Losing either half is the bug this asserts against.
func TestHostsViewCarriesAuthoredAndResolved(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())
	nas := findHost(t, resp, "nas")

	if !nas.Editable || nas.Ref != "@nas" {
		t.Fatalf("a named declaration is editable and referenceable, got %+v", nas)
	}
	want := map[string]string{
		"@nas:8080": "192.168.1.51:8080",
		"@nas:8081": "192.168.1.51:8081",
		"@nas:22":   "192.168.1.51:22",
		"@nas:9100": "192.168.1.51:9100",
		"@nas":      "192.168.1.51",
	}
	if len(nas.References) != 6 {
		t.Fatalf("want 6 records through @nas, got %d: %+v", len(nas.References), nas.References)
	}
	for _, r := range nas.References {
		if r.Value == "" {
			t.Errorf("%s lost its authored value", r.Field)
		}
		if r.Resolved != want[r.Value] {
			t.Errorf("%s: %q resolved to %q, want %q", r.Field, r.Value, r.Resolved, want[r.Value])
		}
		if r.ResolveError != "" {
			t.Errorf("%s resolved fine but carries an error: %q", r.Field, r.ResolveError)
		}
	}
}

// The kinds are the CLI's kinds, not a second taxonomy invented for a screen.
func TestHostsViewKindsAreTheConfigKinds(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())
	nas := findHost(t, resp, "nas")

	seen := map[string]bool{}
	for _, r := range nas.References {
		seen[r.Kind] = true
	}
	for _, kind := range []string{
		config.HostRefKindServiceBackend,
		config.HostRefKindDeployNext,
		config.HostRefKindForward,
		config.HostRefKindLocalDNS,
		config.HostRefKindExporterTarget,
		config.HostRefKindScrapeExclusion,
	} {
		if !seen[kind] {
			t.Errorf("kind %q missing from the view", kind)
		}
	}
	// Sorted by kind, so the client can group by kind without re-sorting.
	for i := 1; i < len(nas.References); i++ {
		if nas.References[i-1].Kind > nas.References[i].Kind {
			t.Fatalf("references are not kind-sorted: %q before %q",
				nas.References[i-1].Kind, nas.References[i].Kind)
		}
	}
}

// Nothing points at it is an ANSWER, and it is not the same answer as "moving
// this is safe" — the response says so rather than leaving the client to guess.
func TestHostsViewUnreferencedHostIsEmptyNotAbsent(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())
	spare := findHost(t, resp, "spare")

	if spare.References == nil {
		t.Fatal("an unreferenced host must carry an empty list, never nil")
	}
	if len(spare.References) != 0 {
		t.Fatalf("nothing references @spare, got %d", len(spare.References))
	}
	if !resp.LiteralsUnlisted {
		t.Error("the response must admit it cannot enumerate literal occurrences")
	}
}

func TestHostsViewNamelessDeclarationCannotBeReferencedOrEdited(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())
	anon := findHost(t, resp, "")

	if anon.Editable {
		t.Error("a nameless declaration cannot be repointed by name")
	}
	if anon.Ref != "" {
		t.Errorf("a nameless declaration has no reference spelling, got %q", anon.Ref)
	}
	if !strings.Contains(anon.NotEditableWhy, "name") {
		t.Errorf("it must say WHY, got %q", anon.NotEditableWhy)
	}
}

// @self before hz has detected its own address: hz can name every dependant and
// cannot resolve one. That is not "these records point at nothing".
func TestHostsViewSelfWithoutLocalInterfaceIsUnknownNotEmpty(t *testing.T) {
	cfg := hostsViewFixture()
	cfg.LocalInterface = ""
	resp := buildHostsView(cfg)
	self := resp.Hosts[0]

	if self.Addressable {
		t.Fatal("@self with no local_interface is not addressable")
	}
	if !strings.Contains(self.NotAddressableWhy, "local_interface") {
		t.Errorf("it must name what fills it in, got %q", self.NotAddressableWhy)
	}
	if len(self.References) != 2 {
		t.Fatalf("the dependants are still known, got %d", len(self.References))
	}
	for _, r := range self.References {
		if r.Resolved != "" {
			t.Errorf("%s claims to resolve to %q with no local_interface", r.Field, r.Resolved)
		}
		if r.ResolveError == "" {
			t.Errorf("%s is unresolvable and says nothing about why", r.Field)
		}
	}
}
