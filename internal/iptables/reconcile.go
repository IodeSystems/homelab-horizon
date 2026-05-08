package iptables

import (
	"fmt"
	"os/exec"
	"strings"
)

// Report is the result of one reconcile pass, returned to callers so the API
// layer can surface it to the UI and logs can show what happened.
type Report struct {
	Summary     Summary          `json:"summary"`
	Deleted     []Rule           `json:"deleted,omitempty"`     // stale rules removed
	Added       []Rule           `json:"added,omitempty"`       // missing expected rules added
	LeftAlone   []ClassifiedRule `json:"left_alone,omitempty"`  // unknown + blessed (surfaced only)
	InferredOld string           `json:"inferred_old,omitempty"` // iface inferred when LastLocalIface was empty
	Errors      []string         `json:"errors,omitempty"`
}

// Reconcile classifies live rules and auto-heals: removes stale, adds any
// missing expected rules. Unknown and blessed rules are left alone — those
// are the admin's decision via the IPTables UI tab.
//
// currentDefaultIface is used only for the empty-LastLocalIface bootstrap: if
// the config has no persisted "last" iface, Reconcile scans live POSTROUTING
// for any `-o X -j MASQUERADE` where X != currentDefaultIface and uses that
// X as a one-shot stale identifier. The inferred value is reported back via
// Report.InferredOld so the caller can persist it as LastLocalIface for the
// next pass.
//
// WG-FORWARD is special-cased: it's wholly horizon-owned and order-sensitive
// (per-peer ACCEPTs must precede the per-peer DROP, catch-all DROP must come
// last). Incremental `-I 1` patching reverses the rules' order when many are
// added at once (e.g. after a wg-quick down/up wiped the chain), which
// silently breaks all VPN forwarding. So WG-FORWARD is rebuilt atomically
// (`-F` + `-A` in expected order) whenever its content or order diverges.
//
// Callers are expected to be holding whatever lock protects concurrent config
// mutation — Reconcile itself only shells out to iptables.
func Reconcile(
	live []Rule,
	expected []Rule,
	stale []Rule,
	blessed []string,
	currentDefaultIface string,
	lastLocalIface string,
) Report {
	report := Report{}

	// Auto-infer a stale iface when we have nothing persisted. Only kicks in
	// for the first reconcile after upgrade — after Reconcile persists the
	// current default, subsequent passes have a real LastLocalIface.
	if lastLocalIface == "" && currentDefaultIface != "" {
		if inferred := inferStaleIface(live, currentDefaultIface); inferred != "" {
			report.InferredOld = inferred
			stale = append(stale, Rule{
				Table: "nat",
				Chain: "POSTROUTING",
				Args:  []string{"-o", inferred, "-j", "MASQUERADE"},
			})
		}
	}

	classified := Classify(live, expected, stale, blessed)
	report.Summary = SummarizeClassified(classified)

	// Delete stale rules first so we don't collide when adding back an
	// expected rule with the same shape but different iface. WG-FORWARD
	// stale rules are skipped here — the atomic rebuild below replaces the
	// whole chain in one shot.
	for _, c := range classified {
		if c.State != StateStale {
			continue
		}
		if isWGForward(c.Rule) {
			continue
		}
		if err := deleteRule(c.Rule); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete %s: %v", c.Rule, err))
			continue
		}
		report.Deleted = append(report.Deleted, c.Rule)
	}

	// Add any expected rule that isn't currently live. WG-FORWARD rules are
	// skipped — the atomic rebuild below installs them in the correct order.
	liveSet := make(map[string]struct{}, len(live))
	for _, r := range live {
		liveSet[r.Canonical()] = struct{}{}
	}
	// Exclude just-deleted rules from liveSet so we re-add the expected form.
	for _, r := range report.Deleted {
		delete(liveSet, r.Canonical())
	}
	for _, r := range expected {
		if isWGForward(r) {
			continue
		}
		if _, already := liveSet[r.Canonical()]; already {
			continue
		}
		if err := addRule(r); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("add %s: %v", r, err))
			continue
		}
		report.Added = append(report.Added, r)
	}

	// WG-FORWARD: atomic rebuild on any drift. Incremental patching can't
	// safely repair this chain because order is load-bearing — first match
	// wins, and per-peer DROP after per-peer ACCEPT (and catch-all DROP last)
	// is what makes the policy work. Rebuild only when expected has content
	// (i.e. WG is being managed by horizon); otherwise leave the chain alone.
	wgFwdExpected := filterChain(expected, ForwardChainName)
	wgFwdLive := filterChain(live, ForwardChainName)
	if len(wgFwdExpected) > 0 && wgForwardDrifted(wgFwdLive, wgFwdExpected) {
		if err := rebuildWGForward(wgFwdExpected); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("rebuild %s: %v", ForwardChainName, err))
		} else {
			// Net effect mirrored into Report so callers/UI see what changed.
			report.Deleted = append(report.Deleted, wgFwdLive...)
			report.Added = append(report.Added, wgFwdExpected...)
		}
	}

	// Surface unknown + blessed for the caller. Expected+stale are covered
	// by the counts; the detailed list is the admin-actionable bucket.
	for _, c := range classified {
		if c.State == StateUnknown || c.State == StateBlessed {
			report.LeftAlone = append(report.LeftAlone, c)
		}
	}

	return report
}

// isWGForward reports whether a rule belongs to the WG-FORWARD chain in the
// filter table. Pulled out so the skip-WG-FORWARD predicate is consistent
// across the stale-delete and missing-add loops.
func isWGForward(r Rule) bool {
	return r.Table == "filter" && r.Chain == ForwardChainName
}

// filterChain returns the subset of rules belonging to the given filter chain.
// Used to slice WG-FORWARD out of the live and expected sets for the atomic
// rebuild path.
func filterChain(rules []Rule, chain string) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Table == "filter" && r.Chain == chain {
			out = append(out, r)
		}
	}
	return out
}

// wgForwardDrifted reports whether the live WG-FORWARD subset diverges from
// expected by content OR by order. Both inputs must already be filtered to
// WG-FORWARD rules.
//
// Order matters because iptables is first-match-wins: a catch-all `-j DROP`
// at position 1 nullifies every ACCEPT below it. The earlier reconciler used
// only set-membership (canonical comparison), which is necessary but not
// sufficient — a chain with the right rules in the wrong order looks "in sync"
// to set-membership but is functionally broken.
func wgForwardDrifted(live, expected []Rule) bool {
	if len(live) != len(expected) {
		return true
	}
	for i := range live {
		if live[i].Canonical() != expected[i].Canonical() {
			return true
		}
	}
	return false
}

// rebuildWGForward atomically replaces the WG-FORWARD chain contents with the
// supplied rules, in order. Ensures the chain exists (no-op if already there)
// before flushing, so this is safe to call after a wg-quick PostDown wipe.
//
// Failure mode: if `-A` fails partway through, the chain is left partially
// populated. The next reconcile tick re-detects drift and retries. We don't
// attempt rollback because the previous live state was already wrong (that's
// why we're rebuilding) and the partial state is at worst no-worse.
func rebuildWGForward(rules []Rule) error {
	// -N exits non-zero when the chain already exists; that's expected, ignore.
	_ = exec.Command("iptables", "-N", ForwardChainName).Run()

	if out, err := exec.Command("iptables", "-F", ForwardChainName).CombinedOutput(); err != nil {
		return fmt.Errorf("flush: %v: %s", err, strings.TrimSpace(string(out)))
	}
	for _, r := range rules {
		if r.Chain != ForwardChainName {
			continue
		}
		args := append([]string{"-t", r.Table, "-A", r.Chain}, r.Args...)
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("append %s: %v: %s", r, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// inferStaleIface scans live POSTROUTING for a `-o X -j MASQUERADE` rule whose
// `-o` token isn't the current default iface. Returns the first such token,
// or "" if nothing matches. Used for the first-upgrade bootstrap where
// LastLocalIface hasn't been persisted yet.
//
// Strictly matches the 4-token shape horizon emits — `-o <iface> -j MASQUERADE`
// with no source restriction or interface negation. This avoids false-positives
// on Docker/k8s style rules like `-s 172.22.0.0/16 ! -o br-b760 -j MASQUERADE`
// which also contain "-o <iface>" but are semantically different: those are
// NAT-only-for-this-bridge rules, not the blanket "outbound via default iface"
// we care about.
func inferStaleIface(live []Rule, currentDefault string) string {
	for _, r := range live {
		if r.Table != "nat" || r.Chain != "POSTROUTING" {
			continue
		}
		if !isHorizonMasqShape(r.Args) {
			continue
		}
		if r.Args[1] != currentDefault {
			return r.Args[1]
		}
	}
	return ""
}

// isHorizonMasqShape reports whether args is the exact 4-token shape horizon
// emits for its MASQUERADE rule: ["-o", "<iface>", "-j", "MASQUERADE"]. Rules
// owned by other tools (docker, libvirt, ufw, custom scripts) usually add
// source/destination predicates or interface negation — those are none of our
// business and must not be misclassified as stale-horizon.
func isHorizonMasqShape(args []string) bool {
	return len(args) == 4 &&
		args[0] == "-o" &&
		args[2] == "-j" &&
		args[3] == "MASQUERADE"
}

// addRule inserts a rule at position 1 in its chain (so WG-FORWARD jumps and
// MASQUERADE rules land before any UFW drop rules that might be below).
func addRule(r Rule) error {
	args := append([]string{"-t", r.Table, "-I", r.Chain, "1"}, r.Args...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// deleteRule removes a rule by its spec (iptables -D matches against args,
// not line number — idempotent if the rule was already removed elsewhere).
func deleteRule(r Rule) error {
	args := append([]string{"-t", r.Table, "-D", r.Chain}, r.Args...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
