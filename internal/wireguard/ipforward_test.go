package wireguard

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// WHAT THE DROP-IN MEANS, not merely what both writers agree it says.
//
// THIS TEST EXISTS BECAUSE A POSITIVE CONTROL FOUND NOTHING. hz's writer and
// the agent's payload both take the drop-in's bytes from RenderIPForwardDropIn,
// which is the point — but it also means the byte-identical compare in
// internal/server moves with the constant. Rendering "net.ipv4.ip_forward=0",
// or misspelling the key, left every test in the tree green while shipping a
// file that does not fix the reboot bug it exists for.
//
// So the drop-in is read the way systemd-sysctl reads it — comments dropped,
// key=value split — and the value is checked against the one hz writes to the
// live flag. The persistent half and the runtime half disagreeing is the same
// bug in a different direction.
func TestTheDropInActuallyEnablesForwarding(t *testing.T) {
	settings := map[string]string{}
	var keys []string
	for _, line := range strings.Split(RenderIPForwardDropIn(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("the drop-in has a line sysctl cannot read: %q", line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		settings[k] = v
		keys = append(keys, k)
	}

	sort.Strings(keys)
	if len(keys) != 1 || keys[0] != "net.ipv4.ip_forward" {
		t.Fatalf("the drop-in sets %v; it must set net.ipv4.ip_forward and nothing else", keys)
	}
	// hz's drop-in is a kernel-wide setting. One key, checked by name, because
	// a typo in it is a file systemd-sysctl applies without complaint and
	// without effect.
	if got := settings["net.ipv4.ip_forward"]; got != "1" {
		t.Fatalf("the drop-in sets forwarding to %q; the whole point of the file is 1", got)
	}
	// And the two lifetimes agree. A drop-in saying 1 beside a live write of
	// something else would make a reboot change the machine's behaviour, which
	// is the bug wearing the opposite face.
	if got, want := settings["net.ipv4.ip_forward"], strings.TrimSpace(IPForwardOn); got != want {
		t.Fatalf("the drop-in sets %q and the live flag is written %q", got, want)
	}
}

// hz's writer makes forwarding survive a reboot, which is the half that was
// missing (plan/icebox.md). Run against temp paths so the suite never touches
// the machine's /etc/sysctl.d or its kernel.
func TestEnableIPForwardingPersistsAndSetsTheFlag(t *testing.T) {
	root := t.TempDir()
	dropIns := filepath.Join(root, "sysctl.d")
	flag := filepath.Join(root, "ip_forward")

	if err := EnableIPForwardingAt(dropIns, flag); err != nil {
		t.Fatalf("EnableIPForwardingAt: %v", err)
	}
	if got := readAll(t, filepath.Join(dropIns, IPForwardDropInName)); got != RenderIPForwardDropIn() {
		t.Fatalf("the drop-in holds %q", got)
	}
	if got := readAll(t, flag); got != IPForwardOn {
		t.Fatalf("the live flag holds %q, want %q", got, IPForwardOn)
	}

	// Idempotent: a second call over its own output changes nothing.
	before := readAll(t, filepath.Join(dropIns, IPForwardDropInName))
	if err := EnableIPForwardingAt(dropIns, flag); err != nil {
		t.Fatal(err)
	}
	if after := readAll(t, filepath.Join(dropIns, IPForwardDropInName)); after != before {
		t.Fatal("a second call rewrote the drop-in differently")
	}
}

// BOTH FAILURES ARE REPORTED, not just the first.
//
// The live flag failing means the box is not routing now; the drop-in failing
// means it stops routing at the next reboot. They are different outages and a
// writer that stopped at the first would hide whichever came second — which is
// the silence the icebox entry is about.
func TestEnableIPForwardingReportsEitherHalfFailing(t *testing.T) {
	root := t.TempDir()

	// A file where the drop-in directory should be, so MkdirAll fails; and a
	// directory where the flag should be, so the write fails.
	blocked := filepath.Join(root, "sysctl.d")
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flagAsDir := filepath.Join(root, "ip_forward")
	if err := os.MkdirAll(flagAsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := EnableIPForwardingAt(blocked, flagAsDir)
	if err == nil {
		t.Fatal("both writes failed and EnableIPForwardingAt said nothing")
	}
	said := err.Error()
	for _, want := range []string{"survive a reboot", "on now"} {
		if !strings.Contains(said, want) {
			t.Fatalf("the error %q does not mention the %q half", said, want)
		}
	}
}

// The sweep takes hz's own drop-in under another number and NOTHING ELSE.
//
// Same claim the agent's Directory makes, from the same glob. The near misses
// are the ones a wider predicate would take.
func TestTheSweepTakesOnlyHZsOwnDropIn(t *testing.T) {
	dropIns := t.TempDir()
	seeded := map[string]bool{
		"99-sysctl.conf":            true,
		"60-docker-forward.conf":    true,
		"70-hz-ip-forward.conf.bak": true,
		"hz-ip-forward.conf":        true,
		"60-hz-ip-forward.conf":     false, // hz's, older number: swept
		"-hz-ip-forward.conf":       false, // the empty-prefix edge: swept
	}
	for name := range seeded {
		if err := os.WriteFile(filepath.Join(dropIns, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := EnableIPForwardingAt(dropIns, filepath.Join(t.TempDir(), "flag")); err != nil {
		t.Fatal(err)
	}
	for name, keep := range seeded {
		_, err := os.Stat(filepath.Join(dropIns, name))
		if keep && err != nil {
			t.Errorf("the sweep took %s, which is not hz's: %v", name, err)
		}
		if !keep && !os.IsNotExist(err) {
			t.Errorf("the sweep left %s, so two files set one key (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dropIns, IPForwardDropInName)); err != nil {
		t.Fatalf("the sweep took the file it had just written: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
