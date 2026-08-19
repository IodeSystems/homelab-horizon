package wireguard

import (
	"testing"
	"time"
)

// Fixture copied from a live `wg show wg0 dump`: an interface line, a connected
// peer, and one that has never handshaked. Keys are truncated only in the sense
// that they are real-shaped; the field layout is verbatim, which is the part
// that matters.
const liveDump = "gO8o2PtWj1u96lO60t21DFqIfYr5go7sAI9/JMdnAlw=\thr3zXTGGVFfIRZ0nSwR8kf6xRiB648KEvbd1R7zlHjo=\t51820\toff\n" +
	"FaoNKT+aIvjGWRscPds87KM6g08AF3WC/7h2g64FVjg=\t(none)\t192.168.1.77:34306\t10.100.0.2/32\t1787106306\t38031204\t117435908\toff\n" +
	"A2QvOb6eTmOUFk7tnUx24u/qUJCOhKsMOP3S/rjCzTU=\t(none)\t(none)\t10.100.0.3/32\t0\t0\t0\toff\n"

func TestParseWGDump(t *testing.T) {
	got := parseWGDump(liveDump)

	if len(got) != 2 {
		t.Fatalf("parsed %d peers, want 2 (the interface line is not a peer)", len(got))
	}

	live := got["FaoNKT+aIvjGWRscPds87KM6g08AF3WC/7h2g64FVjg="]
	if live.RX != 38031204 || live.TX != 117435908 {
		t.Errorf("counters = rx %d tx %d, want rx 38031204 tx 117435908", live.RX, live.TX)
	}
	if !live.LatestHandshake.Equal(time.Unix(1787106306, 0)) {
		t.Errorf("handshake = %v, want the unix seconds from the dump", live.LatestHandshake)
	}

	// "never handshaked" is 0 in the dump. Read as the epoch it would put the
	// peer's last contact in 1970, which reads as decades idle rather than as
	// unknown.
	never := got["A2QvOb6eTmOUFk7tnUx24u/qUJCOhKsMOP3S/rjCzTU="]
	if !never.LatestHandshake.IsZero() {
		t.Errorf("a handshake of 0 parsed to %v, want the zero Time", never.LatestHandshake)
	}
	if never.RX != 0 || never.TX != 0 {
		t.Errorf("unused peer has counters rx %d tx %d", never.RX, never.TX)
	}
}

func TestParseWGDumpTolerance(t *testing.T) {
	// Empty output (interface down) and a truncated line must not panic or
	// invent peers — this parses whatever the kernel last wrote.
	for _, in := range []string{"", "\n", "iface-line-only\n", "a\tb\tc\n"} {
		if got := parseWGDump(in); len(got) != 0 {
			t.Errorf("parseWGDump(%q) = %v, want no peers", in, got)
		}
	}
}
