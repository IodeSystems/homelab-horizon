package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The Machine record: identity and segment membership, and nothing else.
//
// WHAT IS DELIBERATELY ABSENT, because each absence is the model's shape rather
// than a field nobody got round to:
//
//   - NO PROJECT AND NO ENVIRONMENT. plan/architecture.md, "Instance, not
//     machine, carries the environment": *an environment never modifies a
//     machine; it is a coordinate of an instance*. The gateway in
//     plan/example-projection.md §3 hosts instances from two projects at once,
//     so a Project field on a machine would be false for that row the day it
//     was added, and every screen built on it would inherit the lie. The
//     instance carries (project, environment, app, role); this record carries
//     who the box is. TestAMachineCarriesNoProjectAndNoEnvironment pins it.
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
	}
	return nil
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
	m.Note = strings.TrimSpace(m.Note)
	m.Segments = normalizeSegments(m.Segments)

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

	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
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
