package config

import (
	"encoding/json"
	"testing"
	"time"
)

// An infra client is a machine — the owner's AI/dev box serving its own
// services over the VPN — with no person to answer an MFA prompt. Exempt in
// both scopes, as an upstream client is; only while vpn-only.

func infraCfg(scope, profile string) *Config {
	c := &Config{
		VPNMFAEnabled: true,
		VPNMFAScope:   scope,
		VPNProfiles:   map[string]string{"box": profile, "phone": ProfileVPNOnly},
	}
	c.SetInfraClient("box", InfraClient{Reason: "service box", SetAt: 1, SetBy: "admin"})
	return c
}

func TestInfraClientIsExemptInBothScopes(t *testing.T) {
	for _, scope := range []string{"", MFAScopeAdminsExempt, MFAScopeAll} {
		c := infraCfg(scope, ProfileVPNOnly)
		if c.IsPeerMFAJailed("box") {
			t.Errorf("scope %q: infra client jailed", scope)
		}
		if got := c.MFAExemption("box"); got != MFAExemptInfra {
			t.Errorf("scope %q: exemption = %q, want %q", scope, got, MFAExemptInfra)
		}
		// Positive control: a user client in the same config is still jailed.
		if !c.IsPeerMFAJailed("phone") {
			t.Errorf("scope %q: a user client escaped the jail", scope)
		}
	}
}

// The tag was declared for a vpn-only machine. An MFA-free client with LAN
// access or a full tunnel is a different risk, so the tag stops counting the
// moment the profile changes — and resumes if it changes back.
func TestInfraTagCountsOnlyWhileVPNOnly(t *testing.T) {
	for _, profile := range []string{ProfileLanAccess, ProfileFullTunnel, ""} {
		c := infraCfg(MFAScopeAll, profile)
		if !c.IsPeerMFAJailed("box") {
			t.Errorf("profile %q: an infra tag exempted a client that is not vpn-only", profile)
		}
		if !c.IsInfraClient("box") {
			t.Errorf("profile %q: the tag itself must survive, so the report can show it inactive", profile)
		}
	}
}

func TestInfraTagFollowsRenameAndDelete(t *testing.T) {
	c := infraCfg("", ProfileVPNOnly)
	c.RenameMFAPeer("box", "box2")
	if c.IsInfraClient("box") || !c.IsInfraClient("box2") {
		t.Errorf("rename: tags = %v", c.VPNInfraClients)
	}
	c.DeleteMFAPeer("box2")
	if c.IsInfraClient("box2") {
		t.Errorf("delete left the tag behind: %v", c.VPNInfraClients)
	}
}

// One decision, many readers: MFAExemption names each way a client skips MFA,
// and IsPeerMFAJailed is exactly its negation.
func TestMFAExemptionNamesEveryBypass(t *testing.T) {
	now := time.Now().Unix()
	c := &Config{
		VPNMFAEnabled: true,
		VPNProfiles:   map[string]string{"nested": ProfileUpstream, "box": ProfileVPNOnly},
		VPNAdmins:     []string{"admin"},
		VPNMFASessions: map[string]int64{
			"forever": 0, "live": now + 3600, "lapsed": now - 1,
		},
	}
	c.SetInfraClient("box", InfraClient{Reason: "r"})
	c.GrantMFAException("exc", MFAException{Expires: now + 3600, Reason: "r"})
	want := map[string]string{
		"nested": MFAExemptUpstream, "box": MFAExemptInfra, "exc": MFAExemptException,
		"admin": MFAExemptAdmin, "forever": MFAExemptSessionForever, "live": MFAExemptSession,
		"lapsed": "", "nobody": "",
	}
	for name, w := range want {
		if got := c.MFAExemption(name); got != w {
			t.Errorf("%s: exemption %q, want %q", name, got, w)
		}
		if c.IsPeerMFAJailed(name) != (w == "") {
			t.Errorf("%s: jailed=%v disagrees with exemption %q", name, c.IsPeerMFAJailed(name), w)
		}
	}
}

func TestInfraClientsRoundTripJSON(t *testing.T) {
	c := infraCfg("", ProfileVPNOnly)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.VPNInfraClients["box"]; got.Reason != "service box" || got.SetBy != "admin" || got.SetAt != 1 {
		t.Errorf("round trip = %+v", got)
	}
}
