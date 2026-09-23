package config

import (
	"strings"
	"testing"
)

func refConfig() *Config {
	return &Config{
		LastLanCIDR:    "192.168.1.0/24",
		LocalInterface: "192.168.1.1",
		Hosts: []HostDecl{
			{Name: "nas", IP: "192.168.1.160"},
			{Name: "db", IP: "192.168.1.60"},
		},
		Zones: []Zone{{Name: "example.net", ZoneID: "Z1"}},
	}
}

// A bare hostname keeps meaning a hostname. This is the whole reason the sigil
// exists: "nas:8080" has been legal in a backend for as long as backends have
// existed, and it means the DNS name. Resolving it against the host list would
// change what an untouched config means the day someone declares a host with
// that name — two states rendering identically, which is the bug this repo was
// built around.
func TestABareNameIsNotAReference(t *testing.T) {
	c := refConfig()
	for _, v := range []string{"nas:8080", "nas", "192.168.1.160:8080", "", "localhost:1", "::1"} {
		if IsHostRef(v) {
			t.Errorf("IsHostRef(%q) = true; only the @ sigil makes a reference", v)
		}
		got, err := c.ResolveHostRef(v)
		if err != nil {
			t.Errorf("ResolveHostRef(%q): %v", v, err)
		}
		if got != v {
			t.Errorf("ResolveHostRef(%q) = %q; a non-reference must pass through unchanged", v, got)
		}
	}
}

func TestResolveHostRef(t *testing.T) {
	c := refConfig()
	for _, tc := range []struct{ in, want string }{
		{"@nas", "192.168.1.160"},
		{"@nas:8080", "192.168.1.160:8080"},
		{"@db:9187", "192.168.1.60:9187"},
	} {
		got, err := c.ResolveHostRef(tc.in)
		if err != nil {
			t.Fatalf("ResolveHostRef(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ResolveHostRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An unresolvable reference is an error that NAMES THE FIX, never a literal
// hostname passed through and never a silently dropped record.
func TestAnUnresolvableReferenceNamesTheFix(t *testing.T) {
	c := refConfig()
	_, err := c.ResolveHostRef("@printer:9100")
	if err == nil {
		t.Fatal("expected an error for a reference naming no declared host")
	}
	for _, want := range []string{"@printer:9100", "printer", "hz host add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Each record kind refuses an unresolvable reference in its OWN validator,
// with the field named, so the message points at the record being edited.
func TestEveryRecordValidatorRefusesAnUnresolvableReference(t *testing.T) {
	cases := []struct {
		name  string
		field string
		svc   Service
	}{
		{"proxy backend", "proxy.backend", Service{
			Name: "s", Domains: []string{"s.example.net"},
			Proxy: &ProxyConfig{Backend: "@printer:8080"},
		}},
		{"deploy next backend", "proxy.deploy.next_backend", Service{
			Name: "s", Domains: []string{"s.example.net"},
			Proxy: &ProxyConfig{Backend: "192.168.1.60:8080", Deploy: &DeployConfig{NextBackend: "@printer:8081"}},
		}},
		{"internal dns", "internal_dns.ip", Service{
			Name: "s", Domains: []string{"s.example.net"},
			InternalDNS: &InternalDNS{IP: "@printer"},
		}},
		{"forward backend", "forwards[0].backend", Service{
			Name: "s", Domains: []string{"s.example.net"},
			Forwards: []Forward{{Proto: "udp", Port: 4433, Backend: "@printer:4433"}},
		}},
	}
	c := refConfig()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateService(&tc.svc)
			if err == nil {
				t.Fatal("accepted an unresolvable reference")
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("want a *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != tc.field {
				t.Errorf("field = %q, want %q", ve.Field, tc.field)
			}
			if !strings.Contains(ve.Message, "hz host add") {
				t.Errorf("message does not name the fix: %q", ve.Message)
			}
		})
	}
}

// The same records accept a reference that DOES resolve, and the resolved
// value is what the downstream address checks see — a forward's backend still
// has to land inside the LAN, it is just the declared address that has to.
func TestRecordsAcceptAResolvableReference(t *testing.T) {
	c := refConfig()
	svc := Service{
		Name: "s", Domains: []string{"s.example.net"},
		InternalDNS: &InternalDNS{IP: "@nas"},
		Proxy:       &ProxyConfig{Backend: "@nas:8080", Deploy: &DeployConfig{NextBackend: "@nas:8081"}},
		Forwards:    []Forward{{Proto: "udp", Port: 4433, Backend: "@nas:4433"}},
	}
	if err := c.ValidateService(&svc); err != nil {
		t.Fatalf("ValidateService: %v", err)
	}

	// Resolution happens before the LAN check, not instead of it.
	off := refConfig()
	off.Hosts = []HostDecl{{Name: "cloud", IP: "10.9.9.9"}}
	bad := Service{
		Name: "s", Domains: []string{"s.example.net"},
		Forwards: []Forward{{Proto: "udp", Port: 4433, Backend: "@cloud:4433"}},
	}
	err := off.ValidateService(&bad)
	if err == nil || !strings.Contains(err.Error(), "outside the gateway's LAN") {
		t.Fatalf("a reference resolving outside the LAN must still be refused, got: %v", err)
	}
}

// The standby slot had no validator at all before host references; it now gets
// the same shape check slot A has.
func TestDeployNextBackendShapeIsValidated(t *testing.T) {
	c := refConfig()
	svc := Service{
		Name: "s", Domains: []string{"s.example.net"},
		Proxy: &ProxyConfig{Backend: "192.168.1.60:8080", Deploy: &DeployConfig{NextBackend: "not-an-address"}},
	}
	err := c.ValidateService(&svc)
	if err == nil {
		t.Fatal("accepted a next_backend that is not host:port")
	}
	if ve, ok := err.(*ValidationError); !ok || ve.Field != "proxy.deploy.next_backend" {
		t.Fatalf("want a proxy.deploy.next_backend error, got %v", err)
	}
}

// The declaration is the bottom of the chain. "@a" -> "@b" -> an address is a
// second thing to get wrong for nothing a second HostDecl does not give.
func TestAHostDeclarationCannotItselfBeAReference(t *testing.T) {
	c := refConfig()
	c.Hosts = append(c.Hosts, HostDecl{Name: "mirror", IP: "@nas"})
	err := c.ValidateHosts()
	if err == nil {
		t.Fatal("accepted a reference as a declared host's ip")
	}
	if !strings.Contains(err.Error(), "the address itself") {
		t.Errorf("error does not explain why: %v", err)
	}
}

func TestTwoHostsCannotShareAName(t *testing.T) {
	c := refConfig()
	c.Hosts = append(c.Hosts, HostDecl{Name: "nas", IP: "192.168.1.200"})
	if err := c.ValidateHosts(); err == nil {
		t.Fatal("accepted two declarations of the same name; @nas would have two answers")
	}
}

// Removing or renaming a host that is still referenced is refused, and the
// refusal NAMES every dependant — the shape `hz project rm` uses, because a
// refusal that does not say what depends on it is a dead end.
func TestRemovingAReferencedHostIsRefusedAndNamesEveryDependant(t *testing.T) {
	c := refConfig()
	c.Services = []Service{{
		Name: "app", Domains: []string{"app.example.net"},
		Proxy:       &ProxyConfig{Backend: "@nas:8080", Deploy: &DeployConfig{NextBackend: "@nas:8081"}},
		InternalDNS: &InternalDNS{IP: "@nas"},
		Forwards:    []Forward{{Proto: "udp", Port: 4433, Backend: "@nas:4433"}},
	}}
	c.LocalDNSRecords = []LocalDNSRecord{{Name: "nas", IP: "@nas"}}
	c.Exporters = []Exporter{{Job: "node", Mode: "port", Port: 9100, Hosts: []string{"@nas"}}}
	c.ScrapeExclusions = []string{"@db"}

	err := c.ValidateHostRemoval("nas")
	if err == nil {
		t.Fatal("removing a referenced host was allowed")
	}
	msg := err.Error()
	for _, want := range []string{
		"proxy.backend",
		"proxy.deploy.next_backend",
		"forwards[0].backend",
		"internal_dns.ip",
		"local DNS record",
		"exporter",
		"hz host set",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}

	// An unreferenced host removes cleanly.
	if err := c.ValidateHostRemoval("unused"); err != nil {
		t.Errorf("removing an unreferenced host was refused: %v", err)
	}

	// A RENAME arrives as a whole-list replace with the old name gone, and is
	// refused for the same reason under the same message.
	renamed := []HostDecl{{Name: "storage", IP: "192.168.1.160"}, {Name: "db", IP: "192.168.1.60"}}
	if err := c.ValidateHostsAgainst(renamed); err == nil {
		t.Error("renaming a referenced host was allowed; every @nas would stop resolving")
	}
}

// A scrape exclusion names a machine, so it takes a reference too — the one
// case the brief left open. A CIDR is a range rather than a host and does not.
func TestScrapeExclusionsTakeAReference(t *testing.T) {
	c := refConfig()
	c.ScrapeExclusions = []string{"@nas", "10.0.0.0/8"}
	if err := c.ValidateScrapeExclusions(); err != nil {
		t.Fatalf("ValidateScrapeExclusions: %v", err)
	}
	excluded := c.scrapeExcluder()
	if !excluded("192.168.1.160") {
		t.Error("@nas did not exclude the declared address")
	}
	if excluded("192.168.1.60") {
		t.Error("@nas excluded an address that is not the declared one")
	}

	c.ScrapeExclusions = []string{"@printer"}
	if err := c.ValidateScrapeExclusions(); err == nil {
		t.Error("an unresolvable exclusion was accepted")
	}
}

func TestExporterReferencesAreValidated(t *testing.T) {
	c := refConfig()
	c.Exporters = []Exporter{{Job: "pg", Mode: "static", Targets: []string{"@db:9187"}}}
	if err := c.ValidateExporters(); err != nil {
		t.Fatalf("ValidateExporters: %v", err)
	}
	c.Exporters = []Exporter{{Job: "pg", Mode: "static", Targets: []string{"@printer:9187"}}}
	if err := c.ValidateExporters(); err == nil {
		t.Error("an exporter target naming no declared host was accepted")
	}
	c.Exporters = []Exporter{{Job: "node", Mode: "port", Port: 9100, Hosts: []string{"@printer"}}}
	if err := c.ValidateExporters(); err == nil {
		t.Error("an exporter host naming no declared host was accepted")
	}
}

// "@self" is this instance's own address. It is the dominant case on a
// gateway: hz's own address is copied into every service's internal_dns.ip and
// into the backends of the services running on it.
func TestSelfReferenceResolvesToThisInstance(t *testing.T) {
	c := refConfig() // LocalInterface 192.168.1.1
	for _, tc := range []struct{ in, want string }{
		{"@self", "192.168.1.1"},
		{"@self:8080", "192.168.1.1:8080"},
	} {
		got, err := c.ResolveHostRef(tc.in)
		if err != nil {
			t.Fatalf("ResolveHostRef(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ResolveHostRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The point of @self over a literal is a FLEET: LocalInterface is the one
// field peer-sync explicitly does not replicate, so two instances reading the
// same config resolve @self to their own addresses. A literal would be one
// instance's address, wrong on every other.
func TestSelfReferenceIsPerInstance(t *testing.T) {
	a := refConfig()
	b := refConfig()
	b.LocalInterface = "192.168.1.2" // the peer

	svc := Service{
		Name: "app", Domains: []string{"app.example.net"},
		InternalDNS: &InternalDNS{IP: "@self"},
		Proxy:       &ProxyConfig{Backend: "@self:8080"},
	}
	a.Services = []Service{svc}
	b.Services = []Service{svc}

	if got := a.DeriveDNSMappings()["app.example.net"]; got != "192.168.1.1" {
		t.Errorf("primary resolved @self to %q", got)
	}
	if got := b.DeriveDNSMappings()["app.example.net"]; got != "192.168.1.2" {
		t.Errorf("peer resolved @self to %q; a peer must not inherit the primary's address", got)
	}
	if got := a.ServiceBackend(&a.Services[0]); got != "192.168.1.1:8080" {
		t.Errorf("primary backend = %q", got)
	}
	if got := b.ServiceBackend(&b.Services[0]); got != "192.168.1.2:8080" {
		t.Errorf("peer backend = %q", got)
	}
}

// An instance that does not yet know its own address says so, naming where the
// value lives — the same rule every other unresolvable reference follows.
func TestSelfReferenceWithNoDetectedAddressIsAnError(t *testing.T) {
	c := refConfig()
	c.LocalInterface = ""
	_, err := c.ResolveHostRef("@self:8080")
	if err == nil {
		t.Fatal("expected an error when the instance's own address is unknown")
	}
	if !strings.Contains(err.Error(), "local_interface") {
		t.Errorf("error does not name where the value lives: %v", err)
	}
}

// "self" is reserved: a declared host of that name would give @self a second
// meaning, and the wrong one on a peer.
func TestSelfIsAReservedHostName(t *testing.T) {
	c := refConfig()
	c.Hosts = append(c.Hosts, HostDecl{Name: "self", IP: "192.168.1.9"})
	err := c.ValidateHosts()
	if err == nil {
		t.Fatal("accepted a declared host named self")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error does not say it is reserved: %v", err)
	}
}

// `hz host show self` has to work, because "what points at this gateway" is
// the question an operator moving it actually has.
func TestHostShowCountsSelfReferences(t *testing.T) {
	c := refConfig()
	c.Services = []Service{{
		Name: "app", Domains: []string{"app.example.net"},
		InternalDNS: &InternalDNS{IP: "@self"},
		Proxy:       &ProxyConfig{Backend: "@self:8080", Deploy: &DeployConfig{NextBackend: "@self:8081"}},
	}}
	refs := c.HostReferences(HostRefSelfName)
	if len(refs) != 3 {
		t.Fatalf("want 3 self-references, got %d: %+v", len(refs), refs)
	}
	if got := c.SelfHostDecl(); got.Name != "self" || got.IP != "192.168.1.1" {
		t.Errorf("SelfHostDecl = %+v", got)
	}
}

// The localhost rewrite in the DNS derive path is a self-reference spelled as
// a magic string; @self is the same resolution named out loud, so the two must
// produce the same answer.
func TestSelfReferenceMatchesTheLegacyLocalhostRewrite(t *testing.T) {
	legacy := refConfig()
	legacy.Services = []Service{{
		Name: "app", Domains: []string{"app.example.net"},
		InternalDNS: &InternalDNS{IP: "localhost"},
	}}
	explicit := refConfig()
	explicit.Services = []Service{{
		Name: "app", Domains: []string{"app.example.net"},
		InternalDNS: &InternalDNS{IP: "@self"},
	}}
	a := legacy.DeriveDNSMappings()["app.example.net"]
	b := explicit.DeriveDNSMappings()["app.example.net"]
	if a != b || a == "" {
		t.Errorf("localhost rewrote to %q but @self resolved to %q", a, b)
	}
}

// A local DNS record answers with an address, so a reference must resolve to
// one — and the record keeps the reference, so the next `hz host set` moves it.
func TestLocalDNSRecordResolvesAReference(t *testing.T) {
	c := refConfig()
	got, err := c.ResolveLocalDNSRecord(LocalDNSRecord{Name: "NAS", IP: "@nas"})
	if err != nil {
		t.Fatalf("ResolveLocalDNSRecord: %v", err)
	}
	if got.Name != "nas" || got.IP != "192.168.1.160" {
		t.Errorf("got %+v", got)
	}
	if _, err := c.ResolveLocalDNSRecord(LocalDNSRecord{Name: "p", IP: "@printer"}); err == nil {
		t.Error("an unresolvable record was accepted")
	}
	// The plain shape check still refuses an unresolved reference, so a caller
	// that forgets to resolve cannot publish "@nas" as a dnsmasq answer.
	if err := (LocalDNSRecord{Name: "nas", IP: "@nas"}).Validate(); err == nil {
		t.Error("an unresolved reference passed the shape check")
	}
}
