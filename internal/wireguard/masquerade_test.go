package wireguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The PURE half of the interface-change heal. The two-writer comparison lives
// in internal/server (wg_masquerade_test.go); what is pinned here is the
// property every one of those comparisons rests on — that the rewrite touches
// one token and invents nothing.

const movedFromIface = "enx00051b94b7cc"
const movedToIface = "wlan0"

// gatewayConf is the shape of a real wg0.conf: a hand-written comment, the
// spacing an operator left, a PostUp and PostDown that both NAT, an unmodelled
// directive hz does not know about, and a peer block below.
func gatewayConf(iface string) string {
	return "# managed by homelab-horizon — do not hand-edit\n" +
		"[Interface]\n" +
		"PrivateKey = " + fakeKey + "\n" +
		"Address = 10.100.0.1/24\n" +
		"ListenPort=51820\n" +
		"MTU = 1420\n" +
		"PostUp = " + ExpectedPostUp(iface) + "\n" +
		"PostDown = " + ExpectedPostDown(iface) + "\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + fakePeerKey + "\n" +
		"AllowedIPs = 10.100.0.2/32\n"
}

// Obviously fake: the right shape for the parser, not a key anybody has.
const (
	fakeKey     = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	fakePeerKey = "Hx4dHBsaGRgXFhUUExIREA8ODQwLCgkIBwYFBAMCAQA="
)

// The heal itself: the MASQUERADE moves and NOTHING ELSE DOES.
//
// The second half is the load-bearing one. A heal that reformats the file
// makes every later comparison between hz's writer and the agent's payload a
// comparison of formatting, and it silently rewrites directives hz does not
// model — the `ListenPort=51820` below has no spaces around its `=` on purpose.
func TestHealMasqueradeMovesOneTokenAndNothingElse(t *testing.T) {
	before := gatewayConf(movedFromIface)
	after, changed := HealMasqueradeIface(before, movedToIface)
	if !changed {
		t.Fatal("the heal reported no change on a config that NATs through the old interface")
	}
	if strings.Contains(after, movedFromIface) {
		t.Errorf("the old interface survives the heal:\n%s", after)
	}
	if !strings.Contains(after, "-o "+movedToIface+" -j MASQUERADE") {
		t.Errorf("the healed config does not NAT through %s:\n%s", movedToIface, after)
	}

	// Line for line, only the two directives that carry a MASQUERADE moved.
	b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(b) != len(a) {
		t.Fatalf("the heal changed the line count, %d → %d", len(b), len(a))
	}
	var moved []int
	for i := range b {
		if b[i] != a[i] {
			moved = append(moved, i)
		}
	}
	if len(moved) != 2 {
		t.Fatalf("the heal changed %d lines, want 2 (PostUp and PostDown):\n%v", len(moved), moved)
	}
	for _, i := range moved {
		if !strings.HasPrefix(a[i], "PostUp = ") && !strings.HasPrefix(a[i], "PostDown = ") {
			t.Errorf("the heal changed line %d, which is not a Post* directive: %q", i, a[i])
		}
		// And it changed only the interface name within that line.
		if strings.ReplaceAll(a[i], movedToIface, movedFromIface) != b[i] {
			t.Errorf("line %d changed by more than the interface name:\n before %q\n after  %q", i, b[i], a[i])
		}
	}
	// The unmodelled directive kept the operator's spacing.
	if !strings.Contains(after, "ListenPort=51820\n") {
		t.Errorf("the heal reformatted a directive it does not own:\n%s", after)
	}
}

// Idempotent, which is what lets the caller run it every pass instead of only
// when it noticed a change.
func TestHealMasqueradeOnAConfigThatAlreadyAgrees(t *testing.T) {
	already := gatewayConf(movedToIface)
	after, changed := HealMasqueradeIface(already, movedToIface)
	if changed {
		t.Error("the heal reported a change on a config that already NATs through the current interface")
	}
	if after != already {
		t.Error("the heal rewrote a config it said it had not changed")
	}
}

// NO INTERFACE MEANS STAND DOWN. hz cannot name the egress interface during a
// route flap; writing `-o  -j MASQUERADE` would replace a stale-but-nameable
// rule with one that cannot parse.
func TestHealMasqueradeStandsDownWithNoInterface(t *testing.T) {
	before := gatewayConf(movedFromIface)
	after, changed := HealMasqueradeIface(before, "")
	if changed || after != before {
		t.Fatal("the heal rewrote the config with no interface to name")
	}
}

// IT NEVER INTRODUCES A DIRECTIVE. renderInterfaceRules — the other writer in
// this package — INSERTS PostUp and PostDown after ListenPort when they are
// missing, which on a config with only a PostUp would add an empty
// `PostDown = ` line for wg-quick to run as a command.
func TestHealMasqueradeNeverAddsADirective(t *testing.T) {
	onlyUp := "[Interface]\n" +
		"Address = 10.100.0.1/24\n" +
		"ListenPort = 51820\n" +
		"PostUp = iptables -t nat -I POSTROUTING 1 -o " + movedFromIface + " -j MASQUERADE\n"
	after, changed := HealMasqueradeIface(onlyUp, movedToIface)
	if !changed {
		t.Fatal("the PostUp was not healed")
	}
	if strings.Contains(after, "PostDown") {
		t.Fatalf("the heal invented a PostDown directive:\n%s", after)
	}

	// And a config with no Post* at all is returned untouched rather than
	// growing the pair.
	none := "[Interface]\nAddress = 10.100.0.1/24\nListenPort = 51820\n"
	if got, changed := HealMasqueradeIface(none, movedToIface); changed || got != none {
		t.Fatalf("the heal wrote directives into a config that had none:\n%s", got)
	}
}

// A MASQUERADE outside [Interface] is not hz's to move. wg-quick only runs
// Post* from the interface section; anything shaped like one under a [Peer] is
// somebody's note or somebody's mistake, and rewriting it would be hz editing
// a line it does not execute.
func TestHealMasqueradeStaysInsideTheInterfaceSection(t *testing.T) {
	conf := "[Interface]\n" +
		"PostUp = iptables -t nat -I POSTROUTING 1 -o " + movedFromIface + " -j MASQUERADE\n" +
		"\n[Peer]\n" +
		"PublicKey = " + fakePeerKey + "\n" +
		"# PostUp = iptables -t nat -I POSTROUTING 1 -o " + movedFromIface + " -j MASQUERADE\n"
	after, changed := HealMasqueradeIface(conf, movedToIface)
	if !changed {
		t.Fatal("the [Interface] PostUp was not healed")
	}
	peer := after[strings.Index(after, "[Peer]"):]
	if !strings.Contains(peer, movedFromIface) {
		t.Errorf("the heal reached into the [Peer] section:\n%s", after)
	}
}

// The manager half, on a real file: same bytes as the pure function, mode
// 0600, and the in-memory copy updated so a later GetPostUp is not stale.
func TestWGConfigHealMasqueradeWritesTheHealedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	before := gatewayConf(movedFromIface)
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewConfig(path, "wg0")
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}

	changed, err := w.HealMasquerade(movedToIface)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("HealMasquerade reported no change")
	}

	want, _ := HealMasqueradeIface(before, movedToIface)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("the file on disk is not what the pure heal produces:\n got %q\nwant %q", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("wg0.conf left at mode %04o; it holds the machine's private key", fi.Mode().Perm())
	}
	if strings.Contains(w.GetPostUp(), movedFromIface) {
		t.Error("the in-memory PostUp still names the old interface after a heal")
	}

	// Second call, nothing to do, no write.
	if changed, err := w.HealMasquerade(movedToIface); err != nil || changed {
		t.Fatalf("a second heal reported changed=%v err=%v", changed, err)
	}
}

// THE HEAL DOES NOT COME FROM WGConfig's CACHE. hz's old rewrite composed the
// new line from the postUp parsed at Load, so any edit made to the file since —
// a hand edit, another writer, the agent — was silently reverted by the next
// pass.
func TestHealMasqueradeReadsTheFileNotTheCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte(gatewayConf(movedFromIface)), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewConfig(path, "wg0")
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}

	// Somebody adds an MTU line after hz loaded the file.
	edited := strings.Replace(gatewayConf(movedFromIface), "MTU = 1420\n", "MTU = 1380\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := w.HealMasquerade(movedToIface); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "MTU = 1380") {
		t.Errorf("the heal reverted an edit made since Load:\n%s", got)
	}
}
