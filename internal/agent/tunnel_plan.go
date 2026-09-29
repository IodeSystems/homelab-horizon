package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The PURE half of the segment tunnels: payload plus observed in, one decision
// per tunnel out. No file is read, no interface is looked up and nothing is
// run here — seam_test.go lists this file with plan.go. apply.go acts on the
// decision, and it re-derives it rather than trusting a Plan (the discipline
// restartForConfig and prune already follow).

// TunnelAction is what the agent will do to one segment interface.
type TunnelAction string

const (
	// TunnelCreate — the interface does not exist. Create it, load the
	// config, load this box's private key, set the address, bring it up.
	// Safe on first sight: there is nothing live to disturb.
	TunnelCreate TunnelAction = "create"

	// TunnelSync — the interface exists, the agent's own config file for it
	// exists, and something differs: the file, the address set, or the link
	// is down. Re-load the config and key, fix the address, bring it up.
	TunnelSync TunnelAction = "sync"

	// TunnelAdopt — FIRST SIGHTING (CLAUDE.md invariant 11). The interface
	// already exists and the agent has never written its config file, so the
	// interface was made by something else — wg-quick, an operator, a
	// previous install. The agent writes the config file as its record and
	// DOES NOT TOUCH THE LIVE INTERFACE. A later change to what hz wants is a
	// TunnelSync; an interface the agent has only seen is never bounced for
	// being seen.
	TunnelAdopt TunnelAction = "adopt"

	// TunnelUnchanged — the file matches, the link is up with exactly the
	// wanted address. Nothing to do.
	TunnelUnchanged TunnelAction = "unchanged"

	// TunnelUnknown — one side could not be read, or this box holds no
	// private key for the segment. No verdict and no action.
	TunnelUnknown TunnelAction = "unknown"

	// TunnelRemove — the machine has LEFT the segment (hz's model says so,
	// affirmatively: see leftSegment) and this interface is one the agent
	// itself created (TunnelRecord.Created). Delete the interface. Never
	// decided for an adopted interface: the agent did not make it, so it is
	// not the agent's to remove.
	TunnelRemove TunnelAction = "remove"

	// TunnelForget — the same, but the interface is already gone. Nothing
	// is run; the agent drops it from its record of what it created, so a
	// hand-made interface that later takes the name is not torn down as if
	// it were the agent's.
	TunnelForget TunnelAction = "forget"
)

// TunnelDecision is the answer for one tunnel.
type TunnelDecision struct {
	Tunnel SegmentTunnel
	Action TunnelAction

	// StaleAddrs is every address on the live interface other than the one
	// wanted — what a sync removes. Empty on create (nothing is there) and on
	// adopt (nothing is touched).
	StaleAddrs []string

	// Why is one sentence for the report.
	Why string
}

// Acts reports whether this decision changes the live interface.
func (t TunnelDecision) Acts() bool {
	return t.Action == TunnelCreate || t.Action == TunnelSync || t.Action == TunnelRemove
}

// DecideTunnels is the whole decision, in payload order.
//
//	file unreadable, or link unreadable       unknown
//	link exists, the agent's file does not    ADOPT: write the file, touch nothing
//	link missing                              create   (needs this box's key)
//	file differs / address differs / down     sync     (needs this box's key)
//	otherwise                                 unchanged
//
// The key check comes AFTER adopt, deliberately: adopting touches no
// interface, so it needs no key, and a box that has not enrolled yet must still
// be able to record what it found.
func DecideTunnels(d *Desired, obs Observed) []TunnelDecision {
	if d == nil || d.Segments == nil {
		return nil
	}
	out := make([]TunnelDecision, 0, len(d.Segments.Tunnels))
	for _, t := range d.Segments.Tunnels {
		out = append(out, decideTunnel(t, obs))
	}
	return out
}

func decideTunnel(t SegmentTunnel, obs Observed) TunnelDecision {
	dec := TunnelDecision{Tunnel: t}
	file := obs.Files[t.File.Path]
	link, looked := obs.Links[t.Interface]

	switch {
	case file.ReadErr != "":
		dec.Action, dec.Why = TunnelUnknown, "cannot read the agent's config file for it: "+file.ReadErr
		return dec
	case !looked:
		dec.Action, dec.Why = TunnelUnknown, "the agent did not look up the interface"
		return dec
	case link.ReadErr != "":
		dec.Action, dec.Why = TunnelUnknown, "cannot look up the interface: "+link.ReadErr
		return dec
	case link.Exists && !file.Exists:
		dec.Action = TunnelAdopt
		dec.Why = "first sighting: " + t.Interface + " already exists and hz-agent has never written its config. " +
			"The agent will write the config file as its record and will NOT touch the live interface; a later change hz makes is applied"
		return dec
	}

	stale := staleAddrs(link.Addrs, t.Address)
	var action TunnelAction
	var why string
	switch {
	case !link.Exists:
		action = TunnelCreate
		why = fmt.Sprintf("would create %s for segment %s at %s and load %s", t.Interface, t.Segment, t.Address, peerCount(t))
	case !file.Exists || file.Contents != t.File.Contents:
		action, why = TunnelSync, "the config hz wants differs from the one loaded; would re-load it with `wg syncconf`"
	case len(stale) > 0 || !hasAddr(link.Addrs, t.Address):
		action, why = TunnelSync, fmt.Sprintf("the interface carries %v, want exactly %s", link.Addrs, t.Address)
	case !link.Up:
		action, why = TunnelSync, "the interface is down; would bring it up"
	default:
		dec.Action, dec.Why = TunnelUnchanged, "up, at "+t.Address+", config matches"
		return dec
	}

	key := obs.SegmentKeys[t.Segment]
	switch {
	case key.ReadErr != "":
		dec.Action, dec.Why = TunnelUnknown, "cannot check this box's private key for "+t.Segment+": "+key.ReadErr
		return dec
	case !key.Exists:
		dec.Action = TunnelUnknown
		dec.Why = "this box holds no private key for segment " + t.Segment +
			", so the interface cannot be brought up. `hz-agent enroll` mints one on this box and reports its public half to hz"
		return dec
	}

	dec.Action, dec.Why = action, why
	if action == TunnelSync {
		dec.StaleAddrs = stale
	}
	return dec
}

// tunnelChanges turns the decisions into report lines. An adoption is a
// KindCreate for the reason configChanges gives: Apply runs only when a plan
// has something pending, and the record the adoption writes is the thing that
// makes the next pass a comparison rather than another first sighting.
//
// A teardown is a KindRemove, and so is a forget: a forget changes nothing
// live, but it is pending in the same sense an adoption is — Apply has to run
// for the record to stop naming an interface that is gone. apply.go's prune
// skips every segments line: an interface is not a file, and the teardown is
// re-derived from the payload (applyTunnels), never read off a Change.
func tunnelChanges(d *Desired, obs Observed) []Change {
	decs := append(DecideTunnels(d, obs), DecideTeardowns(d, obs)...)
	out := make([]Change, 0, len(decs)+1)
	if d != nil && d.Model != nil && obs.TunnelRecordErr != "" {
		out = append(out, Change{
			Subsystem: SubsystemSegments,
			Target:    "segment tunnel record",
			Kind:      KindUnknown,
			Detail:    obs.TunnelRecordErr + " — no segment interface will be torn down until this is readable",
		})
	}
	for _, dec := range decs {
		c := Change{Subsystem: SubsystemSegments, Target: dec.Tunnel.Interface, Detail: dec.Why}
		switch dec.Action {
		case TunnelCreate, TunnelAdopt:
			c.Kind = KindCreate
		case TunnelSync:
			c.Kind = KindUpdate
		case TunnelRemove, TunnelForget:
			c.Kind = KindRemove
		case TunnelUnchanged:
			c.Kind = KindUnchanged
		default:
			c.Kind = KindUnknown
		}
		out = append(out, c)
	}
	return out
}

// TunnelRecord is the agent's last-known-good for its segment tunnels, kept
// on disk (tunnel_state.go) so the tunnels come back after a reboot WITHOUT a
// poll of hz (CLAUDE.md invariant 5). A box that reaches hz through one of
// these tunnels could otherwise never ask.
//
// ONLY THE SEGMENTS SECTION, not the whole Desired. It is the one section
// whose live state is kernel-only: every other section's files are already on
// disk and their services read them at boot, and the rest of a payload holds
// wg0.conf and served TLS bundles — private keys a second copy would only
// spread further. A SegmentTunnel carries public keys, an address and a file
// body that is already written 0600; nothing here is a secret the box does
// not already hold.
type TunnelRecord struct {
	// Machine is who the payload was for, so a booted Desired is addressed.
	Machine string `json:"machine"`

	// Fingerprint is the fingerprint of the last payload applied into this
	// record. For the boot log line — "which payload am I running" — and
	// NOTHING ELSE. It is a content hash, so it cannot order two payloads:
	// Desired carries no sequence, and a rollback floor needs one. There is
	// no floor here, deliberately, rather than a clock rule standing in for
	// one.
	Fingerprint string `json:"fingerprint"`

	// AppliedAt is when that was, RFC 3339. Printed, never compared: a
	// three-year-old record is a valid record (invariant 5).
	AppliedAt string `json:"applied_at"`

	// Segments is every tunnel this box runs, as the agent last applied it —
	// what the boot path brings up.
	Segments *SegmentsSection `json:"segments,omitempty"`

	// Created is interface -> segment for every interface THIS AGENT created
	// (a TunnelCreate that succeeded). The only interfaces a teardown may
	// ever touch. An adopted interface is never in it.
	Created map[string]string `json:"created,omitempty"`
}

// Desired is the record as a payload the ordinary plan/apply path takes: the
// boot path is Compute and Apply over this, not a second implementation. No
// Model, so nothing in it can decide a teardown or a restart.
func (r TunnelRecord) Desired() *Desired {
	return &Desired{Machine: r.Machine, Segments: r.Segments}
}

// leftSegment reports whether hz's model says, AFFIRMATIVELY, that this
// machine is not a member of seg.
//
// EMPTY AND UNKNOWN ARE DIFFERENT STATES (CLAUDE.md invariant 2). A tunnel
// missing from Desired.Segments proves nothing — hz omits one it could not
// render (with a gap) and serves nil for a machine with none — so absence
// from the tunnel list is never the test. The test is the MODEL: hz projected
// this machine, has no gap about its machine record or its segments, and does
// not list seg among its memberships. Anything short of that is no opinion.
func leftSegment(d *Desired, seg string) bool {
	if d == nil || d.Model == nil {
		return false
	}
	for _, g := range d.Model.Unresolved {
		if g.Section == projection.SectionSegments || g.Section == projection.SectionMachine {
			return false
		}
	}
	for _, s := range d.Model.Segments {
		if s.Name == seg {
			return false
		}
	}
	return true
}

// DecideTeardowns is what leaving a segment does: remove each interface the
// agent created for a segment the machine has left, and forget each one that
// is already gone. Sorted by interface name.
//
// Three bounds, all of which must hold:
//
//   - the interface is in the agent's own record of what it CREATED — never
//     an adopted or hand-made one (the claimed rule, ownership.go's shape);
//   - hz does not serve it in this payload;
//   - hz's model says the machine left its segment (leftSegment).
//
// An unreadable record decides nothing.
func DecideTeardowns(d *Desired, obs Observed) []TunnelDecision {
	if d == nil || obs.TunnelRecordErr != "" || len(obs.CreatedTunnels) == 0 {
		return nil
	}
	served := servedInterfaces(d)
	ifaces := make([]string, 0, len(obs.CreatedTunnels))
	for iface := range obs.CreatedTunnels {
		ifaces = append(ifaces, iface)
	}
	sort.Strings(ifaces)

	var out []TunnelDecision
	for _, iface := range ifaces {
		seg := obs.CreatedTunnels[iface]
		if served[iface] || !leftSegment(d, seg) {
			continue
		}
		dec := TunnelDecision{Tunnel: SegmentTunnel{Segment: seg, Interface: iface}}
		link, looked := obs.Links[iface]
		switch {
		case !looked:
			dec.Action, dec.Why = TunnelUnknown, "the agent did not look up the interface, so it will not tear it down"
		case link.ReadErr != "":
			dec.Action, dec.Why = TunnelUnknown, "cannot look up the interface: "+link.ReadErr
		case !link.Exists:
			dec.Action = TunnelForget
			dec.Why = "this machine left segment " + seg + " and " + iface + " is already gone; the agent drops it from its record"
		default:
			dec.Action = TunnelRemove
			dec.Why = "this machine left segment " + seg + "; hz-agent created " + iface + " and will delete it"
		}
		out = append(out, dec)
	}
	return out
}

func servedInterfaces(d *Desired) map[string]bool {
	out := map[string]bool{}
	if d != nil && d.Segments != nil {
		for _, t := range d.Segments.Tunnels {
			out[t.Interface] = true
		}
	}
	return out
}

// NextTunnelRecord is the record after a pass over d.
//
// applied says whether the pass succeeded (or found nothing to do). Only then
// does d become the last-known-good. A FAILED pass does not replace it — but
// what it did to interfaces is still recorded, because those are facts about
// the box, not about hz: an interface it created is the agent's to remove
// later, and one it tore down must not be booted again.
//
// Which tunnels are in the record after a successful pass:
//
//	served by d                               d's version
//	not served, machine affirmatively left    dropped (torn down, or not ours)
//	not served, anything else (a gap, nil)    the previous version — the live
//	                                          interface was left as it was, so
//	                                          that is what it is running
//
// AppliedAt is left to the caller: this half does not read the clock.
func NextTunnelRecord(prev TunnelRecord, d *Desired, res Result, applied bool) TunnelRecord {
	next := TunnelRecord{
		Machine:     prev.Machine,
		Fingerprint: prev.Fingerprint,
		AppliedAt:   prev.AppliedAt,
		Created:     map[string]string{},
	}
	for iface, seg := range prev.Created {
		next.Created[iface] = seg
	}
	segOf := map[string]string{}
	if d != nil && d.Segments != nil {
		for _, t := range d.Segments.Tunnels {
			segOf[t.Interface] = t.Segment
		}
	}
	for _, iface := range res.CreatedTunnels {
		if seg, ok := segOf[iface]; ok {
			next.Created[iface] = seg
		}
	}
	gone := map[string]bool{}
	for _, iface := range res.TornDown {
		gone[iface] = true
		delete(next.Created, iface)
	}
	if len(next.Created) == 0 {
		next.Created = nil
	}

	var tunnels []SegmentTunnel
	if !applied || d == nil {
		if prev.Segments != nil {
			for _, t := range prev.Segments.Tunnels {
				if !gone[t.Interface] {
					tunnels = append(tunnels, t)
				}
			}
		}
	} else {
		next.Machine = d.Machine
		next.Fingerprint = d.Fingerprint()
		served := servedInterfaces(d)
		if d.Segments != nil {
			tunnels = append(tunnels, d.Segments.Tunnels...)
		}
		if prev.Segments != nil {
			for _, t := range prev.Segments.Tunnels {
				if served[t.Interface] || gone[t.Interface] || leftSegment(d, t.Segment) {
					continue
				}
				tunnels = append(tunnels, t)
			}
		}
	}
	if len(tunnels) > 0 {
		next.Segments = &SegmentsSection{Tunnels: tunnels}
	}
	return next
}

func peerCount(t SegmentTunnel) string {
	n := strings.Count(t.File.Contents, "[Peer]\n")
	if n == 1 {
		return "1 peer"
	}
	return fmt.Sprintf("%d peers", n)
}

func hasAddr(addrs []string, want string) bool {
	for _, a := range addrs {
		if a == want {
			return true
		}
	}
	return false
}

// staleAddrs is what a sync removes: every address but the wanted one, except
// link-local ones, which the kernel may add on its own and which a sync that
// removed them would remove again on every pass.
func staleAddrs(addrs []string, want string) []string {
	var out []string
	for _, a := range addrs {
		if strings.HasPrefix(strings.ToLower(a), "fe80:") || strings.HasPrefix(a, "169.254.") {
			continue
		}
		if a != want {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
