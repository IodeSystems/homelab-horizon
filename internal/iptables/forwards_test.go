package iptables

import (
	"slices"
	"strings"
	"testing"
)

// sprinkInputs is the motivating case: a game server's WebTransport (QUIC)
// port, which HAProxy cannot carry, forwarded from the gateway to a LAN host.
func sprinkInputs() Inputs {
	return Inputs{
		WGInterface:  "wg0",
		OutIface:     "eth0",
		VPNRange:     "10.100.0.0/24",
		LanCIDR:      "192.168.1.0/24",
		ServerWGIP:   "10.100.0.1",
		ListenPort:   "8080",
		HAProxyPorts: []string{"80", "443"},
		Forwards: []ForwardInput{
			{Service: "sprink", Proto: "udp", Port: 4433, BackendIP: "192.168.1.76", BackendPort: 4433},
		},
	}
}

// sprinkRules is the exact rule set a single forward adds, in emitted form.
var sprinkRules = []string{
	"-t nat -A PREROUTING -m addrtype --dst-type LOCAL -j HZ-PREROUTING",
	"-t nat -A POSTROUTING -j HZ-POSTROUTING",
	"-t filter -A FORWARD -j HZ-FORWARD",
	"-t nat -A HZ-PREROUTING -p udp --dport 4433 -j DNAT --to-destination 192.168.1.76:4433",
	"-t nat -A HZ-POSTROUTING -d 192.168.1.76/32 -o eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE",
	"-t filter -A HZ-FORWARD -d 192.168.1.76/32 -i eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
	"-t filter -A HZ-FORWARD -s 192.168.1.76/32 -o eth0 -p udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
}

func ruleStrings(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.String()
	}
	return out
}

func TestForwardRulesSprinkExact(t *testing.T) {
	got := ruleStrings(forwardRules(sprinkInputs()))
	if !slices.Equal(got, sprinkRules) {
		t.Fatalf("forward rules differ\n got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(sprinkRules, "\n  "))
	}
}

// TestForwardTouchesOnlyHorizonChainsAndTheFlow is the safety proof at the
// generator level: adding a forward changes no existing rule, adds nothing to
// INPUT or any chain but the three HZ-* chains and one jump into each, and
// every HZ-* rule is scoped to the named flow.
func TestForwardTouchesOnlyHorizonChainsAndTheFlow(t *testing.T) {
	without := sprinkInputs()
	without.Forwards = nil
	base := ExpectedRules(without)
	with := ExpectedRules(sprinkInputs())

	baseSet := map[string]bool{}
	for _, r := range base {
		baseSet[r.Canonical()] = true
	}
	withSet := map[string]bool{}
	for _, r := range with {
		withSet[r.Canonical()] = true
	}
	for c := range baseSet {
		if !withSet[c] {
			t.Errorf("adding a forward removed an existing rule: %s", c)
		}
	}

	jumps := map[string]bool{}
	for _, r := range ForwardJumpRules() {
		jumps[r.Canonical()] = true
	}
	hzChains := map[string]bool{PreroutingChainName: true, PostroutingChainName: true, ForwardsChainName: true}

	var added []Rule
	for _, r := range with {
		if !baseSet[r.Canonical()] {
			added = append(added, r)
		}
	}
	if got := ruleStrings(added); !slices.Equal(got, sprinkRules) {
		t.Fatalf("a forward must add exactly the sprink rule set\n got:\n  %s", strings.Join(got, "\n  "))
	}

	for _, r := range added {
		switch {
		case hzChains[r.Chain]:
			body := strings.Join(r.Args, " ")
			for _, must := range []string{"192.168.1.76", "4433", "-p udp"} {
				if !strings.Contains(body, must) {
					t.Errorf("%s rule not scoped to the flow (missing %q): %s", r.Chain, must, r)
				}
			}
		case jumps[r.Canonical()]:
			// one of the three jumps
		default:
			t.Errorf("forward emitted a rule outside horizon's chains: %s", r)
		}
		if r.Chain == "INPUT" || r.Chain == InputChainName || r.Chain == "OUTPUT" {
			t.Errorf("forward must never emit %s rules: %s", r.Chain, r)
		}
		for _, a := range r.Args {
			if a == "-P" || a == "--policy" || a == "-F" || a == "-X" {
				t.Errorf("rule body carries a chain-level operation %q: %s", a, r)
			}
		}
	}
}

// TestForwardGeneratorRefusesUnsafe: the generator re-checks every forward and
// emits nothing for one that validation should have rejected, so a hand-edited
// or peer-synced config still cannot point it at SSH or horizon's own ports.
func TestForwardGeneratorRefusesUnsafe(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"ssh tcp/22", func(in *Inputs) { in.Forwards[0].Proto, in.Forwards[0].Port = "tcp", 22 }},
		{"ssh udp/22", func(in *Inputs) { in.Forwards[0].Port = 22 }},
		{"dns 53", func(in *Inputs) { in.Forwards[0].Port = 53 }},
		{"http 80", func(in *Inputs) { in.Forwards[0].Port = 80 }},
		{"https 443", func(in *Inputs) { in.Forwards[0].Port = 443 }},
		{"horizon listen port", func(in *Inputs) { in.Forwards[0].Port = 8080 }},
		{"configured haproxy port", func(in *Inputs) {
			in.HAProxyPorts = append(in.HAProxyPorts, "8443")
			in.Forwards[0].Port = 8443
		}},
		{"reserved wireguard port", func(in *Inputs) {
			in.ReservedPorts = map[int]string{51820: "wireguard"}
			in.Forwards[0].Port = 51820
		}},
		{"bad proto", func(in *Inputs) { in.Forwards[0].Proto = "icmp" }},
		{"public port out of range", func(in *Inputs) { in.Forwards[0].Port = 70000 }},
		{"backend port zero", func(in *Inputs) { in.Forwards[0].BackendPort = 0 }},
		{"backend outside LAN", func(in *Inputs) { in.Forwards[0].BackendIP = "10.0.0.5" }},
		{"backend loopback", func(in *Inputs) {
			in.LanCIDR = "127.0.0.0/8"
			in.Forwards[0].BackendIP = "127.0.0.1"
		}},
		{"backend not an IP", func(in *Inputs) { in.Forwards[0].BackendIP = "game.lan" }},
		{"backend IPv6", func(in *Inputs) { in.Forwards[0].BackendIP = "fd00::76" }},
		{"no out interface", func(in *Inputs) { in.OutIface = "" }},
		{"no LAN CIDR", func(in *Inputs) { in.LanCIDR = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := sprinkInputs()
			c.mutate(&in)
			if got := forwardRules(in); len(got) != 0 {
				t.Fatalf("unsafe forward produced rules:\n  %s", strings.Join(ruleStrings(got), "\n  "))
			}
			for _, r := range ExpectedRules(in) {
				if strings.HasPrefix(r.Chain, "HZ-") || strings.Contains(strings.Join(r.Args, " "), "HZ-") {
					t.Fatalf("ExpectedRules still references a forward chain: %s", r)
				}
			}
		})
	}
}

// One bad forward must not take the good ones with it.
func TestForwardGeneratorSkipsOnlyTheUnsafeEntry(t *testing.T) {
	in := sprinkInputs()
	in.Forwards = append([]ForwardInput{{Proto: "tcp", Port: 22, BackendIP: "192.168.1.76", BackendPort: 22}}, in.Forwards...)
	if got := ruleStrings(forwardRules(in)); !slices.Equal(got, sprinkRules) {
		t.Fatalf("want only the sprink rules, got:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestForwardGeneratorDedupesSharedBackend(t *testing.T) {
	in := sprinkInputs()
	// Two public ports onto one backend: two DNATs, one MASQUERADE, one pair
	// of accepts. Duplicates would read as permanent drift.
	in.Forwards = append(in.Forwards, ForwardInput{Proto: "udp", Port: 4434, BackendIP: "192.168.1.76", BackendPort: 4433})
	got := forwardRules(in)
	if n := len(filterChain(got, "nat", PreroutingChainName)); n != 2 {
		t.Errorf("want 2 DNAT rules, got %d", n)
	}
	if n := len(filterChain(got, "nat", PostroutingChainName)); n != 1 {
		t.Errorf("want 1 MASQUERADE rule, got %d", n)
	}
	if n := len(filterChain(got, "filter", ForwardsChainName)); n != 2 {
		t.Errorf("want 2 ACCEPT rules, got %d", n)
	}
}

// gatewaySave is iptables-save output from a Docker host running horizon with
// the sprink forward installed. The HZ-* and jump lines are the readback forms
// captured from iptables-nft and iptables-legacy 1.8.10 after inserting exactly
// the rules in sprinkRules (identical under both backends).
const gatewaySaveNat = `*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
:DOCKER - [0:0]
:HZ-POSTROUTING - [0:0]
:HZ-PREROUTING - [0:0]
-A PREROUTING -m addrtype --dst-type LOCAL -j HZ-PREROUTING
-A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER
-A OUTPUT ! -d 127.0.0.0/8 -m addrtype --dst-type LOCAL -j DOCKER
-A POSTROUTING -j HZ-POSTROUTING
-A POSTROUTING -s 172.17.0.0/16 ! -o docker0 -j MASQUERADE
-A POSTROUTING -o eth0 -j MASQUERADE
-A HZ-POSTROUTING -d 192.168.1.76/32 -o eth0 -p udp -m udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE
-A HZ-PREROUTING -p udp -m udp --dport 4433 -j DNAT --to-destination 192.168.1.76:4433
COMMIT
`

const gatewaySaveFilter = `*filter
:INPUT ACCEPT [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
:DOCKER-FORWARD - [0:0]
:DOCKER-USER - [0:0]
:HZ-FORWARD - [0:0]
:WG-FORWARD - [0:0]
:WG-INPUT - [0:0]
-A INPUT -i wg0 -j WG-INPUT
-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
-A FORWARD -j DOCKER-USER
-A FORWARD -j DOCKER-FORWARD
-A FORWARD -j HZ-FORWARD
-A FORWARD -i wg0 -j WG-FORWARD
-A FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A HZ-FORWARD -d 192.168.1.76/32 -i eth0 -p udp -m udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT
-A HZ-FORWARD -s 192.168.1.76/32 -o eth0 -p udp -m udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT
-A WG-FORWARD -j DROP
COMMIT
`

func gatewayLive() []Rule {
	nat := parseIptablesSave(gatewaySaveNat, "nat", liveNatChains)
	filter := parseIptablesSave(gatewaySaveFilter, "filter", liveFilterChains)
	return scopeLiveRules(append(nat, filter...))
}

// TestForwardReadbackRoundTrip: what iptables-save prints for the installed
// forward classifies as expected, rule for rule. If this drifts, every
// reconcile tick flushes and rebuilds the forward chains.
func TestForwardReadbackRoundTrip(t *testing.T) {
	live := gatewayLive()
	expected := ExpectedRules(sprinkInputs())
	classified := Classify(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil)

	liveSet := map[string]bool{}
	for _, c := range classified {
		liveSet[c.Rule.Canonical()] = true
		isForward := strings.HasPrefix(c.Rule.Chain, "HZ-") || strings.Contains(strings.Join(c.Rule.Args, " "), "HZ-")
		if isForward && c.State != StateExpected {
			t.Errorf("readback of a forward rule classified %s: %s", c.State, c.Rule)
		}
	}
	for _, r := range forwardRules(sprinkInputs()) {
		if !liveSet[r.Canonical()] {
			t.Errorf("emitted rule has no matching readback: %s", r)
		}
	}
	for _, ref := range ownedChains[2:] {
		if chainDrifted(filterChain(live, ref.Table, ref.Chain), filterChain(expected, ref.Table, ref.Chain)) {
			t.Errorf("%s reads as drifted against its own readback", ref.Chain)
		}
	}

	// Docker's own rules stay unknown; its PREROUTING jump and the INPUT ssh
	// rule are outside the read scope entirely.
	s := SummarizeClassified(classified)
	if s.Unknown != 3 {
		t.Errorf("want 3 unknown (two docker FORWARD jumps, one docker MASQUERADE), got %+v", s)
	}
	for _, r := range live {
		if slices.Contains(r.Args, "DOCKER") && r.Chain == "PREROUTING" {
			t.Errorf("docker PREROUTING rule leaked into the read scope: %s", r)
		}
		if r.Chain == "INPUT" && !jumpsTo(r.Args, InputChainName) {
			t.Errorf("unrelated INPUT rule leaked into the read scope: %s", r)
		}
	}
}
