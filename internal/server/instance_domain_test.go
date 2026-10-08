package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// N4j: an hz instance has ONE registered domain, and its names derive from it.

const (
	bootDomain    = "loadtest.redline.example.com"
	bootDomainKsk = "kiosk.hz." + bootDomain
	bootDomainUI  = "hz." + bootDomain
)

func domainBootReq() apitypes.NestedBootstrapReq {
	r := bootReq()
	r.Kiosk = ""
	r.Domain = bootDomain
	return r
}

// --domain sets the instance's domain, derives the kiosk, the endpoint and
// the UI link, and the box's seed carries the VPN-only hz-ui service.
func TestBootstrapWithADomainSeedsTheDerivedNames(t *testing.T) {
	f := newBootFixture(t)
	out := f.generate(t, domainBootReq())
	h, _ := f.s.cfg().FindHZInstance("loadtest-hz")
	if h.Domain != bootDomain || h.Domains != nil || h.DNSGrant != nil {
		t.Fatalf("instance after --domain: %+v", h)
	}
	hv := out.HumanVPN
	if hv == nil || hv.Domain != bootDomain || hv.Kiosk != bootDomainKsk || hv.KioskURL != "https://"+bootDomainKsk ||
		hv.Endpoint != bootDomainKsk+":51820" || hv.UIURL != "https://"+bootDomainUI+"/app/" || !hv.DomainsChanged {
		t.Fatalf("human VPN: %+v", hv)
	}
	grant := grantOf(t, out.Command)
	if s := f.script(grant); !strings.Contains(s, `KIOSK="`+bootDomainKsk+`"`) || !strings.Contains(s, `UI_HOST="`+bootDomainUI+`"`) {
		t.Fatal("the script does not carry the derived kiosk and UI names")
	}
	resp := decodeEnrol(t, f.enrol(grant, boxEnrol()))
	if resp.Seed.Upstream.Domain != bootDomain || resp.Seed.ServerEndpoint != bootDomainKsk+":51820" || resp.Seed.KioskURL != "https://"+bootDomainKsk {
		t.Fatalf("seed: upstream.domain %q endpoint %q kiosk_url %q", resp.Seed.Upstream.Domain, resp.Seed.ServerEndpoint, resp.Seed.KioskURL)
	}
	var ui *apitypes.NestedSeedService
	for i := range resp.Seed.Services {
		if resp.Seed.Services[i].Name == childUIService {
			ui = &resp.Seed.Services[i]
		}
	}
	if ui == nil || !reflect.DeepEqual(ui.Domains, []string{bootDomainUI}) || ui.ExternalDNS != nil || ui.Proxy == nil || !ui.Proxy.Self ||
		ui.InternalDNS == nil || ui.InternalDNS.IP != "10.101.0.1" {
		t.Fatalf("seeded hz-ui: %+v", ui)
	}

	// --kiosk beside --domain must be the derived kiosk.
	bad := domainBootReq()
	bad.Kiosk = "kiosk.example.com"
	if msg := postDeclareErr(t, f.s, f.s.handleAPINestedBootstrap, apitypes.NestedBootstrapPath, bad); !strings.Contains(msg, "disagrees with domain") {
		t.Fatalf("a disagreeing kiosk: %s", msg)
	}
}

// The deprecated --kiosk alone still sets only the kiosk name.
func TestBootstrapKioskAliasSetsOnlyTheKiosk(t *testing.T) {
	f := newBootFixture(t)
	out := f.generate(t, bootReq())
	h, _ := f.s.cfg().FindHZInstance("loadtest-hz")
	if h.Domain != "" || !reflect.DeepEqual(h.Domains, []string{bootKioskName}) || out.HumanVPN.UIURL != "https://"+bootKioskName+"/app/" {
		t.Fatalf("--kiosk: instance %+v, ui %q", h, out.HumanVPN.UIURL)
	}
}

// On the CHILD, built from the merged seed: dnsmasq answers hz.<domain> with
// the wg0 address, HAProxy routes it to hz's own UI, the parent is never
// asked for it, and the MFA portal and the invite link it.
func TestTheChildAnswersItsVPNOnlyNameAtItsWG0Address(t *testing.T) {
	seed, err := withHumanVPN(childSeed("loadtest-hz", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080"),
		&childHumanVPN{Range: "10.101.0.0/24", Kiosk: bootDomainKsk, Domain: bootDomain})
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadFromJSON(runJQMerge(t, seed, `{}`, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	answered := false
	for _, r := range c.DeriveDNSRecords() {
		if r.Name == bootDomainUI && r.IP == "10.101.0.1" && !r.ForwardUpstream {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("the child's dnsmasq does not answer %s with its wg0 address", bootDomainUI)
	}
	routed := false
	for _, b := range c.DeriveHAProxyBackends() {
		for _, d := range b.DomainMatches {
			if d == bootDomainUI {
				routed = true
			}
		}
	}
	if !routed {
		t.Fatalf("HAProxy does not route %s", bootDomainUI)
	}
	c.PublicIPOverride = "203.0.113.50"
	asks, unasked := c.ParentDNSAsks()
	if unasked != "" {
		t.Fatal(unasked)
	}
	var asked []string
	for _, a := range asks {
		asked = append(asked, a.Name)
	}
	if !reflect.DeepEqual(asked, []string{bootDomainKsk}) {
		t.Fatalf("the child asks its parent for %v, want only the kiosk", asked)
	}
	if got := kioskUIURL(c); got != "https://"+bootDomainUI+"/app/" {
		t.Fatalf("Open hz links %q", got)
	}
	// Without a domain, the kiosk name as before.
	c.Upstream.Domain = ""
	if got := kioskUIURL(c); got != "https://"+bootDomainKsk+"/app/" {
		t.Fatalf("fallback: Open hz links %q", got)
	}
}

// Re-bootstrapping the live child: the jq merge REPLACES the old kiosk's name
// and the endpoint (one hz-kiosk, not two), adds hz-ui, and drops an
// external_dns an earlier hz-ui carried — the VPN-only name is never asked.
func TestTheMergeReplacesTheOldKiosk(t *testing.T) {
	seed, err := withHumanVPN(childSeed("loadtest-hz", "redline", "loadtest", "prod", "redline-loadtest", "http://10.100.0.1:8080"),
		&childHumanVPN{Range: "10.101.0.0/24", Kiosk: bootDomainKsk, Domain: bootDomain})
	if err != nil {
		t.Fatal(err)
	}
	current := `{"listen_addr": "127.0.0.1:8080", "vpn_range": "10.101.0.0/24", "dns": "10.101.0.1", "wg_interface": "wg0",
	  "kiosk_url": "https://kiosk.hz.loadtest.example.com", "server_endpoint": "kiosk.hz.loadtest.example.com:51820",
	  "upstream": {"url": "http://10.100.0.1:8080", "token_file": "/x", "rungs": [{"project": "redline", "environment": "loadtest"}], "instance": "loadtest-hz"},
	  "services": [{"name": "redline-loadtest", "project": "redline", "environment": "loadtest", "domains": ["loadtest.redline.example.com"], "external_dns": {}},
	               {"name": "hz-kiosk", "domains": ["kiosk.hz.loadtest.example.com"], "internal_dns": {"ip": "10.101.0.1"}, "external_dns": {}, "proxy": {"self": true}},
	               {"name": "hz-ui", "domains": ["hz.old.example.com"], "external_dns": {}}]}`
	c, err := config.LoadFromJSON(runJQMerge(t, seed, current, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerEndpoint != bootDomainKsk+":51820" || c.KioskURL != "https://"+bootDomainKsk || c.Upstream.Domain != bootDomain {
		t.Fatalf("endpoint %q kiosk_url %q upstream.domain %q", c.ServerEndpoint, c.KioskURL, c.Upstream.Domain)
	}
	count := map[string]int{}
	for _, s := range c.Services {
		count[s.Name]++
		switch s.Name {
		case childKioskService:
			if !reflect.DeepEqual(s.Domains, []string{bootDomainKsk}) {
				t.Fatalf("hz-kiosk domains %v — the old kiosk name was kept", s.Domains)
			}
		case childUIService:
			if !reflect.DeepEqual(s.Domains, []string{bootDomainUI}) || s.ExternalDNS != nil {
				t.Fatalf("hz-ui: domains %v external_dns %+v", s.Domains, s.ExternalDNS)
			}
		case "redline-loadtest":
			if !reflect.DeepEqual(s.Domains, []string{bootDomain}) || s.ExternalDNS == nil {
				t.Fatalf("the app's service lost something: %+v", s)
			}
		}
	}
	if !reflect.DeepEqual(count, map[string]int{"redline-loadtest": 1, childKioskService: 1, childUIService: 1}) {
		t.Fatalf("services after the merge: %v", count)
	}
}

// The invite page's post-redeem link: hz.<domain> with a domain.
func TestInviteRedeemLinksTheUI(t *testing.T) {
	s, _ := inviteServer(t)
	_ = s.updateConfig(func(c *config.Config) {
		c.Upstream = &config.UpstreamHZ{URL: "http://10.100.0.1:8080", TokenFile: "/x",
			Rungs: []config.UpstreamRung{{Project: "redline", Environment: "loadtest"}}, Domain: bootDomain}
	})
	inv := mintInvite(t, s, apitypes.CreateInviteReq{Name: "carl-2", Profile: config.ProfileLanAccess})
	w := redeem(s, tokenOf(t, inv.URL), inviteDevKeyA, "")
	var out apitypes.InviteRedeemResp
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("redeem: %d %s", w.Code, w.Body.String())
	}
	if out.UIURL != "https://"+bootDomainUI+"/app/" {
		t.Fatalf("uiUrl = %q", out.UIURL)
	}
}

// The names table: the VPN-only name is never a record; a public one reads
// its record status; inCert follows the certificate on file.
func TestInstanceNameRows(t *testing.T) {
	h := config.HZInstance{Name: "child", Domain: bootDomain}
	cert := &apitypes.InstanceCertStatusResp{Issued: true, SANs: []string{bootDomain, bootDomainKsk}}
	dns := &apitypes.InstanceDNSStatusResp{Records: []apitypes.InstanceOwnedRecordResp{
		{Name: bootDomain, Type: "A", Status: apitypes.InstanceDNSPublished},
		{Name: bootDomainKsk, Type: "A", Status: apitypes.InstanceDNSPending},
		{Name: bootDomainKsk, Type: "TXT", Status: apitypes.InstanceDNSPublished},
	}}
	got := instanceNameRows(h, cert, dns)
	want := []apitypes.InstanceNameResp{
		{Name: bootDomain, Scope: apitypes.InstanceNamePublic, Record: apitypes.InstanceDNSPublished, InCert: true},
		{Name: bootDomainKsk, Scope: apitypes.InstanceNamePublic, Record: apitypes.InstanceDNSPending, InCert: true},
		{Name: bootDomainUI, Scope: apitypes.InstanceNameVPNOnly, Record: apitypes.InstanceNameRecordNever, InCert: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%+v\nwant\n%+v", got, want)
	}
	if rows := instanceNameRows(h, nil, nil); rows[0].Record != apitypes.InstanceNameRecordNone || rows[0].InCert {
		t.Fatalf("no cert, no records: %+v", rows[0])
	}
}

// Over the real endpoint: with a domain, the parent publishes the kiosk and
// refuses hz.<domain>, and the provider never sees a write for it.
func TestTheParentRefusesTheVPNOnlyNameOverTheWire(t *testing.T) {
	f, fp := dnsN4a(t)
	const d = "loadtest.child.example.com"
	if err := f.s.updateConfig(func(c *config.Config) {
		domain := d
		if _, err := c.SetHZInstance(nestedInstance, config.HZInstancePatch{Domain: &domain}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	tok := f.mint(t, nestedInstance)
	resp := dnsAnswer(t, f.postDNS(tok, nestedInstance, apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		aRec("kiosk.hz." + d), aRec("hz." + d),
	}}))
	if st := statusOf(t, resp, "kiosk.hz."+d); st.Status == apitypes.InstanceDNSRefused {
		t.Fatalf("the kiosk was refused: %+v", st)
	}
	if st := statusOf(t, resp, "hz."+d); st.Status != apitypes.InstanceDNSRefused || !strings.Contains(st.Reason, "VPN-only") {
		t.Fatalf("hz.%s: %+v", d, st)
	}
	if fp.wrote("hz." + d) {
		t.Fatal("the provider was written the VPN-only name")
	}
	if !fp.wrote("kiosk.hz." + d) {
		t.Fatal("instrument: the provider saw no write for the kiosk either, so the absence above proves nothing")
	}
}
