package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// The `upstream` VPN profile and the nested hz's link to its client
// (vpn_upstream.go; plan/plan.md Tier 1b N1b).

// linkedEstate: a nested hz whose VPN client is `upstream`, beside a human
// client on vpn-only. Built through AddMachine, so it is a config hz would save.
func linkedEstate(t *testing.T) *Config {
	t.Helper()
	c := &Config{
		ListenAddr: ":8080",
		VPNRange:   "10.100.0.0/24",
		WGPeers: []WGPeer{
			{Name: "prod-hz-vpn", AllowedIPs: "10.100.0.7/32"},
			{Name: "alice-phone", AllowedIPs: "10.100.0.2/32"},
		},
		VPNProfiles: map[string]string{"prod-hz-vpn": ProfileUpstream, "alice-phone": ProfileVPNOnly},
	}
	if err := c.AddProject("redline", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "prod-hz", Project: "redline",
		HZ: &MachineHZ{URL: "https://hz.prod.redline.example", VPNClient: " prod-hz-vpn "}}); err != nil {
		t.Fatalf("a link to an upstream client must be accepted: %v", err)
	}
	return c
}

func TestUpstreamClientAllowedIPsIsTheGatewayOnly(t *testing.T) {
	c := &Config{VPNRange: "10.100.0.0/24", AllowedIPs: "10.100.0.0/24, 192.168.1.0/24"}
	if got := c.GetAllowedIPsForProfile(ProfileUpstream); got != "10.100.0.1/32" {
		t.Fatalf("upstream AllowedIPs = %q, want the gateway's WG address /32 only", got)
	}
	if !ValidPeerProfile(ProfileUpstream) || ValidPeerProfile("") || ValidPeerProfile("upstream ") {
		t.Fatal("ValidPeerProfile must accept upstream and nothing unexpected")
	}
}

func TestTheLinkIsTrimmedAndStored(t *testing.T) {
	c := linkedEstate(t)
	m, _ := c.FindMachine("prod-hz")
	if m.HZ.VPNClient != "prod-hz-vpn" {
		t.Fatalf("VPNClient = %q", m.HZ.VPNClient)
	}
	if got := c.UpstreamLinkOf("prod-hz-vpn"); got != "prod-hz" {
		t.Fatalf("UpstreamLinkOf = %q", got)
	}
	if err := Save(filepath.Join(t.TempDir(), "config.json"), c); err != nil {
		t.Fatalf("a linked estate must save: %v", err)
	}
}

// Validation: the link must name a client that exists, is `upstream`, and is
// no other machine's. Each refusal checked at AddMachine AND at Save (the
// chokepoint every writer passes, peer-sync and hand edits included).
func TestTheLinkMustNameAnUpstreamClient(t *testing.T) {
	for label, tc := range map[string]struct {
		client string
		want   string
	}{
		"no such client":        {"ghost", "no such client"},
		"a non-upstream client": {"alice-phone", `profile is "vpn-only"`},
		"another machine's":     {"prod-hz-vpn", "both name VPN client"},
	} {
		c := linkedEstate(t)
		err := c.AddMachine(Machine{Name: "other-hz", HZ: &MachineHZ{URL: "https://hz.other.example", VPNClient: tc.client}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: AddMachine error = %v, want %q", label, err, tc.want)
		}
		if _, ok := c.FindMachine("other-hz"); ok {
			t.Errorf("%s: a refused machine was written", label)
		}

		c.Machines = append(c.Machines, Machine{Name: "other-hz", HZ: &MachineHZ{URL: "https://hz.other.example", VPNClient: tc.client}})
		err = Save(filepath.Join(t.TempDir(), "config.json"), c)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Save error = %v, want %q", label, err, tc.want)
		}
	}
}

// Widening a linked client's profile is refused by name — and by Save, if a
// writer skips the check.
func TestALinkedClientCannotLeaveUpstream(t *testing.T) {
	c := linkedEstate(t)
	for _, p := range []string{ProfileLanAccess, ProfileFullTunnel, ProfileVPNOnly} {
		if err := c.CheckPeerProfileChange("prod-hz-vpn", p); err == nil || !strings.Contains(err.Error(), "prod-hz") {
			t.Errorf("%s: a linked client's widening was not refused naming the machine: %v", p, err)
		}
	}
	if err := c.CheckPeerProfileChange("prod-hz-vpn", ProfileUpstream); err != nil {
		t.Errorf("staying upstream must be allowed: %v", err)
	}
	c.SetPeerProfile("prod-hz-vpn", ProfileLanAccess)
	if err := Save(filepath.Join(t.TempDir(), "config.json"), c); err == nil {
		t.Error("Save accepted a link to a lan-access client")
	}
}

func TestAVPNAdminCannotBeUpstream(t *testing.T) {
	c := linkedEstate(t)
	c.VPNAdmins = []string{"alice-phone"}
	if err := c.CheckPeerProfileChange("alice-phone", ProfileUpstream); err == nil || !strings.Contains(err.Error(), "VPN admin") {
		t.Fatalf("an admin becoming upstream was not refused: %v", err)
	}
	if err := c.CheckPeerProfileChange("alice-phone", "sideways"); err == nil || !strings.Contains(err.Error(), "invalid profile") {
		t.Fatalf("an unknown profile was not refused: %v", err)
	}
}

// Editing the URL must not silently unlink; ClearHZ is the unlink.
func TestAnHZEditKeepsTheLink(t *testing.T) {
	c := linkedEstate(t)
	m, err := c.SetMachine("prod-hz", MachinePatch{HZ: &MachineHZ{URL: "https://hz2.prod.redline.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if m.HZ.URL != "https://hz2.prod.redline.example" || m.HZ.VPNClient != "prod-hz-vpn" {
		t.Fatalf("a URL edit changed the link: %+v", m.HZ)
	}
	if m, err = c.SetMachine("prod-hz", MachinePatch{ClearHZ: true}); err != nil || m.HZ != nil {
		t.Fatalf("ClearHZ: %+v %v", m.HZ, err)
	}
}

func TestARenameCarriesTheLink(t *testing.T) {
	c := linkedEstate(t)
	c.WGPeers[0].Name = "prod-hz-tunnel"
	c.RenamePeerProfile("prod-hz-vpn", "prod-hz-tunnel")
	c.RenameUpstreamClient("prod-hz-vpn", "prod-hz-tunnel")
	if got := c.UpstreamLinkOf("prod-hz-tunnel"); got != "prod-hz" {
		t.Fatalf("the rename lost the link: %q", got)
	}
	if err := Save(filepath.Join(t.TempDir(), "config.json"), c); err != nil {
		t.Fatalf("a renamed link must save: %v", err)
	}
}

// Removal: the client is a dependant. Blocked without cascade, named in the
// removes list with cascade, and the dry run says it leaves wg0.conf.
func TestRemovingALinkedMachineNamesItsVPNClient(t *testing.T) {
	c := linkedEstate(t)
	_, blocked, err := c.MachineRemoval("prod-hz", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Kind != "vpn-client" || blocked[0].Name != "prod-hz-vpn" {
		t.Fatalf("without cascade the client must block: %+v", blocked)
	}
	if _, err := c.RemoveMachine("prod-hz", false, false); err == nil {
		t.Fatal("a blocked removal went through")
	}

	removes, blocked, err := c.MachineRemoval("prod-hz", false, true)
	if err != nil || len(blocked) != 0 {
		t.Fatalf("cascade: blocked=%+v err=%v", blocked, err)
	}
	var said string
	for _, d := range removes {
		if d.Kind == "vpn-client" && d.Name == "prod-hz-vpn" {
			said = d.How
		}
	}
	if !strings.Contains(said, "REMOVED from wg0.conf") {
		t.Fatalf("the cascade dry run does not say the client is removed: %+v", removes)
	}
	if _, err := c.RemoveMachine("prod-hz", false, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindMachine("prod-hz"); ok {
		t.Fatal("the machine is still declared")
	}
}

func TestParentAPIURL(t *testing.T) {
	for _, tc := range []struct {
		listen, wgIP, vpnLn, want, refuse string
	}{
		{":8080", "10.100.0.1", "", "http://10.100.0.1:8080", ""},
		{"0.0.0.0:8080", "10.100.0.1", "", "http://10.100.0.1:8080", ""},
		{"10.100.0.1:9000", "10.100.0.1", "", "http://10.100.0.1:9000", ""},
		{"127.0.0.1:8080", "10.100.0.1", "", "", "not the gateway's WireGuard address"},
		{"192.168.1.10:8080", "10.100.0.1", "", "", "not the gateway's WireGuard address"},
		// The VPN listener, bound exactly where the rule admits: accepted.
		{"127.0.0.1:8080", "10.100.0.1", "10.100.0.1:8080", "http://10.100.0.1:8080", ""},
		{"192.168.1.10:8080", "10.100.0.1", "10.100.0.1:8080", "http://10.100.0.1:8080", ""},
		// Bound somewhere the rule does not admit: still refused.
		{"127.0.0.1:8080", "10.100.0.1", "10.100.0.2:8080", "", "VPN listener is not bound"},
		{"127.0.0.1:8080", "10.100.0.1", "10.100.0.1:9090", "", "VPN listener is not bound"},
		{":8080", "", "", "", "does not know the gateway's WireGuard address"},
		{"", "10.100.0.1", "", "", "has no port"},
	} {
		c := &Config{ListenAddr: tc.listen}
		got, err := c.ParentAPIURL(tc.wgIP, tc.vpnLn)
		if tc.refuse != "" {
			if err == nil || !strings.Contains(err.Error(), tc.refuse) {
				t.Errorf("%q/%q: err = %v, want %q", tc.listen, tc.wgIP, err, tc.refuse)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q/%q: got %q %v, want %q", tc.listen, tc.wgIP, got, err, tc.want)
		}
	}

	// A start-option bind that moves hz off the port the rule admits.
	c := &Config{ListenAddr: ":8080"}
	c.SetListenOverride(":9090")
	if _, err := c.ParentAPIURL("10.100.0.1", ""); err == nil {
		t.Error("an override on another port must be refused")
	}
}
