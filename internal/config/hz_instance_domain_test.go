package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

const testInstanceDomain = "loadtest.redline.example.com"

// domainFixture is tlsFixture with the instance registered under ONE domain
// (N4j) and nothing explicit.
func domainFixture() *Config {
	c := tlsFixture()
	c.HZInstances[0].Domains = nil
	c.HZInstances[0].Domain = testInstanceDomain
	return c
}

// The names and the grant derive from the domain; the certificate covers all
// three names; neither is written to config.json.
func TestADomainDerivesTheNamesTheGrantAndTheCert(t *testing.T) {
	c := domainFixture()
	if err := c.ValidateHZInstances(); err != nil {
		t.Fatal(err)
	}
	h := c.HZInstances[0]
	wantNames := []string{"loadtest.redline.example.com", "kiosk.hz.loadtest.redline.example.com", "hz.loadtest.redline.example.com"}
	if got := h.TLSDomains(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("TLSDomains = %v, want %v", got, wantNames)
	}
	if got, want := h.Grant(), []string{"loadtest.redline.example.com", "*.loadtest.redline.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Grant = %v, want %v", got, want)
	}
	if h.KioskName() != "kiosk.hz.loadtest.redline.example.com" || h.UIName() != "hz.loadtest.redline.example.com" {
		t.Fatalf("kiosk %q ui %q", h.KioskName(), h.UIName())
	}
	dc, ok, err := c.InstanceCertRequest("child")
	if err != nil || !ok {
		t.Fatalf("InstanceCertRequest: ok=%v err=%v", ok, err)
	}
	if got := letsencrypt.RequestedSANs(dc); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("the certificate covers %v, want every derived name %v (the VPN-only one included)", got, wantNames)
	}
	raw, _ := json.Marshal(h)
	if s := string(raw); strings.Contains(s, `"domains"`) || strings.Contains(s, `"dns_grant"`) || !strings.Contains(s, `"domain":"loadtest.redline.example.com"`) {
		t.Fatalf("stored record: %s — the derived names must not be stored", s)
	}
}

// An instance from before N4j keeps its explicit names; nothing is guessed.
func TestAnInstanceWithNoDomainKeepsItsExplicitNames(t *testing.T) {
	c := dnsFixture()
	h := c.HZInstances[0]
	if h.Domain != "" || !reflect.DeepEqual(h.TLSDomains(), h.Domains) || !reflect.DeepEqual(h.Grant(), h.DNSGrant) {
		t.Fatalf("names %v grant %v domain %q", h.TLSDomains(), h.Grant(), h.Domain)
	}
	if h.UIName() != "" || h.KioskName() != "" || h.IsVPNOnly("hz.loadtest.redline.example.com") {
		t.Fatal("an instance with no domain has a derived name")
	}
	if err := c.ValidateHZInstances(); err != nil {
		t.Fatal(err)
	}
}

// #8: with a domain, an explicit grant or name set that DISAGREES with the
// derivation is refused, at load and at an edit; one that agrees is dropped
// at the edit; changing the domain moves the grant with it.
func TestADisagreeingExplicitGrantIsRefused(t *testing.T) {
	c := domainFixture()
	c.HZInstances[0].DNSGrant = []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"}
	if err := c.ValidateHZInstances(); err == nil || !strings.Contains(err.Error(), "disagrees with domain loadtest.redline.example.com") {
		t.Fatalf("a stored grant that disagrees: %v", err)
	}
	if got := c.HZInstances[0].Grant(); !reflect.DeepEqual(got, DerivedInstanceGrant(testInstanceDomain)) {
		t.Fatalf("the grant read %v, not the derivation — a stored grant answered", got)
	}
	c = domainFixture()
	c.HZInstances[0].Domains = []string{"loadtest.redline.example.com"}
	if err := c.ValidateHZInstances(); err == nil || !strings.Contains(err.Error(), "disagree with domain") {
		t.Fatalf("stored domains that disagree: %v", err)
	}

	c = domainFixture()
	other := []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"}
	if _, err := c.SetHZInstance("child", HZInstancePatch{DNSGrant: &other}); err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("an edit to a disagreeing grant: %v", err)
	}
	same := DerivedInstanceGrant(testInstanceDomain)
	h, err := c.SetHZInstance("child", HZInstancePatch{DNSGrant: &same})
	if err != nil || h.DNSGrant != nil {
		t.Fatalf("an edit to the derived grant: %v, stored %v", err, h.DNSGrant)
	}

	moved := "app.example.org"
	h, err = c.SetHZInstance("child", HZInstancePatch{Domain: &moved})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Grant(); !reflect.DeepEqual(got, []string{"app.example.org", "*.app.example.org"}) || h.DNSGrant != nil {
		t.Fatalf("after the domain moved the grant is %v (stored %v)", got, h.DNSGrant)
	}
}

// hz.<domain> is VPN-only on the parent: a request for it is refused, an
// instance-owned record at it is refused at the write and at publish, a
// hand-declared record there is refused, and the office's dnsmasq does not
// answer it.
func TestTheVPNOnlyNameIsNeverPublishedByTheParent(t *testing.T) {
	c := domainFixture()
	ui := "hz." + testInstanceDomain
	next, vs, _, err := c.ApplyInstanceDNSRequest("child", asksOf(
		InstanceDNSAsk{Name: testInstanceDomain, Type: "A", Values: []string{"203.0.113.20"}},
		InstanceDNSAsk{Name: "kiosk.hz." + testInstanceDomain, Type: "A", Values: []string{"203.0.113.20"}},
		InstanceDNSAsk{Name: ui, Type: "A", Values: []string{"203.0.113.20"}},
		InstanceDNSAsk{Name: ui, Type: "TXT", Values: []string{"x"}},
	), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{testInstanceDomain, "kiosk.hz." + testInstanceDomain} {
		if v := verdictFor(t, vs, name, "A"); !v.Accepted {
			t.Fatalf("public name %s refused: %+v", name, v)
		}
	}
	for _, typ := range []string{"A", "TXT"} {
		if v := verdictFor(t, vs, ui, typ); v.Accepted || !strings.Contains(v.Reason, "VPN-only") {
			t.Fatalf("%s %s: %+v — the VPN-only name was accepted for public DNS", typ, ui, v)
		}
	}
	for k := range instanceRecords(next, "child") {
		if strings.HasSuffix(k, " "+ui) {
			t.Fatalf("%s was written to the public zone", k)
		}
	}

	owned := domainFixture()
	rec := DNSRecord{Name: ui, Type: "A", Value: "203.0.113.20", Instance: "child"}
	owned.Zones[0].Records = append(owned.Zones[0].Records, rec)
	if why, ok := owned.InstanceRecordPublishable("example.com", rec); ok || !strings.Contains(why, "VPN-only") {
		t.Fatalf("publishable: %v %q", ok, why)
	}
	if err := owned.ValidateInstanceDNS(); err == nil || !strings.Contains(err.Error(), "VPN-only") {
		t.Fatalf("an instance record at the VPN-only name was accepted: %v", err)
	}

	hand := domainFixture()
	hand.Zones[0].Records = append(hand.Zones[0].Records, DNSRecord{Name: "hz.loadtest.redline", Type: "A", Value: "203.0.113.20"})
	if err := hand.ValidateInstanceDNS(); err == nil || !strings.Contains(err.Error(), "VPN-only") {
		t.Fatalf("a hand-declared record at the VPN-only name was accepted (the hot-fix exception must not cover it): %v", err)
	}

	recs := c.DeriveDNSRecords()
	for _, r := range recs {
		if r.Name == ui && !r.ForwardUpstream {
			t.Fatalf("the office's dnsmasq answers %s: %+v", ui, r)
		}
	}
	if !forwards(recs)[ui] {
		t.Fatalf("%s is not carved out of the office's wildcard answer (server=/%s/#): %v", ui, ui, forwards(recs))
	}
	hosts := dnsmasq.RenderHosts(dnsmasq.HostsInput{Records: recs})
	if strings.Contains(hosts, "address=/"+ui+"/") {
		t.Fatalf("the office's dnsmasq answers %s:\n%s", ui, hosts)
	}
}

// The live shape (2026-10-07, placeholders): explicit names with the kiosk in
// ANOTHER subtree, a grant to match, and an instance-owned A record for each.
// Setting the domain keeps the app's record, retracts the old kiosk's,
// derives the new names, and the child's next request is accepted.
func TestSettingTheDomainMigratesTheLiveShape(t *testing.T) {
	c := tlsFixture()
	c.HZInstances[0].Domains = []string{"loadtest.redline.example.com", "kiosk.hz.loadtest.example.com"}
	c.HZInstances[0].DNSGrant = []string{"loadtest.redline.example.com", "*.hz.loadtest.example.com"}
	c.Zones[0].Records = []DNSRecord{
		{Name: "loadtest.redline.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"},
		{Name: "kiosk.hz.loadtest.example.com", Type: "A", Value: "203.0.113.20", Instance: "child"},
	}
	if err := c.ValidateHZInstances(); err != nil {
		t.Fatalf("instrument: the live shape does not validate: %v", err)
	}
	changed, err := c.SetInstanceDomain("child", testInstanceDomain)
	if err != nil || !changed {
		t.Fatalf("SetInstanceDomain: changed=%v err=%v", changed, err)
	}
	h, _ := c.FindHZInstance("child")
	if h.Domain != testInstanceDomain || h.Domains != nil || h.DNSGrant != nil {
		t.Fatalf("after the migration: %+v", h)
	}
	if got := instanceRecords(c, "child"); !reflect.DeepEqual(got, map[string][]string{"A loadtest.redline.example.com": {"203.0.113.20"}}) {
		t.Fatalf("instance records after the migration: %v — the old kiosk must be retracted, the app kept", got)
	}
	tomb := false
	for _, ts := range c.Zones[0].Tombstones {
		if ts.Name == "kiosk.hz.loadtest.example.com" && ts.Type == "A" && strings.HasPrefix(ts.Reason, InstanceTombstoneReason("child")) {
			tomb = true
		}
	}
	if !tomb {
		t.Fatalf("the old kiosk's record has no tombstone, so the provider keeps it: %+v", c.Zones[0].Tombstones)
	}
	dc, _, err := c.InstanceCertRequest("child")
	if err != nil || !reflect.DeepEqual(letsencrypt.RequestedSANs(dc), DerivedInstanceDomains(testInstanceDomain)) {
		t.Fatalf("the reissued cert: %v %v", letsencrypt.RequestedSANs(dc), err)
	}
	if again, err := c.SetInstanceDomain("child", testInstanceDomain); err != nil || again {
		t.Fatalf("the same domain twice: changed=%v err=%v", again, err)
	}
	_, vs, removed, err := c.ApplyInstanceDNSRequest("child", asksOf(
		InstanceDNSAsk{Name: testInstanceDomain, Type: "A", Values: []string{"203.0.113.20"}},
		InstanceDNSAsk{Name: "kiosk.hz." + testInstanceDomain, Type: "A", Values: []string{"203.0.113.20"}},
	), 200)
	if err != nil || len(removed) != 0 {
		t.Fatalf("the child's next request: %v, removed %v", err, removed)
	}
	for _, v := range vs {
		if !v.Accepted {
			t.Fatalf("refused after the migration: %+v", v)
		}
	}
}

// Domain obeys the existing grant rules through its derivation: an apex is
// refused, a wildcard is refused, a name outside every zone is refused.
func TestTheDomainObeysTheGrantRules(t *testing.T) {
	for d, want := range map[string]string{
		"example.org":       "apex of zone example.org",
		"*.x.example.com":   "not one lower-case hostname",
		"x.example.invalid": "outside every zone",
		"app.example.net":   "no dns_provider",
	} {
		c := tlsFixture()
		_, err := c.SetInstanceDomain("child", d)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("domain %s: %v, want %q", d, err, want)
		}
	}
}

// The deprecated --kiosk path on an instance with a domain: its own derived
// kiosk is no change; another name is refused.
func TestAKioskNameOnAnInstanceWithADomainIsDerived(t *testing.T) {
	c := domainFixture()
	if changed, err := c.AddInstanceKiosk("child", "kiosk.hz."+testInstanceDomain); err != nil || changed {
		t.Fatalf("the derived kiosk: changed=%v err=%v", changed, err)
	}
	if _, err := c.AddInstanceKiosk("child", "kiosk.example.org"); err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("another kiosk name: %v", err)
	}
}

// The CHILD never asks its parent for its VPN-only name, even when a service
// lists it with external_dns by mistake; its other names are asked.
func TestTheChildNeverAsksForItsVPNOnlyName(t *testing.T) {
	c := &Config{
		PublicIPOverride: "203.0.113.20",
		Upstream:         &UpstreamHZ{URL: "http://10.100.0.1:8080", TokenFile: "/x", Rungs: []UpstreamRung{{Project: "redline", Environment: "loadtest"}}, Domain: testInstanceDomain},
		Services: []Service{
			{Name: "app", Domains: []string{testInstanceDomain}, ExternalDNS: &ExternalDNS{}},
			{Name: "hz-kiosk", Domains: []string{"kiosk.hz." + testInstanceDomain}, ExternalDNS: &ExternalDNS{}},
			{Name: "hz-ui", Domains: []string{"hz." + testInstanceDomain}, ExternalDNS: &ExternalDNS{}},
		},
	}
	asks, unasked := c.ParentDNSAsks()
	if unasked != "" {
		t.Fatal(unasked)
	}
	var got []string
	for _, a := range asks {
		got = append(got, a.Name)
	}
	if want := []string{"kiosk.hz." + testInstanceDomain, testInstanceDomain}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want %v", got, want)
	}
	if c.Upstream.UIName() != "hz."+testInstanceDomain {
		t.Fatalf("UIName = %q", c.Upstream.UIName())
	}
	c.Upstream.Domain = "Not A Domain"
	if err := c.ValidateUpstream(); err == nil {
		t.Fatal("upstream accepted a domain that is not a hostname")
	}
}
