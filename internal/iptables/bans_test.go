package iptables

import (
	"strings"
	"testing"
)

// The addresses below are documentation ranges (RFC 5737). 192.0.2.5 is the
// one hz banned and therefore generates a rule for; 198.51.100.7 is the one a
// human typed at the shell and hz has never heard of. Keeping them apart is
// the whole point of these tests.
const (
	hzBannedIP   = "192.0.2.5"
	handBannedIP = "198.51.100.7"
)

// banSaveNat / banSaveFilter are `iptables-save` output from a gateway with
// no port forwards, one lan-access peer, one hz ban and one hand-added DROP.
//
// The INPUT block is the interesting part and each line is there for a reason:
//
//	-i wg0 -j WG-INPUT       hz's own jump — visible before this change
//	-s 192.0.2.5/32 -j DROP  a ban hz recorded in cfg.IPBans
//	-s 198.51.100.7/32 -j DROP  a DROP a human added by hand at the shell
//	-p tcp --dport 22 ACCEPT an ordinary host rule that must STAY invisible
const banSaveNat = `*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
-A POSTROUTING -o eth0 -j MASQUERADE
COMMIT
`

const banSaveFilter = `*filter
:INPUT ACCEPT [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
:WG-FORWARD - [0:0]
:WG-INPUT - [0:0]
-A INPUT -i wg0 -j WG-INPUT
-A INPUT -s 192.0.2.5/32 -j DROP
-A INPUT -s 198.51.100.7/32 -j DROP
-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
-A FORWARD -i wg0 -j WG-FORWARD
-A FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A WG-FORWARD -s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT
-A WG-FORWARD -s 10.100.0.2/32 -d 192.168.1.0/24 -j ACCEPT
-A WG-FORWARD -s 10.100.0.2/32 -j DROP
-A WG-FORWARD -j DROP
COMMIT
`

// banGatewayLive is the same read path LiveRules uses: parse, then scope.
func banGatewayLive() []Rule {
	nat := parseIptablesSave(banSaveNat, "nat", liveNatChains)
	filter := parseIptablesSave(banSaveFilter, "filter", liveFilterChains)
	return scopeLiveRules(append(nat, filter...))
}

// banGatewayInputs is what hz wants on that gateway: one banned address.
func banGatewayInputs(bannedIPs ...string) Inputs {
	return Inputs{
		WGInterface: "wg0",
		OutIface:    "eth0",
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     "192.168.1.0/24",
		Peers:       []PeerInput{{Name: "laptop", IP: "10.100.0.2"}},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
		BannedIPs:   bannedIPs,
	}
}

func canonicals(rules []Rule) map[string]bool {
	out := make(map[string]bool, len(rules))
	for _, r := range rules {
		out[r.Canonical()] = true
	}
	return out
}

func stateOf(t *testing.T, classified []ClassifiedRule, canon string) State {
	t.Helper()
	for _, c := range classified {
		if c.Rule.Canonical() == canon {
			return c.State
		}
	}
	t.Fatalf("rule %q is not in the classified set — it was never read:\n%v", canon, classified)
	return ""
}

// TestLiveScopeAdmitsSourceDropsInInput is the widening itself, at the read.
//
// Before the widening both DROPs were dropped on the floor by scopeLiveRules
// and this test fails on the hz ban and the hand-added one alike. The ssh
// ACCEPT is the control in the other direction: the narrowing that keeps a
// host's ufw/docker INPUT rules out of the tab is still in force.
func TestLiveScopeAdmitsSourceDropsInInput(t *testing.T) {
	live := canonicals(banGatewayLive())

	for _, want := range []string{
		"filter|INPUT|-i wg0 -j WG-INPUT",
		"filter|INPUT|-s " + hzBannedIP + "/32 -j DROP",
		"filter|INPUT|-s " + handBannedIP + "/32 -j DROP",
	} {
		if !live[want] {
			t.Errorf("INPUT rule not read: %q", want)
		}
	}
	if live["filter|INPUT|-p tcp --dport 22 -j ACCEPT"] {
		t.Error("an unrelated INPUT rule leaked into the read scope — the narrowing that keeps ufw/docker out of the tab is gone")
	}
}

// TestBanRuleReadbackRoundTrip guards the dup-insert failure mode. hz inserts
// `-s <addr> -j DROP`; the kernel prints it back as `-s <addr>/32 -j DROP`. If
// the generator emitted the bare address the two canonical forms would differ,
// the live rule would never match the expected one, and Reconcile would insert
// a second copy on every pass.
func TestBanRuleReadbackRoundTrip(t *testing.T) {
	expected := canonicals(ExpectedRules(banGatewayInputs(hzBannedIP)))
	readback := "filter|INPUT|-s " + hzBannedIP + "/32 -j DROP"
	if !expected[readback] {
		t.Errorf("generated ban rule does not match its own readback form %q; got:\n%v", readback, expected)
	}
}

// TestConfiguredBanClassifiesExpected: a ban hz recorded is hz's own rule.
func TestConfiguredBanClassifiesExpected(t *testing.T) {
	live := banGatewayLive()
	expected := ExpectedRules(banGatewayInputs(hzBannedIP))
	classified := Classify(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil)

	if got := stateOf(t, classified, "filter|INPUT|-s "+hzBannedIP+"/32 -j DROP"); got != StateExpected {
		t.Errorf("a ban in cfg.IPBans classified %q, want %q", got, StateExpected)
	}
}

// TestHandAddedInputDropClassifiesUnknown: the newly-visible rule hz does not
// generate lands in the bucket Reconcile never touches.
func TestHandAddedInputDropClassifiesUnknown(t *testing.T) {
	live := banGatewayLive()
	expected := ExpectedRules(banGatewayInputs(hzBannedIP))
	classified := Classify(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil)

	if got := stateOf(t, classified, "filter|INPUT|-s "+handBannedIP+"/32 -j DROP"); got != StateUnknown {
		t.Errorf("a hand-added INPUT DROP classified %q, want %q", got, StateUnknown)
	}
}

// TestReconcileLeavesAHandAddedInputDropAlone is the safety property of the
// whole change, proved at the command level rather than at the classifier.
//
// Widening the read makes a rule hz has never seen visible for the first time.
// The thing that must not happen is that visibility turning into a deletion:
// the rule is somebody's, hz did not write it, and Reconcile removes only what
// it classifies as stale. This asserts on the iptables commands Reconcile
// actually issues, so a future classifier that ranked the rule differently
// would still be caught here.
func TestReconcileLeavesAHandAddedInputDropAlone(t *testing.T) {
	calls := recordIptables(t)
	live := banGatewayLive()
	expected := ExpectedRules(banGatewayInputs(hzBannedIP))

	report := Reconcile(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil, "eth0", "eth0")
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	assertOnlyHorizonCommands(t, *calls, builtinRules(expected))

	handAdded := "-s " + handBannedIP + " -j DROP"
	for _, line := range joined(*calls) {
		if strings.Contains(line, handBannedIP) {
			t.Errorf("Reconcile touched a rule it did not write: %s", line)
		}
		if strings.Contains(line, " -D ") {
			t.Errorf("Reconcile deleted something on an in-sync gateway: %s", line)
		}
	}
	for _, r := range report.Deleted {
		if r.Chain == "INPUT" {
			t.Errorf("report claims an INPUT deletion: %s", r)
		}
	}

	// And it is surfaced, not swallowed: the admin can see it in the tab and
	// remove it deliberately. Left alone is not the same as unreported.
	var surfaced bool
	for _, c := range report.LeftAlone {
		if strings.Contains(strings.Join(c.Rule.Args, " "), handBannedIP) {
			surfaced = true
			if c.State != StateUnknown {
				t.Errorf("hand-added DROP surfaced as %q, want %q", c.State, StateUnknown)
			}
		}
	}
	if !surfaced {
		t.Errorf("hand-added %q is invisible to the admin: not in report.LeftAlone", handAdded)
	}
}

// TestReconcileDoesNotReinstallABanItCanSee is the failure the audit predicted
// for moving bans before widening the read: an already-installed ban that the
// reconciler cannot see is "missing expected", so it gets inserted again, and
// again, on every pass.
func TestReconcileDoesNotReinstallABanItCanSee(t *testing.T) {
	calls := recordIptables(t)
	live := banGatewayLive()
	expected := ExpectedRules(banGatewayInputs(hzBannedIP))

	Reconcile(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil, "eth0", "eth0")

	for _, line := range joined(*calls) {
		if strings.Contains(line, "-I INPUT") {
			t.Errorf("re-inserted a ban that is already installed: %s", line)
		}
	}
}

// TestReconcileInstallsAMissingBan is the other half: hz's ban list is now a
// source for the generator, so a ban whose rule was wiped (a ufw reload, a
// flush, a reboot before reapplyBans ran) is healed on the next pass.
func TestReconcileInstallsAMissingBan(t *testing.T) {
	calls := recordIptables(t)
	var live []Rule
	for _, r := range banGatewayLive() {
		if strings.Contains(strings.Join(r.Args, " "), hzBannedIP) {
			continue // the ban rule is gone from the kernel
		}
		live = append(live, r)
	}
	expected := ExpectedRules(banGatewayInputs(hzBannedIP))

	report := Reconcile(live, expected, StaleRules(configWithoutPriorState(), nil, "", ""), nil, "eth0", "eth0")
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	assertOnlyHorizonCommands(t, *calls, builtinRules(expected))

	want := "-t filter -I INPUT 1 -s " + hzBannedIP + "/32 -j DROP"
	var seen int
	for _, line := range joined(*calls) {
		if line == want {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("want exactly one %q, got %d in:\n  %s", want, seen, strings.Join(joined(*calls), "\n  "))
	}
}

// TestBanRemovedFromConfigIsUnknownNotStale pins the answer to the question
// this change raises: making bans EXPECTED does not make an unbanned one
// STALE.
//
// Stale is a narrow thing — what hz would have generated under the previous
// iface/CIDR (StaleRules) — and nothing derives it from the ban list. So a
// live ban rule whose config entry is gone falls to unknown and Reconcile
// leaves it in place. Removing the rule stays unbanIP's job, explicitly.
//
// That is deliberate, and it is the conservative half of the choice: the
// alternative (a removal from cfg.IPBans deleting a live INPUT rule) would
// widen what Reconcile deletes, which is exactly what this change must not do.
func TestBanRemovedFromConfigIsUnknownNotStale(t *testing.T) {
	calls := recordIptables(t)
	live := banGatewayLive()
	expected := ExpectedRules(banGatewayInputs()) // the ban was lifted in config
	stale := StaleRules(configWithoutPriorState(), nil, "", "")

	classified := Classify(live, expected, stale, nil)
	if got := stateOf(t, classified, "filter|INPUT|-s "+hzBannedIP+"/32 -j DROP"); got != StateUnknown {
		t.Errorf("an unbanned address's live rule classified %q, want %q", got, StateUnknown)
	}

	Reconcile(live, expected, stale, nil, "eth0", "eth0")
	for _, line := range joined(*calls) {
		if strings.Contains(line, hzBannedIP) {
			t.Errorf("Reconcile removed a ban rule because the config entry went away: %s", line)
		}
	}
}

// TestBanRulesNeedTheWGInterface: ExpectedRules returns nothing at all without
// a WireGuard interface, and bans must not change that. iptablesSectionFor
// reads an empty expected set as "hz manages no firewall on this box" and
// publishes no section; a lone ban rule would flip a WG-less machine into
// managed, which is a much bigger claim than the ban.
func TestBanRulesNeedTheWGInterface(t *testing.T) {
	in := banGatewayInputs(hzBannedIP)
	in.WGInterface = ""
	if got := ExpectedRules(in); len(got) != 0 {
		t.Errorf("want no rules without a WG interface, got:\n%v", got)
	}
}

// TestBanRulesSkipWhatIptablesCannotInstall. `iptables` is IPv4 only, so a v6
// (or malformed) entry in cfg.IPBans has no live rule and never will — hz's
// own insert fails for one. Emitting it would show as a permanently missing
// expected rule and a failed add on every pass.
func TestBanRulesSkipWhatIptablesCannotInstall(t *testing.T) {
	rules := ExpectedRules(banGatewayInputs("2001:db8::1", "", "not-an-ip", hzBannedIP))
	var inputDrops int
	for _, r := range rules {
		if r.Chain == "INPUT" && r.Args[0] == "-s" {
			inputDrops++
		}
	}
	if inputDrops != 1 {
		t.Errorf("want 1 ban rule (the IPv4 one), got %d in:\n%v", inputDrops, rules)
	}
}

// TestSourceDropShapeIsExact is the predicate the widened read turns on. It
// admits the ban shape and nothing else: a narrow miss here is a flood of
// unknowns in the IPTables tab, which is what the original narrowing existed
// to prevent.
func TestSourceDropShapeIsExact(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"hz's ban readback", []string{"-s", "192.0.2.5/32", "-j", "DROP"}, true},
		{"a hand-added subnet DROP", []string{"-s", "192.0.2.0/24", "-j", "DROP"}, true},
		{"a bare address (pre-readback form)", []string{"-s", "192.0.2.5", "-j", "DROP"}, true},
		{"source ACCEPT", []string{"-s", "192.0.2.5/32", "-j", "ACCEPT"}, false},
		{"DROP with no source", []string{"-j", "DROP"}, false},
		{"destination DROP", []string{"-d", "192.0.2.5/32", "-j", "DROP"}, false},
		{"negated source DROP", []string{"!", "-s", "192.0.2.5/32", "-j", "DROP"}, false},
		{"source DROP with extra matchers", []string{"-s", "192.0.2.5/32", "-p", "tcp", "-j", "DROP"}, false},
		{"ufw ssh rule", []string{"-p", "tcp", "--dport", "22", "-j", "ACCEPT"}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSourceDropShape(c.args); got != c.want {
				t.Errorf("isSourceDropShape(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}
