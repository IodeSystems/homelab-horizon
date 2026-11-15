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
			{
				// Two records carrying @spare's ADDRESS as a plain string.
				// Nothing references @spare, so before occurrences existed
				// this host read as "nothing depends on it" — the reassuring
				// zero that is the whole reason the second list is here.
				Name:        "backups",
				Proxy:       &config.ProxyConfig{Backend: "192.168.1.60:5000"},
				InternalDNS: &config.InternalDNS{IP: "192.168.1.60"},
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

	// ...and zero references is NOT zero dependants. Two records carry
	// @spare's address as a plain string, plus its own declaration. Before
	// this list existed the screen could only print a caveat here.
	if !spare.OccurrencesKnown {
		t.Fatal("the address is known, so the scan must have run")
	}
	if len(spare.Occurrences) != 3 {
		t.Fatalf("want 2 literal records + the declaration itself, got %d: %+v", len(spare.Occurrences), spare.Occurrences)
	}
	adoptable, refused := 0, 0
	for _, o := range spare.Occurrences {
		switch {
		case o.Ref != "" && o.WhyNotAdoptable == "":
			adoptable++
			if !strings.HasPrefix(o.Ref, "@spare") {
				t.Errorf("%s would be written %q, want an @spare reference", o.Field, o.Ref)
			}
		case o.Ref == "" && o.WhyNotAdoptable != "":
			refused++
		default:
			t.Errorf("%s is neither adoptable nor refused with a reason: %+v", o.Field, o)
		}
	}
	if adoptable != 2 || refused != 1 {
		t.Errorf("want 2 adoptable + 1 refused (the declaration's own ip), got %d/%d", adoptable, refused)
	}
	if spare.AdoptCommand != "hz host adopt spare" {
		t.Errorf("the screen must name the command, got %q", spare.AdoptCommand)
	}

	// The two lists never merge: not one occurrence appears among the
	// references, and the reference list stayed empty.
	for _, o := range spare.Occurrences {
		for _, r := range spare.References {
			if o.Kind == r.Kind && o.Owner == r.Owner && o.Field == r.Field {
				t.Errorf("record %s/%s/%s is in both lists", o.Kind, o.Owner, o.Field)
			}
		}
	}
}

// A host nothing carries the address of reports an EMPTY occurrence list, not a
// missing one — and that empty list is a real answer, unlike an empty reference
// list on a config nobody has adopted.
func TestHostsViewNoOccurrencesIsAnAnswer(t *testing.T) {
	resp := buildHostsView(hostsViewFixture())
	nas := findHost(t, resp, "nas")

	if !nas.OccurrencesKnown {
		t.Fatal("@nas has an address, so the scan ran")
	}
	if nas.Occurrences == nil {
		t.Fatal("an empty occurrence list must be empty, never nil")
	}
	// Only its own declaration carries 192.168.1.51 literally; everything else
	// that points at it is written @nas.
	if len(nas.Occurrences) != 1 || nas.Occurrences[0].Kind != config.HostAddrKindHostDecl {
		t.Fatalf("want only the declaration itself, got %+v", nas.Occurrences)
	}
	if nas.AdoptCommand != "" {
		t.Errorf("nothing is adoptable, so no command should be offered, got %q", nas.AdoptCommand)
	}
}

// @self before hz knows its own address: the scan CANNOT run, which is a
// different state from running and finding nothing. Rendering the two the same
// way is the founding bug of this screen with a new field on it.
func TestHostsViewOccurrencesUnscannedIsNotEmpty(t *testing.T) {
	cfg := hostsViewFixture()
	cfg.LocalInterface = ""
	resp := buildHostsView(cfg)
	self := resp.Hosts[0]

	if self.OccurrencesKnown {
		t.Fatal("with no local_interface there is no address to scan for")
	}
	if self.Occurrences == nil || len(self.Occurrences) != 0 {
		t.Fatalf("an unscanned host carries an empty list, got %+v", self.Occurrences)
	}
	if self.OccurrencesUnknownWhy == "" {
		t.Error("hz did not say why it could not look")
	}
	if self.AdoptCommand != "" {
		t.Errorf("nothing can be adopted without an address, got %q", self.AdoptCommand)
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
