package server

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestCheckPeerTag(t *testing.T) {
	for _, c := range []struct {
		name, tag, reason string
		exists            bool
		profile, wantErr  string
	}{
		{"box", "infra", "AI/dev box: services over the VPN only", true, config.ProfileVPNOnly, ""},
		{"box", "user", "", true, config.ProfileLanAccess, ""}, // back to user: always allowed
		{"box", "infra", "", true, config.ProfileVPNOnly, "reason required"},
		{"box", "infra", "r", true, config.ProfileLanAccess, "must be vpn-only"},
		{"box", "infra", "r", true, "", "must be vpn-only"}, // "" is lan-access
		{"box", "infra", "r", true, config.ProfileFullTunnel, "must be vpn-only"},
		{"box", "admin", "r", true, config.ProfileVPNOnly, "tag must be"},
		{"ghost", "infra", "r", false, config.ProfileVPNOnly, "no VPN client"},
		{"", "infra", "r", true, config.ProfileVPNOnly, "name required"},
	} {
		err := checkPeerTag(c.name, c.tag, c.reason, c.exists, c.profile)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%+v: unexpected error %v", c, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%+v: error %v, want %q", c, err, c.wantErr)
		}
	}
}

// The report is the owner's view of every VPN client that skips MFA. It reads
// the jail's own decision, so it lists exactly the clients the jail spares.
func TestMFAExemptReportListsEveryClientThatSkipsMFA(t *testing.T) {
	now := time.Now().Unix()
	cfg := &config.Config{
		VPNMFAEnabled: true,
		VPNMFAScope:   config.MFAScopeAll,
		VPNProfiles: map[string]string{
			"box": config.ProfileVPNOnly, "moved": config.ProfileLanAccess, "nested": config.ProfileUpstream,
		},
		VPNMFASessions: map[string]int64{"phone": now + 3600, "tablet": 0},
	}
	cfg.SetInfraClient("box", config.InfraClient{Reason: "service box", SetBy: "carl", SetAt: 1794000000})
	cfg.SetInfraClient("moved", config.InfraClient{Reason: "was vpn-only"})
	peers := []string{"phone", "box", "moved", "nested", "tablet", "laptop"}

	exempt, inactive := mfaExemptReport(cfg, peers)
	got := map[string]string{}
	for _, e := range exempt {
		got[e.Name] = e.Why
		if e.Name == "box" && (e.Reason != "service box" || e.SetBy != "carl" || e.SetAt == "") {
			t.Errorf("infra record lost its audit fields: %+v", e)
		}
	}
	want := map[string]string{
		"box": config.MFAExemptInfra, "nested": config.MFAExemptUpstream,
		"phone": config.MFAExemptSession, "tablet": config.MFAExemptSessionForever,
	}
	if len(got) != len(want) {
		t.Errorf("exempt = %v, want %v", got, want)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: why = %q, want %q", n, got[n], w)
		}
	}
	for _, e := range exempt {
		if cfg.IsPeerMFAJailed(e.Name) {
			t.Errorf("%s reported exempt but the jail holds it", e.Name)
		}
	}
	if len(inactive) != 1 || inactive[0] != "moved" {
		t.Errorf("inactive infra tags = %v, want [moved] (tagged, no longer vpn-only)", inactive)
	}

	cfg.VPNMFAEnabled = false
	if e, i := mfaExemptReport(cfg, peers); e != nil || i != nil {
		t.Errorf("MFA off: report = %v / %v, want nothing", e, i)
	}
}

func TestDoctorReportsStandingMFABypasses(t *testing.T) {
	cfg := &config.Config{
		VPNMFAEnabled:  true,
		VPNProfiles:    map[string]string{"box": config.ProfileVPNOnly, "moved": config.ProfileLanAccess},
		VPNMFASessions: map[string]int64{"phone": time.Now().Unix() + 3600, "tablet": 0},
		WGPeers:        []config.WGPeer{{Name: "box"}, {Name: "moved"}, {Name: "phone"}, {Name: "tablet"}, {Name: "laptop"}},
	}
	cfg.SetInfraClient("box", config.InfraClient{Reason: "service box"})
	cfg.SetInfraClient("moved", config.InfraClient{Reason: "r"})
	fs := mfaExemptFindings(cfg)
	got := map[string]string{}
	for _, f := range fs {
		got[f.Subject] = f.Status
	}
	want := map[string]string{"box": "info", "tablet": "info", "moved": "warning"}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want %v (a live session is not a standing bypass)", got, want)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: %q, want %q", n, got[n], w)
		}
	}
}
