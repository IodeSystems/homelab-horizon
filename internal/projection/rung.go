package projection

import (
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Where a rung's placements are answered from (plan/design/estate.md Part A
// §5, "a rung placed elsewhere").
//
// Two states, and neither is "unknown". PlacementHere means THIS hz holds the
// rung's placements, so "which machines run it" is answered by its own
// registrations — placed, unplaced, or (when the registrations cannot be read)
// unknown, which the caller that reads them decides. PlacementRemote means
// another hz holds them, and "no machine here" is then the correct answer
// rather than a gap: the question belongs to the Upstream.
const (
	PlacementHere   = "here"
	PlacementRemote = "remote"
)

// RungPlacement is a STATEMENT about where a rung is placed, not a reading of
// the fleet. It is computed from the records alone.
type RungPlacement struct {
	State string `json:"state"`

	// Upstream is the Machine running the hz that holds the placements, and
	// URL is that machine's HZ.URL. Both empty unless State is remote.
	Upstream string `json:"upstream,omitempty"`
	URL      string `json:"url,omitempty"`

	// Statement is the sentence a screen shows. Always set.
	Statement string `json:"statement"`

	// Unresolved is non-empty only when the records contradict themselves —
	// an Upstream naming no machine, or a machine without an HZ marker. Save
	// refuses both (config.CheckUpstream), so this is a config that reached
	// the projection without passing Save, and the projection says so rather
	// than trusting it.
	Unresolved []Gap `json:"unresolved,omitempty"`
}

// PlaceRung says where one rung's placements are answered from.
//
// A rung with an Upstream is NOT a gap: it is placed in that hz, and this hz
// having no machine for it is the model being correct. Emitting a Gap there
// would make every rung declared here and run elsewhere read as broken — the
// misreport estate.md §5 exists to remove.
func PlaceRung(cfg *config.Config, env config.Environment) RungPlacement {
	if env.Upstream == "" {
		return RungPlacement{
			State:     PlacementHere,
			Statement: "placed here: this hz's own registrations say which machines run " + env.Project + "/" + env.Name,
		}
	}
	out := RungPlacement{
		State:    PlacementRemote,
		Upstream: env.Upstream,
		Statement: "placed in " + env.Upstream + ": that hz holds the placements of " + env.Project + "/" + env.Name +
			", so which machines run it is a question for it, not a gap here",
	}
	m, ok := cfg.FindMachine(env.Upstream)
	switch {
	case !ok:
		out.Unresolved = append(out.Unresolved, Gap{Section: SectionMachine, Reason: ReasonUnmodelled,
			Why: env.Project + "/" + env.Name + " names upstream " + env.Upstream + ", and no machine record declares it — declare the machine with an hz URL"})
	case m.HZ == nil:
		out.Unresolved = append(out.Unresolved, Gap{Section: SectionMachine, Reason: ReasonUnmodelled,
			Why: env.Project + "/" + env.Name + " names upstream " + env.Upstream + ", which is declared without an hz marker — nothing says it runs an hz"})
	default:
		out.URL = m.HZ.URL
	}
	return out
}
