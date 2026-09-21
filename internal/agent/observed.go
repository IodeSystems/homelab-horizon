package agent

import (
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// The other direction: what a machine tells hz about itself.
//
// `Desired` goes hz -> agent over a conditional GET and, until this file,
// nothing came back. `Observed` was computed on the box, handed to Compute,
// printed and discarded, so hz could not show drift and — after item 12 —
// could not read the live firewall at all (plan/privilege-classification.md
// §4.1, the largest gap in the agent's model).
//
// # THE AGENT STILL INITIATES
//
// architecture.md's rule is "the agent polls, hz never initiates". A report
// does not weaken it: the agent dials hz, on its own clock, with its own
// credential. hz opens no connection, holds no inbound credential and needs
// no route to the box. A machine behind NAT reports exactly as it polls.
// handleProbeReport is the same shape and the in-tree precedent.
//
// # WHAT CROSSES, AND WHAT MUST NOT
//
// The PLAN crosses, never the Observed. Observed is raw file contents read
// off the machine — wg0.conf's PrivateKey included — and there is no version
// of "ship the observed state" that is safe. The plan is the already-redacted
// product: File.Secret collapses a secret file to its size (layer 1) and
// redactLine blanks any key-shaped assignment in what is left (layer 2).
// Sanitized applies layer 2 again at both ends of the wire, because a Change
// can be constructed anywhere and hz must not trust a client to have done it.
//
// The one raw thing that crosses is the live iptables rule set, and it is
// raw on purpose: hz classifies it with its own iptables.Classify, the same
// function the IPTables tab already uses. The agent ships the fact
// (iptables-save said this), not an opinion, so there is exactly one
// classifier and the tab cannot disagree with the reconciler. A firewall rule
// carries no key material; every token still goes through layer 2 anyway, so
// "every string in a report has been redacted" is a property with no
// exceptions to remember.

// ObservedPath is the endpoint hz accepts reports at and serves them from.
// POST is the machine's own report; GET is an admin read of the whole fleet.
const ObservedPath = "/api/v1/agent/observed"

// Bounds on one report. A report is a summary an operator reads, not a copy
// of the machine, and hz stores what arrives — so every list is capped here
// rather than trusted to be small.
const (
	// maxReportChanges bounds the change list. The gateway's payload is a
	// handful of files plus a rule summary; a thousand is already a bug.
	maxReportChanges = 500

	// maxReportDetailLines and maxReportDetailWidth bound one Detail. diff.go
	// already emits at most maxDiffLines samples; this is the backstop for a
	// Change that did not come from describeTextChange.
	maxReportDetailLines = 16
	maxReportDetailWidth = 240

	// maxReportRules bounds the live rule set. A gateway runs tens; a box
	// with thousands has a different problem than drift.
	maxReportRules = 2000

	// maxReportField bounds the short identifier strings.
	maxReportField = 128
)

// StateReport is one machine's account of itself: the plan it computed
// against the desired state hz served it, plus the live firewall it read.
//
// Every field is a CLAIM by the machine. hz overrides Machine with the one
// its credential names and refuses the report outright if they disagree, so
// the only claim that authenticates is the credential.
type StateReport struct {
	// Machine is who the agent believes it is. hz checks it against the
	// credential and refuses a mismatch rather than correcting it: a machine
	// reporting under the wrong name is a misconfiguration worth seeing.
	Machine string `json:"machine"`

	// Generation is the Desired.Fingerprint this plan was computed against.
	// hz compares it with what it would serve now: a different one means the
	// box has not polled the current config yet; the SAME one with pending
	// changes means the agent planned against today's config and the machine
	// still does not match it (plan/example-projection.md §5 — "applied and
	// did not take" is a different fault from "behind", and one badge merges
	// them).
	Generation string `json:"generation"`

	// AgentVersion is the reporting binary's version, so agent skew across a
	// fleet is visible without a second channel.
	AgentVersion string `json:"agent_version,omitempty"`

	// Applying says whether this agent is running with --apply. While the
	// agent ships inert this is false everywhere, and a true here on a box
	// nobody flipped is worth an alarm.
	Applying bool `json:"applying"`

	// IntervalSeconds is the cadence the agent promises to report at. hz
	// derives staleness from it rather than from a constant, so a box
	// deliberately reporting slowly is not permanently late. hz clamps it —
	// a report cannot talk its way out of ever being stale.
	IntervalSeconds int `json:"interval_seconds,omitempty"`

	// Changes is the plan, one line per target. Empty with no IPTables
	// section means hz manages nothing on this box: "nothing to report",
	// which is a correct and permanent state for a machine that has an agent
	// and no managed subsystems, NOT silence.
	Changes []Change `json:"changes,omitempty"`

	// IPTables is present only when hz asked this machine to manage the
	// firewall. Absent means unmanaged; present-and-unreadable means the
	// agent could not run iptables-save and says why — which is the state
	// hz web itself lands in after item 12, and the reason a screen must be
	// able to say "I cannot look" instead of showing an empty table.
	IPTables *IPTablesObservation `json:"iptables,omitempty"`

	// Truncated is set when Sanitized dropped entries to stay inside the
	// bounds above. An operator seeing a short list deserves to know it is
	// short because of a cap rather than because the machine is clean.
	Truncated bool `json:"truncated,omitempty"`
}

// IPTablesObservation is what the agent could learn about the live firewall.
//
// Readable false is a first-class answer, not an empty rule set: a non-root
// reader handed "no rules installed" would plan the entire firewall as
// missing, which is the same lie observe.go already refuses to tell.
type IPTablesObservation struct {
	Readable bool   `json:"readable"`
	Why      string `json:"why,omitempty"`

	// Live is what iptables-save reported, unclassified. hz classifies.
	Live []iptables.Rule `json:"live,omitempty"`
}

// NewStateReport builds the report for one pass, already sanitized.
//
// It takes the desired payload as well as the plan because "hz manages
// nothing here" and "hz manages the firewall and the agent could not read it"
// are different states, and only the payload distinguishes them.
func NewStateReport(d *Desired, p Plan, obs Observed) StateReport {
	r := StateReport{
		Machine:    p.Machine,
		Generation: p.Generation,
		Changes:    p.Changes,
	}
	if d != nil && d.Machine != "" {
		r.Machine = d.Machine
	}
	if d != nil && d.IPTables != nil {
		r.IPTables = &IPTablesObservation{
			Readable: obs.IPTablesReadable,
			Why:      obs.IPTablesWhy,
			Live:     obs.LiveRules,
		}
	}
	return r.Sanitized()
}

// Sanitized returns a copy with every string bounded and put through
// redactLine, and every list capped.
//
// Called on BOTH ends: by the agent before it sends, and by hz before it
// stores. That is deliberate — the agent's copy is a courtesy (it keeps the
// wire clean and the agent's own logs safe), hz's copy is the enforcement.
// hz cannot assume a client sanitized anything, and a Change is a plain
// struct anybody can construct; the only thing that makes the stored report
// safe to serve is that hz redacted it itself.
//
// Idempotent, so applying it twice costs nothing and proves nothing changed.
func (r StateReport) Sanitized() StateReport {
	out := r
	out.Machine = clampField(r.Machine)
	out.Generation = clampField(r.Generation)
	out.AgentVersion = clampField(r.AgentVersion)

	changes := r.Changes
	if len(changes) > maxReportChanges {
		changes = changes[:maxReportChanges]
		out.Truncated = true
	}
	out.Changes = make([]Change, 0, len(changes))
	for _, c := range changes {
		c.Subsystem = Subsystem(clampField(string(c.Subsystem)))
		c.Kind = ChangeKind(clampField(string(c.Kind)))
		c.Target = redactLine(truncate(c.Target, maxReportDetailWidth))
		c.Detail = sanitizeDetail(c.Detail)
		out.Changes = append(out.Changes, c)
	}
	if len(out.Changes) == 0 {
		out.Changes = nil
	}

	if r.IPTables != nil {
		live := r.IPTables.Live
		if len(live) > maxReportRules {
			live = live[:maxReportRules]
			out.Truncated = true
		}
		sec := IPTablesObservation{
			Readable: r.IPTables.Readable,
			Why:      sanitizeDetail(r.IPTables.Why),
			Live:     make([]iptables.Rule, 0, len(live)),
		}
		for _, rule := range live {
			sec.Live = append(sec.Live, sanitizeRule(rule))
		}
		if len(sec.Live) == 0 {
			sec.Live = nil
		}
		out.IPTables = &sec
	}
	return out
}

// HasTargets reports whether hz asked this machine to reconcile anything.
//
// False is "nothing to report" — an enrolled box with an agent and no managed
// subsystem, which is correct and permanent and must not render as silence
// (plan/example-projection.md §3, ci-1).
func (r StateReport) HasTargets() bool { return len(r.Changes) > 0 || r.IPTables != nil }

// Pending counts the changes that would touch the machine. A removal counts:
// a file the machine still holds and hz has stopped wanting is drift, and a
// report calling that in sync would hide the one change that destroys
// something.
func (r StateReport) Pending() int { return r.countKinds(KindCreate, KindUpdate, KindRemove) }

// Unknown counts the targets the agent could not read. A report with unknowns
// is not a clean bill of health, and anything printing "in sync" must say so.
func (r StateReport) Unknown() int { return r.countKinds(KindUnknown) }

// InSync reports whether the machine matched the config it planned against.
// Unknowns do not make it false — they make it incomplete, which Unknown says.
func (r StateReport) InSync() bool { return r.Pending() == 0 }

func (r StateReport) countKinds(kinds ...ChangeKind) int {
	n := 0
	for _, c := range r.Changes {
		for _, k := range kinds {
			if c.Kind == k {
				n++
				break
			}
		}
	}
	return n
}

// Condition is a stable key for "the same state, still". It names the
// targets and what each is in, and deliberately excludes the byte counts and
// sample lines in Detail — a file that keeps drifting differently is still
// the same standing condition, and this is what lets hz say "pending since"
// rather than resetting the clock on every poll.
func (r StateReport) Condition() string {
	var b strings.Builder
	b.WriteString(r.Generation)
	for _, c := range r.Changes {
		b.WriteString("\n")
		b.WriteString(string(c.Subsystem))
		b.WriteString("|")
		b.WriteString(c.Target)
		b.WriteString("|")
		b.WriteString(string(c.Kind))
	}
	if r.IPTables != nil {
		b.WriteString("\niptables|readable=")
		if r.IPTables.Readable {
			b.WriteString("yes")
		} else {
			b.WriteString("no")
		}
	}
	return b.String()
}

// sanitizeDetail bounds and redacts one multi-line human string.
func sanitizeDetail(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxReportDetailLines {
		lines = lines[:maxReportDetailLines]
	}
	for i, l := range lines {
		lines[i] = redactLine(truncate(l, maxReportDetailWidth))
	}
	return strings.Join(lines, "\n")
}

// sanitizeRule puts a live firewall rule through the same redaction.
//
// No iptables rule matches the key-assignment pattern today — a rule line
// starts with a flag, not a key — so in practice this changes nothing. It is
// here so the property has no exception to remember: every string in a stored
// report went through layer 2. A `--comment password=…` would be blanked.
func sanitizeRule(r iptables.Rule) iptables.Rule {
	out := iptables.Rule{
		Table: clampField(r.Table),
		Chain: clampField(r.Chain),
		Args:  make([]string, 0, len(r.Args)),
	}
	for _, a := range r.Args {
		out.Args = append(out.Args, redactLine(truncate(a, maxReportDetailWidth)))
	}
	return out
}

func clampField(s string) string { return truncate(strings.TrimSpace(s), maxReportField) }
