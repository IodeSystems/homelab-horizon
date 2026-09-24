package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// What a membership MEANS, once a Segment record answers to the name.
//
// projection_test.go runs §5's estate, which declares no segments at all and is
// the state the projection has always covered: every membership is a label and
// says so. This file runs the same estate ADDRESSED — the segments exist, the
// machines are members of them, and hz resolves the interface, the address and
// the peer set it could not before.
//
// The thing these tests exist to hold is the part resolution does NOT close.
// A peer with no key is a peer hz can name and cannot peer with, and a `peers`
// list that reads as a WireGuard configuration would be the founding bug in a
// new place.

// testWGKeys are real WireGuard public keys — the field is validated as a key
// now (config.ValidateSegments), so a fixture spelling one "solo-key" is a
// fixture describing a config hz would refuse to save. Fixed strings rather
// than minted per run so a failure prints the same thing twice.
var testWGKeys = []string{
	"8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=",
	"IeNDqihcCycgQ9s+UnsC4lShD7/9oHii3oOaBqZqjSY=",
	"UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko=",
	"TivW3BGw5YXBPSQi0UwrLhFO7tUsHP8NM2upYYvImAQ=",
}

// segmentedEstate is exampleEstate() with §2's networks declared and §3's
// machines addressed on them. gw-1 is the hub of all four, which is
// architecture.md's rule for a segment hz owns.
//
// Two rows are deliberately awkward:
//
//   - ci-1 names seg:storefront and is NOT addressed on it. That is the state
//     `hz machine add --segment` leaves, it is legal, and it is the one this
//     file most needs a machine in.
//   - seg:people has the hub and nothing else. A hub with no spoke peers with
//     nobody, and that is an ANSWER rather than an absence.
func segmentedEstate() *config.Config {
	cfg := exampleEstate()
	cfg.Segments = []config.Segment{
		{
			Name: "seg:intern", Project: "intern", CIDR: "10.10.1.0/24", Interface: "wg-intern",
			Members: []config.SegmentMember{
				{Machine: "gw-1", Address: "10.10.1.1", Hub: true, Endpoint: "gw.example.invalid:51820"},
				{Machine: "ci-1", Address: "10.10.1.20"},
			},
		},
		{
			Name: "seg:storefront", Project: "storefront", CIDR: "10.10.2.0/24", Interface: "wg-storefront",
			Members: []config.SegmentMember{
				{Machine: "gw-1", Address: "10.10.2.1", Hub: true, Endpoint: "gw.example.invalid:51821"},
				{Machine: "app-1", Address: "10.10.2.11"},
				{Machine: "app-2", Address: "10.10.2.12"},
				// ci-1 names this segment and is not addressed on it.
			},
		},
		{
			Name: "seg:analytics", Project: "analytics", CIDR: "10.10.3.0/24", Interface: "wg-analytics",
			Members: []config.SegmentMember{
				{Machine: "gw-1", Address: "10.10.3.1", Hub: true, Endpoint: "gw.example.invalid:51822"},
				{Machine: "an-1", Address: "10.10.3.9"},
			},
		},
		{
			Name: "seg:people", Project: "intern", CIDR: "10.10.4.0/24", Interface: "wg-people",
			Members: []config.SegmentMember{
				{Machine: "gw-1", Address: "10.10.4.1", Hub: true, Endpoint: "gw.example.invalid:51823"},
			},
		},
	}
	return cfg
}

// The fixture has to be a config hz would actually accept, or every test below
// is asserting against a shape the validator would have refused.
func TestTheSegmentedEstateIsAConfigHZWouldAccept(t *testing.T) {
	cfg := segmentedEstate()
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("the fixture estate is not a legal config: %v", err)
	}
	if err := cfg.ValidateMachines(); err != nil {
		t.Fatalf("the fixture estate's machines do not validate: %v", err)
	}
}

func segmentNamed(mc MachineConfig, name string) (Segment, bool) {
	for _, s := range mc.Segments {
		if s.Name == name {
			return s, true
		}
	}
	return Segment{}, false
}

// gapsFor is every gap on a section, not just the first: a machine can be
// resolved on one segment and unaddressed on another, and both are said.
func gapsFor(mc MachineConfig, section string) []string {
	var out []string
	for _, g := range mc.Unresolved {
		if g.Section == section {
			out = append(out, g.Why)
		}
	}
	return out
}

func gapMentioning(mc MachineConfig, section, substr string) string {
	for _, why := range gapsFor(mc, section) {
		if strings.Contains(why, substr) {
			return why
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// §5's segment, answered. app-1 is a spoke of seg:storefront: the interface is
// the SEGMENT's, the address is the member entry's, and the peer set is the
// hub because hub and spoke says so.
func TestAnAddressedMembershipResolvesToAnInterfaceAnAddressAndPeers(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "app-1")

	seg, ok := segmentNamed(mc, "seg:storefront")
	if !ok {
		t.Fatalf("segments = %+v, want the one membership app-1 declares", mc.Segments)
	}
	if !seg.Resolved {
		t.Fatalf("seg:storefront came back unresolved: %+v. The record answers to the name and addresses app-1 on it.", seg)
	}
	if seg.Interface != "wg-storefront" {
		t.Errorf("interface = %q, want the segment's wg-storefront", seg.Interface)
	}
	if seg.Address != "10.10.2.11" {
		t.Errorf("address = %q, want app-1's member address", seg.Address)
	}
	if strings.Join(seg.Peers, ",") != "gw-1" {
		t.Errorf("peers = %v, want the hub alone — a spoke peers with the hub and not with the other spokes", seg.Peers)
	}
}

// PEERS ARE DERIVED AND THE DERIVATION IS config.PeersOf. The hub peers with
// every spoke; a spoke peers with the hub and NOT with its fellow spokes. A
// stored peer list would let this disagree with the membership it is a view of,
// which is why there is not one.
func TestPeersAreHubAndSpokeDerivedFromTheMembership(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}

	hub := mustProject(t, g, "gw-1")
	seg, ok := segmentNamed(hub, "seg:storefront")
	if !ok || !seg.Resolved {
		t.Fatalf("the hub's seg:storefront did not resolve: %+v", seg)
	}
	if strings.Join(seg.Peers, ",") != "app-1,app-2" {
		t.Errorf("the hub peers with %v, want both spokes, sorted, and not itself", seg.Peers)
	}

	spoke := mustProject(t, g, "app-2")
	seg, _ = segmentNamed(spoke, "seg:storefront")
	if strings.Join(seg.Peers, ",") != "gw-1" {
		t.Errorf("app-2 peers with %v, want the hub alone — app-1 is a fellow spoke, not a peer", seg.Peers)
	}

	// The agreement between the two is the property a stored list would break.
	if !contains(seg.Peers, "gw-1") {
		t.Fatal("a spoke that does not peer with its hub is a spoke with no tunnel")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A HUB WITH NO SPOKE PEERS WITH NOBODY, AND THAT IS AN ANSWER. seg:people has
// one member. The membership resolves, `peers` is empty because it IS empty,
// and hz must not raise a gap about it — a gap here would say "hz does not
// know" about something hz worked out.
func TestAHubWithNoSpokesResolvesToAnEmptyPeerSet(t *testing.T) {
	cfg := segmentedEstate()
	// A machine whose only segment is the empty one, so the assertion is about
	// this membership rather than about gw-1's other three.
	cfg.Machines = append(cfg.Machines, config.Machine{Name: "solo-1", Segments: []string{"seg:solo"}})
	cfg.Segments = append(cfg.Segments, config.Segment{
		Name: "seg:solo", Project: "intern", CIDR: "10.10.9.0/24", Interface: "wg-solo",
		Members: []config.SegmentMember{
			{Machine: "solo-1", Address: "10.10.9.1", Hub: true, Endpoint: "solo.example.invalid:51820", PublicKey: testWGKeys[0]},
		},
	})
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	mc := mustProject(t, Global{Config: cfg}, "solo-1")
	seg, ok := segmentNamed(mc, "seg:solo")
	if !ok || !seg.Resolved {
		t.Fatalf("seg:solo did not resolve: %+v", mc.Segments)
	}
	if len(seg.Peers) != 0 {
		t.Errorf("peers = %v, want none: the hub is the only member", seg.Peers)
	}
	if len(mc.Segments) != 1 {
		t.Fatalf("segments = %+v", mc.Segments)
	}
	if gs := gapsFor(mc, SectionSegments); len(gs) != 0 {
		t.Errorf("a fully resolved membership raised %d segment gap(s): %v.\n"+
			"An empty peer set hz COMPUTED must not be reported as one hz does not know.", len(gs), gs)
	}
	if len(mc.Hosts) != 0 {
		t.Errorf("hosts = %+v, want none: there is no peer to write a line for", mc.Hosts)
	}
	if gs := gapsFor(mc, SectionHosts); len(gs) != 0 {
		t.Errorf("empty hosts carries %d gap(s): %v. hz worked out that there is nothing to write; that is an opinion, not an absence.", len(gs), gs)
	}
}

// ---------------------------------------------------------------------------
// The two ways a membership stays a label
// ---------------------------------------------------------------------------

// AN UNADDRESSED MEMBERSHIP IS LEGAL. ci-1 names seg:storefront and no member
// entry addresses it. It is in the segment; hz cannot say where. So: the
// segment's interface IS known, the address and peers are not, Resolved is
// false, and the gap says the state is the one `hz machine add --segment`
// leaves rather than reading as a broken record.
func TestAnUnaddressedMembershipIsAGapAndNotAFailure(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "ci-1")

	seg, ok := segmentNamed(mc, "seg:storefront")
	if !ok {
		t.Fatalf("the unaddressed membership was dropped from the projection: %+v", mc.Segments)
	}
	if seg.Resolved {
		t.Error("an unaddressed membership reported itself resolved; nothing says where ci-1 is on seg:storefront")
	}
	if seg.Interface != "wg-storefront" {
		t.Errorf("interface = %q, want the SEGMENT's — the interface is the segment's property and is known without a member entry", seg.Interface)
	}
	if seg.Address != "" || len(seg.Peers) != 0 {
		t.Errorf("hz invented detail for an unaddressed member: %+v", seg)
	}

	why := gapMentioning(mc, SectionSegments, "no address")
	if why == "" {
		t.Fatalf("no gap explains the unaddressed membership; segment gaps = %v", gapsFor(mc, SectionSegments))
	}
	if !strings.Contains(why, "LEGAL") {
		t.Errorf("the gap does not say the state is legal, so it reads as a broken record: %q", why)
	}
	if !strings.Contains(why, "hz segment set") && !strings.Contains(why, "--member") {
		t.Errorf("the gap does not name what would address it: %q", why)
	}

	// The other membership still resolved: one unaddressed segment must not
	// take the machine's whole segment section down with it.
	other, _ := segmentNamed(mc, "seg:intern")
	if !other.Resolved || other.Address != "10.10.1.20" {
		t.Errorf("ci-1's addressed membership did not resolve beside the unaddressed one: %+v", other)
	}
}

// A NAME NO RECORD ANSWERS TO is the other way to stay a label, and it is a
// different gap with a different fix. One machine can be in both states at
// once and both have to be said.
func TestANameNoRecordAnswersToIsItsOwnGap(t *testing.T) {
	cfg := segmentedEstate()
	for i, m := range cfg.Machines {
		if m.Name == "app-1" {
			cfg.Machines[i].Segments = []string{"seg:storefront", "seg:typo"}
			cfg.Machines[i].Note = "deliberately names a segment nothing declares"
		}
	}

	mc := mustProject(t, Global{Config: cfg}, "app-1")
	typo, ok := segmentNamed(mc, "seg:typo")
	if !ok {
		t.Fatalf("the unresolvable membership was dropped: %+v", mc.Segments)
	}
	if typo.Resolved || typo.Interface != "" || typo.Address != "" {
		t.Errorf("hz answered for a segment no record declares: %+v", typo)
	}
	if real, _ := segmentNamed(mc, "seg:storefront"); !real.Resolved {
		t.Error("a typo in one membership unresolved the one that was fine")
	}

	why := gapMentioning(mc, SectionSegments, "no segment record answers to")
	if why == "" {
		t.Fatalf("no gap for the unresolvable name; segment gaps = %v", gapsFor(mc, SectionSegments))
	}
	if !strings.Contains(why, "hz segment add") {
		t.Errorf("the gap does not name what would declare it: %q", why)
	}
	// The two states are different answers and must not collapse into one gap.
	if strings.Contains(why, "has no address") {
		t.Errorf("the missing-record gap and the unaddressed gap are the same sentence: %q", why)
	}
}

// ---------------------------------------------------------------------------
// The limit resolution does not remove
// ---------------------------------------------------------------------------

// RESOLVED IS NOT PEERABLE. Nothing puts a public key on a member — a box mints
// its key at enrolment and `hz-agent enroll` does not send one — so hz can name
// a peer and address it and cannot emit a WireGuard `[Peer]` block for it. A
// `peers` list that stood alone would read as a tunnel that exists.
func TestAResolvedMembershipStillCannotPeerWithoutAPublicKey(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "app-1")

	seg, _ := segmentNamed(mc, "seg:storefront")
	if !seg.Resolved {
		t.Fatal("precondition: the membership must resolve for this to be the interesting case")
	}

	why := gapMentioning(mc, SectionSegments, "[Peer]")
	if why == "" {
		t.Fatalf("a resolved membership whose peers have no keys raised no gap; segment gaps = %v.\n"+
			"Without it `peers: [gw-1]` reads as a usable tunnel config, which it is not.", gapsFor(mc, SectionSegments))
	}
	if !strings.Contains(why, "public key") {
		t.Errorf("the gap does not name what is missing: %q", why)
	}
	if !strings.Contains(why, "gw-1") {
		t.Errorf("the gap does not name WHICH peer hz cannot peer with: %q", why)
	}
	if !strings.Contains(why, "enroll") {
		t.Errorf("the gap does not say why no key exists or what would supply one: %q", why)
	}
}

// And the gap goes away when the record answers: it is a statement about THIS
// estate, not a sentence stapled to every projection.
func TestTheKeylessGapClearsWhenEveryPeerHasAKey(t *testing.T) {
	cfg := segmentedEstate()
	// REAL keys, and the fixture is checked against the validator: a config hz
	// would refuse to save is not evidence about what hz projects. Distinct per
	// (segment, machine), which is the property the whole per-interface design
	// exists for — one key reused everywhere is refused.
	next := 0
	for i := range cfg.Segments {
		for j := range cfg.Segments[i].Members {
			cfg.Segments[i].Members[j].PublicKey = testWGKeys[next%len(testWGKeys)]
			next++
		}
	}
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mc := mustProject(t, Global{Config: cfg}, "app-1")
	if why := gapMentioning(mc, SectionSegments, "[Peer]"); why != "" {
		t.Errorf("every peer is keyed and hz still says it cannot peer: %q", why)
	}
}

// A SPOKE DIALS THE HUB. A hub with no endpoint is a tunnel that cannot come
// up, and that is a different missing field from the key — so it is a different
// gap, with a different fix.
func TestASpokeWithNothingToDialSaysSo(t *testing.T) {
	cfg := segmentedEstate()
	for i := range cfg.Segments {
		if cfg.Segments[i].Name != "seg:storefront" {
			continue
		}
		for j := range cfg.Segments[i].Members {
			if cfg.Segments[i].Members[j].Hub {
				cfg.Segments[i].Members[j].Endpoint = ""
			}
		}
	}

	spoke := mustProject(t, Global{Config: cfg}, "app-1")
	why := gapMentioning(spoke, SectionSegments, "no endpoint")
	if why == "" {
		t.Fatalf("a spoke whose hub has no endpoint raised no gap; segment gaps = %v", gapsFor(spoke, SectionSegments))
	}
	if !strings.Contains(why, "seg:storefront/gw-1") {
		t.Errorf("the gap does not name the hub with no endpoint: %q", why)
	}

	// The HUB itself is not undialable: it answers, it does not dial. A gap on
	// gw-1 here would be hz reporting a problem the hub does not have.
	hub := mustProject(t, Global{Config: cfg}, "gw-1")
	if why := gapMentioning(hub, SectionSegments, "no endpoint"); why != "" {
		t.Errorf("the hub was told its own endpoint is missing: %q. A hub is dialled, it does not dial.", why)
	}
}

// A segment with members and no hub is refused by ValidateSegments, so it can
// only be seen on a config nobody saved — which the projection is still handed
// (a caller builds one in memory). Peers is then empty because hz does not
// know, not because there are none, and that difference is the whole file.
func TestASegmentWithNoHubLeavesPeersUnknownRatherThanEmpty(t *testing.T) {
	cfg := segmentedEstate()
	for i := range cfg.Segments {
		if cfg.Segments[i].Name != "seg:storefront" {
			continue
		}
		for j := range cfg.Segments[i].Members {
			cfg.Segments[i].Members[j].Hub = false
		}
	}
	if err := cfg.ValidateSegments(); err == nil {
		t.Fatal("precondition: a hubless segment must be a config hz refuses, or this test is about a supported state")
	}

	mc := mustProject(t, Global{Config: cfg}, "app-1")
	seg, _ := segmentNamed(mc, "seg:storefront")
	if len(seg.Peers) != 0 {
		t.Fatalf("peers = %v with no hub to peer with", seg.Peers)
	}
	if why := gapMentioning(mc, SectionSegments, "no hub"); why == "" {
		t.Fatalf("an empty peer set with no hub raised no gap; segment gaps = %v.\n"+
			"It then reads identically to seg:people's hub, which peers with nobody and knows it.", gapsFor(mc, SectionSegments))
	}
}

// ---------------------------------------------------------------------------
// /etc/hosts
// ---------------------------------------------------------------------------

// §5's one host entry, computed. It is the gateway's address ON THAT SEGMENT,
// which is what SegmentMember.Address holds — the exact record whose absence
// the old hosts gap named.
func TestHostsCarryEveryPeersAddressOnTheSegment(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "app-1")

	if len(mc.Hosts) != 1 || mc.Hosts[0] != (HostEntry{Name: "gw-1", Address: "10.10.2.1"}) {
		t.Fatalf("hosts = %+v, want the hub's address on seg:storefront", mc.Hosts)
	}

	// The stale gap is gone: hz is no longer saying it holds no per-segment
	// address while printing one.
	for _, why := range gapsFor(mc, SectionHosts) {
		if strings.Contains(why, "no record holds a per-segment address") {
			t.Errorf("hosts is populated and still carries the gap that says it cannot be: %q", why)
		}
	}

	// The hub's own list is every spoke it terminates, across all four of its
	// segments.
	hub := mustProject(t, g, "gw-1")
	got := map[string]string{}
	for _, h := range hub.Hosts {
		got[h.Name] = h.Address
	}
	want := map[string]string{"ci-1": "10.10.1.20", "app-1": "10.10.2.11", "app-2": "10.10.2.12", "an-1": "10.10.3.9"}
	if len(got) != len(want) {
		t.Fatalf("the hub's hosts = %+v, want one line per spoke: %v", hub.Hosts, want)
	}
	for name, addr := range want {
		if got[name] != addr {
			t.Errorf("hosts[%s] = %q, want %q", name, got[name], addr)
		}
	}
}

// THE ADDRESS IS A RECORD; THE NAME IS THE BEST hz HAS. Nothing says what a
// machine answers to ON a segment, so the entry is named by the peer's machine
// name and hz says that is what it did rather than letting a convention it
// invented pass as a record.
func TestHostsSayWhichHalfOfTheEntryIsAGuess(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "app-1")

	why := gapMentioning(mc, SectionHosts, "MACHINE name")
	if why == "" {
		t.Fatalf("hosts are populated with no word on where the NAME came from; hosts gaps = %v", gapsFor(mc, SectionHosts))
	}
	if !strings.Contains(why, "no record says what a machine answers to on a segment") {
		t.Errorf("the gap does not name the missing record: %q", why)
	}
}

// A MEMBERSHIP HZ COULD NOT RESOLVE IS A LINE MISSING FROM /etc/hosts, and the
// difference between "this machine wants no more entries" and "hz could not
// work out the rest" has to survive into the hosts section too.
func TestAnUnresolvedMembershipLeavesAHostsGapBeside(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	mc := mustProject(t, g, "ci-1")

	// ci-1 resolved on seg:intern, so it has the hub's line there...
	if len(mc.Hosts) != 1 || mc.Hosts[0].Name != "gw-1" || mc.Hosts[0].Address != "10.10.1.1" {
		t.Fatalf("hosts = %+v, want the seg:intern hub line", mc.Hosts)
	}
	// ...and nothing for seg:storefront, which it is unaddressed on.
	why := gapMentioning(mc, SectionHosts, "seg:storefront")
	if why == "" {
		t.Fatalf("no hosts gap for the unresolved membership; hosts gaps = %v.\n"+
			"A populated-but-incomplete list with no gap reads as complete.", gapsFor(mc, SectionHosts))
	}
}

// A PEER AT TWO ADDRESSES HAS NO SINGLE /etc/hosts ANSWER. ci-1 addressed on
// both of its segments reaches gw-1 at two addresses; /etc/hosts resolves a
// name to the first match. hz emits both lines because both are true and says
// it cannot choose, because nothing records which segment this machine should
// reach that peer over.
func TestAPeerReachableAtTwoAddressesIsReportedRatherThanPicked(t *testing.T) {
	cfg := segmentedEstate()
	for i := range cfg.Segments {
		if cfg.Segments[i].Name == "seg:storefront" {
			cfg.Segments[i].Members = append(cfg.Segments[i].Members,
				config.SegmentMember{Machine: "ci-1", Address: "10.10.2.30"})
		}
	}
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	mc := mustProject(t, Global{Config: cfg}, "ci-1")
	var addrs []string
	for _, h := range mc.Hosts {
		if h.Name == "gw-1" {
			addrs = append(addrs, h.Address)
		}
	}
	if len(addrs) != 2 {
		t.Fatalf("gw-1 appears %d time(s) in %+v, want both of its addresses — dropping one would be hz picking silently", len(addrs), mc.Hosts)
	}

	why := gapMentioning(mc, SectionHosts, "more than one")
	if why == "" {
		t.Fatalf("two addresses for one name and no gap; hosts gaps = %v", gapsFor(mc, SectionHosts))
	}
	if !strings.Contains(why, "10.10.1.1") || !strings.Contains(why, "10.10.2.1") {
		t.Errorf("the gap does not name the addresses it could not choose between: %q", why)
	}
}

// ---------------------------------------------------------------------------
// The wire
// ---------------------------------------------------------------------------

// The honesty has to survive marshalling, resolved as well as not: a resolved
// membership carries its three fields, `resolved` is present and true, and no
// section arrives as null.
func TestAResolvedSegmentCrossesTheWireWhole(t *testing.T) {
	g := Global{Config: segmentedEstate(), Instances: exampleInstances()}
	b, err := json.Marshal(mustProject(t, g, "app-1"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"resolved":true`,
		`"interface":"wg-storefront"`,
		`"address":"10.10.2.11"`,
		`"peers":["gw-1"]`,
		`{"name":"gw-1","address":"10.10.2.1"}`,
		`"unresolved":[`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("payload does not carry %s: %s", want, s)
		}
	}
	if strings.Contains(s, `:null`) {
		t.Fatalf("a null crossed the wire; every empty section must be an empty list: %s", s)
	}
}
