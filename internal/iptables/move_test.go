package iptables

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// THE OFFICE MOVE, SIMULATED — the drift this whole package exists for.
//
// The gateway is about to be moved to wireless, so its egress interface goes
// from enx00051b94b7cc (a USB ethernet adapter) to wlan0 and its LAN from
// 192.168.1.0/24 to 192.168.5.0/24. A stale MASQUERADE left bound to an
// interface that no longer exists IS the founding outage of this repo.
//
// plan/plan.md records that this exact transition was MEASURED across two
// branches — "the same 2028 bytes, correctly condemning stale
// nat|POSTROUTING|-o enx00051b94b7cc -j MASQUERADE" — but the measurement was
// run by hand and never committed, so nothing in the tree reproduced it. That
// is the gap this file closes: the heal for a move that has not happened yet
// is the thing least able to afford an uncommitted proof.
//
// It asserts on the iptables COMMANDS Reconcile issues, in order, against an
// expectation spelled out here by hand. Not on the classifier's verdict, and
// not on Report.Deleted: the ban hand-over learned twice that a guard phrased
// in terms of what is *allowed* stays green when the thing that should happen
// stops happening (privilege-audit.md §7 B — removing unbanIP's `-D` call, and
// then swapping its chain INPUT→FORWARD, both left the tree green). An
// independently written command list cannot do that.

const (
	movedFromIface = "enx00051b94b7cc"
	movedToIface   = "wlan0"
	movedFromCIDR  = "192.168.1.0/24"
	movedToCIDR    = "192.168.5.0/24"

	// The sprink WebTransport forward (test/integration/forwards_test.go): a
	// UDP port forward whose NAT and FORWARD rules are pinned to the egress
	// interface, so it moves with the gateway. Without a forward in the
	// scenario the HZ-* chains are never exercised and the move looks simpler
	// than it is.
	//
	// The backend is on the LAN and moves with it — the machines are the same
	// machines, re-addressed into the new subnet. What happens when one is NOT
	// re-addressed is its own test below, because it is not nothing.
	movedFromBackend = "192.168.1.76"
	movedToBackend   = "192.168.5.76"
)

// officeGateway is the box's rule set under one set of network coordinates:
// one lan-access peer, one UDP forward, no bans, nobody jailed.
func officeGateway(outIface, lanCIDR, backendIP string) Inputs {
	return Inputs{
		WGInterface: "wg0",
		OutIface:    outIface,
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     lanCIDR,
		Peers:       []PeerInput{{Name: "laptop", IP: "10.100.0.2"}},
		Profiles:    map[string]string{"laptop": "lan-access"},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
		Forwards: []ForwardInput{
			{Proto: "udp", Port: 4433, BackendIP: backendIP, BackendPort: 4433},
		},
	}
}

func beforeTheMove() Inputs {
	return officeGateway(movedFromIface, movedFromCIDR, movedFromBackend)
}

func afterTheMove() Inputs {
	return officeGateway(movedToIface, movedToCIDR, movedToBackend)
}

// movedConfig is hz's record of where the gateway WAS, which is the only thing
// StaleRules has to condemn the old interface's rules from.
func movedConfig() *config.Config {
	return &config.Config{
		WGInterface:    "wg0",
		VPNRange:       "10.100.0.0/24",
		LastLocalIface: movedFromIface,
		LastLanCIDR:    movedFromCIDR,
		VPNProfiles:    map[string]string{"laptop": "lan-access"},
	}
}

func movedPeers() []PeerInput {
	return []PeerInput{{Name: "laptop", IP: "10.100.0.2"}}
}

// TestTheMoveToWirelessCondemnsTheOldMasquerade is the classifier half: the
// rule bound to the interface that is about to stop existing reads STALE, which
// is the one class Reconcile deletes.
func TestTheMoveToWirelessCondemnsTheOldMasquerade(t *testing.T) {
	live := ExpectedRules(beforeTheMove())
	expected := ExpectedRules(afterTheMove())
	stale := StaleRules(movedConfig(), movedPeers(), "10.100.0.1", "8080")

	classified := Classify(live, expected, stale, nil)

	// THE INSTRUMENT FIRST. A live set that never carried the old MASQUERADE
	// would pass every assertion below for the wrong reason.
	oldMasq := "nat|POSTROUTING|-o " + movedFromIface + " -j MASQUERADE"
	newMasq := "nat|POSTROUTING|-o " + movedToIface + " -j MASQUERADE"
	if stateOf(t, classified, oldMasq) != StateStale {
		t.Fatalf("%s classified %q, want %q — nothing will delete it",
			oldMasq, stateOf(t, classified, oldMasq), StateStale)
	}
	if canonicals(live)[newMasq] {
		t.Fatal("the fixture box already NATs through wlan0; the move is not being simulated")
	}
	if !canonicals(expected)[newMasq] {
		t.Fatalf("hz does not want a MASQUERADE through %s after the move", movedToIface)
	}

	// The peer's LAN reach moves with the LAN, and the old one is condemned
	// rather than left as a hole into a network this box is no longer on.
	oldLan := "filter|WG-FORWARD|-s 10.100.0.2/32 -d " + movedFromCIDR + " -j ACCEPT"
	if stateOf(t, classified, oldLan) != StateStale {
		t.Errorf("%s classified %q, want %q", oldLan, stateOf(t, classified, oldLan), StateStale)
	}
	if !canonicals(expected)["filter|WG-FORWARD|-s 10.100.0.2/32 -d "+movedToCIDR+" -j ACCEPT"] {
		t.Errorf("hz does not want the peer to reach the new LAN %s", movedToCIDR)
	}

	// And the forward jumps survive the move. They are in the stale set
	// unconditionally (StaleRules), so a classifier that ranked stale above
	// expected would take the gateway's every port forward out on a move.
	for _, jump := range ForwardJumpRules() {
		if got := stateOf(t, classified, jump.Canonical()); got != StateExpected {
			t.Errorf("forward jump %s classified %q during the move, want %q", jump, got, StateExpected)
		}
	}
}

// TestTheMoveToWirelessIssuesExactlyTheseCommands is the heal itself, pinned.
//
// Every line below was written by reading what the gateway needs, not by
// capturing what the code produced. Order is part of the assertion: the old
// MASQUERADE has to come OFF before the new one goes on, because both are
// `nat POSTROUTING` inserts at position 1 and the surviving order decides which
// interface the box NATs through in the seconds between the two commands.
func TestTheMoveToWirelessIssuesExactlyTheseCommands(t *testing.T) {
	calls := recordIptables(t)
	live := ExpectedRules(beforeTheMove())
	expected := ExpectedRules(afterTheMove())
	stale := StaleRules(movedConfig(), movedPeers(), "10.100.0.1", "8080")

	report := Reconcile(live, expected, stale, nil, movedToIface, movedFromIface)
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}

	want := []string{
		// The owned chains are created before anything jumps to them.
		"-t filter -N WG-FORWARD",
		"-t filter -N WG-INPUT",
		"-t nat -N HZ-PREROUTING",
		"-t nat -N HZ-POSTROUTING",
		"-t filter -N HZ-FORWARD",

		// THE HEAL. The old interface's NAT comes off, the new one goes on.
		"-t nat -D POSTROUTING -o " + movedFromIface + " -j MASQUERADE",
		"-t nat -I POSTROUTING 1 -o " + movedToIface + " -j MASQUERADE",

		// WG-FORWARD is rebuilt whole because the peer's LAN reach moved. Order
		// inside it is load-bearing — the per-peer ACCEPTs precede the per-peer
		// DROP, and the catch-all DROP is last.
		"-t filter -N WG-FORWARD",
		"-t filter -F WG-FORWARD",
		"-t filter -A WG-FORWARD -s 10.100.0.2/32 -d 10.100.0.0/24 -j ACCEPT",
		"-t filter -A WG-FORWARD -s 10.100.0.2/32 -d " + movedToCIDR + " -j ACCEPT",
		"-t filter -A WG-FORWARD -s 10.100.0.2/32 -j DROP",
		"-t filter -A WG-FORWARD -j DROP",

		// The port forward moves twice over: its DNAT target is the backend's
		// new address, and its NAT/FORWARD bodies are pinned to the egress
		// interface. All three chains are rebuilt whole.
		//
		// HZ-PREROUTING is in this list because the BACKEND was re-addressed.
		// A move that changed only the interface would leave it alone — a DNAT
		// by destination port names no interface — and that is worth knowing:
		// the chain rebuilt here is not evidence that the interface change
		// reached it.
		"-t nat -N HZ-PREROUTING",
		"-t nat -F HZ-PREROUTING",
		"-t nat -A HZ-PREROUTING -p udp --dport 4433 -j DNAT --to-destination " + movedToBackend + ":4433",
		"-t nat -N HZ-POSTROUTING",
		"-t nat -F HZ-POSTROUTING",
		"-t nat -A HZ-POSTROUTING -d " + movedToBackend + "/32 -o " + movedToIface +
			" -p udp --dport 4433 -m conntrack --ctstate DNAT -j MASQUERADE",
		"-t filter -N HZ-FORWARD",
		"-t filter -F HZ-FORWARD",
		"-t filter -A HZ-FORWARD -d " + movedToBackend + "/32 -i " + movedToIface +
			" -p udp --dport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
		"-t filter -A HZ-FORWARD -s " + movedToBackend + "/32 -o " + movedToIface +
			" -p udp --sport 4433 -m conntrack --ctstate DNAT -j ACCEPT",
	}

	got := joined(*calls)
	if len(got) != len(want) {
		t.Fatalf("the move issued %d commands, want %d:\n got:\n  %s\nwant:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command %d:\n got  %s\nwant  %s\n\nfull sequence:\n  %s",
				i, got[i], want[i], strings.Join(got, "\n  "))
		}
	}

	// And nothing outside horizon's own chains was touched on the way. The
	// allowlist is expected PLUS stale, because a move's whole point is that
	// horizon edits a built-in chain with a rule it emitted under the PREVIOUS
	// coordinates — the delete above.
	assertOnlyHorizonCommands(t, *calls, builtinRules(append(append([]Rule(nil), expected...), stale...)))
}

// TestTheMoveNeedsNoPersistedInterfaceToHeal is axis 3 — the first-run
// bootstrap — on the same move.
//
// hz's record of where the box was is the only thing StaleRules works from, and
// it can be missing: a box upgraded from before the field existed, or one whose
// persist failed. Reconcile then INFERS the old interface from the live
// `-o X -j MASQUERADE` whose X is not the current default, and heals anyway.
//
// This matters for the move specifically. If the interface changes while hz is
// down, hz comes back up, detects wlan0, and has an old record only if the
// previous run wrote one.
func TestTheMoveNeedsNoPersistedInterfaceToHeal(t *testing.T) {
	calls := recordIptables(t)
	live := ExpectedRules(beforeTheMove())
	expected := ExpectedRules(afterTheMove())

	// No LastLocalIface, no LastLanCIDR: StaleRules returns only the forward
	// jumps, so the old MASQUERADE is stale to nobody until it is inferred.
	blank := &config.Config{WGInterface: "wg0"}
	stale := StaleRules(blank, movedPeers(), "10.100.0.1", "8080")
	for _, r := range stale {
		if strings.Contains(strings.Join(r.Args, " "), movedFromIface) {
			t.Fatalf("StaleRules named %s with nothing persisted; the inference is not what is being tested", movedFromIface)
		}
	}

	report := Reconcile(live, expected, stale, nil, movedToIface, "")
	if report.InferredOld != movedFromIface {
		t.Fatalf("Reconcile inferred %q as the old interface, want %q", report.InferredOld, movedFromIface)
	}
	del := "-t nat -D POSTROUTING -o " + movedFromIface + " -j MASQUERADE"
	add := "-t nat -I POSTROUTING 1 -o " + movedToIface + " -j MASQUERADE"
	seen := joined(*calls)
	di, ai := indexOfCommand(seen, del), indexOfCommand(seen, add)
	if di < 0 {
		t.Fatalf("the inferred-old heal never removed %s:\n  %s", movedFromIface, strings.Join(seen, "\n  "))
	}
	if ai < 0 {
		t.Fatalf("the inferred-old heal never installed the new MASQUERADE:\n  %s", strings.Join(seen, "\n  "))
	}
	if di > ai {
		t.Fatal("the new MASQUERADE was installed before the old one came off")
	}
}

// TestAMoveThatStrandsABackendRemovesItsForwardEntirely is a consequence of
// the move that nothing in the tree said out loud, found by writing the command
// list above by hand and having it disagree with the code.
//
// forwardRules is FAIL-CLOSED on a backend outside the LAN CIDR (see its
// header): it emits nothing for that forward — and because the jumps are
// emitted only while at least one forward survives, and the jumps are
// unconditionally stale, a move that re-addresses the LAN but leaves a backend
// on the old subnet takes out the gateway's port forwarding ENTIRELY. The
// HZ-* chains are flushed and deleted, the three jumps are removed from the
// built-in chains, and nothing logs an opinion about why.
//
// That is the right call — the rule could not work — but it is a cliff, and it
// is the second-most likely thing to go wrong on this move after the
// MASQUERADE. Pinned so it is a known behaviour rather than a surprise at the
// far end of an office move.
func TestAMoveThatStrandsABackendRemovesItsForwardEntirely(t *testing.T) {
	calls := recordIptables(t)
	live := ExpectedRules(beforeTheMove())
	// The LAN and the interface move; the backend does not.
	stranded := ExpectedRules(officeGateway(movedToIface, movedToCIDR, movedFromBackend))
	stale := StaleRules(movedConfig(), movedPeers(), "10.100.0.1", "8080")

	// THE INSTRUMENT FIRST: the box really did have forwards to lose.
	for _, jump := range ForwardJumpRules() {
		if !canonicals(live)[jump.Canonical()] {
			t.Fatalf("the fixture box has no %s; there is no forward to strand", jump)
		}
	}
	for _, r := range stranded {
		if strings.HasPrefix(r.Chain, "HZ-") || strings.Contains(strings.Join(r.Args, " "), "HZ-") {
			t.Fatalf("hz still wants forward rule %s for a backend off the LAN", r)
		}
	}

	Reconcile(live, stranded, stale, nil, movedToIface, movedFromIface)

	seen := joined(*calls)
	for _, want := range []string{
		"-t nat -D PREROUTING -m addrtype --dst-type LOCAL -j HZ-PREROUTING",
		"-t nat -D POSTROUTING -j HZ-POSTROUTING",
		"-t filter -D FORWARD -j HZ-FORWARD",
		"-t nat -X HZ-PREROUTING",
		"-t nat -X HZ-POSTROUTING",
		"-t filter -X HZ-FORWARD",
	} {
		if indexOfCommand(seen, want) < 0 {
			t.Errorf("a stranded backend did not produce %q; the fail-closed path has changed:\n  %s",
				want, strings.Join(seen, "\n  "))
		}
	}
	// The gateway's own NAT is still healed. Losing a forward must not cost
	// the box its route out.
	if indexOfCommand(seen, "-t nat -I POSTROUTING 1 -o "+movedToIface+" -j MASQUERADE") < 0 {
		t.Errorf("the stranded-backend path skipped the MASQUERADE heal:\n  %s", strings.Join(seen, "\n  "))
	}
}

func indexOfCommand(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}
