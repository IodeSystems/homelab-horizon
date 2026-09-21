package agent

import (
	"fmt"
	"sort"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
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

	// KindUnknown — the agent could not read the current state, so it will
	// not claim one. Distinct from "unchanged" on purpose: an unreadable file
	// reported as in-sync is a lie that survives right up until it matters.
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
		if c.Kind == KindCreate || c.Kind == KindUpdate {
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

	for _, of := range d.files() {
		p.Changes = append(p.Changes, fileChange(of, obs.Files[of.File.Path]))
	}
	p.Changes = append(p.Changes, iptablesChanges(d.IPTables, obs)...)
	return p
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
