package config

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// The Segment record: what a Machine.Segments NAME resolves to.
//
// Item 13 shipped the membership and said so in its own comment: "segment names
// resolve against nothing yet — that is item 15". This is item 15's half.
// plan/upstream-and-promotion.md §7 promotes it from optional to required and
// names the consumer: redline-prod-hz joins an iodesystems segment as a CLIENT,
// which IS a segment membership, and a list of labels cannot say it.
//
// WHAT THE SHAPE IS FOR. plan/projection's Segment asks three questions of a
// membership — Interface, Address, Peers — and plan/architecture.md item 15
// lists what answering them takes, per (machine, segment). Every field here
// exists to answer one of them and nothing here answers anything else:
//
//	Interface  → Segment.Interface. The interface name is a property of the
//	             SEGMENT, not of the box: two segments must not land on one
//	             interface, which is the collision item 15 exists to prevent,
//	             so the name is declared once and checked unique.
//	Address    → SegmentMember.Address. Per (machine, segment), so it cannot
//	             live on the Segment and it cannot live on the Machine either:
//	             a machine in two segments has two addresses.
//	Peers      → PeersOf, derived from Members and the one Hub. Hub and spoke
//	             (architecture.md, "Segments"): a spoke peers with the hub, the
//	             hub peers with every spoke. Storing the peer set would let it
//	             disagree with the membership it is a view of.
//
// WHY MEMBERSHIP IS STILL DECLARED ON THE MACHINE. Machine.Segments stays the
// list of who is in — this record does not take it over, it ADDRESSES it. A
// SegmentMember for a machine that does not name the segment is refused, so the
// two cannot disagree; a machine that names a segment with no member entry is
// the honest intermediate state (in the segment, not addressed on it yet), and
// it is exactly what an operator has after `hz machine add --segment`.
//
// WHAT IS DELIBERATELY ABSENT:
//
//   - NO PRIVATE KEY, ever. PublicKey is per (machine, segment) because
//     WireGuard forces a key per interface and architecture.md wants that
//     property kept — a compromise of wg-code must not hand over wg-redline.
//     hz holds the public half and the box holds the secret one, which is the
//     same split internal/wireguard already lives with.
//
//   - NO AllowedIPs. It is derivable from CIDR + Members + Hub (a spoke routes
//     the whole CIDR to the hub; the hub routes each spoke its own /32) and a
//     stored copy is a second answer free to disagree — the founding bug in
//     plan/architecture.md's goal property 6. The projection derives it when it
//     is wired to (item 15's follow-up); nothing derives it here because
//     nothing here would call it.
//
//   - NO POSTURE, NO VERSION, NO SERVICES. A segment is a network, not a rung.
//     It names a project because a project's machines form a segment; it does
//     not name an environment, for the reason a Machine does not.
type Segment struct {
	// Name is what Machine.Segments names, and it is the identity: unique
	// across the config.
	Name string `json:"name"`

	// Project is the project whose machines this segment is for.
	// architecture.md: "A project's machines form a network segment. `code` has
	// machines, `redline` has machines, and those are different segments."
	// Required — a segment owned by nobody is a network nobody is responsible
	// for, and the ownership is what makes a cross-project membership (a
	// redline box on an iodesystems segment) READ as a crossing.
	Project string `json:"project"`

	// CIDR is the segment's range, in canonical network form (10.42.0.0/24,
	// not 10.42.0.1/24). Today's single Config.VPNRange, gone plural.
	//
	// Checked not to overlap any other segment's: two segments sharing a range
	// means a machine in both has two routes for one prefix and one of them
	// silently wins — the same failure internal/config/cidr_advice.go exists to
	// warn about, here between two networks hz itself hands out.
	CIDR string `json:"cidr"`

	// Interface is the WireGuard interface this segment appears on, on every
	// machine in it. Today's single Config.WGInterface, gone plural.
	//
	// Unique across segments, which is the whole point of pluralising it: a
	// machine in two segments needs two interfaces rather than one that
	// collides (architecture.md item 15). Uniqueness is enforced globally
	// rather than per machine because the cheap global rule cannot be defeated
	// by a later membership, and a per-machine rule can — a box joining a
	// second segment would meet the collision at join time, at the box, which
	// is the one place this model exists to stop sending people.
	Interface string `json:"interface"`

	// Note is free prose about the segment: what it is for, who else is on it.
	// Optional. It is NOT the declared reason a machine bridges two segments —
	// that lives on the Machine, because bridging is a property of the box.
	Note string `json:"note,omitempty"`

	// Members addresses the machines that are in this segment. A member entry
	// is the (machine, segment) pair the projection asks its three questions
	// about.
	//
	// It is a subset of the machines naming this segment, never a superset: a
	// member that does not declare the segment is refused, an unaddressed
	// membership is legal and means what it says.
	Members []SegmentMember `json:"members,omitempty"`
}

// SegmentMember is one machine's presence on one segment — the (machine,
// segment) pair, which is the coordinate architecture.md item 15 names and the
// reason none of these fields can live on the Machine or on the Segment alone.
type SegmentMember struct {
	// Machine is the declared machine. It must name this segment in its own
	// Segments; hz refuses a member the machine does not claim, so the two
	// records cannot say different things about who is in.
	Machine string `json:"machine"`

	// Address is this machine's address ON THIS SEGMENT, a bare IP inside the
	// segment's CIDR (10.42.0.2, not 10.42.0.2/32). Unique within the segment.
	Address string `json:"address"`

	// PublicKey is this machine's WireGuard public key FOR THIS INTERFACE. The
	// secret half never reaches hz. Empty until the box has one — a declared
	// address with no key is a member hz can route to and cannot yet peer with,
	// which is a different state from not being a member.
	PublicKey string `json:"public_key,omitempty"`

	// Hub marks the one member every other member peers with. Hub and spoke,
	// and for a segment hz owns the hub is hz's own box (architecture.md,
	// "Segments"). Exactly one member carries it once a segment has any.
	//
	// It is also what makes "a CLIENT of someone else's segment" expressible:
	// a member with Hub false on a segment whose hub is another project's
	// machine is precisely redline-prod-hz's relationship to iodesystems
	// (plan/upstream-and-promotion.md §5).
	Hub bool `json:"hub,omitempty"`

	// Endpoint is where this member is reachable from outside the segment,
	// host:port. The hub needs one — a spoke with nothing to dial cannot bring
	// the tunnel up — and a spoke may have one too (a second hub-capable box,
	// a site-to-site pair), so it is optional on the record and only the hub's
	// absence is worth remarking on.
	Endpoint string `json:"endpoint,omitempty"`
}

// Hub returns the segment's hub member.
func (s Segment) Hub() (SegmentMember, bool) {
	for _, m := range s.Members {
		if m.Hub {
			return m, true
		}
	}
	return SegmentMember{}, false
}

// Member returns one machine's presence on this segment.
func (s Segment) Member(machine string) (SegmentMember, bool) {
	for _, m := range s.Members {
		if m.Machine == machine {
			return m, true
		}
	}
	return SegmentMember{}, false
}

// PeersOf is who a machine peers with on this segment, derived rather than
// stored: hub and spoke, so a spoke peers with the hub and the hub peers with
// every spoke. Sorted by machine name so two identical segments answer
// identically.
//
// A machine that is not a member peers with nothing — and so does every member
// of a segment with no hub, which is why a segment with members and no hub is
// refused rather than left to answer "nobody" everywhere.
func (s Segment) PeersOf(machine string) []SegmentMember {
	self, ok := s.Member(machine)
	if !ok {
		return nil
	}
	var out []SegmentMember
	if self.Hub {
		for _, m := range s.Members {
			if !m.Hub {
				out = append(out, m)
			}
		}
	} else if hub, has := s.Hub(); has {
		out = append(out, hub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Machine < out[j].Machine })
	return out
}

// ValidateSegments checks the segment records are usable: names unique,
// projects real, ranges parseable, canonical and non-overlapping, interfaces
// unique, and every member a declared machine that claims the segment, at an
// address inside the range, with exactly one hub.
//
// It is the sixth validator Save runs, and it is separate from ValidateMachines
// for the reason ValidateFeeds is separate from ValidateProjects: they answer
// different questions. ValidateMachines asks whether a machine's memberships
// RESOLVE; this asks whether the things they resolve to hold together.
func (c *Config) ValidateSegments() error {
	byName := make(map[string]struct{}, len(c.Segments))
	byInterface := make(map[string]string, len(c.Segments))
	projects := make(map[string]struct{}, len(c.Projects))
	for _, p := range c.Projects {
		projects[p.Name] = struct{}{}
	}
	machines := make(map[string]Machine, len(c.Machines))
	for _, m := range c.Machines {
		machines[m.Name] = m
	}

	nets := make([]struct {
		name string
		cidr *net.IPNet
	}, 0, len(c.Segments))

	for _, s := range c.Segments {
		if strings.TrimSpace(s.Name) == "" {
			return errors.New("a segment has no name")
		}
		if _, dup := byName[s.Name]; dup {
			return fmt.Errorf("segment %q is declared twice", s.Name)
		}
		byName[s.Name] = struct{}{}

		if strings.TrimSpace(s.Project) == "" {
			return fmt.Errorf("segment %q has no project — a project's machines form a segment, so a segment nobody owns is a network nobody is responsible for", s.Name)
		}
		if _, ok := projects[s.Project]; !ok {
			return fmt.Errorf("segment %q names project %q, which does not exist", s.Name, s.Project)
		}

		if strings.TrimSpace(s.Interface) == "" {
			return fmt.Errorf("segment %q has no interface — a membership has to land on a named interface on the box", s.Name)
		}
		if other, dup := byInterface[s.Interface]; dup {
			return fmt.Errorf("segments %q and %q both use interface %q — a machine in both would have one interface for two networks, which is the collision per-segment interfaces exist to prevent",
				other, s.Name, s.Interface)
		}
		byInterface[s.Interface] = s.Name

		ipnet, err := parseSegmentCIDR(s)
		if err != nil {
			return err
		}
		nets = append(nets, struct {
			name string
			cidr *net.IPNet
		}{s.Name, ipnet})

		if err := validateSegmentMembers(s, ipnet, machines); err != nil {
			return err
		}
	}

	// Two segments sharing a range means a machine in both has two routes for
	// one prefix; one wins and the other network is silently unreachable.
	for i := 0; i < len(nets); i++ {
		for j := i + 1; j < len(nets); j++ {
			if nets[i].cidr.Contains(nets[j].cidr.IP) || nets[j].cidr.Contains(nets[i].cidr.IP) {
				return fmt.Errorf("segments %q (%s) and %q (%s) overlap — a machine in both would have two routes for one prefix and one of them silently wins",
					nets[i].name, nets[i].cidr.String(), nets[j].name, nets[j].cidr.String())
			}
		}
	}
	return nil
}

// parseSegmentCIDR checks one segment's range and returns it. The canonical
// form is required rather than normalised away: 10.42.0.1/24 declared as the
// segment's range is almost always somebody typing a HOST address where a
// network belongs, and silently masking it would hide the confusion that the
// member addresses are then checked against.
func parseSegmentCIDR(s Segment) (*net.IPNet, error) {
	if strings.TrimSpace(s.CIDR) == "" {
		return nil, fmt.Errorf("segment %q has no CIDR — the range is what a member address is checked against and what a spoke routes to the hub", s.Name)
	}
	ip, ipnet, err := net.ParseCIDR(s.CIDR)
	if err != nil {
		return nil, fmt.Errorf("segment %q has CIDR %q, which is not a network: %v", s.Name, s.CIDR, err)
	}
	if !ip.Equal(ipnet.IP) {
		return nil, fmt.Errorf("segment %q declares CIDR %q, which is a host address in %s — declare the network (%s); a host address here reads as the hub's and is not",
			s.Name, s.CIDR, ipnet.String(), ipnet.String())
	}
	return ipnet, nil
}

// validateSegmentMembers checks one segment's member list against the machines
// that exist and the range it is on.
func validateSegmentMembers(s Segment, ipnet *net.IPNet, machines map[string]Machine) error {
	seen := make(map[string]struct{}, len(s.Members))
	addrs := make(map[string]string, len(s.Members))
	hubs := 0

	for _, mem := range s.Members {
		if strings.TrimSpace(mem.Machine) == "" {
			return fmt.Errorf("segment %q has a member with no machine", s.Name)
		}
		if _, dup := seen[mem.Machine]; dup {
			return fmt.Errorf("segment %q lists machine %q twice", s.Name, mem.Machine)
		}
		seen[mem.Machine] = struct{}{}

		m, declared := machines[mem.Machine]
		if !declared {
			return fmt.Errorf("segment %q has a member %q, which is not a declared machine", s.Name, mem.Machine)
		}
		if !machineNamesSegment(m, s.Name) {
			return fmt.Errorf("segment %q addresses machine %q, but %s does not name %s among its segments — membership is declared on the machine and addressed here, and the two must not disagree",
				s.Name, mem.Machine, mem.Machine, s.Name)
		}

		if strings.TrimSpace(mem.Address) == "" {
			return fmt.Errorf("segment %q gives machine %q no address — a member hz cannot address is a membership, and that is already on the machine", s.Name, mem.Machine)
		}
		ip := net.ParseIP(strings.TrimSpace(mem.Address))
		if ip == nil {
			return fmt.Errorf("segment %q gives machine %q address %q, which is not an IP address (a bare address, not a /32)", s.Name, mem.Machine, mem.Address)
		}
		if !ipnet.Contains(ip) {
			return fmt.Errorf("segment %q gives machine %q address %s, which is outside the segment's range %s", s.Name, mem.Machine, ip, ipnet.String())
		}
		if other, dup := addrs[ip.String()]; dup {
			return fmt.Errorf("segment %q gives %s and %s the same address %s", s.Name, other, mem.Machine, ip)
		}
		addrs[ip.String()] = mem.Machine

		if mem.Hub {
			hubs++
		}
	}

	if len(s.Members) > 0 && hubs == 0 {
		return fmt.Errorf("segment %q has %d member(s) and no hub — it is hub and spoke, so one member has to be the hub or every member peers with nothing",
			s.Name, len(s.Members))
	}
	if hubs > 1 {
		return fmt.Errorf("segment %q has %d hubs — hub and spoke has one", s.Name, hubs)
	}
	return nil
}

func machineNamesSegment(m Machine, segment string) bool {
	for _, name := range m.Segments {
		if name == segment {
			return true
		}
	}
	return false
}

// FindSegment returns one declared segment.
func (c *Config) FindSegment(name string) (Segment, bool) {
	for _, s := range c.Segments {
		if s.Name == name {
			return s, true
		}
	}
	return Segment{}, false
}

// SegmentsOf is every segment one machine is a member of, by its own
// declaration, sorted by name. A name the machine declares that no segment
// answers to is absent from the result — ValidateMachines refuses that, so it
// can only be seen on a config nobody has saved yet.
func (c *Config) SegmentsOf(machine string) []Segment {
	m, ok := c.FindMachine(machine)
	if !ok {
		return nil
	}
	var out []Segment
	for _, name := range m.Segments {
		if s, found := c.FindSegment(name); found {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AddSegment declares a segment.
//
// It writes immediately, for the reason AddMachine and AddProject do: a
// declared segment renders nothing. hz has no whole-file WireGuard renderer —
// internal/wireguard says the file on disk is the state of record — so nothing
// on any box moves because this record exists. What it DOES confer is
// resolution: from the first segment on, a machine's membership names something
// real, and ValidateMachines starts saying so.
func (c *Config) AddSegment(s Segment) error {
	s.Name = strings.TrimSpace(s.Name)
	s.Project = strings.TrimSpace(s.Project)
	s.CIDR = strings.TrimSpace(s.CIDR)
	s.Interface = strings.TrimSpace(s.Interface)
	s.Note = strings.TrimSpace(s.Note)
	for i := range s.Members {
		s.Members[i].Machine = strings.TrimSpace(s.Members[i].Machine)
		s.Members[i].Address = strings.TrimSpace(s.Members[i].Address)
		s.Members[i].PublicKey = strings.TrimSpace(s.Members[i].PublicKey)
		s.Members[i].Endpoint = strings.TrimSpace(s.Members[i].Endpoint)
	}

	if s.Name == "" {
		return fmt.Errorf("a segment needs a name")
	}
	if _, exists := c.FindSegment(s.Name); exists {
		return fmt.Errorf("segment %q already exists — `hz segment show %s` is what it holds", s.Name, s.Name)
	}
	if s.Project == "" {
		return fmt.Errorf("segment %s needs --project: a project's machines form the segment, and the owner is what makes another project's machine on it read as a crossing%s",
			s.Name, c.projectHint())
	}
	if !c.hasProject(s.Project) {
		return fmt.Errorf("no project %q to own segment %s — declare the project first%s", s.Project, s.Name, c.projectHint())
	}
	if s.CIDR == "" {
		return fmt.Errorf("segment %s needs --cidr: the range is what a member address is checked against and what a spoke routes to the hub", s.Name)
	}
	if s.Interface == "" {
		return fmt.Errorf("segment %s needs --interface: a membership has to land on a named interface on the box, and two segments must not land on one", s.Name)
	}

	next := c.copyForWrite()
	next.Segments = append(next.Segments, s)
	if err := next.validateModel(); err != nil {
		return err
	}
	c.adopt(next)
	return nil
}

// SegmentRemoval reports what removing a segment would take (`removes`, which
// always begins with the segment itself) and what stands in the way
// (`blocked`).
//
// Every machine that NAMES the segment is a dependant, addressed or not: the
// name on the machine is the membership, and leaving it behind would leave a
// config Save refuses — the operator would meet it as a validation error about
// a machine they never mentioned, which is the failure ProjectRemoval exists to
// avoid.
//
// With cascade the membership is dropped from each machine. That is a change to
// records the operator did not name, so each one is listed before it happens,
// and a machine left in no segment at all is legal (it stays declared and stays
// enrolled; it peers with nothing).
func (c *Config) SegmentRemoval(name string, cascade bool) (removes, blocked []Dependant, err error) {
	if _, ok := c.FindSegment(name); !ok {
		return nil, nil, fmt.Errorf("no segment %q — `hz segment ls` lists what exists%s", name, c.segmentHint())
	}
	removes = []Dependant{{Kind: "segment", Name: name, How: "the segment being removed"}}

	for _, m := range c.Machines {
		if !machineNamesSegment(m, name) {
			continue
		}
		d := Dependant{Kind: "machine", Name: m.Name, How: "is a member of " + name}
		if cascade {
			d.How = "is a member of " + name + ", and the membership would be DROPPED"
			if len(m.Segments) == 1 {
				d.How += " — leaving it in no segment at all (legal; it peers with nothing)"
			}
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}

	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
}

// RemoveSegment removes a segment, and with cascade drops the membership from
// every machine that named it. It refuses while anything depends on it and
// cascade is off, naming what.
func (c *Config) RemoveSegment(name string, cascade bool) ([]Dependant, error) {
	removes, blocked, err := c.SegmentRemoval(name, cascade)
	if err != nil {
		return nil, err
	}
	if len(blocked) > 0 {
		return nil, blockedError("segment", name, blocked)
	}

	next := c.copyForWrite()
	segments := make([]Segment, 0, len(next.Segments))
	for _, s := range next.Segments {
		if s.Name != name {
			segments = append(segments, s)
		}
	}
	next.Segments = segments

	for i, m := range next.Machines {
		if !machineNamesSegment(m, name) {
			continue
		}
		kept := make([]string, 0, len(m.Segments))
		for _, seg := range m.Segments {
			if seg != name {
				kept = append(kept, seg)
			}
		}
		if len(kept) == 0 {
			kept = nil
		}
		next.Machines[i].Segments = kept
	}

	if err := next.validateModel(); err != nil {
		return nil, err
	}
	c.adopt(next)
	return removes, nil
}

// segmentHint lists the declared segments, for the reason projectHint lists the
// projects: "no segment X" with no list reads as a typo when the real answer is
// that this config declares no segments at all, and every membership in it is
// still a label.
func (c *Config) segmentHint() string {
	if len(c.Segments) == 0 {
		return ". This config declares no segments at all; `hz segment add <name> --project <project> --cidr <range> --interface <iface>` makes the first one"
	}
	names := make([]string, 0, len(c.Segments))
	for _, s := range c.Segments {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return ". Declared: " + strings.Join(names, ", ")
}
