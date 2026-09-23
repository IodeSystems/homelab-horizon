package main

import (
	"strings"
	"testing"

	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz segment` as an operator meets it, over the same stub `hz machine` is
// tested against: a REAL config, the REAL writers, and a real Save at every
// write.

// The walkthrough plan/upstream-and-promotion.md §5 asks for, typed out: an
// iodesystems segment with the gateway as its hub, and redline-prod-hz on it as
// a CLIENT. Before the record, the last of those was a label and could not be
// said at all.
func TestSegmentAddShowAndList(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)

	if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gw-1", "redline-prod-hz"} {
		if err := runMachine(c, []string{"add", m, "--segment", "iode-net"}); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() {
		if err := runSegment(c, []string{"add", "iode-net",
			"--project", "iodesystems", "--cidr", "10.42.0.0/24", "--interface", "wg-iode",
			"--member", "machine=gw-1,address=10.42.0.1,hub=true,endpoint=hz.example.com:51820",
			"--member", "machine=redline-prod-hz,address=10.42.0.2",
		}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"Declared segment iode-net", "iodesystems", "10.42.0.0/24", "wg-iode",
		"HUB", "gw-1", "redline-prod-hz", "hz.example.com:51820",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("add did not report %q:\n%s", want, out)
		}
	}
	// The peer set is printed, because "who does this box talk to" is the
	// question a membership exists to answer.
	if !strings.Contains(out, "peers: gw-1") {
		t.Fatalf("the client's peers are not shown:\n%s", out)
	}
	// And an absent public key says it is absent rather than showing blank.
	if !strings.Contains(out, "public key — (none yet") {
		t.Fatalf("a missing public key is rendered as an empty field:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"iode-net", "iodesystems", "10.42.0.0/24", "wg-iode", "gw-1", "hub and spoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the listing does not mention %q:\n%s", want, out)
		}
	}

	// A membership declared after the segment is UNADDRESSED, and the listing
	// says so rather than reporting a smaller segment.
	if err := runMachine(c, []string{"add", "late-box", "--segment", "iode-net"}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"show", "iode-net"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"UNADDRESSED", "late-box", "no address"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show does not report the unaddressed membership (%q):\n%s", want, out)
		}
	}
	// A segment has no posture and no version, and says so rather than leaving
	// a reader to wonder where they went — the convention `hz machine show`
	// sets for a machine's missing project.
	if !strings.Contains(out, "posture") || !strings.Contains(out, "a segment is a network") {
		t.Fatalf("show does not state what a segment is not:\n%s", out)
	}
}

// An empty listing says what the emptiness MEANS — every membership is a label
// — rather than printing nothing and leaving that to be inferred.
func TestSegmentListSaysWhatNoSegmentsMeans(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	out := captureStdout(t, func() {
		if err := runSegment(c, []string{"ls"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"No segments declared", "LABEL", "hz segment add"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the empty listing does not say %q:\n%s", want, out)
		}
	}
}

// rm is a DRY RUN without --confirm, refuses while a machine is a member and
// names it, and --cascade lists the memberships it will drop before dropping
// them. The contract `hz project rm` and `hz machine rm` already set.
func TestSegmentRmIsADryRunAndRefusesWithMembers(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
		t.Fatal(err)
	}
	if err := runMachine(c, []string{"add", "gw-1", "--segment", "iode-net"}); err != nil {
		t.Fatal(err)
	}
	if err := runSegment(c, []string{"add", "iode-net",
		"--project", "iodesystems", "--cidr", "10.42.0.0/24", "--interface", "wg-iode",
		"--member", "machine=gw-1,address=10.42.0.1,hub=true"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		err := runSegment(c, []string{"rm", "iode-net", "--confirm"})
		if err == nil {
			t.Fatal("a confirmed removal was not refused by the member")
		}
	})
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "gw-1") {
		t.Fatalf("the refusal does not name the member:\n%s", out)
	}
	if _, ok := s.cfg.FindSegment("iode-net"); !ok {
		t.Fatal("a refused removal removed the segment anyway")
	}

	// Cascade, no confirm: the dry run lists what it would take and writes
	// nothing.
	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"rm", "iode-net", "--cascade"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "DROPPED") {
		t.Fatalf("the dry run does not say what cascade would do:\n%s", out)
	}
	if _, ok := s.cfg.FindSegment("iode-net"); !ok {
		t.Fatal("a dry run removed the segment")
	}

	// Cascade + confirm: gone, and the machine survives in no segment.
	if err := runSegment(c, []string{"rm", "iode-net", "--cascade", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.cfg.FindSegment("iode-net"); ok {
		t.Fatal("the segment survived its own removal")
	}
	m, _ := s.cfg.FindMachine("gw-1")
	if len(m.Segments) != 0 {
		t.Fatalf("cascade left gw-1 naming %v", m.Segments)
	}
}

// --member is key=value so a four-field member stays readable, and so a public
// key's base64 padding survives: the value is split on the FIRST '=' only.
func TestMemberSpecParsing(t *testing.T) {
	m, err := parseMemberSpec("machine=gw-1,address=10.42.0.1,hub=true,endpoint=hz.example.com:51820,key=abc+/def=")
	if err != nil {
		t.Fatal(err)
	}
	if m.Machine != "gw-1" || m.Address != "10.42.0.1" || !m.Hub {
		t.Fatalf("parsed %+v", m)
	}
	if m.Endpoint != "hz.example.com:51820" {
		t.Fatalf("a host:port endpoint did not survive: %q", m.Endpoint)
	}
	if m.PublicKey != "abc+/def=" {
		t.Fatalf("base64 padding did not survive: %q", m.PublicKey)
	}

	for _, bad := range []string{
		"address=10.42.0.1",      // no machine
		"machine=gw-1",           // no address
		"machine=gw-1,rubbish=x", // unknown field
		"machine=gw-1,10.42.0.1", // not key=value
	} {
		if _, err := parseMemberSpec(bad); err == nil {
			t.Fatalf("--member %q was accepted", bad)
		}
	}
}

// THE GAP CLOSED, as an operator meets it. Before `set`, addressing a
// membership on a segment that already existed meant `rm --cascade` and
// re-declaring the whole segment — a destructive round trip for a routine edit.
func TestSegmentSetAddressesAndReAddressesAMember(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
		t.Fatal(err)
	}
	if err := runMachine(c, []string{"add", "gw-1", "--segment", "iode-net"}); err != nil {
		t.Fatal(err)
	}
	if err := runSegment(c, []string{"add", "iode-net",
		"--project", "iodesystems", "--cidr", "10.42.0.0/24", "--interface", "wg-iode",
		"--member", "machine=gw-1,address=10.42.0.1,hub=true,endpoint=hz.example.com:51820"}); err != nil {
		t.Fatal(err)
	}
	if err := runMachine(c, []string{"add", "late-box", "--segment", "iode-net"}); err != nil {
		t.Fatal(err)
	}

	// `show` points at the command that fixes it, rather than saying there is
	// none — which is what it used to say.
	out := captureStdout(t, func() {
		if err := runSegment(c, []string{"show", "iode-net"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "hz segment set iode-net --member machine=late-box,address=") {
		t.Fatalf("show does not name the command that addresses a membership:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"set", "iode-net",
			"--member", "machine=late-box,address=10.42.0.7"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Changes:", "10.42.0.7", "Set segment iode-net", "peers: gw-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the set does not report %q:\n%s", want, out)
		}
	}
	seg, _ := s.cfg.FindSegment("iode-net")
	mem, ok := seg.Member("late-box")
	if !ok || mem.Address != "10.42.0.7" {
		t.Fatalf("late-box is %+v", mem)
	}

	// RE-ADDRESSING KEEPS WHAT IT WAS NOT ASKED TO CHANGE: the hub's endpoint
	// survives a command that never mentions it, and a key survives a move.
	if err := runSegment(c, []string{"set", "iode-net", "--member", "machine=late-box,key=abc+/def="}); err != nil {
		t.Fatal(err)
	}
	if err := runSegment(c, []string{"set", "iode-net", "--member", "machine=late-box,address=10.42.0.8"}); err != nil {
		t.Fatal(err)
	}
	seg, _ = s.cfg.FindSegment("iode-net")
	mem, _ = seg.Member("late-box")
	if mem.Address != "10.42.0.8" || mem.PublicKey != "abc+/def=" {
		t.Fatalf("re-addressing dropped something: %+v", mem)
	}
	hub, _ := seg.Member("gw-1")
	if hub.Endpoint != "hz.example.com:51820" || !hub.Hub {
		t.Fatalf("the hub was disturbed: %+v", hub)
	}

	// --unaddress leaves the machine IN the segment, unaddressed.
	if err := runSegment(c, []string{"set", "iode-net", "--unaddress", "late-box"}); err != nil {
		t.Fatal(err)
	}
	seg, _ = s.cfg.FindSegment("iode-net")
	if _, addressed := seg.Member("late-box"); addressed {
		t.Fatal("late-box is still addressed")
	}
	m, _ := s.cfg.FindMachine("late-box")
	if len(m.Segments) != 1 {
		t.Fatalf("unaddressing dropped the membership too: %+v", m)
	}

	// An empty patch is refused rather than reported as a successful no-op.
	if err := runSegment(c, []string{"set", "iode-net"}); err == nil {
		t.Fatal("an empty set reported success")
	}
}

// Moving the hub PRINTS the rewiring. Peers is derived hub and spoke, so it is
// the one change here that would otherwise happen entirely off-screen.
func TestSegmentSetPrintsTheHubRewiring(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gw-1", "gw-2", "spoke"} {
		if err := runMachine(c, []string{"add", m, "--segment", "iode-net"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSegment(c, []string{"add", "iode-net",
		"--project", "iodesystems", "--cidr", "10.42.0.0/24", "--interface", "wg-iode",
		"--member", "machine=gw-1,address=10.42.0.1,hub=true",
		"--member", "machine=gw-2,address=10.42.0.2",
		"--member", "machine=spoke,address=10.42.0.3"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runSegment(c, []string{"set", "iode-net", "--hub", "gw-2"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"HUB MOVES: gw-1 → gw-2", "topology change", "PEERS NOW", "WAS",
		"gw-1", "spoke",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the hub move does not report %q:\n%s", want, out)
		}
	}
	seg, _ := s.cfg.FindSegment("iode-net")
	h, ok := seg.Hub()
	if !ok || h.Machine != "gw-2" {
		t.Fatalf("the hub is %+v", h)
	}

	// hub= inside --member is refused and points at --hub, rather than being a
	// second way to say the same thing.
	if _, err := parseMemberSetSpec("machine=gw-1,hub=true"); err == nil {
		t.Fatal("hub= was accepted inside a set --member")
	}
}

// A range that would strand a member is REFUSED and names each address; cascade
// is a dry run until --confirm, and unaddresses rather than renumbering. The
// discipline `hz segment rm` already uses, on the one field that can invalidate
// a record the operator did not name.
func TestSegmentSetRefusesARangeThatStrandsMembers(t *testing.T) {
	s := newDeclareStub(t, &hzconfig.Config{})
	c := s.start(t)
	if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gw-1", "spoke"} {
		if err := runMachine(c, []string{"add", m, "--segment", "iode-net"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSegment(c, []string{"add", "iode-net",
		"--project", "iodesystems", "--cidr", "10.42.0.0/24", "--interface", "wg-iode",
		"--member", "machine=gw-1,address=10.42.0.1,hub=true",
		"--member", "machine=spoke,address=10.42.0.2"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		err := runSegment(c, []string{"set", "iode-net", "--cidr", "10.99.0.0/24", "--confirm"})
		if err == nil {
			t.Fatal("a confirmed range change was not refused by the stranded members")
		}
	})
	for _, want := range []string{"REFUSED", "gw-1", "spoke", "10.42.0.1", "outside the new range", "--cascade"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the refusal does not report %q:\n%s", want, out)
		}
	}
	seg, _ := s.cfg.FindSegment("iode-net")
	if seg.CIDR != "10.42.0.0/24" {
		t.Fatal("a refused range change was written anyway")
	}

	// Cascade, no confirm: a dry run that names who it would unaddress.
	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"set", "iode-net", "--cidr", "10.99.0.0/24", "--cascade"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"UNADDRESS", "Dry run", "--confirm"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dry run does not report %q:\n%s", want, out)
		}
	}
	seg, _ = s.cfg.FindSegment("iode-net")
	if seg.CIDR != "10.42.0.0/24" {
		t.Fatal("a dry run wrote the range")
	}

	// The move an operator actually wants: the range and the addresses in ONE
	// command, no cascade needed because nobody is stranded.
	out = captureStdout(t, func() {
		if err := runSegment(c, []string{"set", "iode-net", "--cidr", "10.99.0.0/24",
			"--member", "machine=gw-1,address=10.99.0.1",
			"--member", "machine=spoke,address=10.99.0.2"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "Dry run") || !strings.Contains(out, "Set segment iode-net") {
		t.Fatalf("a renumber in one command did not write:\n%s", out)
	}
	seg, _ = s.cfg.FindSegment("iode-net")
	if seg.CIDR != "10.99.0.0/24" || len(seg.Members) != 2 {
		t.Fatalf("the segment reads %+v", seg)
	}
	hub, _ := seg.Member("gw-1")
	if hub.Address != "10.99.0.1" || !hub.Hub {
		t.Fatalf("the hub reads %+v", hub)
	}
}

// `segment set --member` is a PATCH, so an absent key means "leave it alone"
// and a present-but-empty one means "clear it". That is what makes re-addressing
// safe, and it is why this parser is not parseMemberSpec.
func TestMemberSetSpecParsing(t *testing.T) {
	m, err := parseMemberSetSpec("machine=gw-1,address=10.42.0.1,endpoint=hz.example.com:51820,key=abc+/def=")
	if err != nil {
		t.Fatal(err)
	}
	if m.Machine != "gw-1" || m.Address == nil || *m.Address != "10.42.0.1" {
		t.Fatalf("parsed %+v", m)
	}
	if m.Endpoint == nil || *m.Endpoint != "hz.example.com:51820" {
		t.Fatalf("a host:port endpoint did not survive: %+v", m.Endpoint)
	}
	if m.PublicKey == nil || *m.PublicKey != "abc+/def=" {
		t.Fatalf("base64 padding did not survive: %+v", m.PublicKey)
	}

	// Absent is nil — "leave it alone" — rather than an empty string that would
	// clear the field the operator never mentioned.
	m, err = parseMemberSetSpec("machine=gw-1,address=10.42.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if m.PublicKey != nil || m.Endpoint != nil {
		t.Fatalf("an absent key was read as a clear: %+v", m)
	}
	// Present and empty IS a clear.
	m, err = parseMemberSetSpec("machine=gw-1,key=")
	if err != nil {
		t.Fatal(err)
	}
	if m.PublicKey == nil || *m.PublicKey != "" {
		t.Fatalf("an empty key was not read as a clear: %+v", m)
	}

	for _, bad := range []string{
		"address=10.42.0.1",      // no machine
		"machine=gw-1,rubbish=x", // unknown field
		"machine=gw-1,10.42.0.1", // not key=value
		"machine=gw-1,hub=true",  // the hub is --hub
	} {
		if _, err := parseMemberSetSpec(bad); err == nil {
			t.Fatalf("--member %q was accepted", bad)
		}
	}
}
