package server

import (
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// hz DECLARES the wg0.conf that NATs through the interface this box actually
// uses; it does not stop healing it here.
//
// This is privilege-audit.md §7 B's "move reconcileIPTables axes 2/3 to the
// agent; keep axis 1's observe+persist in hz". The axes, READ OUT OF THE CODE
// rather than out of the checklist (see §3.4A, which is new because the
// document the three checklist lines cross-referenced was deleted on
// 2026-09-24):
//
//	axis 1    the LAN address drifts     → updateConfig + dnsmasq write/reload
//	axis 2/3  the egress interface or    → (a) iptables classify + heal
//	          LAN CIDR drifts, incl.       (b) persist LastLocalIface/LastLanCIDR
//	          the first-run inference      (c) rewrite wg0.conf's MASQUERADE
//	axis 4/5  legacy PostUp migrations   → one-shot rewrites, still undecided
//
// AND 2/3(a) WAS ALREADY THE AGENT'S. hz has served `IPTablesSection` —
// expected, stale, blessed, the current interface and LastLocalIface — since
// the payload existed, and `agent.SystemReloader.IPTables` hands all five
// straight to the same `iptables.Reconcile` hz calls. 2/3(b) is an
// hz-config write that no agent can do and must not move (see below). So what
// was actually left un-handed-over in axes 2/3 is (c), the wg0.conf rewrite —
// and it is the half that survives a reboot.
//
// # WHY (c) IS THE HALF THAT MATTERS FOR THE MOVE
//
// The live MASQUERADE and the wg0.conf PostUp that installs it are two
// different pieces of state, and only one of them was being handed over:
//
//	live rule     healed every pass by Reconcile, from hz's rule sets — SERVED
//	PostUp line   healed only by hz, and it is what runs at the next `wg-quick up`
//
// Leave (c) behind and step 5 produces a gateway whose firewall is correct
// until it reboots, at which point PostUp reinstalls the OLD interface's
// MASQUERADE and the box runs on it until the next reconcile pass condemns it
// — for up to a poll interval, every boot, for ever, with `hz-agent diff`
// reporting in sync throughout because nothing in the payload ever mentioned
// the file being wrong. That is the founding outage with a timer on it.
//
// # WHAT HZ STILL DOES, AND WHEN IT STOPS
//
// hz keeps healing the file itself — `reconcileIPTables` → `WGConfig.HealMasquerade`,
// on the 60s health tick — until item 12 step 5, exactly like every other
// hand-over in §7 B. The agent is inert (`cmd/hz-agent/install.go` emits no
// `--apply`), so nothing about today's behaviour changes; what changes is that
// the agent is now ABLE to own the file, and that both writers take the heal
// from one function. `wg_masquerade_test.go` runs each over identically seeded
// configs and compares bytes and mode.
//
// # THE UN-ARMED WINDOW, AND THE ONE THING THAT MUST MOVE WITH THE HEAL
//
// Between "hz stops" and "the agent is armed" there is no writer at all, and
// for this file that is survivable: the stale PostUp only takes effect at the
// next interface up, and the live rule is still condemned as stale on every
// pass by whoever is reconciling. The window is a reboot-shaped risk, not a
// running-gateway one.
//
// What is NOT survivable is stopping the heal while keeping 2/3(b). hz
// persists LastLocalIface AFTER healing, in the same pass, and
// `iptables.StaleRules` derives the entire old-interface rule set from that
// field. Persist without healing and the old MASQUERADE stops being STALE and
// becomes UNKNOWN — the one class `Reconcile` never deletes — so the rule
// nothing has removed yet becomes a rule nothing will ever remove.
// `TestPersistingLastLocalIfaceWithoutHealingBlindsTheStaleSet` is that
// statement as an executable one. So 2/3(b) is not axis 1's observe+persist and
// must not be kept "because hz keeps observing": it moves out of
// reconcileIPTables WITH the heal, at step 5, and whatever still advances
// LastLocalIface then has to do it from evidence that the old rules are gone.
//
// Axis 1's persist is a different thing and does stay: `LocalInterface` is an
// ADDRESS hz's own renderers read (`@self` host references follow a moved
// gateway through it), it feeds no delete, and nothing on the machine is
// condemned by it.

// declaredWGConfig is the wg0.conf hz publishes: the file hz maintains, with
// its MASQUERADE clause pointed at the egress interface hz can name right now.
//
// currentIface is the SAME read the firewall section makes — desiredFor takes
// it once, from buildClassifierInputs, and hands it to both. Two reads of the
// routing table inside one payload can straddle a flap, and a payload whose
// rule sets name one interface while its wg0.conf names another is a payload
// that heals the box in two directions.
//
// EMPTY MEANS STAND DOWN, and it is spelled as "publish the file unchanged"
// rather than as "publish nothing". The WireGuard section is the file hz
// maintains; withholding it would tell the agent hz manages no tunnel here,
// which is the mistake iptablesSectionFor's header is about. Serving the file
// as it is on disk is honest: hz has an opinion about this file and no opinion
// about which interface it should name this instant.
func declaredWGConfig(raw, currentIface string) string {
	healed, _ := wireguard.HealMasqueradeIface(raw, currentIface)
	return healed
}
