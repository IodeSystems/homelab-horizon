package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// This file is the PURE half. It takes what hz wants and what the machine has
// and returns a list of changes. It reads no file, runs no command, dials
// nothing and asks no clock — so `hz-agent diff` computes the same answer for
// anybody, root or not, on the box or off it. seam_test.go enforces that.

// ChangeKind is what would happen to one target.
type ChangeKind string

const (
	// KindUnchanged — desired and observed agree. Reported, not acted on.
	//
	// This is the case the whole package exists to get right: identical
	// contents must not report changed, because a change reloads HAProxy and
	// a reload for nothing is a reload that eventually breaks something.
	KindUnchanged ChangeKind = "unchanged"

	// KindCreate — the target does not exist yet.
	KindCreate ChangeKind = "create"

	// KindUpdate — it exists and differs.
	KindUpdate ChangeKind = "update"

	// KindRemove — it is in a directory hz claims, the claim covers its name,
	// and the payload does not list it.
	//
	// The only kind that destroys something, and the only one whose target is
	// not named anywhere in the payload. Both halves of that are why it goes
	// through Desired.prunable rather than being decided here.
	KindRemove ChangeKind = "remove"

	// KindUnknown — one of the two sides is missing, so the agent will not
	// claim a verdict. Distinct from "unchanged" on purpose: an unreadable
	// file reported as in-sync is a lie that survives right up until it
	// matters.
	//
	// Either side can be the missing one. Usually it is the observed side —
	// the agent could not read the target. It is also the answer when hz
	// withheld the DESIRED side (IPTablesSection.StoodDown): there is nothing
	// to compare against, so there is no verdict either way, and the same
	// three things have to follow — it is not pending, it is not in sync, and
	// Apply must not act on it.
	KindUnknown ChangeKind = "unknown"
)

// Change is one line of the report.
type Change struct {
	Subsystem Subsystem  `json:"subsystem"`
	Target    string     `json:"target"`
	Kind      ChangeKind `json:"kind"`

	// Detail is human text, already redacted. Never raw file contents.
	Detail string `json:"detail,omitempty"`

	// secret marks a change whose target carries key material, so the differ
	// never widens it into line-level output. Unexported: nothing downstream
	// gets to opt out of it.
	secret bool
}

// Plan is one reconcile pass, computed and not yet applied.
type Plan struct {
	Machine    string   `json:"machine"`
	Generation string   `json:"generation"`
	Changes    []Change `json:"changes"`
}

// Pending returns only the changes that would actually do something.
func (p Plan) Pending() []Change {
	out := make([]Change, 0, len(p.Changes))
	for _, c := range p.Changes {
		if c.Kind == KindCreate || c.Kind == KindUpdate || c.Kind == KindRemove {
			out = append(out, c)
		}
	}
	return out
}

// Changed reports whether applying this plan would touch the machine.
func (p Plan) Changed() bool { return len(p.Pending()) > 0 }

// Unknown returns the targets the agent could not read. A plan with unknowns
// is not a clean bill of health, and callers that print "in sync" must say so.
func (p Plan) Unknown() []Change {
	out := make([]Change, 0, len(p.Changes))
	for _, c := range p.Changes {
		if c.Kind == KindUnknown {
			out = append(out, c)
		}
	}
	return out
}

// Compute is the reconcile: desired plus observed, in, changes out.
func Compute(d *Desired, obs Observed) Plan {
	p := Plan{Changes: []Change{}}
	if d == nil {
		return p
	}
	p.Machine = d.Machine
	p.Generation = d.Fingerprint()

	for _, of := range d.allFiles() {
		p.Changes = append(p.Changes, fileChange(of, obs.Files[of.File.Path]))
	}
	p.Changes = append(p.Changes, removals(d, obs)...)
	p.Changes = append(p.Changes, iptablesChanges(d.IPTables, obs)...)
	p.Changes = append(p.Changes, configChanges(d, obs)...)
	return p
}

// AppliedGenerations is the agent's record of the sealed-config generation it
// last restarted each unit for, keyed by unit name.
//
// It is the ONE thing this agent remembers across a restart, and
// generations.go says at length why it has to. What matters here is what an
// ABSENT entry means: the agent has no record for that unit, which is NOT
// "the generation changed". See DecideConfigRestarts.
type AppliedGenerations map[string]string

// ConfigRestart is one unit whose sealed config hz has moved.
//
// From empty means the agent had no record for the unit — an ADOPTION, not a
// restart. The two are carried in the same shape because they are the same
// comparison with different answers, and separated into two fields of
// ConfigDecision because they lead to two different actions.
type ConfigRestart struct {
	Unit string `json:"unit"`
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// ConfigDecision is the whole pure answer to "what does the sealed-config
// generation mean for this pass".
type ConfigDecision struct {
	// Restarts is the units whose recorded generation and hz's differ. These
	// and only these get restarted.
	Restarts []ConfigRestart

	// Adopted is the units hz named a generation for that the agent has no
	// record of. NOTHING IS RESTARTED FOR THESE — the record is written and
	// that is all. This is the first-run case, and it is the whole safety
	// property of the feature (see DecideConfigRestarts).
	Adopted []ConfigRestart

	// Forgotten is the units the agent holds a record for that the payload no
	// longer names. Their entries are dropped from Next; nothing is
	// restarted, nothing is stopped. hz not listing a unit is item 16's
	// business, not this trigger's.
	Forgotten []string

	// Next is the record to persist IF every restart above succeeds. NIL
	// means "do not write anything" — the decision could not be made, so the
	// record must be left exactly as it is.
	Next AppliedGenerations

	// Unknown is why no decision could be made, when none could. Set when the
	// agent could not read its own record: an unreadable record is not an
	// empty one, and treating it as empty would adopt hz's current answer and
	// throw away the record of what is actually running.
	Unknown string
}

// Restarting reports whether this decision would restart anything.
func (c ConfigDecision) Restarting() bool { return len(c.Restarts) > 0 }

// DecideConfigRestarts is the whole trigger, and it is PURE: hz's projection
// plus the agent's own record in, a list of units to restart out. It reads no
// file, runs no systemctl and asks no clock, so the same inputs give the same
// answer on a box and in a test — which is the only way the rules below can be
// checked at all.
//
// # THE RULES, AND THE ONE THAT MATTERS MOST
//
//	no record for the unit        ADOPT. Restart NOTHING.
//	record, same generation       nothing.
//	record, different generation  restart THAT unit, and only that unit.
//	generation empty in the payload   nothing, ever. The record is kept.
//	record for a unit hz no longer names   forget it. Restart nothing.
//	record unreadable             no decision at all, and no write.
//
// UNKNOWN AND CHANGED ARE DIFFERENT STATES. An agent that treated "I have
// never seen a generation for this unit" as "the generation moved" would
// restart every unit on every box the first time it was armed — a fleet-wide
// outage produced by a feature whose entire purpose is a graceful restart. The
// adopt branch is not a convenience; it is the feature's safety property, and
// it is the first thing its tests check.
//
// AN EMPTY GENERATION IS NEVER A RESTART, and it is not one state either
// (projection.Unit.ConfigGeneration): empty with no SectionConfig gap beside
// it means hz resolved the address and holds no config for it, empty WITH one
// means hz does not know. Neither is a restart, so this function does not have
// to tell them apart — but it must not DISCARD the record on either, or hz
// going quiet for one pass and coming back with a new generation would be
// adopted silently and the restart would be missed. So an empty generation
// leaves the unit's record exactly as it was.
//
// FORGETTING IS GUARDED BY THE UNITS GAP for the same reason. hz failing to
// project units at all is not hz saying there are none, and dropping every
// record on a transient gap would turn the next real move into a silent
// adoption.
func DecideConfigRestarts(d *Desired, obs Observed) ConfigDecision {
	var dec ConfigDecision

	// The agent could not read its own record. No verdict either way, and —
	// critically — Next stays nil, so nothing overwrites a record that could
	// not be read.
	if obs.GenerationsErr != "" {
		dec.Unknown = obs.GenerationsErr
		return dec
	}
	// hz did not project for this machine. nil means unmanaged everywhere
	// else in this package and it means unmanaged here: no opinion, no
	// restarts, and no reason to touch the record.
	if d == nil || d.Model == nil {
		return dec
	}

	applied := obs.ConfigGenerations
	next := AppliedGenerations{}
	named := map[string]bool{}

	for _, u := range d.Model.Units {
		if u.Name == "" || named[u.Name] {
			continue
		}
		named[u.Name] = true
		prior, had := applied[u.Name]

		if u.ConfigGeneration == "" {
			// hz holds no config for this unit, or does not know which. Not a
			// restart, and not a reason to forget what the unit is running.
			if had {
				next[u.Name] = prior
			}
			continue
		}
		switch {
		case !had:
			dec.Adopted = append(dec.Adopted, ConfigRestart{Unit: u.Name, To: u.ConfigGeneration})
		case prior != u.ConfigGeneration:
			dec.Restarts = append(dec.Restarts, ConfigRestart{Unit: u.Name, From: prior, To: u.ConfigGeneration})
		}
		next[u.Name] = u.ConfigGeneration
	}

	// A record for a unit hz no longer lists. Dropped — unless hz could not
	// work out this machine's units at all, in which case an empty list is
	// not an answer and forgetting on it would lose the record for nothing.
	if d.Model.Unresolvable(projection.SectionUnits) {
		for _, name := range sortedUnits(applied) {
			if !named[name] {
				next[name] = applied[name]
			}
		}
	} else {
		for _, name := range sortedUnits(applied) {
			if !named[name] {
				dec.Forgotten = append(dec.Forgotten, name)
			}
		}
	}

	sort.SliceStable(dec.Restarts, func(i, j int) bool { return dec.Restarts[i].Unit < dec.Restarts[j].Unit })
	sort.SliceStable(dec.Adopted, func(i, j int) bool { return dec.Adopted[i].Unit < dec.Adopted[j].Unit })
	dec.Next = next
	return dec
}

// configChanges turns the decision into report lines.
//
// AN ADOPTION IS A KindCreate, and that is not cosmetic. Apply is only called
// when a plan has something pending (cmd/hz-agent/run.go), so a first sighting
// that reported as unchanged would never be written down — and the agent would
// still have no record the next time the generation actually moved, and would
// adopt again, forever. The trigger would never fire on a box where nothing
// else ever changes. So the line is what it is: the agent will create a record
// and restart nothing, and it says exactly that.
//
// A forget produces no line. It is bookkeeping with no effect on the machine,
// it happens whenever the record is next written, and a stale entry is inert
// in the meantime because the decision only ever walks units the payload
// names.
func configChanges(d *Desired, obs Observed) []Change {
	dec := DecideConfigRestarts(d, obs)
	if dec.Unknown != "" {
		return []Change{{
			Subsystem: SubsystemConfig,
			Target:    "applied config generations",
			Kind:      KindUnknown,
			Detail:    dec.Unknown + " — no unit will be restarted for a config change until this is readable",
		}}
	}
	out := make([]Change, 0, len(dec.Restarts)+len(dec.Adopted))
	for _, r := range dec.Restarts {
		out = append(out, Change{
			Subsystem: SubsystemConfig,
			Target:    r.Unit,
			Kind:      KindUpdate,
			Detail: fmt.Sprintf("the sealed config hz says this unit should be running moved from %s to %s — it will be restarted",
				short(r.From), short(r.To)),
		})
	}
	for _, a := range dec.Adopted {
		out = append(out, Change{
			Subsystem: SubsystemConfig,
			Target:    a.Unit,
			Kind:      KindCreate,
			Detail: fmt.Sprintf("first sighting: the agent will record sealed config %s for this unit and restart NOTHING. A generation it has never seen is not a generation that moved",
				short(a.To)),
		})
	}
	return out
}

// short trims a generation for a report line. The digest is 64 hex characters
// and a human comparing two of them reads the first few.
func short(gen string) string {
	if len(gen) > 12 {
		return gen[:12] + "…"
	}
	if gen == "" {
		return "(none)"
	}
	return gen
}

// removals turns the payload's directory claims into report lines.
//
// It walks what the agent OBSERVED in each claimed directory and asks
// Desired.prunable about each entry — it never walks the payload and never
// composes a path of its own, so a removal line can only ever name something
// that is really there, inside something hz really claimed.
//
// A directory that could not be listed is KindUnknown, the same answer an
// unreadable file gets: "I would remove nothing here because I cannot see" is
// honest, and "nothing to remove" would not be.
func removals(d *Desired, obs Observed) []Change {
	var out []Change
	for _, od := range d.dirs() {
		claimed, ok := cleanDir(od.Dir.Path)
		if !ok {
			out = append(out, Change{
				Subsystem: od.Subsystem,
				Target:    od.Dir.Path,
				Kind:      KindUnknown,
				Detail:    "not an absolute directory path, so nothing here is claimed",
			})
			continue
		}
		st := obs.Dirs[claimed]
		if st.ReadErr != "" {
			out = append(out, Change{
				Subsystem: od.Subsystem,
				Target:    claimed,
				Kind:      KindUnknown,
				Detail:    "cannot list it: " + st.ReadErr,
			})
			continue
		}
		if !st.Exists {
			// Not provisioned yet. The files the payload puts in it are
			// creates; there is nothing to prune from a directory with
			// nothing in it.
			continue
		}
		for _, e := range st.Entries {
			if !e.Regular {
				// Directories, symlinks, sockets and devices are never
				// removed. hz renders plain files; anything else in a claimed
				// directory was put there by something the agent does not
				// speak for.
				continue
			}
			target := claimed + "/" + e.Name
			sub, prune := d.prunable(target)
			if !prune {
				continue
			}
			out = append(out, Change{
				Subsystem: sub,
				Target:    target,
				Kind:      KindRemove,
				Detail:    "hz owns this directory and no longer lists this file",
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// fileChange compares one desired file against what is on disk.
func fileChange(of ownedFile, st FileState) Change {
	c := Change{Subsystem: of.Subsystem, Target: of.File.Path, secret: of.File.Secret}

	switch {
	case st.ReadErr != "":
		c.Kind = KindUnknown
		c.Detail = "cannot read it: " + st.ReadErr
	case !st.Exists:
		c.Kind = KindCreate
		c.Detail = fmt.Sprintf("would create it, %d bytes, mode %04o", len(of.File.Contents), of.File.Mode)
	case st.Contents == of.File.Contents:
		c.Kind = KindUnchanged
		c.Detail = "already matches"
	default:
		c.Kind = KindUpdate
		c.Detail = describeTextChange(st.Contents, of.File.Contents, of.File.Secret)
	}
	return c
}

// iptablesChanges turns the rule sets into report lines.
//
// It reuses iptables.Classify — the same pure classifier the reconciler itself
// uses — so the report cannot disagree with what Apply would then do. Building
// a second opinion here is precisely the drift that shipped the MFA jail
// covering FORWARD but not INPUT (see internal/wireguard/apply.go's
// rebuildChain comment); one definition, two consumers.
func iptablesChanges(sec *IPTablesSection, obs Observed) []Change {
	if sec == nil {
		return nil
	}
	// hz withheld the desired set. Checked BEFORE the observed side, because
	// a readable live set changes nothing here: with no expected set to
	// compare it against, every live rule would classify as unknown-or-stale
	// off an empty opinion, and that "comparison" is the damage. Reported as
	// a line rather than as nothing, so the pass cannot read as in sync.
	if sec.StoodDown {
		return []Change{{
			Subsystem: SubsystemIPTables,
			Target:    "live rule set",
			Kind:      KindUnknown,
			Detail:    orStoodDownWhy(sec.Why),
		}}
	}
	if !obs.IPTablesReadable {
		return []Change{{
			Subsystem: SubsystemIPTables,
			Target:    "live rule set",
			Kind:      KindUnknown,
			Detail:    obs.IPTablesWhy,
		}}
	}

	classified := iptables.Classify(obs.LiveRules, sec.Expected, sec.Stale, sec.Blessed)

	live := make(map[string]struct{}, len(obs.LiveRules))
	for _, r := range obs.LiveRules {
		live[r.Canonical()] = struct{}{}
	}

	var out []Change
	for _, r := range sec.Expected {
		if _, ok := live[r.Canonical()]; !ok {
			out = append(out, Change{
				Subsystem: SubsystemIPTables,
				Target:    r.String(),
				Kind:      KindCreate,
				Detail:    "expected rule is not installed",
			})
		}
	}
	for _, c := range classified {
		if c.State == iptables.StateStale {
			out = append(out, Change{
				Subsystem: SubsystemIPTables,
				Target:    c.Rule.String(),
				Kind:      KindUpdate,
				Detail:    "stale rule would be removed",
			})
		}
	}

	summary := iptables.SummarizeClassified(classified)
	out = append(out, Change{
		Subsystem: SubsystemIPTables,
		Target:    "live rule set",
		Kind:      KindUnchanged,
		Detail: fmt.Sprintf("%d live: %d expected, %d stale, %d blessed, %d unknown (unknown and blessed are left alone)",
			len(classified), summary.Expected, summary.Stale, summary.Blessed, summary.Unknown),
	})

	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// orStoodDownWhy keeps a stood-down line from printing with no explanation.
// hz always sends one; a payload from a version that did not, or a hand-built
// section, still has to say what happened rather than showing a bare kind.
func orStoodDownWhy(why string) string {
	if strings.TrimSpace(why) == "" {
		return "hz withheld the desired firewall for this pass and did not say why; nothing will be added or removed"
	}
	return why
}
