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
	// expected rule with the same shape but different iface.
	for _, c := range classified {
		if c.State != StateStale {
			continue
		}
		if err := deleteRule(c.Rule); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete %s: %v", c.Rule, err))
			continue
		}
		report.Deleted = append(report.Deleted, c.Rule)
	}

	// Add any expected rule that isn't currently live.
	liveSet := make(map[string]struct{}, len(live))
	for _, r := range live {
		liveSet[r.Canonical()] = struct{}{}
	}
	// Exclude just-deleted rules from liveSet so we re-add the expected form.
	for _, r := range report.Deleted {
		delete(liveSet, r.Canonical())
	}
	for _, r := range expected {
		if _, already := liveSet[r.Canonical()]; already {
			continue
		}
		if err := addRule(r); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("add %s: %v", r, err))
			continue
		}
		report.Added = append(report.Added, r)
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
