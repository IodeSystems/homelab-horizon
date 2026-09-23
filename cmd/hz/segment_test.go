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
