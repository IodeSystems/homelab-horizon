package config

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/wgkey"
)

// The Segment record: what a Machine.Segments NAME resolves to.
//
// Item 13 shipped the membership and said so in its own comment: "segment names
// resolve against nothing yet — that is item 15". This is item 15's half.
// plan/design/estate.md §7 promotes it from optional to required and
// names the consumer: redline-prod-hz joins an iodesystems segment as a CLIENT,
// which IS a segment membership, and a list of labels cannot say it.
//
// WHAT THE SHAPE IS FOR. plan/projection's Segment asks three questions of a
// membership — Interface, Address, Peers — and plan/design/architecture.md item 15
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
//     plan/design/architecture.md's goal property 6. internal/projection
//     derives it (allowedIPs) on every projection;
//     TestASegmentMemberHasNowhereToStoreAllowedIPs pins that no field here
//     can hold one.
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
	//
	// FILLED BY THE BOX, at enrolment: `hz-agent enroll` mints a key per segment
	// and reports the public half (internal/agent/segmentkey.go), and the
	// enrolment handler writes it here. An operator can still set it by hand
	// (`hz segment set --member machine=M,key=K`) for a peer that runs no agent.
	//
	// STORED, AND THAT IS CORRECT HERE, unlike AllowedIPs or the peer set.
	// Those are derivations of other fields on this record (CIDR, Address,
	// Hub), so a stored copy is a second answer that can disagree with the
	// first (CLAUDE.md invariant 8). A public key derives from nothing hz
	// holds: it is a FACT the box reports about a private key only the box
	// has. Recording it is the only way hz can know it at all.
	//
	// VALIDATED AS A KEY when it is set — 32 Curve25519 bytes in canonical
	// base64, ValidateSegments below. A string that is not a key renders a
	// `[Peer]` block WireGuard refuses to load, and the box that finds out is
	// the one whose tunnel does not come up.
	PublicKey string `json:"public_key,omitempty"`

	// Hub marks the one member every other member peers with. Hub and spoke,
	// and for a segment hz owns the hub is hz's own box (architecture.md,
	// "Segments"). Exactly one member carries it once a segment has any.
	//
	// It is also what makes "a CLIENT of someone else's segment" expressible:
	// a member with Hub false on a segment whose hub is another project's
	// machine is precisely redline-prod-hz's relationship to iodesystems
	// (plan/design/estate.md §5).
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
	keys := make(map[string]string, len(s.Members))
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

		// A KEY IS CHECKED AS A KEY. Empty is legal and means "not keyed yet" —
		// the state the projection raises its `[Peer]` gap for. Anything else has
		// to be a WireGuard key, because the only thing this field is ever used
		// for is a PublicKey line in a wg config, and a string that is not a key
		// there is an interface that will not load. hz is the last place that can
		// refuse it cheaply; after this it is a tunnel that does not come up on a
		// box nobody is standing at.
		if mem.PublicKey != "" {
			if _, err := wgkey.Parse(mem.PublicKey); err != nil {
				return fmt.Errorf("segment %q gives machine %q a public key that is %v — WireGuard keys are %d bytes in base64 (%d characters), the form `wg pubkey` prints",
					s.Name, mem.Machine, err, wgkey.KeyLen, wgkey.EncodedLen)
			}
			if other, dup := keys[mem.PublicKey]; dup {
				// Two members on one key is a peer set that cannot be rendered:
				// WireGuard identifies a peer BY its key, so the second [Peer]
				// block silently replaces the first and one machine loses its
				// route. Usually a copy-paste of somebody else's key.
				return fmt.Errorf("segment %q gives %s and %s the same public key — WireGuard identifies a peer by its key, so one of those two would silently lose its peering. Each interface mints its own",
					s.Name, other, mem.Machine)
			}
			keys[mem.PublicKey] = mem.Machine
		}

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

// SegmentPatch is a partial update to one segment and to the memberships on it.
// A nil field is "leave this alone"; a non-nil one is the new value.
//
// WHY A PATCH AND NOT A WHOLE RECORD, the reason EnvironmentPatch gives: these
// are independent facts about one network, and making an operator restate the
// range and the interface in order to address one member is how a range gets
// retyped wrong. It matters more here than on a rung — a Segment carries a
// member list, and a whole-record write would make "re-address one member" mean
// "resend every member", with the hub and every public key along for the ride.
//
// WHAT IS NOT IN HERE, and each absence is the model rather than an omission:
//
//   - NO NAME. The name is the IDENTITY: every Machine.Segments entry resolves
//     through it, so renaming a segment is renaming it on every machine that is
//     in it — a different operation from editing this record, and one that would
//     have to rewrite records the operator did not name. `hz segment rm
//     --cascade` and re-declare is the honest way to say it.
//
//   - NO MEMBERSHIP. Members can be ADDRESSED and UNADDRESSED here; a machine
//     cannot be joined to or removed from the segment. Membership is declared on
//     the Machine (segment.go's opening comment), and a second writer for it
//     here is exactly how the two records come to disagree.
//
//   - NO HUB FLAG PER MEMBER. Hub is single-valued across the whole segment, so
//     it is SegmentPatch.Hub — a machine name — rather than a bool on each
//     member. Two members each carrying hub=true is a contradiction the record
//     cannot hold and a per-member flag invites; naming the one hub cannot
//     express it at all.
type SegmentPatch struct {
	// Project is the owner. Settable: ownership moves, and nothing resolves
	// through it. Not clearable — a network nobody owns is a network nobody is
	// responsible for.
	Project *string

	// CIDR is the range. Settable, and the one field that can STRAND members:
	// every member address is checked against it, so a narrower or moved range
	// can put an existing address outside. See SegmentSet — stranded members
	// block the write unless cascade is given, and cascade UNADDRESSES them
	// rather than renumbering, because hz does not invent an address.
	CIDR *string

	// Interface is the interface the segment lands on. Settable and checked
	// unique across segments by the same validator `add` runs.
	Interface *string

	// Note is free prose. Settable, and the only field here that clears: a
	// non-nil empty string removes it.
	Note *string

	// Hub names the member that becomes the hub, demoting whoever holds it now.
	// It is a TOPOLOGY change and not a field edit — Peers is derived hub and
	// spoke, so moving the hub rewires every member's peer set — which is why
	// SegmentChange carries the whole before/after rewiring for the caller to
	// print rather than letting it happen quietly.
	//
	// There is no way to say "no hub": a segment with members and no hub is
	// refused, so the only legal move is naming a different one.
	Hub *string

	// Members addresses or re-addresses machines already in the segment. This
	// is the gap `ls|show|add|rm` left: with only those verbs, changing one
	// member's address meant `rm --cascade` and re-declaring the whole segment.
	Members []SegmentMemberPatch

	// Unaddress drops the member entry for each named machine, leaving it IN
	// the segment and unaddressed — the legal state `hz machine add --segment`
	// leaves, and the honest one for a box that is being renumbered later.
	Unaddress []string
}

// SegmentMemberPatch is a partial update to one membership. Machine identifies
// it; every other field is nil for "leave this alone".
//
// Partial for the reason the segment patch is: re-addressing a member must not
// silently drop its public key and its endpoint, which a whole-member write
// would do to every field the operator did not retype.
type SegmentMemberPatch struct {
	// Machine is which membership. It must already name the segment — this
	// addresses a membership, it does not grant one.
	Machine string

	// Address is the new address, a bare IP inside the segment's range. It
	// cannot be cleared: a member with no address is not a member entry at all,
	// and SegmentPatch.Unaddress is how that is said.
	Address *string

	// PublicKey is the public half, clearable with an empty string — a key that
	// has been rotated away is better absent than stale.
	PublicKey *string

	// Endpoint is host:port, clearable with an empty string.
	Endpoint *string
}

// Empty reports a patch that changes nothing, so a caller can refuse it rather
// than write a no-op and report success.
func (p SegmentPatch) Empty() bool {
	return p.Project == nil && p.CIDR == nil && p.Interface == nil && p.Note == nil &&
		p.Hub == nil && len(p.Members) == 0 && len(p.Unaddress) == 0
}

// SegmentChange is what a set did, or — on a dry run — what it would do.
//
// It is returned whole rather than left for a caller to diff, for the reason
// RemovalResp is: a client that re-derived "what changed" from a read before and
// a read after would be a second answer, free to disagree with the one that
// wrote it, and the hub rewiring in particular is not a diff anybody would think
// to look for.
type SegmentChange struct {
	// Segment is the record as it would read, or now reads.
	Segment Segment

	// Fields is one line per changed value, "name  before → after". Empty when
	// the patch asked only for something that was already true.
	Fields []string

	// Strands is every member entry a new CIDR would put outside the range.
	// Without cascade they are the BLOCKERS; with it they are what the write
	// unaddresses, listed before it happens.
	Strands []Dependant

	// HubMove is set only when the hub actually moves, and carries the peer
	// rewiring that follows from it.
	HubMove *SegmentHubMove
}

// SegmentHubMove is the topology change moving the hub amounts to: who it was,
// who it is, and what every member's peer set becomes as a result.
type SegmentHubMove struct {
	From  string
	To    string
	Peers []SegmentPeerChange
}

// SegmentPeerChange is one member's peer set before and after. Only members
// whose peers actually change are listed.
type SegmentPeerChange struct {
	Machine string
	Before  []string
	After   []string
}

// SegmentSet computes what a patch would do to one segment, WITHOUT writing:
// the segment as it would read, the values that would change, the hub rewiring,
// and what stands in the way.
//
// It is the SegmentRemoval half of the pair — the dry run and the write share
// one computation, so the thing `--confirm` writes is the thing the dry run
// printed. Only the CIDR can block: it is the one field whose new value can
// invalidate a record the operator did not name.
func (c *Config) SegmentSet(name string, patch SegmentPatch, cascade bool) (SegmentChange, []Dependant, error) {
	before, ok := c.FindSegment(name)
	if !ok {
		return SegmentChange{}, nil, fmt.Errorf("no segment %q — `hz segment ls` lists what exists%s", name, c.segmentHint())
	}
	if patch.Empty() {
		return SegmentChange{}, nil, fmt.Errorf("nothing to set on segment %s — pass at least one of --project, --cidr, --interface, --note, --hub, --member or --unaddress", name)
	}

	next := before
	next.Members = append([]SegmentMember(nil), before.Members...)
	change := SegmentChange{}

	if patch.Project != nil {
		v := strings.TrimSpace(*patch.Project)
		if v == "" {
			return SegmentChange{}, nil, fmt.Errorf("segment %s cannot have its project cleared — a project's machines form the segment, and a network nobody owns is a network nobody is responsible for", name)
		}
		if !c.hasProject(v) {
			return SegmentChange{}, nil, fmt.Errorf("no project %q to own segment %s — declare the project first%s", v, name, c.projectHint())
		}
		change.note("project", next.Project, v)
		next.Project = v
	}
	if patch.Interface != nil {
		v := strings.TrimSpace(*patch.Interface)
		if v == "" {
			return SegmentChange{}, nil, fmt.Errorf("segment %s cannot have its interface cleared — a membership has to land on a named interface on the box", name)
		}
		change.note("interface", next.Interface, v)
		next.Interface = v
	}
	if patch.Note != nil {
		v := strings.TrimSpace(*patch.Note)
		change.note("note", next.Note, v)
		next.Note = v
	}

	if err := c.applyMemberPatches(name, &next, patch.Members, &change); err != nil {
		return SegmentChange{}, nil, err
	}
	if err := applyUnaddress(name, &next, patch.Unaddress, &change); err != nil {
		return SegmentChange{}, nil, err
	}
	if err := applyHubMove(before, &next, patch.Hub, &change); err != nil {
		return SegmentChange{}, nil, err
	}

	// CIDR LAST, deliberately. An operator renumbering a segment passes the new
	// range and the new addresses in one command; checking the range against the
	// addresses as they will BE is the only order in which that is not a refusal
	// about an address the operator just replaced.
	blocked, err := applyCIDR(name, &next, patch.CIDR, cascade, &change)
	if err != nil {
		return SegmentChange{}, nil, err
	}

	change.Segment = next
	return change, blocked, nil
}

// SetSegment applies a patch and returns what it changed. It refuses while
// anything stands in the way and cascade is off, naming what — the contract
// RemoveSegment sets, and for the same reason: the alternative writes a config
// Save would refuse and hands the operator a validation error about a record
// they never mentioned.
func (c *Config) SetSegment(name string, patch SegmentPatch, cascade bool) (SegmentChange, error) {
	change, blocked, err := c.SegmentSet(name, patch, cascade)
	if err != nil {
		return SegmentChange{}, err
	}
	if len(blocked) > 0 {
		// Not blockedError: that one says "removing it", and nothing is being
		// removed. The refusal has to describe the change the operator actually
		// typed, which is the whole reason for refusing rather than cascading.
		lines := make([]string, 0, len(blocked))
		for _, d := range blocked {
			lines = append(lines, "  "+d.String())
		}
		return SegmentChange{}, fmt.Errorf("segment %s: the new range would strand %d addressed member(s), and a member outside its own segment's range is a config that cannot be saved:\n%s\nGive each of them an address inside the new range in the same command, or pass cascade to UNADDRESS them (hz will not renumber a box for you — two machines on one address is the failure that would cause)",
			name, len(blocked), strings.Join(lines, "\n"))
	}

	next := c.copyForWrite()
	for i := range next.Segments {
		if next.Segments[i].Name == name {
			next.Segments[i] = change.Segment
			break
		}
	}
	// The same validator `add` runs, over the whole model. A second path that
	// validated differently is how a config becomes unloadable: addresses inside
	// the range, unique per segment, members the machine claims, one hub,
	// interfaces unique across segments — all of it is checked here or nowhere.
	if err := next.validateModel(); err != nil {
		return SegmentChange{}, err
	}
	c.adopt(next)
	return change, nil
}

// note records one changed value, and records nothing when the patch asked for
// the value the field already holds — a "set" that changed nothing must not
// report that it did.
func (ch *SegmentChange) note(field, before, after string) {
	if before == after {
		return
	}
	ch.Fields = append(ch.Fields, fmt.Sprintf("%-11s %s → %s", field, dashOrEmpty(before), dashOrEmpty(after)))
}

func dashOrEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// applyMemberPatches addresses or re-addresses memberships.
//
// The refusals here are ahead of ValidateSegments and say more than it can: the
// validator can only report that a member is not claimed by its machine, and the
// answer an operator needs is that membership is declared on the MACHINE and
// this command addresses it.
func (c *Config) applyMemberPatches(segment string, next *Segment, patches []SegmentMemberPatch, change *SegmentChange) error {
	for _, mp := range patches {
		machine := strings.TrimSpace(mp.Machine)
		if machine == "" {
			return fmt.Errorf("segment %s: a member patch names no machine — a member is a MACHINE at an address on this segment", segment)
		}
		m, declared := c.FindMachine(machine)
		if !declared {
			return fmt.Errorf("no machine %q to address on segment %s%s", machine, segment, c.machineHint())
		}
		if !machineNamesSegment(m, segment) {
			return fmt.Errorf("machine %s does not name segment %s among its segments (%s), so there is no membership here to address — membership is declared on the MACHINE and addressed here, and this command does not grant one",
				machine, segment, dashOrEmpty(strings.Join(m.Segments, ", ")))
		}

		idx := -1
		for i := range next.Members {
			if next.Members[i].Machine == machine {
				idx = i
				break
			}
		}
		mem := SegmentMember{Machine: machine}
		existed := idx >= 0
		if existed {
			mem = next.Members[idx]
		}

		if mp.Address != nil {
			v := strings.TrimSpace(*mp.Address)
			if v == "" {
				return fmt.Errorf("segment %s cannot give %s an empty address — a member with no address is not an entry at all, and unaddressing is how that is said (`hz segment set %s --unaddress %s`)",
					segment, machine, segment, machine)
			}
			change.note("member "+machine, mem.Address, v)
			mem.Address = v
		} else if !existed {
			return fmt.Errorf("segment %s does not address %s yet, so setting its key or endpoint has nothing to attach to — give it an address in the same command (`--member machine=%s,address=<ip>`)",
				segment, machine, machine)
		}
		if mp.PublicKey != nil {
			v := strings.TrimSpace(*mp.PublicKey)
			// Ahead of ValidateSegments so a DRY RUN refuses it too: SegmentSet
			// computes the change without saving, and a dry run that printed
			// "key → nonsense" and then had the confirmed write refuse it would
			// be the one thing a dry run exists to prevent.
			if v != "" {
				if _, err := wgkey.Parse(v); err != nil {
					return fmt.Errorf("segment %s: the key given for %s is %v. A WireGuard public key is what `wg pubkey` prints — %d base64 characters. hz holds the PUBLIC half only; the private one never leaves the box",
						segment, machine, err, wgkey.EncodedLen)
				}
			}
			change.note(machine+" key", mem.PublicKey, v)
			mem.PublicKey = v
		}
		if mp.Endpoint != nil {
			v := strings.TrimSpace(*mp.Endpoint)
			change.note(machine+" endpoint", mem.Endpoint, v)
			mem.Endpoint = v
		}

		if existed {
			next.Members[idx] = mem
		} else {
			next.Members = append(next.Members, mem)
		}
	}
	return nil
}

// applyUnaddress drops member entries, leaving the machines IN the segment.
func applyUnaddress(segment string, next *Segment, machines []string, change *SegmentChange) error {
	for _, raw := range machines {
		machine := strings.TrimSpace(raw)
		if machine == "" {
			continue
		}
		idx := -1
		for i := range next.Members {
			if next.Members[i].Machine == machine {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("segment %s does not address %s, so there is nothing to unaddress — `hz segment show %s` lists who is addressed and who is not",
				segment, machine, segment)
		}
		change.note("member "+machine, next.Members[idx].Address, "")
		next.Members = append(next.Members[:idx], next.Members[idx+1:]...)
	}
	return nil
}

// applyHubMove moves the hub and records the peer rewiring it causes.
//
// The rewiring is computed rather than described because it is not obvious: a
// spoke's peers become the new hub, the OLD hub stops peering with every other
// spoke and gains one peer, and the new hub gains all of them. None of that is
// visible in the record — Peers is derived — so it is the one change here that
// would otherwise happen entirely off-screen.
func applyHubMove(before Segment, next *Segment, hub *string, change *SegmentChange) error {
	if hub == nil {
		return nil
	}
	machine := strings.TrimSpace(*hub)
	if machine == "" {
		return fmt.Errorf("segment %s cannot be left without a hub — it is hub and spoke, so a segment with members has one, and the only move is naming a different member",
			next.Name)
	}
	found := false
	for i := range next.Members {
		if next.Members[i].Machine == machine {
			found = true
		}
	}
	if !found {
		addressed := make([]string, 0, len(next.Members))
		for _, m := range next.Members {
			addressed = append(addressed, m.Machine)
		}
		sort.Strings(addressed)
		return fmt.Errorf("segment %s does not address %s, so it cannot be the hub — the hub is a MEMBER with an address and an endpoint to dial. Addressed: %s",
			next.Name, machine, dashOrEmpty(strings.Join(addressed, ", ")))
	}

	old, hadHub := next.Hub()
	if hadHub && old.Machine == machine {
		return nil // already the hub; not a change and not an error
	}
	for i := range next.Members {
		next.Members[i].Hub = next.Members[i].Machine == machine
	}

	move := &SegmentHubMove{From: old.Machine, To: machine}
	if !hadHub {
		move.From = ""
	}
	seen := map[string]bool{}
	for _, m := range append(append([]SegmentMember(nil), before.Members...), next.Members...) {
		if seen[m.Machine] {
			continue
		}
		seen[m.Machine] = true
		was, now := peerNames(before.PeersOf(m.Machine)), peerNames(next.PeersOf(m.Machine))
		if strings.Join(was, ",") == strings.Join(now, ",") {
			continue
		}
		move.Peers = append(move.Peers, SegmentPeerChange{Machine: m.Machine, Before: was, After: now})
	}
	sort.Slice(move.Peers, func(i, j int) bool { return move.Peers[i].Machine < move.Peers[j].Machine })
	change.HubMove = move
	return nil
}

func peerNames(peers []SegmentMember) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.Machine)
	}
	return out
}

// applyCIDR changes the range, and answers the question the range change raises:
// what happens to a member whose address is no longer inside it.
//
// REFUSE, THEN CASCADE — the discipline `rm` already uses, for the same reason.
// A narrower range silently dropping three machines' addresses is a change to
// records the operator did not name; naming each one and stopping is the only
// version of this that the operator can act on. Cascade UNADDRESSES them — it
// does not renumber them, because hz inventing an address is how two machines
// come to hold one, and an unaddressed membership is a state the model already
// has a meaning for.
func applyCIDR(segment string, next *Segment, cidr *string, cascade bool, change *SegmentChange) ([]Dependant, error) {
	if cidr == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*cidr)
	if v == "" {
		return nil, fmt.Errorf("segment %s cannot have its range cleared — the range is what a member address is checked against and what a spoke routes to the hub", segment)
	}
	was := next.CIDR
	next.CIDR = v
	ipnet, err := parseSegmentCIDR(*next)
	if err != nil {
		next.CIDR = was
		return nil, err
	}
	change.note("range", was, v)

	kept := make([]SegmentMember, 0, len(next.Members))
	var stranded []Dependant
	for _, m := range next.Members {
		ip := net.ParseIP(strings.TrimSpace(m.Address))
		if ip != nil && ipnet.Contains(ip) {
			kept = append(kept, m)
			continue
		}
		d := Dependant{
			Kind: "machine", Name: m.Machine,
			How: "is addressed " + m.Address + ", which is outside the new range " + ipnet.String(),
		}
		if !cascade {
			stranded = append(stranded, d)
			kept = append(kept, m)
			continue
		}
		d.How += ", and it would be UNADDRESSED — it stays IN the segment and peers with nothing until it is given an address inside " + ipnet.String()
		if m.Hub {
			d.How += " (it is the HUB, so name a new one with --hub in the same command)"
		}
		change.Strands = append(change.Strands, d)
	}
	next.Members = kept
	sortDependants(stranded)
	sortDependants(change.Strands)
	return stranded, nil
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
