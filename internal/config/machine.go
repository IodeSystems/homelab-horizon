package config

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// The Machine record: identity, an optional owner, and segment membership.
//
// WHAT IS DELIBERATELY ABSENT, because each absence is the model's shape rather
// than a field nobody got round to:
//
//   - NO ENVIRONMENT. plan/design/architecture.md, "Instance, not machine,
//     carries the environment": *an environment never modifies a machine; it
//     is a coordinate of an instance*. The instance carries (project,
//     environment, app, role); this record carries who the box is.
//     TestAMachineCarriesNoEnvironment pins it, and pins the exact field set.
//
//   - Project IS PRESENT, and it is NOT placement (plan/design/ui.md, Decision
//     1 amendment 6, which amended CLAUDE.md invariant 6). It names the project
//     RESPONSIBLE for the box — who answers for it — and "" is global. It says
//     nothing about what the box hosts: the gateway in
//     plan/design/example-projection.md §3 hosts instances from two projects
//     whatever its owner is, and an instance of another project on an owned
//     machine reads as a crossing. No renderer and no projection reads it
//     (TestAttributionChangesNoRenderedArtifact, internal/server).
//
//   - NO OBSERVED VERSION. It settled onto the registration in migration 0011
//     because it belongs to an INSTANCE, and several instances share a box: a
//     machine-level column would report a half-finished rollout as finished.
//     §3's ci-1 is the other half of the same argument — it hosts nothing, so
//     it will never report a version, and that is correct rather than silence.
//     TestAMachineCarriesNoObservedVersion pins it.
//
//   - NO PLACEMENT (port, state dir, unit name, slot). Machine-local, never in
//     hz. Same section of architecture.md.
//
// WHAT THE SEGMENT NAMES RESOLVE AGAINST: the Segment record, once one is
// declared (internal/config/segment.go, phase 4 item 15). A config that
// declares no segments is a config where the model is not in use, and every
// membership in it is still a LABEL — ValidateMachines checks only the SHAPE
// there (non-empty, not repeated), because checking existence against a record
// nobody has written would refuse every machine on every config written before
// segments existed. From the first segment on, a name that resolves to nothing
// is a typo and is refused.
type Machine struct {
	// Name is the identity: what the box calls itself and what its agent
	// credential is keyed by. Unique across the config.
	Name string `json:"name"`

	// Project is the owning project, or "" for global. Responsibility, not
	// placement — see the header. A non-empty name must be a declared project
	// (ValidateProjects).
	Project string `json:"project,omitempty"`

	// Segments are the network segments this machine is a member of, by name.
	// Usually one. More than one is legal and is the case the model exists to
	// make visible (architecture.md, "Machines in more than one segment"): a
	// LAN CI runner that publishes packages and deploys two projects crosses
	// segments as its job.
	//
	// Blast radius is the UNION of these, which is only a useful sentence if
	// they can be enumerated — hence MultiHomed and MultiSegmentMachines
	// below, and `hz machine ls --multi-homed`.
	Segments []string `json:"segments,omitempty"`

	// Note is why a multi-homed box is multi-homed, and it is REQUIRED on one:
	// architecture.md's rule is that forwarding between a machine's own
	// segment interfaces is denied by default and a crossing is *a declared
	// exception with a reason*, so that "bad design but possible" becomes
	// "possible, visible, and it has to be explained". A reason nobody is
	// obliged to give is a reason nobody gives.
	//
	// Optional on a single-segment machine, where there is nothing to explain.
	Note string `json:"note,omitempty"`

	// HZ marks that this machine runs its OWN hz: a separate config layer
	// with its own records and keys (a nested hz), NOT an HA peer of this one
	// — a peer shares these records and is config.Peers. nil is "not known to
	// run an hz", which is every machine that is not declared as one.
	//
	// It is what Environment.Upstream names (plan/plan.md Tier 1b, decided
	// 2026-09-29: a nested hz is a Machine that runs hz, no new record type).
	// It confers nothing: hz does not dial the URL (CLAUDE.md invariant 1) and
	// no renderer reads it (TestNestedMarkersChangeNoRenderedArtifact).
	HZ *MachineHZ `json:"hz,omitempty"`
}

// MachineHZ is the nested-hz marker on a Machine.
type MachineHZ struct {
	// URL is where that hz answers, http or https with a host. It is a
	// statement for an operator and the address the Instances list shows;
	// this hz never contacts it.
	URL string `json:"url"`

	// VPNClient is the VPN client (a wg0 peer, by name) that hz reaches THIS
	// hz — its parent — through: profile `upstream`, so it reaches only this
	// hz's API (config/vpn_upstream.go). "" is no link. When set it must name a
	// client that exists and is `upstream` (validateUpstreamLinks), and the
	// client is a dependant of this machine: removing the machine is blocked
	// by it, and cascade removes it.
	//
	// The CHILD dials; this hz never does (CLAUDE.md invariant 1). The link is
	// bookkeeping for the operator and for removal — no renderer reads it
	// (TestNestedMarkersChangeNoRenderedArtifact); the client's PROFILE is what
	// changes rules, and it is present with or without the link.
	VPNClient string `json:"vpn_client,omitempty"`

	// TokenSHA256 is the sha256 (hex) of the INSTANCE TOKEN this hz minted for
	// that nested hz (POST /api/v1/machines/hz-token, plan/plan.md N4a). The
	// token is shown once and never stored; "" is "none minted". It
	// authenticates the child as `instance:<machine>` for exactly three calls
	// — desired, artifact download and the forwarded report — and only for
	// rungs whose Upstream names this machine. A digest, not a key: it rides
	// peer-sync like the rest of the record and confers nothing on a reader.
	// No client edit sets it — an edit of the marker keeps it (SetMachine).
	TokenSHA256 string `json:"token_sha256,omitempty"`
}

// CheckHZURL refuses a nested-hz URL that is not http(s) with a host. An
// empty URL is refused too: a marker with no address is "runs an hz,
// somewhere", which is the unknown this field exists to replace.
func CheckHZURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("a nested hz needs a URL (http:// or https:// with a host) — a marker with no address says nothing about where that hz is")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("nested hz URL %q does not parse: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("nested hz URL %q must be http:// or https://, not %q", raw, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("nested hz URL %q has no host", raw)
	}
	return nil
}

// normalizeHZ trims the URL. nil stays nil.
func normalizeHZ(h *MachineHZ) *MachineHZ {
	if h == nil {
		return nil
	}
	return &MachineHZ{URL: strings.TrimSpace(h.URL), VPNClient: strings.TrimSpace(h.VPNClient), TokenSHA256: strings.TrimSpace(h.TokenSHA256)}
}

// MultiHomed reports a machine in more than one segment — the row that has to
// be explained, and the row a listing flags.
func (m Machine) MultiHomed() bool { return len(m.Segments) > 1 }

// ValidateMachines checks the machine records are usable: names present and
// unique, segment names present, not repeated within a machine and — once the
// config declares any segment at all — naming one that EXISTS, and every
// multi-homed machine carrying its reason.
//
// The existence check is conditional, which is the same shape ValidateFleet
// uses for single-instance mode: with no Segment records declared a membership
// is a label and nothing can resolve it, so requiring resolution would refuse
// every machine on every config written before item 15. Declaring the first
// segment is the opt-in, and from there a name nothing answers to is a typo
// that would otherwise reach a box as an interface nobody configures.
func (c *Config) ValidateMachines() error {
	// Built once rather than per machine: a machine names few segments and a
	// fleet has many machines.
	segments := make(map[string]struct{}, len(c.Segments))
	for _, s := range c.Segments {
		segments[s.Name] = struct{}{}
	}

	byName := make(map[string]struct{}, len(c.Machines))
	for _, m := range c.Machines {
		if strings.TrimSpace(m.Name) == "" {
			return errors.New("a machine has no name")
		}
		if _, dup := byName[m.Name]; dup {
			return fmt.Errorf("machine %q is declared twice", m.Name)
		}
		byName[m.Name] = struct{}{}

		seen := make(map[string]struct{}, len(m.Segments))
		for _, seg := range m.Segments {
			if strings.TrimSpace(seg) == "" {
				return fmt.Errorf("machine %q has a segment with no name", m.Name)
			}
			if _, dup := seen[seg]; dup {
				return fmt.Errorf("machine %q names segment %q twice", m.Name, seg)
			}
			seen[seg] = struct{}{}
			if len(segments) > 0 {
				if _, ok := segments[seg]; !ok {
					return fmt.Errorf("machine %q is a member of segment %q, which does not exist%s",
						m.Name, seg, c.segmentHint())
				}
			}
		}

		if m.MultiHomed() && strings.TrimSpace(m.Note) == "" {
			return fmt.Errorf("machine %q is in %d segments (%s) and has no note — a machine that bridges segments is a declared exception and has to say why",
				m.Name, len(m.Segments), strings.Join(m.Segments, ", "))
		}
		if m.HZ != nil {
			if err := CheckHZURL(m.HZ.URL); err != nil {
				return fmt.Errorf("machine %q: %w", m.Name, err)
			}
		}
	}
	return c.validateUpstreamLinks()
}

// FindMachine returns one declared machine.
func (c *Config) FindMachine(name string) (Machine, bool) {
	for _, m := range c.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return Machine{}, false
}

// MultiSegmentMachines is every machine in more than one segment, sorted by
// name. The enumeration architecture.md asks for: "hz can list every
// multi-segment machine and what it bridges".
func (c *Config) MultiSegmentMachines() []Machine {
	var out []Machine
	for _, m := range c.Machines {
		if m.MultiHomed() {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddMachine declares a machine.
//
// It writes immediately and it renders nothing, for the reason AddProject
// gives: a declared machine with no instance on it changes no HAProxy backend,
// no DNS record and no apt source. What it DOES confer is the right to enrol —
// hz mints an agent credential only for a machine it has been told about
// (internal/server/handlers_api_machines.go), which is why declaring one is a
// deliberate admin act rather than something a box can do for itself.
func (c *Config) AddMachine(m Machine) error {
	m.Name = strings.TrimSpace(m.Name)
	m.Project = strings.TrimSpace(m.Project)
	m.Note = strings.TrimSpace(m.Note)
	m.Segments = normalizeSegments(m.Segments)
	m.HZ = normalizeHZ(m.HZ)

	if m.Name == "" {
		return fmt.Errorf("a machine needs a name")
	}
	if _, exists := c.FindMachine(m.Name); exists {
		return fmt.Errorf("machine %q already exists — `hz machine show %s` is what it holds", m.Name, m.Name)
	}
	if m.MultiHomed() && m.Note == "" {
		return fmt.Errorf("%s would be in %d segments (%s), so it needs --note saying why — forwarding between a machine's own segments is denied by default and a machine that bridges them is a declared exception, not a default",
			m.Name, len(m.Segments), strings.Join(m.Segments, ", "))
	}

	next := c.copyForWrite()
	next.Machines = append(next.Machines, m)
	if err := next.validateModel(); err != nil {
		return err
	}
	c.adopt(next)
	return nil
}

// MachinePatch is a partial edit of a machine. nil means leave the field
// alone; a non-nil "" clears it (for Project: makes the machine global).
//
// Segments is the WHOLE new membership list when non-nil — a non-nil empty
// list takes the machine out of every segment. Membership is declared on the
// Machine (segment.go's opening comment), so this is its one writer besides
// `add`; SegmentPatch deliberately cannot grant or drop one.
//
// HZ is the nested-hz marker: nil leaves it alone, a non-nil value with a URL
// sets it, and ClearHZ removes it. A marker sent with no VPNClient KEEPS the
// one the machine has — an edit of the URL must not silently unlink the
// nested hz's VPN client; unlinking is ClearHZ. Two fields rather than a pointer-to-pointer
// because "clear" has to be an explicit act — removing the marker from a
// machine an environment names as its Upstream is refused, naming the rung.
type MachinePatch struct {
	Project  *string
	Note     *string
	Segments *[]string
	HZ       *MachineHZ
	ClearHZ  bool
}

// Empty reports a patch that would change nothing.
func (p MachinePatch) Empty() bool {
	return p.Project == nil && p.Note == nil && p.Segments == nil && p.HZ == nil && !p.ClearHZ
}

// SetMachine edits a declared machine's owner, note and segment membership in
// place, so none of them costs remove and re-add — which would cost the box
// its agent credential. It validates the whole model, so clearing the note of a
// multi-homed machine is refused here, naming the machine.
//
// LEAVING A SEGMENT THE MACHINE IS ADDRESSED ON IS REFUSED, not cascaded. A
// member entry must be claimed by its machine (validateSegmentMembers), and
// dropping the claim would either leave an entry Save refuses or silently
// delete an address and a public key the operator did not name. The refusal
// names the command that unaddresses it first — the same refuse-then-name
// discipline SegmentRemoval uses.
func (c *Config) SetMachine(name string, patch MachinePatch) (Machine, error) {
	if patch.Empty() {
		return Machine{}, fmt.Errorf("nothing to change on machine %q — give a project, a note, segments or an hz marker", name)
	}
	idx := -1
	for i, m := range c.Machines {
		if m.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Machine{}, fmt.Errorf("no machine %q — `hz machine ls` lists what exists%s", name, c.machineHint())
	}

	next := c.copyForWrite()
	m := next.Machines[idx]
	if patch.Project != nil {
		m.Project = strings.TrimSpace(*patch.Project)
		if err := c.CheckProjectRef(m.Project); err != nil {
			return Machine{}, err
		}
	}
	if patch.Note != nil {
		m.Note = strings.TrimSpace(*patch.Note)
	}
	if patch.Segments != nil {
		segments := normalizeSegments(*patch.Segments)
		keep := make(map[string]bool, len(segments))
		for _, s := range segments {
			keep[s] = true
		}
		for _, old := range m.Segments {
			if keep[old] {
				continue
			}
			seg, ok := c.FindSegment(old)
			if !ok {
				continue
			}
			if mem, addressed := seg.Member(m.Name); addressed {
				return Machine{}, fmt.Errorf("%s is addressed on segment %s (%s), so it cannot leave it here — a member entry must be claimed by its machine, and dropping the claim would delete an address and a key nobody named. Unaddress it first: `hz segment set %s --unaddress %s`",
					m.Name, old, mem.Address, old, m.Name)
			}
		}
		if len(segments) > 1 && m.Note == "" {
			return Machine{}, fmt.Errorf("%s would be in %d segments (%s), so it needs a note saying why — forwarding between a machine's own segments is denied by default and a machine that bridges them is a declared exception, not a default",
				m.Name, len(segments), strings.Join(segments, ", "))
		}
		m.Segments = segments
	}
	switch {
	case patch.HZ != nil && patch.ClearHZ:
		return Machine{}, fmt.Errorf("machine %q: an hz marker was both set and cleared — send one", name)
	case patch.HZ != nil:
		hz := normalizeHZ(patch.HZ)
		if hz.VPNClient == "" && m.HZ != nil {
			hz.VPNClient = m.HZ.VPNClient
		}
		// The instance token is minted, never edited: an edit of the URL
		// keeps it, or the child would lose its credential to a typo fix.
		if m.HZ != nil {
			hz.TokenSHA256 = m.HZ.TokenSHA256
		}
		m.HZ = hz
	case patch.ClearHZ:
		if users := c.upstreamUsers(name); len(users) > 0 {
			return Machine{}, fmt.Errorf("%s is the upstream hz of %s, so its hz marker cannot be cleared — an Upstream must name a machine that runs hz. Clear the Upstream first (`env set <project>/<name>` with upstream \"\")",
				name, strings.Join(users, ", "))
		}
		m.HZ = nil
	}
	next.Machines[idx] = m
	if err := next.validateModel(); err != nil {
		return Machine{}, err
	}
	c.adopt(next)
	return m, nil
}

// MachineRemoval reports what removing a machine would take (`removes`, which
// always begins with the machine itself) and what stands in the way
// (`blocked`).
//
// `enrolled` is whether hz holds an agent credential for this machine. It is an
// INPUT rather than something this function reads, for the reason
// haproxy.CertStore is an input to the renderer: the credential store lives
// BESIDE the config on purpose (it must not ride peer-sync or the backup zip,
// see internal/agent/credential.go), so config cannot read it without becoming
// a second reader of a file it does not own. The caller that owns the store
// passes what it knows.
//
// An enrolment is a real dependant and not a formality: leaving it behind would
// leave a credential naming a machine hz no longer declares, which authenticates
// and then cannot be explained by anything in the config.
func (c *Config) MachineRemoval(name string, enrolled, cascade bool) (removes, blocked []Dependant, err error) {
	if _, ok := c.FindMachine(name); !ok {
		return nil, nil, fmt.Errorf("no machine %q — `hz machine ls` lists what exists%s", name, c.machineHint())
	}
	removes = []Dependant{{Kind: "machine", Name: name, How: "the machine being removed"}}

	if enrolled {
		d := Dependant{
			Kind: "credential", Name: name,
			How: "hz holds an agent credential for " + name + ", which would still authenticate for a machine hz no longer declares",
		}
		if cascade {
			d.How = "hz holds an agent credential for " + name + ", and it would be REVOKED — that box's agent stops being able to poll"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}

	if m, _ := c.FindMachine(name); m.HZ != nil && m.HZ.VPNClient != "" {
		d := Dependant{
			Kind: "vpn-client", Name: m.HZ.VPNClient,
			How: "is " + name + "'s VPN client (profile upstream), which would still reach this hz's API for a machine hz no longer declares",
		}
		if cascade {
			d.How = "is " + name + "'s VPN client (profile upstream), and it would be REMOVED from wg0.conf — that hz loses its tunnel to this one"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}

	for _, rung := range c.upstreamUsers(name) {
		d := Dependant{
			Kind: "environment", Name: rung,
			How: "names " + name + " as its upstream hz, so it would name a machine nobody declares",
		}
		if cascade {
			d.How = "names " + name + " as its upstream hz; its Upstream would be CLEARED and the rung read as placed here (the rung itself is kept)"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}

	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
}

// upstreamUsers is every rung whose Upstream names this machine, as
// "<project>/<name>", sorted.
func (c *Config) upstreamUsers(machine string) []string {
	var out []string
	for _, e := range c.Environments {
		if e.Upstream == machine {
			out = append(out, e.Project+"/"+e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// RemoveMachine removes a machine record. It refuses while anything depends on
// it and cascade is off, naming what.
//
// It removes the CONFIG record only. Revoking the agent credential that cascade
// promises is the caller's, because the store is the caller's — the handler does
// both halves in one request and reports one list.
func (c *Config) RemoveMachine(name string, enrolled, cascade bool) ([]Dependant, error) {
	removes, blocked, err := c.MachineRemoval(name, enrolled, cascade)
	if err != nil {
		return nil, err
	}
	if len(blocked) > 0 {
		return nil, blockedError("machine", name, blocked)
	}

	next := c.copyForWrite()
	machines := make([]Machine, 0, len(next.Machines))
	for _, m := range next.Machines {
		if m.Name != name {
			machines = append(machines, m)
		}
	}
	next.Machines = machines
	// Cascade CLEARS an Upstream naming this machine and never deletes the
	// rung: the rung is still declared here, and what it loses is only the
	// statement that another hz holds its placements.
	for i := range next.Environments {
		if next.Environments[i].Upstream == name {
			next.Environments[i].Upstream = ""
		}
	}
	if err := next.validateModel(); err != nil {
		return nil, err
	}
	c.adopt(next)
	return removes, nil
}

// normalizeSegments trims each name and drops the empties, so a `--segment ""`
// or a trailing comma in a shell loop does not become a segment called "".
// Order is preserved: the operator's order is the order the record shows, and
// re-sorting would make a diff of two identical memberships look like a change.
func normalizeSegments(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// machineHint lists the declared machines, for the reason projectHint lists the
// projects: "no machine X" with no list reads as a typo when the real answer is
// that this config declares no machines at all.
func (c *Config) machineHint() string {
	if len(c.Machines) == 0 {
		return ". This config declares no machines at all; `hz machine add <name>` makes the first one"
	}
	names := make([]string, 0, len(c.Machines))
	for _, m := range c.Machines {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return ". Declared: " + strings.Join(names, ", ")
}
