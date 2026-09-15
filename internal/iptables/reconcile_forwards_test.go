package iptables

import (
	"slices"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func configWithoutPriorState() *config.Config {
	return &config.Config{WGInterface: "wg0"}
}

// recordIptables swaps the command runner for a recorder for one test.
func recordIptables(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	prev := runIptables
	runIptables = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}
	t.Cleanup(func() { runIptables = prev })
	return &calls
}

func joined(calls [][]string) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

// assertOnlyHorizonCommands is the safety proof at the command level. Every
// iptables invocation Reconcile makes must be one of:
//   - -N / -F / -X / -A on a horizon-owned chain, or
//   - -I <chain> 1 / -D on a built-in chain with a body that is one of the
//     rules horizon emits there (allowedBuiltin).
//
// Never a policy change, never a flush of a chain horizon does not own, never
// a rule in INPUT other than horizon's own jump.
func assertOnlyHorizonCommands(t *testing.T, calls [][]string, allowedBuiltin []Rule) {
	t.Helper()
	allowed := map[string]bool{}
	for _, r := range allowedBuiltin {
		allowed[r.Canonical()] = true
	}
	for _, c := range calls {
		line := strings.Join(c, " ")
		for _, a := range c {
			if a == "-P" || a == "--policy" || a == "-Z" || a == "-E" {
				t.Errorf("forbidden operation %q: %s", a, line)
			}
		}
		if len(c) < 4 || c[0] != "-t" {
			t.Errorf("command without an explicit table and chain: %s", line)
			continue
		}
		table, op, chain := c[1], c[2], c[3]
		owned := isOwnedChain(Rule{Table: table, Chain: chain})
		switch op {
		case "-N", "-F", "-X", "-A":
			if !owned {
				t.Errorf("%s on a chain horizon does not own: %s", op, line)
			}
		case "-I", "-D":
			if owned {
				continue
			}
			body := c[4:]
			if op == "-I" {
				if len(body) == 0 || body[0] != "1" {
					t.Errorf("insert without position 1: %s", line)
					continue
				}
				body = body[1:]
			}
			r := Rule{Table: table, Chain: chain, Args: body}
			if !allowed[r.Canonical()] {
				t.Errorf("edit of a built-in chain with a rule horizon does not emit: %s", line)
			}
		default:
			t.Errorf("unexpected operation %q: %s", op, line)
		}
	}
}

// builtinRules is the subset of rules horizon places in built-in chains.
func builtinRules(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if !isOwnedChain(r) {
			out = append(out, r)
		}
	}
	return out
}

// withoutForwardRules strips the forward rules and chains from a live set, which
// is what the gateway looks like before any forward is configured.
func withoutForwardRules(live []Rule) []Rule {
	var out []Rule
	for _, r := range live {
		if strings.HasPrefix(r.Chain, "HZ-") || strings.Contains(strings.Join(r.Args, " "), "HZ-") {
			continue
		}
		out = append(out, r)
	}
	return out
}

func TestReconcileInstallsForwardInsideHorizonChains(t *testing.T) {
	calls := recordIptables(t)
	expected := ExpectedRules(sprinkInputs())
	live := withoutForwardRules(gatewayLive())

	report := Reconcile(live, expected, ForwardJumpRules(), nil, "eth0", "eth0")
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	assertOnlyHorizonCommands(t, *calls, builtinRules(expected))

	got := joined(*calls)
	for _, want := range []string{
		"-t nat -I PREROUTING 1 -m addrtype --dst-type LOCAL -j HZ-PREROUTING",
		"-t nat -I POSTROUTING 1 -j HZ-POSTROUTING",
		"-t filter -I FORWARD 1 -j HZ-FORWARD",
		"-t nat -A HZ-PREROUTING -p udp --dport 4433 -j DNAT --to-destination 192.168.1.76:4433",
		"-t nat -A HZ-POSTROUTING -d 192.168.1.76/32 -o eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE",
		"-t filter -A HZ-FORWARD -d 192.168.1.76/32 -i eth0 -p udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
		"-t filter -A HZ-FORWARD -s 192.168.1.76/32 -o eth0 -p udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing command %q in:\n  %s", want, strings.Join(got, "\n  "))
		}
	}
	for _, line := range got {
		if strings.Contains(line, " -D ") {
			t.Errorf("installing a forward must delete nothing: %s", line)
		}
		if strings.Contains(line, "DOCKER") || strings.Contains(line, " INPUT ") {
			t.Errorf("installing a forward touched another tool's or INPUT's rules: %s", line)
		}
	}
}

func TestReconcileRemovesLastForwardCompletely(t *testing.T) {
	calls := recordIptables(t)
	in := sprinkInputs()
	in.Forwards = nil
	expected := ExpectedRules(in)

	report := Reconcile(gatewayLive(), expected, ForwardJumpRules(), nil, "eth0", "eth0")
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	assertOnlyHorizonCommands(t, *calls, append(builtinRules(expected), ForwardJumpRules()...))

	got := joined(*calls)
	for _, want := range []string{
		"-t nat -D PREROUTING -m addrtype --dst-type LOCAL -j HZ-PREROUTING",
		"-t nat -D POSTROUTING -j HZ-POSTROUTING",
		"-t filter -D FORWARD -j HZ-FORWARD",
		"-t nat -F HZ-PREROUTING", "-t nat -X HZ-PREROUTING",
		"-t nat -F HZ-POSTROUTING", "-t nat -X HZ-POSTROUTING",
		"-t filter -F HZ-FORWARD", "-t filter -X HZ-FORWARD",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing command %q in:\n  %s", want, strings.Join(got, "\n  "))
		}
	}
	for _, line := range got {
		if strings.Contains(line, " -A HZ-") || strings.Contains(line, " -I ") {
			t.Errorf("removing a forward must add nothing: %s", line)
		}
	}
}

func TestReconcileRebuildsForwardChainsOnChangeWithoutTouchingJumps(t *testing.T) {
	calls := recordIptables(t)
	in := sprinkInputs()
	in.Forwards[0].BackendIP = "192.168.1.77"
	expected := ExpectedRules(in)

	Reconcile(gatewayLive(), expected, ForwardJumpRules(), nil, "eth0", "eth0")
	assertOnlyHorizonCommands(t, *calls, builtinRules(expected))

	got := joined(*calls)
	if !slices.Contains(got, "-t nat -A HZ-PREROUTING -p udp --dport 4433 -j DNAT --to-destination 192.168.1.77:4433") {
		t.Errorf("new DNAT not installed:\n  %s", strings.Join(got, "\n  "))
	}
	for _, line := range got {
		if strings.Contains(line, " -I ") || strings.Contains(line, " -D ") || strings.Contains(line, " -X ") {
			t.Errorf("a changed backend must only rebuild the HZ chains: %s", line)
		}
		if strings.Contains(line, "192.168.1.76") {
			t.Errorf("old backend re-added: %s", line)
		}
	}
}

func TestReconcileSteadyStateIssuesNoMutations(t *testing.T) {
	calls := recordIptables(t)
	expected := ExpectedRules(sprinkInputs())

	Reconcile(gatewayLive(), expected, ForwardJumpRules(), nil, "eth0", "eth0")
	for _, line := range joined(*calls) {
		if !strings.Contains(line, " -N ") {
			t.Errorf("in-sync host got a mutating command: %s", line)
		}
	}
}

// A host that never configured a forward must not get the chains created,
// flushed or deleted — nat PREROUTING is none of horizon's business there.
func TestReconcileWithoutForwardsNeverTouchesForwardChains(t *testing.T) {
	calls := recordIptables(t)
	in := sprinkInputs()
	in.Forwards = nil

	Reconcile(withoutForwardRules(gatewayLive()), ExpectedRules(in), ForwardJumpRules(), nil, "eth0", "eth0")
	for _, line := range joined(*calls) {
		if strings.Contains(line, "HZ-") || strings.Contains(line, "PREROUTING") {
			t.Errorf("host without forwards got a forward-chain command: %s", line)
		}
	}
}

func TestRebuildChainRefusesChainsHorizonDoesNotOwn(t *testing.T) {
	for _, ref := range []chainRef{
		{"filter", "FORWARD"}, {"filter", "INPUT"}, {"filter", "DOCKER-USER"},
		{"nat", "PREROUTING"}, {"nat", "POSTROUTING"}, {"nat", "DOCKER"},
		{"filter", PreroutingChainName}, // right name, wrong table
	} {
		calls := recordIptables(t)
		if err := rebuildChain(ref.Table, ref.Chain, nil); err == nil {
			t.Errorf("rebuildChain(%s %s) should refuse", ref.Table, ref.Chain)
		}
		if len(*calls) != 0 {
			t.Errorf("rebuildChain(%s %s) ran commands: %v", ref.Table, ref.Chain, joined(*calls))
		}
	}
}

func TestStaleRulesAlwaysCarryForwardJumps(t *testing.T) {
	for _, cfg := range []*config.Config{
		configWithoutPriorState(),
		{WGInterface: "wg0", LastLocalIface: "eth1", LastLanCIDR: "10.0.0.0/24"},
	} {
		stale := StaleRules(cfg, nil, "", "")
		set := map[string]bool{}
		for _, r := range stale {
			set[r.Canonical()] = true
		}
		for _, j := range ForwardJumpRules() {
			if !set[j.Canonical()] {
				t.Errorf("stale set for %+v lacks forward jump %s", cfg, j)
			}
		}
	}
}

func TestScopeLiveRulesKeepsOnlyHorizonPreroutingJump(t *testing.T) {
	live := scopeLiveRules([]Rule{
		{Table: "nat", Chain: "PREROUTING", Args: []string{"-m", "addrtype", "--dst-type", "LOCAL", "-j", "DOCKER"}},
		{Table: "nat", Chain: "PREROUTING", Args: []string{"-m", "addrtype", "--dst-type", "LOCAL", "-j", PreroutingChainName}},
		{Table: "nat", Chain: PreroutingChainName, Args: []string{"-p", "udp", "--dport", "4433", "-j", "DNAT", "--to-destination", "192.168.1.76:4433"}},
		{Table: "filter", Chain: "INPUT", Args: []string{"-p", "tcp", "--dport", "22", "-j", "ACCEPT"}},
	})
	if len(live) != 2 {
		t.Fatalf("want the HZ jump and the HZ chain rule, got %v", live)
	}
	for _, r := range live {
		if r.Chain == "INPUT" || jumpsTo(r.Args, "DOCKER") {
			t.Errorf("rule should be out of scope: %s", r)
		}
	}
}
