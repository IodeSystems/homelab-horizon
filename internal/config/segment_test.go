package config

import (
	"strings"
	"testing"
)

// estateWithSegment is the smallest config that has something to resolve: a
// project, a hub box and a spoke box, and the segment they are both on. It is
// the shape plan/upstream-and-promotion.md §5 describes in miniature — one
// gateway and one client.
func estateWithSegment(t *testing.T) *Config {
	t.Helper()
	c := &Config{}
	if err := c.AddProject("iodesystems", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "gw-1", Segments: []string{"iode-net"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMachine(Machine{Name: "redline-prod-hz", Segments: []string{"iode-net"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSegment(Segment{
		Name:      "iode-net",
		Project:   "iodesystems",
		CIDR:      "10.42.0.0/24",
		Interface: "wg-iode",
		Members: []SegmentMember{
			{Machine: "gw-1", Address: "10.42.0.1", Hub: true, Endpoint: "hz.example.com:51820"},
			{Machine: "redline-prod-hz", Address: "10.42.0.2"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// THE POINT OF THE RECORD: a membership stops being a label. The three
// questions projection.Segment asks — interface, address, peers — all have an
// answer, and the answer for a CLIENT of someone else's segment is different
// from the hub's. That asymmetry is what plan/upstream-and-promotion.md §5
// needs and what a list of names could not express.
func TestAMembershipResolvesToAnInterfaceAnAddressAndPeers(t *testing.T) {
	c := estateWithSegment(t)

	segs := c.SegmentsOf("redline-prod-hz")
	if len(segs) != 1 || segs[0].Name != "iode-net" {
		t.Fatalf("the membership resolved to %+v", segs)
	}
	s := segs[0]
	if s.Interface != "wg-iode" {
		t.Fatalf("interface: %q", s.Interface)
	}
	mem, ok := s.Member("redline-prod-hz")
	if !ok || mem.Address != "10.42.0.2" {
		t.Fatalf("address: %+v", mem)
	}
	if mem.Hub {
		t.Fatal("the client reported itself the hub of someone else's segment")
	}

	// A spoke peers with the hub, and with nothing else.
	peers := s.PeersOf("redline-prod-hz")
	if len(peers) != 1 || peers[0].Machine != "gw-1" || peers[0].Endpoint != "hz.example.com:51820" {
		t.Fatalf("the client's peers are %+v", peers)
	}
	// The hub peers with every spoke.
	peers = s.PeersOf("gw-1")
	if len(peers) != 1 || peers[0].Machine != "redline-prod-hz" {
		t.Fatalf("the hub's peers are %+v", peers)
	}
	// A machine that is not a member peers with nothing, rather than with
	// everyone.
	if peers := s.PeersOf("someone-else"); peers != nil {
		t.Fatalf("a non-member was given peers: %+v", peers)
	}
}

// A segment belongs to a project, and the project has to exist — the same rule
// an environment lives under, for the same reason: a segment under a project
// nobody declared reads later as an empty grouping rather than a wrong one.
func TestASegmentNamesAProjectThatExists(t *testing.T) {
	c := &Config{}
	err := c.AddSegment(Segment{Name: "orphan", Project: "nope", CIDR: "10.1.0.0/24", Interface: "wg-orphan"})
	if err == nil {
		t.Fatal("a segment was declared under a project that does not exist")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("the refusal does not name the project: %v", err)
	}
	if len(c.Segments) != 0 {
		t.Fatal("a refused declaration was written anyway")
	}

	// And with no project at all, which is the same hole spelled differently.
	if err := c.AddSegment(Segment{Name: "orphan", CIDR: "10.1.0.0/24", Interface: "wg-orphan"}); err == nil {
		t.Fatal("a segment was declared with no project")
	}
}

// Once ONE segment is declared, a membership that names nothing is a typo and
// is refused. Before that it is a label, and refusing it would refuse every
// machine on every config written before this record existed.
func TestAMembershipResolvesOnlyOnceSegmentsAreDeclared(t *testing.T) {
	// No segments declared: labels, and Save writes them.
	labels := &Config{Machines: []Machine{{Name: "app-1", Segments: []string{"seg:storefront"}}}}
	if err := labels.ValidateMachines(); err != nil {
		t.Fatalf("a label membership was refused on a config with no segments: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", labels); err != nil {
		t.Fatalf("Save refused a config with no segments: %v", err)
	}

	// One segment declared: every membership has to resolve.
	c := estateWithSegment(t)
	err := c.AddMachine(Machine{Name: "stray", Segments: []string{"typo-net"}})
	if err == nil {
		t.Fatal("a machine joined a segment that does not exist")
	}
	for _, want := range []string{"stray", "typo-net", "iode-net"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if _, ok := c.FindMachine("stray"); ok {
		t.Fatal("a refused machine was written anyway")
	}
}

// Two segments must not share a range. A machine in both would have two routes
// for one prefix, one silently wins, and the other network is unreachable by
// name — the failure cidr_advice.go warns about between hz's range and a
// hotel's, here between two ranges hz hands out itself.
func TestSegmentRangesMayNotOverlap(t *testing.T) {
	c := estateWithSegment(t)
	err := c.AddSegment(Segment{
		Name: "iode-net-2", Project: "iodesystems",
		CIDR: "10.42.0.0/25", Interface: "wg-iode-2",
	})
	if err == nil {
		t.Fatal("two segments were declared on overlapping ranges")
	}
	for _, want := range []string{"iode-net", "iode-net-2", "overlap"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// A range beside it is fine — the check is overlap, not adjacency.
	if err := c.AddSegment(Segment{
		Name: "iode-net-2", Project: "iodesystems",
		CIDR: "10.43.0.0/24", Interface: "wg-iode-2",
	}); err != nil {
		t.Fatalf("a non-overlapping segment was refused: %v", err)
	}
}

// Two segments must not share an interface either. That is the collision
// pluralising WGInterface exists to prevent (plan/architecture.md item 15): a
// machine in both would have one interface for two networks, and it would
// discover that at the box.
func TestTwoSegmentsMayNotShareAnInterface(t *testing.T) {
	c := estateWithSegment(t)
	err := c.AddSegment(Segment{
		Name: "other", Project: "iodesystems",
		CIDR: "10.43.0.0/24", Interface: "wg-iode",
	})
	if err == nil {
		t.Fatal("two segments were declared on one interface")
	}
	if !strings.Contains(err.Error(), "wg-iode") {
		t.Fatalf("the refusal does not name the interface: %v", err)
	}
}

// The machine's list and the segment's member list are two halves of one fact,
// so they are not allowed to disagree: a member the machine does not claim is
// refused. The other direction — a claimed membership with no address yet — is
// legal, and is the state `hz machine add --segment` leaves.
func TestASegmentMayNotAddressAMachineThatDoesNotClaimIt(t *testing.T) {
	c := estateWithSegment(t)
	if err := c.AddMachine(Machine{Name: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddSegment(Segment{
		Name: "other", Project: "iodesystems", CIDR: "10.43.0.0/24", Interface: "wg-other",
		Members: []SegmentMember{{Machine: "unrelated", Address: "10.43.0.1", Hub: true}},
	}); err == nil {
		t.Fatal("a segment addressed a machine that does not name it")
	}

	// A member that is not a machine at all is refused too.
	if err := c.AddSegment(Segment{
		Name: "other", Project: "iodesystems", CIDR: "10.43.0.0/24", Interface: "wg-other",
		Members: []SegmentMember{{Machine: "ghost", Address: "10.43.0.1", Hub: true}},
	}); err == nil {
		t.Fatal("a segment addressed a machine that does not exist")
	}

	// And the legal intermediate: in the segment, not addressed on it.
	if err := c.AddMachine(Machine{Name: "late", Segments: []string{"iode-net"}}); err != nil {
		t.Fatalf("an unaddressed membership was refused: %v", err)
	}
	s, _ := c.FindSegment("iode-net")
	if _, addressed := s.Member("late"); addressed {
		t.Fatal("joining a segment invented an address")
	}
}

// Validated on Save, beside the other five validators. Hand-edited JSON is the
// path that skips AddSegment, so the chokepoint has to catch it too.
func TestSaveRefusesABadSegment(t *testing.T) {
	base := func(members ...SegmentMember) *Config {
		return &Config{
			Projects: []Project{{Name: "p"}},
			Machines: []Machine{{Name: "m-1", Segments: []string{"s"}}},
			Segments: []Segment{{
				Name: "s", Project: "p", CIDR: "10.9.0.0/24", Interface: "wg-s", Members: members,
			}},
		}
	}
	for name, cfg := range map[string]*Config{
		"no name":            {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: " ", Project: "p", CIDR: "10.9.0.0/24", Interface: "wg-s"}}},
		"declared twice":     {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.0/24", Interface: "wg-s"}, {Name: "s", Project: "p", CIDR: "10.10.0.0/24", Interface: "wg-s2"}}},
		"no interface":       {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.0/24"}}},
		"no cidr":            {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: "s", Project: "p", Interface: "wg-s"}}},
		"cidr is not a cidr": {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.0", Interface: "wg-s"}}},
		// A host address where a network belongs: masked away silently, every
		// member address would then be checked against a range nobody declared.
		"cidr is a host address": {Projects: []Project{{Name: "p"}}, Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.1/24", Interface: "wg-s"}}},
		"member with no address": base(SegmentMember{Machine: "m-1", Hub: true}),
		"member outside the range": base(
			SegmentMember{Machine: "m-1", Address: "10.8.0.5", Hub: true}),
		"members but no hub": base(SegmentMember{Machine: "m-1", Address: "10.9.0.5"}),
	} {
		if err := cfg.ValidateSegments(); err == nil {
			t.Fatalf("%s: ValidateSegments accepted it", name)
		}
		if err := Save(t.TempDir()+"/config.json", cfg); err == nil {
			t.Fatalf("%s: Save wrote it", name)
		}
	}

	// The positive control: the valid shape is accepted, so the table above is
	// not passing because Save refuses everything.
	ok := base(SegmentMember{Machine: "m-1", Address: "10.9.0.1", Hub: true})
	if err := ok.ValidateSegments(); err != nil {
		t.Fatalf("a valid segment was refused: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", ok); err != nil {
		t.Fatalf("Save refused a valid segment: %v", err)
	}

	// Two members cannot hold one address, and there cannot be two hubs.
	twoAddrs := &Config{
		Projects: []Project{{Name: "p"}},
		Machines: []Machine{{Name: "m-1", Segments: []string{"s"}}, {Name: "m-2", Segments: []string{"s"}}},
		Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.0/24", Interface: "wg-s", Members: []SegmentMember{
			{Machine: "m-1", Address: "10.9.0.1", Hub: true},
			{Machine: "m-2", Address: "10.9.0.1"},
		}}},
	}
	if err := twoAddrs.ValidateSegments(); err == nil {
		t.Fatal("two members were given one address")
	}
	twoHubs := &Config{
		Projects: []Project{{Name: "p"}},
		Machines: []Machine{{Name: "m-1", Segments: []string{"s"}}, {Name: "m-2", Segments: []string{"s"}}},
		Segments: []Segment{{Name: "s", Project: "p", CIDR: "10.9.0.0/24", Interface: "wg-s", Members: []SegmentMember{
			{Machine: "m-1", Address: "10.9.0.1", Hub: true},
			{Machine: "m-2", Address: "10.9.0.2", Hub: true},
		}}},
	}
	if err := twoHubs.ValidateSegments(); err == nil {
		t.Fatal("a segment was declared with two hubs")
	}
}

// A segment name is taken once, for the reason a machine name is: the name is
// what a membership resolves through, and two records answering to it is a
// resolution hz cannot make.
func TestASegmentNameIsTakenOnce(t *testing.T) {
	c := estateWithSegment(t)
	err := c.AddSegment(Segment{Name: "iode-net", Project: "iodesystems", CIDR: "10.44.0.0/24", Interface: "wg-dup"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a duplicate segment name was accepted: %v", err)
	}
	if len(c.Segments) != 1 {
		t.Fatalf("the config holds %d segments", len(c.Segments))
	}
}

// Removal refuses while a machine is a member, and NAMES every one — the
// convention `hz project rm`, `hz env rm` and `hz machine rm` set. Removing it
// anyway would leave memberships resolving to nothing, which Save now refuses:
// the operator would meet it as a validation error about a machine they never
// mentioned.
func TestRemovingASegmentWithMembersIsRefusedAndNamesThem(t *testing.T) {
	c := estateWithSegment(t)

	removes, blocked, err := c.SegmentRemoval("iode-net", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 2 {
		t.Fatalf("the members did not block the removal: %+v", blocked)
	}
	for _, d := range blocked {
		if d.Kind != "machine" {
			t.Fatalf("the blocker is not a machine: %+v", d)
		}
	}
	if len(removes) != 1 {
		t.Fatalf("a blocked removal proposed to remove %d things", len(removes))
	}
	if _, err := c.RemoveSegment("iode-net", false); err == nil {
		t.Fatal("a segment with members was removed without cascade")
	}
	if _, ok := c.FindSegment("iode-net"); !ok {
		t.Fatal("a refused removal removed the segment anyway")
	}

	// Cascade lists each membership it will DROP, then drops it — and the
	// machines survive, in no segment, which is legal.
	removes, blocked, err = c.SegmentRemoval("iode-net", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 || len(removes) != 3 {
		t.Fatalf("cascade proposed removes=%+v blocked=%+v", removes, blocked)
	}
	if !strings.Contains(removes[1].How, "DROPPED") {
		t.Fatalf("cascade does not say what happens to the membership: %q", removes[1].How)
	}
	if _, err := c.RemoveSegment("iode-net", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindSegment("iode-net"); ok {
		t.Fatal("the segment survived its own removal")
	}
	for _, name := range []string{"gw-1", "redline-prod-hz"} {
		m, ok := c.FindMachine(name)
		if !ok {
			t.Fatalf("cascade removed machine %s, which it only promised to unjoin", name)
		}
		if len(m.Segments) != 0 {
			t.Fatalf("machine %s still names %v", name, m.Segments)
		}
	}
	// And the result is savable, which is the whole reason cascade touches the
	// machines at all.
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the config after a cascade cannot be saved: %v", err)
	}
}

// A removal that names no segment says which ones exist, for the reason every
// other hint in this package does: "no segment X" with no list reads as a typo
// when the real answer is that this config declares none.
func TestRemovingAnUnknownSegmentListsTheDeclaredOnes(t *testing.T) {
	c := estateWithSegment(t)
	_, _, err := c.SegmentRemoval("nope", false)
	if err == nil || !strings.Contains(err.Error(), "iode-net") {
		t.Fatalf("the refusal does not list what exists: %v", err)
	}

	empty := &Config{}
	_, _, err = empty.SegmentRemoval("nope", false)
	if err == nil || !strings.Contains(err.Error(), "declares no segments at all") {
		t.Fatalf("an empty config does not say it is empty: %v", err)
	}
}
