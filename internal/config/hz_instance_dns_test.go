package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// dnsFixture is tlsFixture with the grant N4h needs: the child serves
// loadtest.redline.example.com (its TLS name) and everything under
// hz.loadtest.example.com.
func dnsFixture() *Config {
	c := tlsFixture()
	c.HZInstances[0].Domains = []string{"loadtest.redline.example.com"}
	c.HZInstances[0].DNSGrant = []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"}
	return c
}

func TestGrantEntryCovers(t *testing.T) {
	for _, tc := range []struct {
		entry, name string
		want        bool
	}{
		{"a.example.com", "a.example.com", true},
		{"a.example.com", "b.a.example.com", false},
		{"*.s.example.com", "x.s.example.com", true},
		{"*.s.example.com", "a.x.s.example.com", true},
		{"*.s.example.com", "*.s.example.com", true},
		{"*.s.example.com", "*.x.s.example.com", true},
		{"*.s.example.com", "s.example.com", false},
		{"*.s.example.com", "xs.example.com", false},
		{"*.s.example.com", "X.S.Example.com.", true},
	} {
		if got := grantEntryCovers(tc.entry, tc.name); got != tc.want {
			t.Errorf("grantEntryCovers(%q, %q) = %v, want %v", tc.entry, tc.name, got, tc.want)
		}
	}
}

// Every refusal names the conflict.
func TestDNSGrantValidation(t *testing.T) {
	cases := []struct {
		name  string
		grant []string
		edit  func(*Config)
		want  string // "" is valid
	}{
		{name: "exact and subtree", grant: []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"}},
		{name: "not a hostname", grant: []string{"loadtest.redline.example.com", "Bad_Name.example.com"}, want: "not a lower-case hostname"},
		{name: "listed twice", grant: []string{"loadtest.redline.example.com", "loadtest.redline.example.com"}, want: "twice"},
		{name: "outside every zone", grant: []string{"loadtest.redline.example.com", "x.example.invalid"}, want: "outside every zone this hz owns"},
		{name: "zone with no provider", grant: []string{"loadtest.redline.example.com", "app.example.net"}, want: "has no dns_provider"},
		{name: "the apex", grant: []string{"loadtest.redline.example.com", "example.org"}, want: "the apex of zone example.org"},
		{name: "the whole zone", grant: []string{"loadtest.redline.example.com", "*.example.org"}, want: "covers the whole zone example.org"},
		{name: "below a delegation", grant: []string{"loadtest.redline.example.com", "*.deleg.example.com"},
			edit: func(c *Config) {
				c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "deleg", Type: "NS", Value: "ns1.example.org"})
			}, want: "delegated"},
		{name: "a service domain", grant: []string{"loadtest.redline.example.com", "redline.example.com"}, want: "domain of service redline"},
		{name: "a service domain under a subtree", grant: []string{"loadtest.redline.example.com", "*.apps.example.com"},
			edit: func(c *Config) {
				c.Services = append(c.Services, Service{Name: "web", Domains: []string{"web.apps.example.com"}})
			}, want: "web.apps.example.com, a domain of service web"},
		{name: "a sub_zones name", grant: []string{"loadtest.redline.example.com", "vpn.example.com"},
			edit: func(c *Config) { c.Zones[0].SubZones = append(c.Zones[0].SubZones, "vpn") },
			want: "this hz's own certificate (zone example.com sub_zones \"vpn\")"},
		{name: "a record this hz declares", grant: []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"},
			edit: func(c *Config) {
				c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "mail.hz.loadtest", Type: "TXT", Value: "v=spf1 -all"})
			}, want: "covers TXT mail.hz.loadtest.example.com -> v=spf1 -all, a record this hz declares on zone example.com"},
		{name: "the hand-declared pointer at a TLS domain is allowed (hot fix)", grant: []string{"loadtest.redline.example.com"},
			edit: func(c *Config) {
				c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "loadtest.redline", Type: "A", Value: "203.0.113.20"})
			}},
		{name: "a TXT at the TLS domain is still the parent's", grant: []string{"loadtest.redline.example.com"},
			edit: func(c *Config) {
				c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "loadtest.redline", Type: "MX", Value: "10 mx.example.org"})
			}, want: "covers MX loadtest.redline.example.com"},
		{name: "overlaps another instance", grant: []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"},
			edit: func(c *Config) {
				c.HZInstances = append(c.HZInstances, HZInstance{Name: "other", URL: "http://192.0.2.8:8080", DNSGrant: []string{"api.hz.loadtest.example.com"}})
			}, want: "overlaps grant *.hz.loadtest.example.com of hz instance \"child\""},
		{name: "a TLS domain outside the grant", grant: []string{"*.hz.loadtest.example.com"}, want: "TLS domain loadtest.redline.example.com is outside its DNS grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dnsFixture()
			c.HZInstances[0].DNSGrant = tc.grant
			if tc.edit != nil {
				tc.edit(c)
			}
			err := c.ValidateHZInstances()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused a valid grant: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("accepted; want an error containing %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// An instance with TLS domains and no grant loads with grant = domains, and
// the config — with the live hot fix, a hand-declared A record at that
// name — saves.
func TestGrantMigratesFromDomains(t *testing.T) {
	raw := `{
  "zones": [{"name": "example.com", "zone_id": "Z1", "dns_provider": {"type": "route53"},
             "records": [{"name": "loadtest.redline.example.com", "type": "A", "value": "203.0.113.20"}]}],
  "hz_instances": [{"name": "redline-loadtest", "url": "http://192.0.2.9:8080", "domains": ["loadtest.redline.example.com"]}]
}`
	c, err := LoadFromJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.HZInstances[0].DNSGrant; !reflect.DeepEqual(got, []string{"loadtest.redline.example.com"}) {
		t.Fatalf("migrated grant = %v", got)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, c); err != nil {
		t.Fatalf("the migrated config does not save: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"dns_grant"`) {
		t.Fatalf("the grant was not written:\n%s", b)
	}
	// An explicit grant is never widened, and an explicit revoke with domains
	// still set is refused, naming the domain.
	empty := []string{}
	if _, err := c.SetHZInstance("redline-loadtest", HZInstancePatch{DNSGrant: &empty}); err == nil || !strings.Contains(err.Error(), "TLS domain loadtest.redline.example.com is outside its DNS grant") {
		t.Fatalf("revoking the grant under live domains: %v", err)
	}
}

func asksOf(recs ...InstanceDNSAsk) []InstanceDNSAsk { return recs }

func verdictFor(t *testing.T, vs []InstanceDNSVerdict, name, typ string) InstanceDNSVerdict {
	t.Helper()
	for _, v := range vs {
		if v.Name == name && v.Type == typ {
			return v
		}
	}
	t.Fatalf("no verdict for %s %s in %+v", typ, name, vs)
	return InstanceDNSVerdict{}
}

func instanceRecords(c *Config, instance string) map[string][]string {
	out := map[string][]string{}
	for i := range c.Zones {
		z := &c.Zones[i]
		for _, r := range z.Records {
			if r.Instance == instance {
				k := r.NormalizedType() + " " + z.qualify(r.Name)
				out[k] = append(out[k], r.Value)
			}
		}
	}
	return out
}

// Inside the grant is accepted and stored attributed; outside is refused
// with the reason; a bad type is refused; the same set twice changes nothing.
func TestApplyInstanceDNSRequest(t *testing.T) {
	c := dnsFixture()
	asks := asksOf(
		InstanceDNSAsk{Name: "app.hz.loadtest.example.com", Type: "A", Values: []string{"203.0.113.20"}},
		InstanceDNSAsk{Name: "Loadtest.Redline.Example.com.", Type: "A", Values: []string{"203.0.113.20"}, TTL: 60},
		InstanceDNSAsk{Name: "evil.example.com", Type: "A", Values: []string{"203.0.113.66"}},
		InstanceDNSAsk{Name: "mx.hz.loadtest.example.com", Type: "MX", Values: []string{"10 mx.example.org"}},
		InstanceDNSAsk{Name: "bad.hz.loadtest.example.com", Type: "A", Values: []string{"not-an-ip"}},
	)
	next, vs, removed, err := c.ApplyInstanceDNSRequest("child", asks, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != len(asks) || len(removed) != 0 {
		t.Fatalf("verdicts %d, removed %v", len(vs), removed)
	}
	if v := verdictFor(t, vs, "app.hz.loadtest.example.com", "A"); !v.Accepted || v.Zone != "example.com" {
		t.Fatalf("inside the grant: %+v", v)
	}
	if v := verdictFor(t, vs, "loadtest.redline.example.com", "A"); !v.Accepted {
		t.Fatalf("the TLS name: %+v", v)
	}
	if v := verdictFor(t, vs, "evil.example.com", "A"); v.Accepted || !strings.Contains(v.Reason, "outside hz instance child's DNS grant") {
		t.Fatalf("outside the grant: %+v", v)
	}
	if v := verdictFor(t, vs, "mx.hz.loadtest.example.com", "MX"); v.Accepted || !strings.Contains(v.Reason, "A, AAAA, CNAME or TXT") {
		t.Fatalf("MX: %+v", v)
	}
	if v := verdictFor(t, vs, "bad.hz.loadtest.example.com", "A"); v.Accepted || !strings.Contains(v.Reason, "not an IPv4 address") {
		t.Fatalf("bad value: %+v", v)
	}
	got := instanceRecords(next, "child")
	want := map[string][]string{"A app.hz.loadtest.example.com": {"203.0.113.20"}, "A loadtest.redline.example.com": {"203.0.113.20"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instance-owned records = %v, want %v", got, want)
	}
	for _, r := range next.Zones[0].Records {
		if strings.Contains(r.Name, "evil") {
			t.Fatalf("a refused name was written: %+v", r)
		}
	}
	if len(c.Zones[0].Records) != 0 {
		t.Fatal("the receiver was modified")
	}
	if owner := next.ClassifyRecord("example.com", "app.hz.loadtest.example.com", "A", "203.0.113.20"); owner != RecordOwnerInstance {
		t.Fatalf("owner = %q", owner)
	}
	// Idempotent.
	again, _, removed, err := next.ApplyInstanceDNSRequest("child", asks, 200)
	if err != nil || len(removed) != 0 {
		t.Fatalf("again: %v %v", err, removed)
	}
	a, _ := json.Marshal(next.Zones)
	b, _ := json.Marshal(again.Zones)
	if string(a) != string(b) {
		t.Fatalf("the same set twice changed the zones:\n%s\n%s", a, b)
	}
}

// The full set every time: a name the instance owned and no longer lists is
// removed and tombstoned — and nothing it does not own is touched, though the
// set lists none of them.
func TestInstanceDNSRemovalTouchesOnlyItsOwn(t *testing.T) {
	c := dnsFixture()
	c.HZInstances = append(c.HZInstances, HZInstance{Name: "other", URL: "http://192.0.2.8:8080", DNSGrant: []string{"*.other.example.com"}})
	c.Zones[0].Records = []DNSRecord{
		{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"},                                    // the parent's
		{Name: "app.other.example.com", Type: "A", Value: "203.0.113.30", Instance: "other"},        // another instance's
		{Name: "old.hz.loadtest.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"},  // this one's, dropped
		{Name: "keep.hz.loadtest.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"}, // this one's, kept
		{Name: "keep.hz.loadtest.example.com", Type: "TXT", Value: "verify-me", Instance: "child"},  // same name, other type, dropped
	}
	if err := c.ValidateHZInstances(); err != nil {
		t.Fatalf("instrument: the fixture is invalid: %v", err)
	}
	next, _, removed, err := c.ApplyInstanceDNSRequest("child", asksOf(
		InstanceDNSAsk{Name: "keep.hz.loadtest.example.com", Type: "A", Values: []string{"203.0.113.20"}},
	), 100)
	if err != nil {
		t.Fatal(err)
	}
	var gone []string
	for _, r := range removed {
		gone = append(gone, r.Type+" "+r.Name)
	}
	if want := []string{"A old.hz.loadtest.example.com", "TXT keep.hz.loadtest.example.com"}; !reflect.DeepEqual(gone, want) {
		t.Fatalf("removed %v, want %v", gone, want)
	}
	have := map[string]string{}
	for _, r := range next.Zones[0].Records {
		have[r.NormalizedType()+" "+next.Zones[0].qualify(r.Name)] = r.Instance
	}
	for k, owner := range map[string]string{"TXT _dmarc.example.com": "", "A app.other.example.com": "other", "A keep.hz.loadtest.example.com": "child"} {
		if got, ok := have[k]; !ok || got != owner {
			t.Errorf("%s (owner %q) after the removal: present=%v owner=%q", k, owner, ok, got)
		}
	}
	if len(next.Zones[0].Tombstones) != 2 {
		t.Fatalf("tombstones = %+v", next.Zones[0].Tombstones)
	}
	for _, ts := range next.Zones[0].Tombstones {
		if !strings.HasPrefix(ts.Reason, InstanceTombstoneReason("child")) {
			t.Errorf("tombstone not attributed: %+v", ts)
		}
	}
	// An empty set removes everything it owns — and still nothing else.
	empty, _, removed, err := next.ApplyInstanceDNSRequest("child", nil, 200)
	if err != nil || len(removed) != 1 {
		t.Fatalf("empty set: %v %v", err, removed)
	}
	if got := instanceRecords(empty, "child"); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
	if got := instanceRecords(empty, "other"); len(got) != 1 {
		t.Fatalf("another instance's record went: %v", got)
	}
	if len(empty.Zones[0].Records) != 2 {
		t.Fatalf("records = %+v", empty.Zones[0].Records)
	}
}

// The live hot fix: the operator declared the child's name by hand. An
// identical request converts it to the instance's, said; a different value
// is refused, naming the record, and the record stays the parent's.
func TestHotFixRecordConversion(t *testing.T) {
	c := dnsFixture()
	c.Zones[0].Records = []DNSRecord{{Name: "loadtest.redline.example.com", Type: "A", Value: "203.0.113.20", TTL: 120, Note: "hot fix"}}
	if err := c.ValidateHZInstances(); err != nil {
		t.Fatalf("instrument: the hot-fix shape is invalid: %v", err)
	}

	differ, vs, _, err := c.ApplyInstanceDNSRequest("child", asksOf(
		InstanceDNSAsk{Name: "loadtest.redline.example.com", Type: "A", Values: []string{"203.0.113.21"}}), 100)
	if err != nil {
		t.Fatal(err)
	}
	v := vs[0]
	if v.Accepted || v.Converted || !strings.Contains(v.Reason, "declares A loadtest.redline.example.com -> 203.0.113.20 by hand") || !strings.Contains(v.Reason, "operator decides") {
		t.Fatalf("a different value: %+v", v)
	}
	if r := differ.Zones[0].Records; len(r) != 1 || r[0].Instance != "" || r[0].Value != "203.0.113.20" {
		t.Fatalf("the parent's record changed: %+v", r)
	}

	same, vs, removed, err := c.ApplyInstanceDNSRequest("child", asksOf(
		InstanceDNSAsk{Name: "loadtest.redline.example.com", Type: "A", Values: []string{"203.0.113.20"}}), 100)
	if err != nil || len(removed) != 0 {
		t.Fatalf("%v %v", err, removed)
	}
	if v := vs[0]; !v.Accepted || !v.Converted || !strings.Contains(v.Reason, "converted") {
		t.Fatalf("an identical value: %+v", v)
	}
	r := same.Zones[0].Records
	if len(r) != 1 || r[0].Instance != "child" || r[0].Value != "203.0.113.20" || r[0].TTL != 120 {
		t.Fatalf("after conversion: %+v", r)
	}
	if len(same.Zones[0].Tombstones) != 0 {
		t.Fatalf("a conversion tombstoned something: %+v", same.Zones[0].Tombstones)
	}
}

// Narrowing the grant removes (and tombstones) what the instance owns outside
// it; removing the instance removes all of it. Neither touches the parent's.
func TestNarrowingTheGrantRevokes(t *testing.T) {
	c := dnsFixture()
	c.Zones[0].Records = []DNSRecord{
		{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"},
		{Name: "app.hz.loadtest.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"},
		{Name: "loadtest.redline.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"},
	}
	g := []string{"loadtest.redline.example.com"}
	if _, err := c.SetHZInstance("child", HZInstancePatch{DNSGrant: &g}); err != nil {
		t.Fatal(err)
	}
	if got := instanceRecords(c, "child"); !reflect.DeepEqual(got, map[string][]string{"A loadtest.redline.example.com": {"203.0.113.20"}}) {
		t.Fatalf("after narrowing: %v", got)
	}
	if ts := c.Zones[0].Tombstones; len(ts) != 1 || ts[0].Name != "app.hz.loadtest.example.com" {
		t.Fatalf("tombstones: %+v", ts)
	}
	removes, blocked, err := c.HZInstanceRemoval("child", false)
	if err != nil || len(blocked) != 0 {
		t.Fatal(err, blocked)
	}
	found := false
	for _, d := range removes {
		found = found || (d.Kind == "dns-record" && d.Name == "A loadtest.redline.example.com")
	}
	if !found {
		t.Fatalf("the removal dry run does not name the record: %+v", removes)
	}
	if _, err := c.RemoveHZInstance("child", false); err != nil {
		t.Fatal(err)
	}
	if len(c.Zones[0].Records) != 1 || c.Zones[0].Records[0].Name != "_dmarc" {
		t.Fatalf("after removal: %+v", c.Zones[0].Records)
	}
}

// A record outside its instance's grant, written by hand into config, is
// refused at the write and reported unpublishable.
func TestAnInstanceRecordOutsideTheGrantIsRefused(t *testing.T) {
	c := dnsFixture()
	r := DNSRecord{Name: "evil.example.com", Type: "A", Value: "203.0.113.66", Instance: "child"}
	c.Zones[0].Records = []DNSRecord{r}
	if err := c.ValidateHZInstances(); err == nil || !strings.Contains(err.Error(), "outside its DNS grant") {
		t.Fatalf("validate: %v", err)
	}
	if why, ok := c.InstanceRecordPublishable("example.com", r); ok || !strings.Contains(why, "outside its DNS grant") {
		t.Fatalf("publishable: %v %q", ok, why)
	}
	r.Instance = "ghost"
	if _, ok := c.InstanceRecordPublishable("example.com", r); ok {
		t.Fatal("an undeclared instance's record is publishable")
	}
}

// The CHILD's half: an A record for each external_dns service domain in a
// zone it does not publish itself, valued with its public IP; nothing when
// the IP is unknown.
func TestParentDNSAsks(t *testing.T) {
	c := &Config{
		PublicIP:            "203.0.113.20",
		PublicIPLastChecked: 0,
		PublicIPOverride:    "203.0.113.20",
		Zones:               []Zone{{Name: "child.example.org", ZoneID: "Z9", DNSProvider: &DNSProviderConfig{Type: "route53"}}, {Name: "example.net"}},
		Services: []Service{
			{Name: "app", Domains: []string{"app.hz.loadtest.example.com"}, ExternalDNS: &ExternalDNS{TTL: 60}},
			{Name: "own", Domains: []string{"own.child.example.org"}, ExternalDNS: &ExternalDNS{}},
			{Name: "lan", Domains: []string{"lan.hz.loadtest.example.com"}},
			{Name: "noprov", Domains: []string{"x.example.net"}, ExternalDNS: &ExternalDNS{}},
			{Name: "pinned", Domains: []string{"pin.hz.loadtest.example.com"}, ExternalDNS: &ExternalDNS{IPs: []string{"203.0.113.40"}}},
		},
	}
	asks, unasked := c.ParentDNSAsks()
	if unasked != "" {
		t.Fatal(unasked)
	}
	var got []string
	for _, a := range asks {
		got = append(got, a.Type+" "+a.Name+" "+strings.Join(a.Values, ","))
	}
	want := []string{
		"A app.hz.loadtest.example.com 203.0.113.20",
		"A pin.hz.loadtest.example.com 203.0.113.40",
		"A x.example.net 203.0.113.20",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want %v", got, want)
	}
	c.PublicIPOverride, c.PublicIP = "", ""
	if asks, unasked := c.ParentDNSAsks(); asks != nil || !strings.Contains(unasked, "public IP is unknown") {
		t.Fatalf("unknown IP: %v %q", asks, unasked)
	}
}
