package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// testWGKey and testWGKey2 are real WireGuard public keys — minted once with
// wgkey.Generate and pasted here rather than generated per run, so a failure
// prints the same string twice and a test that compares keys is deterministic.
// They are PUBLIC halves of key pairs whose private halves were never written
// down; there is nothing here to protect.
const (
	testWGKey  = "8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0="
	testWGKey2 = "IeNDqihcCycgQ9s+UnsC4lShD7/9oHii3oOaBqZqjSY="
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

// THE GAP `ls|show|add|rm` LEFT, closed: addressing a membership on a segment
// that already exists. Before SetSegment the only way to give `late-box` an
// address was `rm --cascade` and re-declaring the whole segment — a destructive
// round trip for a routine edit, which is how a hub and two public keys get
// dropped on the way through.
func TestAMembershipCanBeAddressedOnASegmentThatAlreadyExists(t *testing.T) {
	c := estateWithSegment(t)
	if err := c.AddMachine(Machine{Name: "late-box", Segments: []string{"iode-net"}}); err != nil {
		t.Fatal(err)
	}
	s, _ := c.FindSegment("iode-net")
	if _, addressed := s.Member("late-box"); addressed {
		t.Fatal("joining a segment invented an address")
	}

	addr := "10.42.0.7"
	change, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "late-box", Address: &addr}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Fields) != 1 || !strings.Contains(change.Fields[0], "10.42.0.7") {
		t.Fatalf("the change was not reported: %+v", change.Fields)
	}
	s, _ = c.FindSegment("iode-net")
	mem, ok := s.Member("late-box")
	if !ok || mem.Address != "10.42.0.7" {
		t.Fatalf("late-box is %+v", mem)
	}
	// And it now peers with the hub, which is the whole point of addressing it.
	peers := s.PeersOf("late-box")
	if len(peers) != 1 || peers[0].Machine != "gw-1" {
		t.Fatalf("an addressed member peers with %+v", peers)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the config after a set cannot be saved: %v", err)
	}
}

// RE-ADDRESSING KEEPS WHAT IT WAS NOT ASKED TO CHANGE. A whole-member write
// would drop the public key and the endpoint the operator did not retype, and
// the box would go on answering to a key hz no longer holds.
func TestReAddressingKeepsTheKeyAndTheEndpoint(t *testing.T) {
	c := estateWithSegment(t)
	// A REAL key, because the field is validated as one now. The fixture used
	// to be "abc+/def=", which is nine characters and nothing WireGuard would
	// load — a test that proved the key survived a re-address while proving
	// nothing about what a key is.
	key := testWGKey

	// A key on a membership with no address has nothing to attach to: the entry
	// would be a member with a key and no address, which the validator refuses
	// anyway. Refused here instead, where the answer is "give it an address".
	if err := c.AddMachine(Machine{Name: "late-box", Segments: []string{"iode-net"}}); err != nil {
		t.Fatal(err)
	}
	_, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "late-box", PublicKey: &key}},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatalf("a key on an unaddressed membership was accepted: %v", err)
	}

	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", PublicKey: &key}},
	}, false); err != nil {
		t.Fatal(err)
	}

	moved := "10.42.0.9"
	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", Address: &moved}},
	}, false); err != nil {
		t.Fatal(err)
	}
	s, _ := c.FindSegment("iode-net")
	mem, _ := s.Member("redline-prod-hz")
	if mem.Address != "10.42.0.9" {
		t.Fatalf("the address did not move: %+v", mem)
	}
	if mem.PublicKey != key {
		t.Fatalf("re-addressing dropped the public key: %+v", mem)
	}
	// The hub's endpoint survived a write that never mentioned the hub.
	hub, _ := s.Member("gw-1")
	if hub.Endpoint != "hz.example.com:51820" || !hub.Hub {
		t.Fatalf("the hub was disturbed by a member patch: %+v", hub)
	}

	// An empty key CLEARS it, which is the pointer contract and is what a
	// rotated-away key needs: absent beats stale.
	empty := ""
	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", PublicKey: &empty}},
	}, false); err != nil {
		t.Fatal(err)
	}
	s, _ = c.FindSegment("iode-net")
	mem, _ = s.Member("redline-prod-hz")
	if mem.PublicKey != "" {
		t.Fatalf("an empty key did not clear it: %q", mem.PublicKey)
	}
}

// A set routes through the SAME validator `add` does, so every invariant still
// holds afterwards. A second write path that validated differently is how a
// config becomes unloadable.
func TestASetCannotBreakAnInvariant(t *testing.T) {
	str := func(s string) *string { return &s }

	for name, tc := range map[string]struct {
		patch SegmentPatch
		want  string
	}{
		"address outside the range": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", Address: str("10.99.0.2")}}},
			"outside the segment's range",
		},
		"address already held": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", Address: str("10.42.0.1")}}},
			"same address",
		},
		"address is not an address": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "redline-prod-hz", Address: str("not-an-ip")}}},
			"not an IP address",
		},
		"a machine that does not claim the segment": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "unrelated", Address: str("10.42.0.5")}}},
			"does not name segment",
		},
		"a machine that does not exist": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "ghost", Address: str("10.42.0.5")}}},
			"no machine",
		},
		"the interface another segment holds": {
			SegmentPatch{Interface: str("wg-other")},
			"both use interface",
		},
		// Wide enough to still hold both member addresses, so the refusal is
		// about the overlap and not about stranding somebody.
		"a range another segment holds": {
			SegmentPatch{CIDR: str("10.32.0.0/12")},
			"overlap",
		},
		"a range that is a host address": {
			SegmentPatch{CIDR: str("10.42.0.1/24")},
			"host address",
		},
		"a project that does not exist": {
			SegmentPatch{Project: str("nope")},
			"no project",
		},
		"no project at all": {
			SegmentPatch{Project: str("")},
			"cannot have its project cleared",
		},
		"no interface at all": {
			SegmentPatch{Interface: str("")},
			"cannot have its interface cleared",
		},
		"no range at all": {
			SegmentPatch{CIDR: str("")},
			"cannot have its range cleared",
		},
		"no hub at all": {
			SegmentPatch{Hub: str("")},
			"cannot be left without a hub",
		},
		"a hub that is not addressed": {
			SegmentPatch{Hub: str("unrelated")},
			"does not address unrelated",
		},
		"unaddressing the hub with spokes left": {
			SegmentPatch{Unaddress: []string{"gw-1"}},
			"no hub",
		},
		"unaddressing somebody who is not addressed": {
			SegmentPatch{Unaddress: []string{"unrelated"}},
			"nothing to unaddress",
		},
		"an empty address": {
			SegmentPatch{Members: []SegmentMemberPatch{{Machine: "gw-1", Address: str("")}}},
			"empty address",
		},
	} {
		// A subtest each, so breaking ONE invariant shows up as that invariant
		// rather than as whichever map key the runtime happened to visit first.
		t.Run(name, func(t *testing.T) {
			c := estateWithSegment(t)
			if err := c.AddMachine(Machine{Name: "unrelated"}); err != nil {
				t.Fatal(err)
			}
			if err := c.AddSegment(Segment{Name: "other", Project: "iodesystems", CIDR: "10.43.0.0/24", Interface: "wg-other"}); err != nil {
				t.Fatal(err)
			}
			before, _ := c.FindSegment("iode-net")

			_, err := c.SetSegment("iode-net", tc.patch, false)
			if err == nil {
				t.Fatal("the set was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say %q: %v", tc.want, err)
			}
			after, _ := c.FindSegment("iode-net")
			if fmt.Sprintf("%+v", before) != fmt.Sprintf("%+v", after) {
				t.Fatalf("a refused set was written anyway:\n  before %+v\n  after  %+v", before, after)
			}
		})
	}

	// The positive control: the table is not passing because SetSegment refuses
	// everything. A legal move on the same estate is accepted and saved.
	c := estateWithSegment(t)
	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Interface: str("wg-iode-2"),
		Members:   []SegmentMemberPatch{{Machine: "redline-prod-hz", Address: str("10.42.0.3")}},
	}, false); err != nil {
		t.Fatalf("a legal set was refused: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("Save refused the result of a legal set: %v", err)
	}
}

// MOVING THE HUB IS A TOPOLOGY CHANGE, and the rewiring it causes is REPORTED
// rather than left to happen quietly. Peers is derived hub and spoke, so the new
// hub gains every spoke, the old one drops to a single peer, and every other
// spoke swaps the peer it had — none of which shows in a diff of the record.
func TestMovingTheHubRewiresEveryPeerSetAndSaysSo(t *testing.T) {
	c := estateWithSegment(t)
	if err := c.AddMachine(Machine{Name: "third", Segments: []string{"iode-net"}}); err != nil {
		t.Fatal(err)
	}
	addr := "10.42.0.3"
	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "third", Address: &addr}},
	}, false); err != nil {
		t.Fatal(err)
	}

	hub := "redline-prod-hz"
	change, err := c.SetSegment("iode-net", SegmentPatch{Hub: &hub}, false)
	if err != nil {
		t.Fatal(err)
	}
	if change.HubMove == nil {
		t.Fatal("moving the hub was not reported as a hub move")
	}
	if change.HubMove.From != "gw-1" || change.HubMove.To != "redline-prod-hz" {
		t.Fatalf("the hub move reads %+v", change.HubMove)
	}
	// Every one of the three members' peer sets changed, and every one is named.
	if len(change.HubMove.Peers) != 3 {
		t.Fatalf("the rewiring lists %d members: %+v", len(change.HubMove.Peers), change.HubMove.Peers)
	}
	got := map[string][]string{}
	for _, p := range change.HubMove.Peers {
		got[p.Machine] = p.After
	}
	want := map[string][]string{
		"gw-1":            {"redline-prod-hz"},
		"redline-prod-hz": {"gw-1", "third"},
		"third":           {"redline-prod-hz"},
	}
	for machine, peers := range want {
		if strings.Join(got[machine], ",") != strings.Join(peers, ",") {
			t.Fatalf("%s now peers with %v, want %v", machine, got[machine], peers)
		}
	}
	// And the record agrees with the report — the report is not a second answer.
	s, _ := c.FindSegment("iode-net")
	for machine, peers := range want {
		if strings.Join(peerNames(s.PeersOf(machine)), ",") != strings.Join(peers, ",") {
			t.Fatalf("the record disagrees with the reported rewiring for %s", machine)
		}
	}
	if h, ok := s.Hub(); !ok || h.Machine != "redline-prod-hz" {
		t.Fatalf("the hub is %+v", h)
	}
	// Exactly one hub survived: the old one was demoted, not joined.
	hubs := 0
	for _, m := range s.Members {
		if m.Hub {
			hubs++
		}
	}
	if hubs != 1 {
		t.Fatalf("the segment has %d hubs", hubs)
	}

	// Naming the hub that already holds it is not a change and not an error.
	change, err = c.SetSegment("iode-net", SegmentPatch{Hub: &hub}, false)
	if err != nil {
		t.Fatal(err)
	}
	if change.HubMove != nil {
		t.Fatalf("re-naming the same hub reported a move: %+v", change.HubMove)
	}
}

// A NEW RANGE CAN STRAND EVERY MEMBER INSIDE THE OLD ONE, so it follows the
// discipline `rm` uses: refuse, and NAME each address that would fall outside.
// hz does not renumber a box — two machines on one address is the failure that
// would cause — so cascade UNADDRESSES them instead, and that is opt-in.
func TestANewRangeThatStrandsMembersIsRefusedAndNamesThem(t *testing.T) {
	c := estateWithSegment(t)
	narrow := "10.42.9.0/24"

	change, blocked, err := c.SegmentSet("iode-net", SegmentPatch{CIDR: &narrow}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 2 {
		t.Fatalf("the stranded members did not block the change: %+v", blocked)
	}
	for _, d := range blocked {
		if !strings.Contains(d.How, "outside the new range") {
			t.Fatalf("a blocker does not say why: %+v", d)
		}
	}
	if len(change.Strands) != 0 {
		t.Fatalf("a blocked change proposed to unaddress %+v", change.Strands)
	}
	if _, err := c.SetSegment("iode-net", SegmentPatch{CIDR: &narrow}, false); err == nil {
		t.Fatal("a range that strands two members was written without cascade")
	}
	s, _ := c.FindSegment("iode-net")
	if s.CIDR != "10.42.0.0/24" || len(s.Members) != 2 {
		t.Fatalf("a refused range change was written anyway: %+v", s)
	}

	// Cascade: the dry run lists exactly who it would unaddress, and says they
	// STAY in the segment. Nothing is renumbered.
	change, blocked, err = c.SegmentSet("iode-net", SegmentPatch{CIDR: &narrow}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 || len(change.Strands) != 2 {
		t.Fatalf("cascade proposed strands=%+v blocked=%+v", change.Strands, blocked)
	}
	if !strings.Contains(change.Strands[0].How, "UNADDRESSED") {
		t.Fatalf("cascade does not say what happens to the address: %q", change.Strands[0].How)
	}
	s, _ = c.FindSegment("iode-net")
	if s.CIDR != "10.42.0.0/24" {
		t.Fatal("computing a change wrote it")
	}

	// And with cascade it writes: the machines stay IN the segment, unaddressed.
	if _, err := c.SetSegment("iode-net", SegmentPatch{CIDR: &narrow}, true); err != nil {
		t.Fatal(err)
	}
	s, _ = c.FindSegment("iode-net")
	if s.CIDR != "10.42.9.0/24" || len(s.Members) != 0 {
		t.Fatalf("cascade left %+v", s)
	}
	for _, name := range []string{"gw-1", "redline-prod-hz"} {
		m, ok := c.FindMachine(name)
		if !ok || len(m.Segments) != 1 || m.Segments[0] != "iode-net" {
			t.Fatalf("cascade took %s out of the segment, which it only promised to unaddress: %+v", name, m)
		}
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the config after a cascading range change cannot be saved: %v", err)
	}
}

// Renumbering in ONE command is the move an operator actually wants: the new
// range and the new addresses together. The range is applied LAST so the
// addresses are checked as they will BE, not as they were.
func TestARangeAndItsAddressesCanMoveInOneCommand(t *testing.T) {
	c := estateWithSegment(t)
	cidr, hubAddr, spokeAddr := "10.50.0.0/24", "10.50.0.1", "10.50.0.2"

	change, err := c.SetSegment("iode-net", SegmentPatch{
		CIDR: &cidr,
		Members: []SegmentMemberPatch{
			{Machine: "gw-1", Address: &hubAddr},
			{Machine: "redline-prod-hz", Address: &spokeAddr},
		},
	}, false)
	if err != nil {
		t.Fatalf("a renumber in one command was refused: %v", err)
	}
	if len(change.Strands) != 0 {
		t.Fatalf("a complete renumber stranded %+v", change.Strands)
	}
	s, _ := c.FindSegment("iode-net")
	if s.CIDR != "10.50.0.0/24" || len(s.Members) != 2 {
		t.Fatalf("the segment reads %+v", s)
	}
	hub, _ := s.Member("gw-1")
	if hub.Address != "10.50.0.1" || !hub.Hub || hub.Endpoint != "hz.example.com:51820" {
		t.Fatalf("the hub reads %+v", hub)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the renumbered config cannot be saved: %v", err)
	}
}

// Unaddressing leaves the machine IN the segment — the legal state
// `hz machine add --segment` leaves, not a removal. The hub has to move first,
// because a segment with members and no hub is refused.
func TestUnaddressingLeavesTheMachineInTheSegment(t *testing.T) {
	c := estateWithSegment(t)
	hub := "redline-prod-hz"
	change, err := c.SetSegment("iode-net", SegmentPatch{Hub: &hub, Unaddress: []string{"gw-1"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Fields) != 1 || !strings.Contains(change.Fields[0], "gw-1") {
		t.Fatalf("the unaddressing was not reported: %+v", change.Fields)
	}
	s, _ := c.FindSegment("iode-net")
	if _, addressed := s.Member("gw-1"); addressed {
		t.Fatal("gw-1 is still addressed")
	}
	m, ok := c.FindMachine("gw-1")
	if !ok || len(m.Segments) != 1 {
		t.Fatalf("unaddressing removed the membership too: %+v", m)
	}
	// It peers with nothing, which is what an unaddressed membership means.
	if peers := s.PeersOf("gw-1"); peers != nil {
		t.Fatalf("an unaddressed member peers with %+v", peers)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("the config after an unaddress cannot be saved: %v", err)
	}
}

// The name is IDENTITY and there is no way to change it here: every machine's
// membership resolves through it. The patch type has no Name field at all, so
// the guarantee is structural rather than a check — this pins that a set on a
// segment leaves the name, and every membership that resolves through it, alone.
func TestASetCannotRenameASegment(t *testing.T) {
	c := estateWithSegment(t)
	note := "the iodesystems network"
	if _, err := c.SetSegment("iode-net", SegmentPatch{Note: &note}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.FindSegment("iode-net"); !ok {
		t.Fatal("a set renamed the segment")
	}
	for _, name := range []string{"gw-1", "redline-prod-hz"} {
		m, _ := c.FindMachine(name)
		if len(m.Segments) != 1 || m.Segments[0] != "iode-net" {
			t.Fatalf("%s resolves through %v", name, m.Segments)
		}
	}

	// And an empty patch is refused rather than written as a no-op success.
	if _, err := c.SetSegment("iode-net", SegmentPatch{}, false); err == nil {
		t.Fatal("an empty patch reported success")
	}
	// As is a set on a segment that does not exist, listing the ones that do.
	_, err := c.SetSegment("nope", SegmentPatch{Note: &note}, false)
	if err == nil || !strings.Contains(err.Error(), "iode-net") {
		t.Fatalf("the refusal does not list what exists: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A key is validated as a key
// ---------------------------------------------------------------------------

// A PublicKey is the ONE field on a member that another program parses: it goes
// into a `PublicKey =` line and wg-quick either loads the interface or does not.
// So hz refuses a string that is not a key, at the same validator every write
// runs through, rather than storing it and letting the box find out.
func TestAMemberKeyMustBeAWireGuardKey(t *testing.T) {
	for name, key := range map[string]string{
		"a placeholder":   "key-gw-1",
		"the old fixture": "abc+/def=",
		"a hostname":      "gw-1.example.com",
		"unpadded":        strings.TrimSuffix(testWGKey, "="),
		"one char short":  testWGKey[:43],
	} {
		c := estateWithSegment(t)
		c.Segments[0].Members[0].PublicKey = key
		if err := c.ValidateSegments(); err == nil {
			t.Errorf("%s: %q was accepted as a public key", name, key)
		} else if !strings.Contains(err.Error(), "public key") {
			t.Errorf("%s: the refusal does not say it is about the key: %v", name, err)
		}
	}

	// And a real one is accepted, through the write path an operator uses.
	c := estateWithSegment(t)
	if _, err := c.SetSegment("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "gw-1", PublicKey: strPtr(testWGKey)}},
	}, false); err != nil {
		t.Fatalf("a real key was refused: %v", err)
	}
	if err := Save(t.TempDir()+"/config.json", c); err != nil {
		t.Fatalf("a keyed segment cannot be saved: %v", err)
	}
}

// A DRY RUN REFUSES IT TOO. SegmentSet computes without writing, and a dry run
// that printed "key → nonsense" only for the confirmed write to refuse it is
// the failure a dry run exists to prevent.
func TestADryRunRefusesAKeyThatIsNotAKey(t *testing.T) {
	c := estateWithSegment(t)
	_, _, err := c.SegmentSet("iode-net", SegmentPatch{
		Members: []SegmentMemberPatch{{Machine: "gw-1", PublicKey: strPtr("not-a-key")}},
	}, false)
	if err == nil {
		t.Fatal("a dry run accepted a key the write would refuse")
	}
	if !strings.Contains(err.Error(), "wg pubkey") {
		t.Fatalf("the refusal does not say what a key looks like: %v", err)
	}
}

// TWO MEMBERS, ONE KEY is a peer set that cannot be rendered: WireGuard
// identifies a peer BY its key, so the second [Peer] block replaces the first
// and one machine silently loses its route. It is what a copy-pasted key looks
// like, and it is refused.
func TestTwoMembersCannotShareAPublicKey(t *testing.T) {
	c := estateWithSegment(t)
	c.Segments[0].Members[0].PublicKey = testWGKey
	c.Segments[0].Members[1].PublicKey = testWGKey
	err := c.ValidateSegments()
	if err == nil {
		t.Fatal("two members share one public key and the config was accepted")
	}
	if !strings.Contains(err.Error(), "gw-1") || !strings.Contains(err.Error(), "redline-prod-hz") {
		t.Fatalf("the refusal does not name both members: %v", err)
	}

	// Distinct keys on the same two members are fine — this is the normal state
	// of a keyed segment, and the check above must not be refusing that.
	c.Segments[0].Members[1].PublicKey = testWGKey2
	if err := c.ValidateSegments(); err != nil {
		t.Fatalf("two members with two keys were refused: %v", err)
	}
}

// THE RECORD CANNOT HOLD A PRIVATE KEY, structurally: SegmentMember has exactly
// one key field and it is the public half. This is the guarantee that matters,
// because a private key has the SAME SHAPE as a public one (wgkey's own test
// says so) and no validator can tell them apart. If a field named for the
// private half ever appears here, this fails and somebody has to explain it.
func TestASegmentMemberHasNowhereToPutAPrivateKey(t *testing.T) {
	for _, field := range structFields(SegmentMember{}) {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "priv") || strings.Contains(lower, "secret") {
			t.Errorf("SegmentMember has a field %q — hz holds the public half and only the public half", field)
		}
	}
	for _, field := range structFields(SegmentMemberPatch{}) {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "priv") || strings.Contains(lower, "secret") {
			t.Errorf("SegmentMemberPatch has a field %q — nothing may write a private key onto a member", field)
		}
	}
}

func strPtr(s string) *string { return &s }

// structFields is every field name on a struct, for the structural guards
// above: a rule about what a record may NOT hold has to be checked against the
// type, not against one instance of it.
func structFields(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}
