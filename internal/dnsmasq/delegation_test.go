package dnsmasq

import (
	"strings"
	"testing"
)

// A delegated subdomain under a wildcard service domain is the office hz's
// measured state: address=/redline.iodesystems.com/192.168.1.160 answered .160
// for every name below it, including the delegated loadtest.redline. The
// server=/…/# line carves it out — dnsmasq takes the longest matching domain
// across --address and --server (checked against dnsmasq 2.91 with dig; see
// the commit that added this test).
func TestRenderHostsForwardsADelegationUpstream(t *testing.T) {
	hosts := RenderHosts(HostsInput{Records: []Record{
		{Name: "redline.iodesystems.com", IP: "192.168.1.160", Wildcard: true},
		{Name: "loadtest.redline.iodesystems.com", IP: "192.0.2.99", ForwardUpstream: true, Comment: "delegated"},
	}})
	want := "# delegated\nserver=/loadtest.redline.iodesystems.com/#\n"
	if !strings.Contains(hosts, want) {
		t.Fatalf("want %q in rendered hosts:\n%s", want, hosts)
	}
	if strings.Contains(hosts, "192.0.2.99") {
		t.Errorf("a forwarded name must not be answered locally:\n%s", hosts)
	}
	if !strings.Contains(hosts, "address=/redline.iodesystems.com/192.168.1.160\n") {
		t.Errorf("the parent's own answer must stay:\n%s", hosts)
	}
}

func TestRenderHostsWithoutDelegationHasNoServerLine(t *testing.T) {
	hosts := RenderHosts(HostsInput{Records: []Record{
		{Name: "redline.iodesystems.com", IP: "192.168.1.160", Wildcard: true},
	}})
	if strings.Contains(hosts, "server=") {
		t.Fatalf("no delegation, but a server= line:\n%s", hosts)
	}
}

// The read-back is name→IP; a forwarded name has no IP and must not appear as
// a mapping.
func TestParseMappingsIgnoresForwards(t *testing.T) {
	got := ParseMappings(RenderHosts(HostsInput{Records: []Record{
		{Name: "loadtest.redline.iodesystems.com", ForwardUpstream: true},
	}}))
	if len(got) != 0 {
		t.Fatalf("mappings = %v", got)
	}
}
