package config

import (
	"fmt"
	"strings"
	"testing"
)

// everyFieldConfig builds a config in which EVERY address-carrying field holds
// `addr`, so one traversal of it exercises every record kind at once.
//
// The two declaration sites are included deliberately: local_interface and the
// "gw" host declaration both carry the same address, which is the live
// gateway's own shape (it declares itself) and the case where "@self" and a
// HostDecl compete.
func everyFieldConfig(addr string) *Config {
	withPort := func(port string) string { return addr + ":" + port }
	return &Config{
		LocalInterface: addr,
		LastLanCIDR:    "192.168.1.0/24",
		Hosts: []HostDecl{
			{Name: "gw", IP: addr},
			{Name: "db", IP: "192.168.1.60"},
		},
		// NEAR MISSES, in the fixture rather than only in the unit test for
		// the matcher: a scan that matched on substring would pick these up,
		// and every count below would move. Without them the fixture would do
		// the matcher's job and a broken match would show up in one test
		// instead of all of them.
		Services: []Service{{
			Name:  "elsewhere",
			Proxy: &ProxyConfig{Backend: "192.168.1.1600:80"},
			Forwards: []Forward{
				{Proto: "tcp", Port: 25, Backend: "2192.168.1.160:25", Name: "nearmiss"},
			},
			InternalDNS: &InternalDNS{IP: "192.168.1.16"},
		}, {
			Name:        "app",
			Domains:     []string{"app.example.net"},
			InternalDNS: &InternalDNS{IP: addr},
			Proxy: &ProxyConfig{
				Backend: withPort("8080"),
				Deploy:  &DeployConfig{NextBackend: withPort("8081"), Token: "t"},
			},
			Forwards: []Forward{{Proto: "udp", Port: 4433, Backend: withPort("4433"), Name: "wt"}},
		}},
		LocalDNSRecords:  []LocalDNSRecord{{Name: "gw", IP: addr}},
		Exporters:        []Exporter{{Job: "node", Mode: "port", Port: 9100, Hosts: []string{addr, "192.168.1.16"}}, {Job: "pg", Mode: "static", Targets: []string{withPort("9187"), "192.168.1.1600:9187"}}},
		ScrapeExclusions: []string{addr},
	}
}

// key is one record's identity, independent of what it currently holds.
type key struct{ kind, owner, field string }

func occurrenceKeys(occs []AddressOccurrence) map[key]string {
	m := map[key]string{}
	for _, o := range occs {
		m[key{o.Kind, o.Owner, o.Field}] = o.Value
	}
	return m
}

func referenceKeys(refs []HostReference) map[key]string {
	m := map[key]string{}
	for _, r := range refs {
		m[key{r.Kind, r.Owner, r.Field}] = r.Value
	}
	return m
}

// THE LISTS MUST LINE UP FIELD FOR FIELD. An occurrence and a reference are
// opposite behaviours in the same record, so the operator reads them against
// each other: "8 records reference @gw, 31 carry its address". If the scan
// walked a different set of fields from HostReferences, that comparison would
// be between two different populations and nobody could tell.
//
// The declaration sites are the deliberate extra: they carry the address and
// can never carry a reference, so they appear in the occurrence list only.
func TestOccurrencesCoverTheSameRecordsReferencesDo(t *testing.T) {
	literal := everyFieldConfig("192.168.1.160")
	referenced := everyFieldConfig("@gw")
	referenced.LocalInterface = "192.168.1.160" // a declaration, never a reference
	referenced.Hosts[0].IP = "192.168.1.160"    // ditto

	occs := occurrenceKeys(literal.AddressOccurrences("192.168.1.160"))
	refs := referenceKeys(referenced.HostReferences("gw"))

	declSites := map[key]bool{
		{HostAddrKindLocalInterface, "", "local_interface"}: true,
		{HostAddrKindHostDecl, "gw", "hosts[0].ip"}:         true,
	}
	for k := range declSites {
		if _, ok := occs[k]; !ok {
			t.Errorf("the occurrence scan missed the declaration site %+v", k)
		}
		if _, ok := refs[k]; ok {
			t.Errorf("%+v turned up as a REFERENCE; a declaration can never hold one", k)
		}
	}
	for k := range refs {
		if _, ok := occs[k]; !ok {
			t.Errorf("record %+v can hold a reference but the occurrence scan does not walk it", k)
		}
	}
	for k := range occs {
		if declSites[k] {
			continue
		}
		if _, ok := refs[k]; !ok {
			t.Errorf("record %+v turns up as an occurrence but HostReferences does not walk it; the two lists no longer line up", k)
		}
	}
	// The live gateway's shape: the config carries its own address in more
	// places than anything references it.
	if len(occs) < 9 {
		t.Errorf("expected every address-carrying field to be found, got %d: %+v", len(occs), occs)
	}
}

// THE TWO LISTS ARE NEVER THE SAME LIST. A record written as a reference is
// not an occurrence of the address it resolves to — it follows the host, which
// is the entire difference — and a record written as a literal is not a
// reference.
func TestAReferenceIsNotAnOccurrenceAndViceVersa(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	c.Services[1].Proxy.Backend = "@gw:8080" // one record adopted already ("app")

	for _, o := range c.AddressOccurrences("192.168.1.160") {
		if o.Kind == HostRefKindServiceBackend {
			t.Errorf("a record written %q was reported as carrying the address literally: %+v", "@gw:8080", o)
		}
	}
	found := false
	for _, r := range c.HostReferences("gw") {
		if r.Kind == HostRefKindServiceBackend {
			found = true
		}
		for _, o := range c.AddressOccurrences("192.168.1.160") {
			if o.Kind == r.Kind && o.Owner == r.Owner && o.Field == r.Field {
				t.Errorf("record %s/%s/%s appears in BOTH lists; they describe opposite behaviours and must never merge", r.Kind, r.Owner, r.Field)
			}
		}
	}
	if !found {
		t.Fatal("the adopted record did not show up as a reference — this test no longer models anything")
	}
}

// The match is on the host part and it is exact. A substring test would claim
// 192.168.1.1600 carries 192.168.1.160, and adopting that would rewrite a
// record pointing at a different machine.
func TestAddressMatchIsExactOnTheHostPart(t *testing.T) {
	for _, tc := range []struct {
		value, addr string
		want        bool
	}{
		{"192.168.1.160", "192.168.1.160", true},
		{"192.168.1.160:8080", "192.168.1.160", true},
		{" 192.168.1.160:8080 ", "192.168.1.160", true},
		{"192.168.1.1600", "192.168.1.160", false},
		{"2192.168.1.160", "192.168.1.160", false},
		{"192.168.1.160/24", "192.168.1.160", false},
		{"@gw:8080", "192.168.1.160", false},
		{"@self", "192.168.1.160", false},
		{"", "192.168.1.160", false},
		{"192.168.1.160", "", false},
		{"nas:8080", "nas", true},
	} {
		if got := carriesAddress(tc.value, tc.addr); got != tc.want {
			t.Errorf("carriesAddress(%q, %q) = %v, want %v", tc.value, tc.addr, got, tc.want)
		}
	}
}

// An empty occurrence list is a real answer, unlike an empty reference list on
// a config that has never been adopted.
func TestNoOccurrencesIsAnAnswer(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	if occs := c.AddressOccurrences("10.0.0.9"); len(occs) != 0 {
		t.Errorf("an address nothing carries returned %d occurrence(s): %+v", len(occs), occs)
	}
}

// @self WINS over a HostDecl carrying the same address, and says why.
//
// This is the case that matters on the live gateway: it declares itself, so
// both spellings resolve here. They are not equally correct — a HostDecl
// naming this box's address is wrong on every peer, and @self resolves per
// instance.
func TestSelfBeatsAHostDeclarationWithTheSameAddress(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	plan, err := c.PlanAddressAdoption("192.168.1.160")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Ref != SelfRef {
		t.Fatalf("adopted to %q; @self must win when the address is this instance's own", plan.Ref)
	}
	if !strings.Contains(plan.RefWhy, "gw") || plan.RefWhy == "" {
		t.Errorf("the preference was applied without naming the host it beat: %q", plan.RefWhy)
	}
	for _, a := range plan.Adopt {
		if !strings.HasPrefix(a.Ref, SelfRef) {
			t.Errorf("record %s/%s would be written %q, not a @self reference", a.Kind, a.Field, a.Ref)
		}
	}

	// With no local_interface match, the declaration is the only answer.
	c2 := everyFieldConfig("192.168.1.160")
	c2.LocalInterface = "192.168.1.1"
	plan2, err := c2.PlanAddressAdoption("192.168.1.160")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan2.Ref != "@gw" {
		t.Errorf("adopted to %q, want @gw when the address is not this instance's own", plan2.Ref)
	}
	if plan2.RefWhy != "" {
		t.Errorf("a choice with only one candidate explained itself: %q", plan2.RefWhy)
	}
}

// The port suffix survives adoption: "192.168.1.160:8080" becomes
// "@self:8080", never "@self".
func TestAdoptionKeepsThePort(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	plan, err := c.PlanAddressAdoption("192.168.1.160")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := map[string]string{
		"proxy.backend":             SelfRef + ":8080",
		"proxy.deploy.next_backend": SelfRef + ":8081",
		"forwards[0].backend":       SelfRef + ":4433",
		"internal_dns.ip":           SelfRef,
		"targets[0]":                SelfRef + ":9187",
		"hosts[0]":                  SelfRef,
	}
	got := map[string]string{}
	for _, a := range plan.Adopt {
		got[a.Field] = a.Ref
	}
	for field, w := range want {
		if got[field] != w {
			t.Errorf("%s would be written %q, want %q", field, got[field], w)
		}
	}
}

// REFUSE RATHER THAN GUESS. local_interface is the declaration @self resolves
// TO: rewriting it would be a cycle. A HostDecl's ip is where a reference
// bottoms out, and Save refuses a reference there anyway.
func TestAdoptionRefusesTheDeclarationSitesByName(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	plan, err := c.AdoptAddress("192.168.1.160")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}

	refused := map[string]string{}
	for _, r := range plan.Refused {
		if r.Ref != "" {
			t.Errorf("a refused record still carries a replacement value: %+v", r)
		}
		if r.WhyNot == "" {
			t.Errorf("record %s/%s was refused with no reason", r.Kind, r.Field)
		}
		refused[r.Field] = r.WhyNot
	}
	if _, ok := refused["local_interface"]; !ok {
		t.Error("local_interface was not refused; rewriting it to @self is a cycle")
	}
	if _, ok := refused["hosts[0].ip"]; !ok {
		t.Error("the host declaration's ip was not refused; it is where references bottom out")
	}

	// And the refusal is a refusal: the values are untouched after a WRITE.
	if c.LocalInterface != "192.168.1.160" {
		t.Errorf("local_interface was rewritten to %q", c.LocalInterface)
	}
	if c.Hosts[0].IP != "192.168.1.160" {
		t.Errorf("the host declaration's ip was rewritten to %q", c.Hosts[0].IP)
	}
	if err := c.ValidateHosts(); err != nil {
		t.Errorf("the config no longer validates after adoption: %v", err)
	}
}

// An address no declaration owns cannot be adopted: there is nothing for the
// reference to bottom out in, and inventing a HostDecl would be hz guessing
// what the operator meant to call the box.
func TestAdoptionRefusesAnUndeclaredAddress(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	c.LocalInterface = "192.168.1.1"
	_, err := c.PlanAddressAdoption("192.168.1.60") // declared as "db"... check the other way
	if err != nil {
		t.Fatalf("a declared address should plan: %v", err)
	}
	_, err = c.PlanAddressAdoption("10.9.9.9")
	if err == nil {
		t.Fatal("adopting an address nothing declares was allowed")
	}
	for _, want := range []string{"10.9.9.9", "hz host add", "local_interface"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// THE DRY RUN IS THE PRODUCT. It must describe exactly what the write does and
// must itself write nothing — both halves, because a plan that touches the
// config is not a dry run and a plan that differs from the write is a lie.
func TestTheDryRunWritesNothingAndMatchesTheWrite(t *testing.T) {
	dry := everyFieldConfig("192.168.1.160")
	before := fmt.Sprintf("%+v", dry)
	plan, err := dry.PlanAddressAdoption("192.168.1.160")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if after := fmt.Sprintf("%+v", dry); after != before {
		t.Errorf("the dry run mutated the config:\n before %s\n after  %s", before, after)
	}
	if plan.Written != 0 {
		t.Errorf("the dry run reported %d record(s) written", plan.Written)
	}

	wet := everyFieldConfig("192.168.1.160")
	done, err := wet.AdoptAddress("192.168.1.160")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if done.Written != len(plan.Adopt) {
		t.Errorf("the write changed %d record(s), the dry run promised %d", done.Written, len(plan.Adopt))
	}
	if fmt.Sprintf("%+v", plan.Adopt) != fmt.Sprintf("%+v", done.Adopt) {
		t.Errorf("the write's list differs from the dry run's:\n plan %+v\n done %+v", plan.Adopt, done.Adopt)
	}
	if fmt.Sprintf("%+v", plan.Refused) != fmt.Sprintf("%+v", done.Refused) {
		t.Errorf("the write refused a different set from the dry run:\n plan %+v\n done %+v", plan.Refused, done.Refused)
	}

	// Every adopted record now resolves back to the address it replaced, and
	// nothing carries the literal any more except the two declaration sites.
	for _, a := range done.Adopt {
		got, err := wet.ResolveHostRef(a.Ref)
		if err != nil {
			t.Errorf("%s/%s was written %q, which does not resolve: %v", a.Kind, a.Field, a.Ref, err)
			continue
		}
		if got != a.Value {
			t.Errorf("%s/%s was %q and now resolves to %q", a.Kind, a.Field, a.Value, got)
		}
	}
	left := wet.AddressOccurrences("192.168.1.160")
	if len(left) != len(done.Refused) {
		t.Errorf("after adoption %d literal(s) remain but only %d were refused: %+v", len(left), len(done.Refused), left)
	}
	if err := wet.ValidateHostRefs(); err != nil {
		t.Errorf("the adopted config does not validate: %v", err)
	}
}

// Adopting twice is a no-op, because there is nothing left to adopt. The
// second run is the one an operator does by accident.
func TestAdoptingTwiceChangesNothingTheSecondTime(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	if _, err := c.AdoptAddress("192.168.1.160"); err != nil {
		t.Fatalf("first adopt: %v", err)
	}
	again, err := c.AdoptAddress("192.168.1.160")
	if err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if again.Written != 0 || len(again.Adopt) != 0 {
		t.Errorf("the second run rewrote %d record(s): %+v", again.Written, again.Adopt)
	}
}

// Adoption takes an address, not the spelling that points at one. Passing
// "@self" is a mistake worth a sentence rather than an empty result.
func TestAdoptionRefusesAReferenceAsItsArgument(t *testing.T) {
	c := everyFieldConfig("192.168.1.160")
	if _, err := c.PlanAddressAdoption(SelfRef); err == nil {
		t.Fatal("adopting a reference was allowed")
	}
}
