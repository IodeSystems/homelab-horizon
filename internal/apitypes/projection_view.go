package apitypes

// What GET /api/v1/machines/projection serves: `project(global, machine)` for
// one machine, read-only, for a human rather than for an agent.
//
// # Why this shape exists at all, given internal/projection already has it
//
// It is a MIRROR of projection.MachineConfig and its parts, field for field
// and json-tag for json-tag. tygo generates ui/src/api/generated-types.ts from
// this package alone — it cannot follow a type into another package, and a
// struct that embedded projection.MachineConfig would come out of tygo as
// `any`, which is how cm_wire.go's aliases broke the generated file before.
// So the wire shape lives here, exactly as MachineResp mirrors config.Machine
// and EnvironmentResp mirrors config.Environment.
//
// A mirror can drift from the thing it mirrors, and a drifted mirror is worse
// than no mirror: it silently drops a field, and a dropped field on THIS type
// is a dropped Gap. So the correspondence is enforced rather than remembered —
// internal/server/handlers_api_model_test.go reflects over both structs and
// fails if the json tag sets differ in either direction.
//
// # The one rule this type exists to keep
//
// Every slice is non-nil on the wire, and Unresolved is rendered even when
// empty. An empty section with no gap beside it is hz saying "nothing is
// wanted here". An empty section WITH a gap is hz saying "I have no opinion,
// and here is what would tell me". They are different states and a client must
// be able to tell them apart — which is the whole reason projection.Gap is
// carried out to a screen rather than logged.
type MachineProjectionResp struct {
	// Machine is who this was computed for, echoed from the request.
	Machine string `json:"machine"`

	// Serial is the monotonic rollback floor. NOTHING PRODUCES ONE and it is
	// always 0 — see projection.MachineConfig.Serial, which says why the field
	// is carried rather than deleted. A screen must not render it as a
	// generation: the generation is the agent channel's content hash.
	Serial uint64 `json:"serial"`

	// Segments are the segment NAMES this machine is a member of. Resolved is
	// false on every one of them today; see ProjectionSegment.
	Segments []ProjectionSegment `json:"segments"`

	// Forwards are the declared exceptions to "a machine may not forward
	// between its own segment interfaces". EMPTY IS AN OPINION, not a gap:
	// the rule's default is deny, and empty is the rule's own answer.
	Forwards []ProjectionForward `json:"forwards"`

	// Hosts is what /etc/hosts should carry. Empty AND gapped today.
	Hosts []ProjectionHostEntry `json:"hosts"`

	// Packages is what should be installed, at an exact version, held.
	Packages []ProjectionPackage `json:"packages"`

	// Feeds are the apt sources those packages come from, resolved up the
	// project tree. From names the project that supplied each one.
	Feeds []ProjectionFeed `json:"feeds"`

	// Units is which instance units should exist and be enabled.
	Units []ProjectionUnit `json:"units"`

	// Unresolved is what hz could NOT compute, and why. THE FIELD THAT MAKES
	// THIS A PROJECTION RATHER THAN A GUESS.
	//
	// Note it keeps projection.MachineConfig's `omitempty`: the two shapes are
	// checked against each other tag for tag, so this one cannot quietly
	// diverge. A client must therefore read a missing key as "no gaps", which
	// is the same thing an empty list means — for this field, and only this
	// field, absent and empty ARE the same state, because a gap list is a list
	// of hz's own admissions and hz never fails to know whether it made one.
	Unresolved []ProjectionGap `json:"unresolved,omitempty"`
}

// ProjectionSegment is one segment membership.
//
// Resolved: false means hz can NAME this segment and cannot say what the
// membership means — there is no Segment record (plan/architecture.md phase 4
// item 15), so Interface, Address and Peers are empty because they are
// UNKNOWN, not because they are absent. A screen that renders a blank
// interface field here has reintroduced the founding bug.
type ProjectionSegment struct {
	Name string `json:"name"`

	Interface string   `json:"interface,omitempty"`
	Address   string   `json:"address,omitempty"`
	Peers     []string `json:"peers,omitempty"`

	Resolved bool `json:"resolved"`
}

// ProjectionForward is one declared crossing between two of a machine's own
// segments, with the reason it is allowed. Nothing can declare one yet.
type ProjectionForward struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// ProjectionHostEntry is one /etc/hosts line hz wants on the machine.
type ProjectionHostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ProjectionPackage is one package at one exact version. Hold means the
// version is pinned: hz installs a version, never "whatever the feed holds".
type ProjectionPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Hold    bool   `json:"hold"`
}

// ProjectionFeed is one apt source, with the project that supplied it. From
// equal to the machine's own project means declared there; anything else is
// inherited up the tree.
type ProjectionFeed struct {
	From string `json:"from"`

	URL       string `json:"url"`
	Suite     string `json:"suite"`
	Component string `json:"component"`
	KeyID     string `json:"key_id,omitempty"`
}

// ProjectionUnit is one systemd unit that should exist and be enabled.
type ProjectionUnit struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// ConfigGeneration identifies the sealed config this unit is meant to be
	// running, without disclosing it — a digest over ciphertext hz holds and
	// cannot read (projection.Unit.ConfigGeneration).
	//
	// EMPTY IS TWO DIFFERENT ANSWERS and a screen must not merge them. Empty
	// with no gap in SectionConfig means hz holds no config for this address,
	// which IS the answer. Empty WITH one means hz does not know. Rendering
	// both as a blank field is the bug this whole model exists to prevent.
	ConfigGeneration string `json:"configGeneration,omitempty"`
}

// ProjectionGap is one thing hz could not compute, named and explained.
//
// Section is the MachineConfig field it belongs beside, so a screen can render
// a gap IN the section it is about rather than in a footnote nobody reads.
// Reason is which kind of not-knowing this is, as a key. Why is prose naming
// what would close it — "hz does not know" without "and here is what would
// tell it" is a dead end.
type ProjectionGap struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
	Why     string `json:"why"`
}

// MachineProjectionPath is where the projection is served, as a constant so
// the route, the test and any client name the same string.
const MachineProjectionPath = "/api/v1/machines/projection"
