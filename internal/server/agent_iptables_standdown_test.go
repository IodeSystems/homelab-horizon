package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// THE BUG THIS FILE IS ABOUT (plan/privilege-audit.md §8.3, blocker 1).
//
// desiredFor used to publish an IPTablesSection unconditionally. The sets in
// it come from iptables.ExpectedRules, which emits NO MASQUERADE and NO port
// forward when the out-interface cannot be named — while iptables.StaleRules
// carries the forward jumps unconditionally and re-derives the previous
// interface's rules from LastLocalIface. During a route flap, a boot before
// the default route settles, or a second of link-down, hz therefore computes
// an expected set in which the gateway's NAT and every port forward are
// MISSING and a stale set in which they are NAMED. An armed agent polling at
// that moment reconciles them away.
//
// The tests below do not assert that a guard exists. They build the payload hz
// would have published WITHOUT it — from hz's own generators, with the same
// inputs — and show the plan removing the gateway's forwards, then show the
// guarded payload standing down instead.

// gatewayWithForwards is one gateway, correctly reconciled on eth0: a VPN, a
// LAN, and one UDP port forward. Synthetic addresses only.
func gatewayWithForwards() (*config.Config, []iptables.ForwardInput) {
	cfg := &config.Config{
		WGInterface:    "wg0",
		VPNRange:       "10.100.0.0/24",
		ListenAddr:     ":8080",
		LastLocalIface: "eth0",
		LastLanCIDR:    "192.0.2.0/24",
	}
	return cfg, []iptables.ForwardInput{
		{Proto: "udp", Port: 4433, BackendIP: "192.0.2.50", BackendPort: 4433},
	}
}

func rulesFor(cfg *config.Config, outIface, lanCIDR string, fwd []iptables.ForwardInput) []iptables.Rule {
	return iptables.ExpectedRules(iptables.Inputs{
		WGInterface: cfg.WGInterface,
		OutIface:    outIface,
		VPNRange:    cfg.VPNRange,
		LanCIDR:     lanCIDR,
		Peers:       []iptables.PeerInput{{Name: "laptop", AllowedIPs: "10.100.0.2/32"}},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
		Forwards:    fwd,
	})
}

// removalTargets is every rule the plan says would be taken off the box.
func removalTargets(p agent.Plan) []string {
	var out []string
	for _, c := range p.Changes {
		if c.Subsystem != agent.SubsystemIPTables {
			continue
		}
		if c.Kind == agent.KindUpdate || c.Kind == agent.KindRemove {
			out = append(out, c.Target)
		}
	}
	return out
}

func containsRule(targets []string, want iptables.Rule) bool {
	for _, t := range targets {
		if t == want.String() {
			return true
		}
	}
	return false
}

// THE POSITIVE CONTROL. Without the stand-down, a poll during a route flap
// plans the removal of the gateway's NAT and of the jumps every port forward
// hangs off. If this test ever stops failing against the unguarded payload,
// the scenario stopped reproducing the bug and the guarded half below is
// measuring nothing.
func TestAFlapWouldHaveRemovedTheGatewaysForwards(t *testing.T) {
	cfg, fwd := gatewayWithForwards()

	// The box as it really is: reconciled on eth0, forwards installed.
	live := rulesFor(cfg, "eth0", cfg.LastLanCIDR, fwd)
	masq := iptables.Rule{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth0", "-j", "MASQUERADE"}}
	if !containsRule(ruleStrings(live), masq) {
		t.Fatal("the fixture gateway has no MASQUERADE; the scenario is not a gateway")
	}
	for _, jump := range iptables.ForwardJumpRules() {
		if !containsRule(ruleStrings(live), jump) {
			t.Fatalf("the fixture gateway is missing forward jump %s; the scenario has no port forward to lose", jump)
		}
	}

	// What hz computes at the instant the default route is gone: same config,
	// same generators, no out-interface to name.
	flappedExpected := rulesFor(cfg, "", config.GetLocalNetworkCIDR(""), fwd)
	stale := iptables.StaleRules(cfg, nil, "10.100.0.1", "8080")

	// The payload desiredFor USED to build, verbatim.
	unguarded := &agent.IPTablesSection{
		Expected:         flappedExpected,
		Stale:            stale,
		Blessed:          nil,
		DefaultInterface: "",
		LastLocalIface:   cfg.LastLocalIface,
	}
	plan := agent.Compute(
		&agent.Desired{Machine: "gateway", IPTables: unguarded},
		agent.Observed{IPTablesReadable: true, LiveRules: live},
	)

	targets := removalTargets(plan)
	if !containsRule(targets, masq) {
		t.Fatalf("the unguarded payload did not plan to remove the gateway's MASQUERADE; removals were %v", targets)
	}
	for _, jump := range iptables.ForwardJumpRules() {
		if !containsRule(targets, jump) {
			t.Fatalf("the unguarded payload did not plan to remove forward jump %s; removals were %v", jump, targets)
		}
	}
	// Classify is what Reconcile deletes off, so the plan is not a second
	// opinion: these rules are stale to the reconciler too.
	for _, c := range iptables.Classify(live, flappedExpected, stale, nil) {
		if c.Rule.Canonical() == masq.Canonical() && c.State != iptables.StateStale {
			t.Fatalf("MASQUERADE classified %q, so Reconcile would not delete it and the plan disagrees with the reconciler", c.State)
		}
	}
}

// THE FIX, on exactly that scenario: hz publishes no rule sets at all.
func TestTheStandDownWithholdsTheFirewallInstead(t *testing.T) {
	cfg, fwd := gatewayWithForwards()
	live := rulesFor(cfg, "eth0", cfg.LastLanCIDR, fwd)
	flappedExpected := rulesFor(cfg, "", config.GetLocalNetworkCIDR(""), fwd)
	stale := iptables.StaleRules(cfg, nil, "10.100.0.1", "8080")

	sec := iptablesSectionFor(flappedExpected, stale, nil, "", cfg.LastLocalIface)
	if sec == nil {
		t.Fatal("the section is nil, which reads as \"hz manages no firewall here\" — an agent leaves the box alone quietly and the plan reports in sync")
	}
	if !sec.StoodDown {
		t.Fatal("hz published a firewall computed with no out-interface")
	}
	if len(sec.Expected) > 0 || len(sec.Stale) > 0 {
		t.Fatalf("a stood-down section still carries rule sets (%d expected, %d stale); the agent would reconcile against them",
			len(sec.Expected), len(sec.Stale))
	}

	plan := agent.Compute(
		&agent.Desired{Machine: "gateway", IPTables: sec},
		agent.Observed{IPTablesReadable: true, LiveRules: live},
	)
	if targets := removalTargets(plan); len(targets) > 0 {
		t.Fatalf("a stood-down payload still plans removals: %v", targets)
	}
	if plan.Changed() {
		t.Fatalf("a stood-down payload has pending changes: %v", plan.Pending())
	}

	// And it does not read as agreement. One unknown, and the text says so.
	if len(plan.Unknown()) != 1 {
		t.Fatalf("a stood-down firewall produced %d unknowns, want 1 — nothing on the plan says hz withheld it", len(plan.Unknown()))
	}
	report := agent.Report(plan)
	if strings.Contains(report, "in sync") {
		t.Fatalf("a machine whose firewall hz withheld reports in sync:\n%s", report)
	}
	if !strings.Contains(report, "default route") {
		t.Fatalf("the report does not say why the firewall was withheld:\n%s", report)
	}
}

// standDownReloader fails the test if anything reconciles.
type standDownReloader struct{ t *testing.T }

func (r standDownReloader) HAProxy(*agent.HAProxySection) error     { return nil }
func (r standDownReloader) DNSMasq(*agent.DNSMasqSection) error     { return nil }
func (r standDownReloader) WireGuard(*agent.WireGuardSection) error { return nil }
func (r standDownReloader) Units(*agent.FilesSection) error         { return nil }
func (r standDownReloader) RestartUnit(string) error                { return nil }
func (r standDownReloader) IPTables(*agent.IPTablesSection, []iptables.Rule) (iptables.Report, error) {
	r.t.Fatal("Apply reconciled the firewall off a stood-down payload")
	return iptables.Report{}, nil
}

// Applying is the half that actually destroys something, so the refusal is
// asserted there too rather than inferred from an empty plan.
func TestApplyDoesNotReconcileAStoodDownFirewall(t *testing.T) {
	cfg, fwd := gatewayWithForwards()
	live := rulesFor(cfg, "eth0", cfg.LastLanCIDR, fwd)

	d := &agent.Desired{
		Machine:  "gateway",
		IPTables: iptablesSectionFor(rulesFor(cfg, "", "", fwd), iptables.StaleRules(cfg, nil, "10.100.0.1", "8080"), nil, "", cfg.LastLocalIface),
	}
	obs := agent.Observed{IPTablesReadable: true, LiveRules: live}
	res, err := agent.Apply(d, agent.Compute(d, obs), obs, standDownReloader{t}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IPTables != nil {
		t.Fatal("Apply returned a reconcile report for a firewall hz withheld")
	}
}

// The three answers the section carries are three different things, and the
// difference is the whole point: nil is hz having no opinion here, a stood-down
// section is hz holding one back, and a populated one is hz's opinion.
func TestTheThreeFirewallAnswersAreDistinct(t *testing.T) {
	cfg, fwd := gatewayWithForwards()
	expected := rulesFor(cfg, "eth0", cfg.LastLanCIDR, fwd)

	if sec := iptablesSectionFor(nil, nil, nil, "eth0", "eth0"); sec != nil {
		t.Fatal("hz generates no rules on this box and still published a section")
	}
	stood := iptablesSectionFor(expected, nil, nil, "", "eth0")
	if stood == nil || !stood.StoodDown || stood.Why == "" {
		t.Fatalf("no-out-interface did not produce an explained stand-down: %+v", stood)
	}
	published := iptablesSectionFor(expected, nil, nil, "eth0", "eth0")
	if published == nil || published.StoodDown || len(published.Expected) == 0 {
		t.Fatalf("a nameable out-interface did not produce a published section: %+v", published)
	}
}

// A withheld section must not be filed as one of the two absences hz already
// had. "hz has no record for this" and "hz cannot read that machine's files"
// both persist until somebody acts; a stand-down clears on its own, and a
// reader that cannot tell them apart chases the wrong one.
func TestAWithheldFirewallIsNotTheSameGapAsAnUnreadableOrUnmodelledOne(t *testing.T) {
	var mc projection.MachineConfig
	mc.AddGapReason(agentSectionIPTables, projection.ReasonStoodDown, iptablesStandDownWhy)

	var remote projection.MachineConfig
	noteRemoteGaps(&remote, &config.Config{HAProxyEnabled: true})

	stood := gapFor(t, mc, agentSectionIPTables)
	unmodelled := gapFor(t, remote, agentSectionIPTables)
	unreadable := gapFor(t, remote, agentSectionWireGuard)

	if stood.Reason == unmodelled.Reason {
		t.Fatalf("the stand-down and the remote-edge gap share reason %q, so a screen cannot tell a flap from a missing record", stood.Reason)
	}
	if stood.Reason == unreadable.Reason {
		t.Fatalf("the stand-down and the unreadable-file gap share reason %q", stood.Reason)
	}
	if unmodelled.Reason == unreadable.Reason {
		t.Fatalf("hz's two pre-existing absences collapsed into one reason %q", unmodelled.Reason)
	}
	if stood.Reason != projection.ReasonStoodDown {
		t.Fatalf("stand-down reason = %q", stood.Reason)
	}

	// And it is not "there are no rules" either: an empty section with no gap
	// is hz saying it checked and wants nothing.
	if len(mc.Unresolved) == 0 {
		t.Fatal("a withheld firewall left no gap, so it is indistinguishable from hz wanting no rules")
	}
}

func gapFor(t *testing.T, mc projection.MachineConfig, section string) projection.Gap {
	t.Helper()
	for _, g := range mc.Unresolved {
		if g.Section == section {
			return g
		}
	}
	t.Fatalf("no gap recorded for section %q", section)
	return projection.Gap{}
}

func ruleStrings(rules []iptables.Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.String())
	}
	return out
}
