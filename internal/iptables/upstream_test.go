package iptables

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The `upstream` profile (config.ProfileUpstream, plan/plan.md Tier 1b N1b): a
// nested hz reaching its parent's API and nothing else.

// upstreamInputs is a gateway with everything a wider rule could leak to: a LAN,
// a VPN range with other clients on it, HAProxy on 80/443 and hz on 8080.
func upstreamInputs() Inputs {
	return Inputs{
		WGInterface:  "wg0",
		OutIface:     "eth0",
		VPNRange:     "10.100.0.0/24",
		LanCIDR:      "192.168.1.0/24",
		ServerWGIP:   "10.100.0.1",
		ListenPort:   "8080",
		HAProxyPorts: []string{"80", "443"},
		Peers: []PeerInput{
			{Name: "alice-phone", AllowedIPs: "10.100.0.2/32"},
			{Name: "prod-hz", AllowedIPs: "10.100.0.7/32"},
		},
		Profiles: map[string]string{"prod-hz": config.ProfileUpstream},
	}
}

// Byte for byte: every rule the generator emits for a gateway with one
// lan-access client and one upstream client. The upstream client gets exactly
// three rules — the API port ACCEPT and a DROP on INPUT, a DROP on FORWARD.
func TestUpstreamRulesExact(t *testing.T) {
	got := ExpectedRules(upstreamInputs())
	want := []string{
		"nat|POSTROUTING|-o eth0 -j MASQUERADE",
		"filter|FORWARD|-i wg0 -j WG-FORWARD",
		"filter|FORWARD|-o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"filter|INPUT|-i wg0 -j WG-INPUT",
		"filter|WG-FORWARD|-s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT",
		"filter|WG-FORWARD|-s 10.100.0.2/32 -d 192.168.1.0/24 -j ACCEPT",
		"filter|WG-FORWARD|-s 10.100.0.2/32 -j DROP",
		"filter|WG-INPUT|-s 10.100.0.7/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT",
		"filter|WG-INPUT|-s 10.100.0.7/32 -j DROP",
		"filter|WG-FORWARD|-s 10.100.0.7/32 -j DROP",
		"filter|WG-FORWARD|-j DROP",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d rules, got %d:\n%s", len(want), len(got), canonList(got))
	}
	for i, w := range want {
		if got[i].Canonical() != w {
			t.Errorf("rule[%d] = %q, want %q", i, got[i].Canonical(), w)
		}
	}
}

// The property, stated apart from the golden so a reordering that keeps it is
// not mistaken for a hole and a hole is not mistaken for a reordering: the only
// ACCEPT whose source is the upstream client is the API port, on INPUT, to the
// gateway's own WG address — nothing forwarded, no HAProxy, no DNS.
func TestUpstreamAdmitsOnlyTheAPIPort(t *testing.T) {
	var accepts []Rule
	for _, r := range ExpectedRules(upstreamInputs()) {
		args := strings.Join(r.Args, " ")
		if !strings.HasPrefix(args, "-s 10.100.0.7/32 ") {
			continue
		}
		if r.Chain == ForwardChainName && !strings.HasSuffix(args, "-j DROP") {
			t.Errorf("an upstream client has a non-DROP forward rule: %s", r)
		}
		if strings.HasSuffix(args, "-j ACCEPT") {
			accepts = append(accepts, r)
		}
		for _, leak := range []string{"192.168.1.0/24", "10.100.0.0/24", "--dport 80 ", "--dport 443 ", "--dport 53 "} {
			if strings.Contains(args+" ", leak) {
				t.Errorf("an upstream client's rule reaches %q: %s", strings.TrimSpace(leak), r)
			}
		}
	}
	if len(accepts) != 1 || accepts[0].Chain != InputChainName ||
		strings.Join(accepts[0].Args, " ") != "-s 10.100.0.7/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT" {
		t.Fatalf("want exactly the API-port ACCEPT on WG-INPUT, got:\n%s", canonList(accepts))
	}
}

// Fails CLOSED: with no gateway address or no hz port there is nothing to
// admit, and the DROPs still stand. (The MFA jail fails open; see
// upstreamRules for why this one must not.)
func TestUpstreamFailsClosedWithoutTheAPIAddress(t *testing.T) {
	for label, mutate := range map[string]func(*Inputs){
		"no server WG IP": func(in *Inputs) { in.ServerWGIP = "" },
		"no listen port":  func(in *Inputs) { in.ListenPort = "" },
	} {
		in := upstreamInputs()
		mutate(&in)
		var mine []string
		for _, r := range ExpectedRules(in) {
			if strings.HasPrefix(strings.Join(r.Args, " "), "-s 10.100.0.7/32 ") {
				mine = append(mine, r.Canonical())
			}
		}
		want := []string{
			"filter|WG-INPUT|-s 10.100.0.7/32 -j DROP",
			"filter|WG-FORWARD|-s 10.100.0.7/32 -j DROP",
		}
		if strings.Join(mine, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: want only the DROPs, got:\n%s", label, strings.Join(mine, "\n"))
		}
	}
}

// MFA: an upstream client is never jailed — so, rendered through the same glue
// the server uses (cfg.GetJailedPeers), MFA enforced in the strictest scope
// produces no jail rule for it, while the human client beside it IS jailed.
// The human is the positive control: without it, "no jail rules" could mean
// MFA simply was not on.
func TestAnUpstreamClientIsNeverJailed(t *testing.T) {
	cfg := &config.Config{
		VPNMFAEnabled: true,
		VPNMFAScope:   config.MFAScopeAll,
		// An admin entry too: in scope "all" admins get no bypass, so the
		// exemption must come from the profile, not from being an admin.
		VPNAdmins: []string{"prod-hz"},
		WGPeers: []config.WGPeer{
			{Name: "alice-phone", AllowedIPs: "10.100.0.2/32"},
			{Name: "prod-hz", AllowedIPs: "10.100.0.7/32"},
		},
		VPNProfiles: map[string]string{"prod-hz": config.ProfileUpstream},
	}
	jailed := cfg.GetJailedPeers()
	if !jailed["alice-phone"] {
		t.Fatal("positive control: the human client with no MFA session must be jailed")
	}
	if jailed["prod-hz"] || cfg.IsPeerMFAJailed("prod-hz") {
		t.Fatal("an upstream client was jailed")
	}
	if len(jailed) != 1 {
		t.Fatalf("the jailed count (what the metrics gauge reports) counts the upstream client: %v", jailed)
	}
	for _, ip := range cfg.JailedPeerIPs() {
		if ip == "10.100.0.7" {
			t.Fatal("the upstream client's address reached the HAProxy jail ACL")
		}
	}

	in := upstreamInputs()
	in.JailedPeers = jailed
	in.Profiles = cfg.VPNProfiles
	var upstream, human []string
	for _, r := range ExpectedRules(in) {
		switch args := strings.Join(r.Args, " "); {
		case strings.HasPrefix(args, "-s 10.100.0.7/32 "):
			upstream = append(upstream, r.Canonical())
		case strings.HasPrefix(args, "-s 10.100.0.2/32 "):
			human = append(human, r.Canonical())
		}
	}
	for _, r := range upstream {
		if strings.Contains(r, "--dport 53") || strings.Contains(r, "--dport 443") {
			t.Errorf("a jail rule was emitted for the upstream client: %s", r)
		}
	}
	if len(upstream) != 3 {
		t.Errorf("the upstream client's rules changed under MFA:\n%s", strings.Join(upstream, "\n"))
	}
	if !strings.Contains(strings.Join(human, "\n"), "--dport 53") {
		t.Errorf("positive control: the jailed human has no jail rules:\n%s", strings.Join(human, "\n"))
	}
}

func canonList(rs []Rule) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Canonical()
	}
	return strings.Join(out, "\n")
}
