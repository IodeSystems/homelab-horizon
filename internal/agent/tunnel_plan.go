package agent

import (
	"fmt"
	"sort"
	"strings"
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
	return t.Action == TunnelCreate || t.Action == TunnelSync
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
func tunnelChanges(d *Desired, obs Observed) []Change {
	decs := DecideTunnels(d, obs)
	out := make([]Change, 0, len(decs))
	for _, dec := range decs {
		c := Change{Subsystem: SubsystemSegments, Target: dec.Tunnel.Interface, Detail: dec.Why}
		switch dec.Action {
		case TunnelCreate, TunnelAdopt:
			c.Kind = KindCreate
		case TunnelSync:
			c.Kind = KindUpdate
		case TunnelUnchanged:
			c.Kind = KindUnchanged
		default:
			c.Kind = KindUnknown
		}
		out = append(out, c)
	}
	return out
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
