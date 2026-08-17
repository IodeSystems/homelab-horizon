package wireguard

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// rulesFor is what rebuildChain would apply, without touching the kernel.
func rulesFor(chain string, opts ForwardChainOpts) []string {
	var out []string
	for _, r := range iptables.ExpectedRules(opts.expectedRulesInputs()) {
		if r.Chain != chain || r.Table != "filter" {
			continue
		}
		out = append(out, strings.Join(r.Args, " "))
	}
	return out
}

func baseOpts() ForwardChainOpts {
	return ForwardChainOpts{
		Peers:       []Peer{{Name: "laptop", AllowedIPs: "10.100.0.2/32"}},
		Profiles:    map[string]string{},
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     "192.168.1.0/24",
		JailedPeers: map[string]bool{},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
	}
}

// The per-profile rule sets the hand-written builder used to emit. Pinned here
// because the collapse onto the generator is only correct if these are
// unchanged — a refactor that quietly widened a profile would be a hole.
func TestRebuildForwardChainRulesByProfile(t *testing.T) {
	tests := []struct {
		profile string
		want    []string
	}{
		{"full-tunnel", []string{
			"-s 10.100.0.2/32 -j ACCEPT",
			"-j DROP",
		}},
		{"vpn-only", []string{
			"-s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT",
			"-s 10.100.0.2/32 -j DROP",
			"-j DROP",
		}},
		{"lan-access", []string{
			"-s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT",
			"-s 10.100.0.2/32 -d 192.168.1.0/24 -j ACCEPT",
			"-s 10.100.0.2/32 -j DROP",
			"-j DROP",
		}},
		// Empty profile means lan-access, which the old builder did with a
		// default branch and the generator does with a default case.
		{"", []string{
			"-s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT",
			"-s 10.100.0.2/32 -d 192.168.1.0/24 -j ACCEPT",
			"-s 10.100.0.2/32 -j DROP",
			"-j DROP",
		}},
	}

	for _, tc := range tests {
		t.Run("profile="+tc.profile, func(t *testing.T) {
			opts := baseOpts()
			opts.Profiles["laptop"] = tc.profile
			got := rulesFor(forwardChainName, opts)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("WG-FORWARD =\n  %s\nwant\n  %s",
					strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// The bug this collapse exists to prevent: the jail must appear on BOTH chains.
// It shipped once covering only FORWARD, because the fix went into one builder
// and the other kept emitting the old set.
func TestJailedPeerIsDroppedOnBothChains(t *testing.T) {
	opts := baseOpts()
	opts.JailedPeers["laptop"] = true
	opts.HAProxyPorts = []string{"80", "443"}

	forward := rulesFor(forwardChainName, opts)
	if strings.Join(forward, "\n") != "-s 10.100.0.2/32 -j DROP\n-j DROP" {
		t.Errorf("WG-FORWARD for a jailed peer =\n  %s", strings.Join(forward, "\n  "))
	}

	input := rulesFor(inputChainName, opts)
	wantInput := []string{
		"-s 10.100.0.2/32 -d 10.100.0.1/32 -p tcp --dport 8080 -j ACCEPT",
		"-s 10.100.0.2/32 -d 10.100.0.1/32 -p tcp --dport 80 -j ACCEPT",
		"-s 10.100.0.2/32 -d 10.100.0.1/32 -p tcp --dport 443 -j ACCEPT",
		"-s 10.100.0.2/32 -d 10.100.0.1/32 -p udp --dport 53 -j ACCEPT",
		"-s 10.100.0.2/32 -d 10.100.0.1/32 -p tcp --dport 53 -j ACCEPT",
		"-s 10.100.0.2/32 -j DROP",
	}
	if strings.Join(input, "\n") != strings.Join(wantInput, "\n") {
		t.Errorf("WG-INPUT for a jailed peer =\n  %s\nwant\n  %s",
			strings.Join(input, "\n  "), strings.Join(wantInput, "\n  "))
	}
}

// Fail open when the portal cannot be named: emitting the DROPs without the
// matching ACCEPTs would strand the peer with no route to the page that
// un-jails it.
func TestJailFailsOpenWithoutAPortalAddress(t *testing.T) {
	for _, missing := range []string{"serverWGIP", "listenPort"} {
		opts := baseOpts()
		opts.JailedPeers["laptop"] = true
		if missing == "serverWGIP" {
			opts.ServerWGIP = ""
		} else {
			opts.ListenPort = ""
		}

		if input := rulesFor(inputChainName, opts); len(input) != 0 {
			t.Errorf("missing %s: WG-INPUT should be empty, got %v", missing, input)
		}

		// Asserted on the ACCEPTs, not on the absence of a DROP: a jailed
		// peer's drop and lan-access's trailing drop render as the same string,
		// and only the accepts in front of it distinguish "cut off" from
		// "allowed the LAN then denied the rest".
		forward := rulesFor(forwardChainName, opts)
		accepts := 0
		for _, r := range forward {
			if strings.HasSuffix(r, "-j ACCEPT") {
				accepts++
			}
		}
		if accepts == 0 {
			t.Errorf("missing %s: peer has no accepts, so it was jailed anyway: %v", missing, forward)
		}
	}
}

// A chain body never mentions the interface, so these entry points must work
// without one — but the generator refuses to emit anything when it is empty.
func TestChainBodyRebuildWorksWithoutAnInterface(t *testing.T) {
	opts := baseOpts()
	opts.WGInterface = ""
	if got := rulesFor(forwardChainName, opts); len(got) == 0 {
		t.Fatal("no rules generated without an explicit interface")
	}

	// And an explicit one is honoured rather than overridden.
	opts.WGInterface = "wg7"
	if got := opts.expectedRulesInputs().WGInterface; got != "wg7" {
		t.Fatalf("WGInterface = %q, want wg7", got)
	}
}

// The jump rules belong to the reconciler, which installs them in FORWARD and
// INPUT proper. A chain-body rebuild must not try to apply them.
func TestChainBodyRebuildSkipsTheJumpRules(t *testing.T) {
	opts := baseOpts()
	for _, chain := range []string{forwardChainName, inputChainName} {
		for _, r := range rulesFor(chain, opts) {
			if strings.Contains(r, "-i wg") || strings.Contains(r, "conntrack") {
				t.Errorf("%s body contains a jump/state rule: %q", chain, r)
			}
		}
	}
}
