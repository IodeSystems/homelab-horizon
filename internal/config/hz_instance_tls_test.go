package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// tlsFixture is the measured shape (2026-10-03) with placeholders: this hz
// owns example.com with a DNS provider and issues its own certificate for the
// apex and the wildcard; a service domain redline.example.com is a wildcard
// local answer; the nested hz "child" serves loadtest.redline.example.com.
func tlsFixture() *Config {
	return &Config{
		PublicIP:       "198.51.100.1",
		LocalInterface: "192.168.1.10",
		Zones: []Zone{
			{Name: "example.com", ZoneID: "Z1", DNSProvider: &DNSProviderConfig{Type: "route53"},
				SSL: &ZoneSSL{Enabled: true, Email: "ops@example.com"}, SubZones: []string{"", "*"}},
			{Name: "example.net", ZoneID: "Z2"},
			{Name: "example.org", ZoneID: "Z3", DNSProvider: &DNSProviderConfig{Type: "route53"}},
		},
		Services: []Service{{Name: "redline", Domains: []string{"redline.example.com"}, InternalDNS: &InternalDNS{IP: "192.168.1.10"}}},
		HZInstances: []HZInstance{{Name: "child", URL: "http://192.0.2.9:8080",
			Domains: []string{"loadtest.redline.example.com", "api.loadtest.redline.example.com"}}},
	}
}

func TestInstanceDomainValidation(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		edit    func(*Config)
		want    string // "" is valid
	}{
		{name: "in a provider zone", domains: []string{"loadtest.redline.example.com"}},
		{name: "wildcard allowed", domains: []string{"*.loadtest.redline.example.com"}},
		{name: "outside every zone", domains: []string{"loadtest.example.invalid"}, want: "outside every zone this hz owns"},
		{name: "zone with no provider", domains: []string{"app.example.net"}, want: "has no dns_provider"},
		{name: "two zones", domains: []string{"a.example.com", "a.example.org"}, want: "different zones"},
		{name: "a service's domain", domains: []string{"redline.example.com"}, want: "also a domain of service redline"},
		{name: "in this hz's own certificate", domains: []string{"*.example.com"}, want: "this hz's own certificate"},
		{name: "not a hostname", domains: []string{"bad_name.example.com"}, want: "not a lower-case hostname"},
		{name: "listed twice", domains: []string{"a.example.com", "a.example.com"}, want: "twice"},
		{name: "below a delegation", domains: []string{"x.deleg.example.com"},
			edit: func(c *Config) {
				c.Zones[0].Records = append(c.Zones[0].Records, DNSRecord{Name: "deleg", Type: "NS", Value: "ns1.example.org"})
			}, want: "delegated"},
		{name: "two instances, one name", domains: []string{"a.example.com"},
			edit: func(c *Config) {
				c.HZInstances = append(c.HZInstances, HZInstance{Name: "other", URL: "http://192.0.2.8:8080", Domains: []string{"a.example.com"}})
			}, want: "listed by hz instances"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tlsFixture()
			c.HZInstances[0].Domains = tc.domains
			if tc.edit != nil {
				tc.edit(c)
			}
			err := c.ValidateInstanceDomains()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("accepted, want a refusal containing %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// The refusal is a sentence an operator can act on: it names the instance,
// the domain, and why this hz cannot issue it.
func TestInstanceDomainOutsideEveryZoneIsRefusedWithASentence(t *testing.T) {
	c := tlsFixture()
	c.HZInstances[0].Domains = []string{"app.example.invalid"}
	err := c.ValidateHZInstances()
	if err == nil {
		t.Fatal("accepted a domain outside every zone")
	}
	for _, want := range []string{`"child"`, "app.example.invalid", "cannot issue it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
}

// THE DEDICATED CERTIFICATE. The instance's request is exactly its declared
// names, and this hz's own certificates carry none of them — the two sets are
// disjoint, so neither can be served for the other.
func TestTheInstanceCertCoversExactlyItsDomainsAndNoOfficeCertCarriesThem(t *testing.T) {
	c := tlsFixture()
	dc, ok, err := c.InstanceCertRequest("child")
	if err != nil || !ok {
		t.Fatalf("InstanceCertRequest: ok=%v err=%v", ok, err)
	}
	if got, want := letsencrypt.RequestedSANs(dc), c.HZInstances[0].Domains; !reflect.DeepEqual(got, want) {
		t.Fatalf("instance cert SANs = %v, want exactly %v", got, want)
	}
	if dc.Email != "ops@example.com" || dc.DNSProvider == nil || dc.DNSProvider.AWSHostedZoneID != "Z1" {
		t.Fatalf("issued through the wrong zone: %+v provider %+v", dc, dc.DNSProvider)
	}
	office := c.DeriveSSLDomains()
	if len(office) == 0 {
		t.Fatal("instrument: the fixture issues no office certificate, so disjointness proves nothing")
	}
	inst := map[string]bool{}
	for _, d := range c.HZInstances[0].Domains {
		inst[d] = true
	}
	for _, od := range office {
		for _, san := range letsencrypt.RequestedSANs(od) {
			if inst[san] {
				t.Errorf("office certificate %s carries the instance's name %s", od.Domain, san)
			}
		}
	}
	// No domains, no request.
	c.HZInstances[0].Domains = nil
	if _, ok, err := c.InstanceCertRequest("child"); ok || err != nil {
		t.Fatalf("an instance with no domains asked for a certificate: ok=%v err=%v", ok, err)
	}
}

// The field survives a write: Set with domains normalises them, Set with an
// empty slice clears them, nil leaves them.
func TestSetHZInstanceDomains(t *testing.T) {
	c := tlsFixture()
	d := []string{" Loadtest.Redline.Example.com. "}
	h, err := c.SetHZInstance("child", HZInstancePatch{Domains: &d})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.Domains, []string{"loadtest.redline.example.com"}) {
		t.Fatalf("domains = %v", h.Domains)
	}
	note := "n"
	if h, _ = c.SetHZInstance("child", HZInstancePatch{Note: &note}); len(h.Domains) != 1 {
		t.Fatalf("a note edit dropped the domains: %v", h.Domains)
	}
	empty := []string{}
	if h, _ = c.SetHZInstance("child", HZInstancePatch{Domains: &empty}); len(h.Domains) != 0 {
		t.Fatalf("clear left %v", h.Domains)
	}
	bad := []string{"x.example.invalid"}
	if _, err := c.SetHZInstance("child", HZInstancePatch{Domains: &bad}); err == nil {
		t.Fatal("Set accepted a domain outside every zone")
	}
}

func forwards(recs []dnsmasq.Record) map[string]bool {
	out := map[string]bool{}
	for _, r := range recs {
		if r.ForwardUpstream {
			out[r.Name] = true
		}
	}
	return out
}

// THE DNSMASQ FIX. A zone record pointing ELSEWHERE and an instance domain,
// both under the service's wildcard address=, are carved out with
// server=/name/#; a record pointing at this hz is not.
func TestDNSCarvesOutExternalRecordsAndInstanceDomains(t *testing.T) {
	c := tlsFixture()
	c.HZInstances[0].Domains = []string{"loadtest.redline.example.com"}
	c.Zones[0].Records = []DNSRecord{
		{Name: "ext.redline", Type: "A", Value: "203.0.113.7"},              // elsewhere
		{Name: "self.redline", Type: "A", Value: "198.51.100.1"},            // this hz's public IP
		{Name: "lan.redline", Type: "A", Value: "192.168.1.10"},             // this hz's LAN answer
		{Name: "alias.redline", Type: "CNAME", Value: "elsewhere.example."}, // elsewhere
		{Name: "outside.example.com", Type: "A", Value: "203.0.113.8"},      // no wildcard covers it
		{Name: "txt.redline", Type: "TXT", Value: "v=1"},                    // not an address
	}
	got := forwards(c.DeriveDNSRecords())
	for _, want := range []string{"ext.redline.example.com", "alias.redline.example.com", "loadtest.redline.example.com"} {
		if !got[want] {
			t.Errorf("%s is not forwarded upstream; got %v", want, got)
		}
	}
	for _, not := range []string{"self.redline.example.com", "lan.redline.example.com", "outside.example.com", "txt.redline.example.com", "redline.example.com"} {
		if got[not] {
			t.Errorf("%s is forwarded upstream but must not be", not)
		}
	}
	hosts := dnsmasq.RenderHosts(dnsmasq.HostsInput{Records: c.DeriveDNSRecords()})
	for _, line := range []string{"server=/loadtest.redline.example.com/#\n", "server=/ext.redline.example.com/#\n", "address=/redline.example.com/192.168.1.10\n"} {
		if !strings.Contains(hosts, line) {
			t.Errorf("rendered hosts lack %q:\n%s", line, hosts)
		}
	}
}
