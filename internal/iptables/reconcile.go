package iptables

// This file is the PRIVILEGED half of the package: the whole list of things hz
// needs root on the gateway for, as far as iptables is concerned.
//
//   - run `iptables-save -t <table>` to read what is installed (LiveRules)
//   - run `iptables -I|-D` to add and remove single rules in built-in chains
//   - run `iptables -N|-F|-A|-X` to rebuild horizon's own chains atomically
//
// Nothing here decides what the rule set should be — that is rules.go and
// forwards.go, which are pure — and nothing here decides what a live rule
// *means*, which is classify.go, also pure. Reconcile is the orchestration
// between the two: it reads, asks the pure half for a verdict, and applies.
// When this half moves into hz-agent (plan/architecture.md, phase 4, item 10),
// this list is what moves.

import (
	"fmt"
	"os/exec"
	"strings"
)

// LiveRules reads horizon-managed rules from the host kernel via iptables-save.
// Scopes the read to the chains horizon cares about; rules in other chains
// (OUTPUT, PREROUTING, custom admin chains, etc.) are not returned — that's
// part of the "horizon only manages what it manages" boundary.
//
// INPUT is narrowed further, to just the rules that jump to WG-INPUT. Unlike
// FORWARD, a normal host's INPUT is full of ufw/docker rules that horizon has
// no opinion about; reading them all would classify every one as "unknown" and
// bury the IPTables tab in noise the admin can't act on.
//
// Returns an empty slice (not error) when iptables-save isn't available, so
// the classifier can still run on hosts without iptables installed yet.
func LiveRules() ([]Rule, error) {
	natRules, err := runIptablesSave("nat", liveNatChains)
	if err != nil {
		return nil, fmt.Errorf("iptables-save nat: %w", err)
	}
	filterRules, err := runIptablesSave("filter", liveFilterChains)
	if err != nil {
		return nil, fmt.Errorf("iptables-save filter: %w", err)
	}
	return scopeLiveRules(append(natRules, filterRules...)), nil
}

// The chains LiveRules reads. PREROUTING and INPUT are narrowed further by
// scopeLiveRules.
var (
	liveNatChains    = []string{"PREROUTING", "POSTROUTING", PreroutingChainName, PostroutingChainName}
	liveFilterChains = []string{"FORWARD", ForwardChainName, InputChainName, "INPUT", ForwardsChainName}
)

// runIptablesSave executes `iptables-save -t <table>` and parses the output,
// filtering to rules in the given chains.
func runIptablesSave(table string, chains []string) ([]Rule, error) {
	cmd := exec.Command("iptables-save", "-t", table)
	out, err := cmd.Output()
	if err != nil {
		// iptables-save not installed, table missing, etc. Return empty so
		// the classifier treats "no live rules" as the input.
		return nil, nil
	}
	return parseIptablesSave(string(out), table, chains), nil
}

// Report is the result of one reconcile pass, returned to callers so the API
// layer can surface it to the UI and logs can show what happened.
type Report struct {
	Summary     Summary          `json:"summary"`
	Deleted     []Rule           `json:"deleted,omitempty"`      // stale rules removed
	Added       []Rule           `json:"added,omitempty"`        // missing expected rules added
	LeftAlone   []ClassifiedRule `json:"left_alone,omitempty"`   // unknown + blessed (surfaced only)
	InferredOld string           `json:"inferred_old,omitempty"` // iface inferred when LastLocalIface was empty
	Errors      []string         `json:"errors,omitempty"`
}

// runIptables executes one iptables command as the horizon process (root).
// Every mutation Reconcile makes goes through here, which is what lets the
// tests record the exact command set and prove it stays inside horizon's
// chains.
var runIptables = func(args ...string) ([]byte, error) {
	return exec.Command("iptables", args...).CombinedOutput()
}

// chainRef names a chain in a table.
type chainRef struct {
	Table string
	Chain string
}

// ownedChains are the chains horizon owns outright and rebuilds atomically.
// Nothing else on the host writes to them, which is why flushing them is safe
// and flushing anything else never happens.
var ownedChains = []chainRef{
	{"filter", ForwardChainName},
	{"filter", InputChainName},
	{"nat", PreroutingChainName},
	{"nat", PostroutingChainName},
	{"filter", ForwardsChainName},
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
// The owned chains (WG-FORWARD, WG-INPUT, and the HZ-* forward chains) are
// special-cased: they're wholly horizon-owned and order-sensitive (per-peer
// ACCEPTs must precede the per-peer DROP, catch-all DROP must come last).
// Incremental `-I 1` patching reverses the rules' order when many are added at
// once (e.g. after a wg-quick down/up wiped the chain), which silently breaks
// all VPN forwarding. So they are rebuilt atomically (`-F` + `-A` in expected
// order) whenever their content or order diverges.
//
// Built-in chains are only ever edited one rule at a time (`-I <chain> 1` for
// a missing expected rule, `-D` for a stale one). Reconcile never flushes a
// chain it does not own and never changes a policy.
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

	// Owned chains must exist before the add loop below, because the jump
	// rules that target them fail to install against a missing chain.
	if len(expected) > 0 {
		ensureChains(expected)
	}

	// Delete stale rules first so we don't collide when adding back an
	// expected rule with the same shape but different iface. Owned-chain
	// stale rules are skipped here — the atomic rebuild below replaces the
	// whole chain in one shot.
	for _, c := range classified {
		if c.State != StateStale {
			continue
		}
		if isOwnedChain(c.Rule) {
			continue
		}
		if err := deleteRule(c.Rule); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete %s: %v", c.Rule, err))
			continue
		}
		report.Deleted = append(report.Deleted, c.Rule)
	}

	// Add any expected rule that isn't currently live. Owned-chain rules are
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
		if isOwnedChain(r) {
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

	// WG-FORWARD / WG-INPUT: atomic rebuild on any drift. Incremental patching
	// can't safely repair these chains because order is load-bearing — first
	// match wins, and per-peer DROP after per-peer ACCEPT (and catch-all DROP
	// last) is what makes the policy work.
	//
	// Gated on WG-FORWARD having content, which is horizon's proxy for "WG is
	// managed on this host" (it always carries at least the catch-all DROP).
	// WG-INPUT is then rebuilt even when its expected set is *empty*, because
	// empty is a meaningful state: it's what un-jailing everyone looks like,
	// and leaving a stale DROP behind would strand a peer that just authed.
	if len(filterChain(expected, "filter", ForwardChainName)) > 0 {
		for _, chain := range []string{ForwardChainName, InputChainName} {
			rebuildIfDrifted(&report, live, expected, chainRef{"filter", chain})
		}

		// HZ-* forward chains, same gate. Rebuilt to empty when the last
		// forward is removed (its jumps are stale and were deleted above), and
		// then dropped, so a host that stops using forwards is left without
		// the chains. A host that never had forwards has no live rules in them,
		// so nothing is created, flushed or deleted there.
		for _, ref := range ownedChains[2:] {
			chainExpected := filterChain(expected, ref.Table, ref.Chain)
			if rebuildIfDrifted(&report, live, expected, ref) && len(chainExpected) == 0 {
				_, _ = runIptables("-t", ref.Table, "-X", ref.Chain)
			}
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

// rebuildIfDrifted rebuilds one owned chain when its live content or order
// differs from expected, mirroring the net effect into the report. Reports
// whether it rebuilt successfully.
func rebuildIfDrifted(report *Report, live, expected []Rule, ref chainRef) bool {
	chainExpected := filterChain(expected, ref.Table, ref.Chain)
	chainLive := filterChain(live, ref.Table, ref.Chain)
	if !chainDrifted(chainLive, chainExpected) {
		return false
	}
	if err := rebuildChain(ref.Table, ref.Chain, chainExpected); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("rebuild %s: %v", ref.Chain, err))
		return false
	}
	// Net effect mirrored into Report so callers/UI see what changed.
	report.Deleted = append(report.Deleted, chainLive...)
	report.Added = append(report.Added, chainExpected...)
	return true
}

// isOwnedChain reports whether a rule lives in one of the chains horizon owns
// outright and rebuilds atomically. Pulled out so the skip predicate stays
// consistent across the stale-delete and missing-add loops — those two must
// leave these chains alone or they'd fight the rebuild below.
func isOwnedChain(r Rule) bool {
	for _, ref := range ownedChains {
		if r.Table == ref.Table && r.Chain == ref.Chain {
			return true
		}
	}
	return false
}

// ensureChains creates the owned chains the expected set needs. The WG chains
// are always created (horizon manages WireGuard on every host it runs on); the
// forward chains only when some expected rule lives in or jumps to them, so a
// host with no forwards never gets them. `-N` exits non-zero when the chain
// already exists; that's the common case, ignore it.
func ensureChains(expected []Rule) {
	for _, ref := range ownedChains {
		needed := ref.Chain == ForwardChainName || ref.Chain == InputChainName
		for _, r := range expected {
			if needed {
				break
			}
			if (r.Table == ref.Table && r.Chain == ref.Chain) || (r.Table == ref.Table && jumpsTo(r.Args, ref.Chain)) {
				needed = true
			}
		}
		if needed {
			_, _ = runIptables("-t", ref.Table, "-N", ref.Chain)
		}
	}
}

// filterChain returns the subset of rules belonging to the given table and
// chain. Used to slice an owned chain out of the live and expected sets for
// the atomic rebuild path.
func filterChain(rules []Rule, table, chain string) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Table == table && r.Chain == chain {
			out = append(out, r)
		}
	}
	return out
}

// chainDrifted reports whether the live subset of a chain diverges from
// expected by content OR by order. Both inputs must already be filtered to
// the same chain.
//
// Order matters because iptables is first-match-wins: a catch-all `-j DROP`
// at position 1 nullifies every ACCEPT below it. The earlier reconciler used
// only set-membership (canonical comparison), which is necessary but not
// sufficient — a chain with the right rules in the wrong order looks "in sync"
// to set-membership but is functionally broken.
func chainDrifted(live, expected []Rule) bool {
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

// rebuildChain atomically replaces an owned chain's contents with the supplied
// rules, in order. Ensures the chain exists (no-op if already there) before
// flushing, so this is safe to call after a wg-quick PostDown wipe.
//
// Refuses any chain not in ownedChains: flushing a built-in or another tool's
// chain on a remotely administered gateway is how a reconcile turns into a
// lockout, so it is excluded here rather than trusted to every caller.
//
// Failure mode: if `-A` fails partway through, the chain is left partially
// populated. The next reconcile tick re-detects drift and retries. We don't
// attempt rollback because the previous live state was already wrong (that's
// why we're rebuilding) and the partial state is at worst no-worse.
func rebuildChain(table, chain string, rules []Rule) error {
	if !isOwnedChain(Rule{Table: table, Chain: chain}) {
		return fmt.Errorf("refusing to rebuild %s %s: not a horizon-owned chain", table, chain)
	}

	// -N exits non-zero when the chain already exists; that's expected, ignore.
	_, _ = runIptables("-t", table, "-N", chain)

	if out, err := runIptables("-t", table, "-F", chain); err != nil {
		return fmt.Errorf("flush: %v: %s", err, strings.TrimSpace(string(out)))
	}
	for _, r := range rules {
		if r.Table != table || r.Chain != chain {
			continue
		}
		args := append([]string{"-t", r.Table, "-A", r.Chain}, r.Args...)
		if out, err := runIptables(args...); err != nil {
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
	out, err := runIptables(args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// deleteRule removes a rule by its spec (iptables -D matches against args,
// not line number — idempotent if the rule was already removed elsewhere).
func deleteRule(r Rule) error {
	args := append([]string{"-t", r.Table, "-D", r.Chain}, r.Args...)
	out, err := runIptables(args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
