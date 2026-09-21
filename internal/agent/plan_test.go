package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// A WireGuard private key shaped like a real one. Base64 of 32 bytes, and it
// is not one: generated from a fixed byte pattern for this test only.
const fakePrivateKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func haproxyDesired(path, contents string) *Desired {
	return &Desired{
		Machine: "gateway",
		HAProxy: &HAProxySection{
			ConfigPath: path,
			Files:      []File{{Path: path, Mode: 0o644, Contents: contents}},
		},
	}
}

// The property the whole package turns on: identical contents are UNCHANGED.
// A change reloads HAProxy, and a reload for nothing is a reload that
// eventually lands in the middle of something.
func TestIdenticalContentsReportUnchanged(t *testing.T) {
	const cfg = "global\n  daemon\n"
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", cfg)
	obs := Observed{Files: map[string]FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, Contents: cfg},
	}}

	p := Compute(d, obs)
	if p.Changed() {
		t.Fatalf("identical contents planned a change: %+v", p.Pending())
	}
	if len(p.Changes) != 1 || p.Changes[0].Kind != KindUnchanged {
		t.Fatalf("want one unchanged change, got %+v", p.Changes)
	}
}

func TestDifferentContentsPlanAnUpdate(t *testing.T) {
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n  daemon\n  maxconn 4096\n")
	obs := Observed{Files: map[string]FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, Contents: "global\n  daemon\n"},
	}}

	p := Compute(d, obs)
	pending := p.Pending()
	if len(pending) != 1 || pending[0].Kind != KindUpdate {
		t.Fatalf("want one update, got %+v", p.Changes)
	}
	if !strings.Contains(pending[0].Detail, "+1/-0 lines") {
		t.Fatalf("the detail should say what moved, got %q", pending[0].Detail)
	}
	if !strings.Contains(pending[0].Detail, "maxconn 4096") {
		t.Fatalf("a non-secret file should show the changed line, got %q", pending[0].Detail)
	}
}

func TestMissingFilePlansACreate(t *testing.T) {
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	p := Compute(d, Observed{Files: map[string]FileState{}})
	if len(p.Pending()) != 1 || p.Pending()[0].Kind != KindCreate {
		t.Fatalf("want a create, got %+v", p.Changes)
	}
}

// An unreadable file is UNKNOWN, never "in sync" and never "changed".
// Reporting it as in-sync is a lie; reporting it as changed would have the
// agent overwrite a file whose contents it never saw.
func TestUnreadableFileIsUnknownNotChanged(t *testing.T) {
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	obs := Observed{Files: map[string]FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, ReadErr: "permission denied"},
	}}

	p := Compute(d, obs)
	if p.Changed() {
		t.Fatal("an unreadable file must not be planned as a change")
	}
	if len(p.Unknown()) != 1 {
		t.Fatalf("want one unknown, got %+v", p.Changes)
	}
	if !strings.Contains(Report(p), "could not be read") {
		t.Fatalf("the report must not call this in sync:\n%s", Report(p))
	}
}

// A WireGuard config holds the machine's private key. It must never reach the
// report — not the line, not a fragment, not via the "changed lines" sample.
// The key is ROTATED between the two versions on purpose: if both sides
// carried the same key line, the line-delta would never emit it and the test
// would pass with no redaction at all.
func TestWireGuardKeyNeverReachesTheReport(t *testing.T) {
	const rotatedKey = "Hx8eHRwbGhkYFxYVFBMSERAPDg0MCwoJCAcGBQQDAgEA"
	current := "[Interface]\nPrivateKey = " + fakePrivateKey + "\nAddress = 10.0.0.1/24\n"
	desired := "[Interface]\nPrivateKey = " + rotatedKey + "\nAddress = 10.0.0.1/24\nListenPort = 51820\n"

	d := &Desired{
		Machine: "gateway",
		WireGuard: &WireGuardSection{
			Interface: "wg0", ConfigPath: "/etc/wireguard/wg0.conf",
			Files: []File{{Path: "/etc/wireguard/wg0.conf", Mode: 0o600, Contents: desired}},
		},
	}
	obs := Observed{Files: map[string]FileState{
		"/etc/wireguard/wg0.conf": {Exists: true, Contents: current},
	}}

	report := Report(Compute(d, obs))
	for _, key := range []string{fakePrivateKey, rotatedKey} {
		if strings.Contains(report, key) {
			t.Fatalf("a private key reached the diff report:\n%s", report)
		}
	}
	if !strings.Contains(report, "key material") {
		t.Fatalf("the report should say why it is not showing the contents:\n%s", report)
	}
}

// Layer two of the redaction: a file the producer forgot to mark secret still
// must not hand over a key. The Secret flag is a promise made elsewhere;
// redactLine is the check made here.
func TestKeyIsRedactedEvenWhenTheFileIsNotMarkedSecret(t *testing.T) {
	d := &Desired{
		Machine: "gateway",
		HAProxy: &HAProxySection{
			ConfigPath: "/etc/haproxy/haproxy.cfg",
			// Deliberately NOT Secret, and deliberately carrying a key.
			Files: []File{{Path: "/etc/x.conf", Mode: 0o644,
				Contents: "listen 80\nPrivateKey = " + fakePrivateKey + "\n"}},
		},
	}
	obs := Observed{Files: map[string]FileState{
		"/etc/x.conf": {Exists: true, Contents: "listen 80\n"},
	}}

	report := Report(Compute(d, obs))
	if strings.Contains(report, fakePrivateKey) {
		t.Fatalf("an unflagged key reached the report:\n%s", report)
	}
	if !strings.Contains(report, "[redacted]") {
		t.Fatalf("the redaction should be visible, not silent:\n%s", report)
	}
}

func TestRedactLineCoversTheUsualNames(t *testing.T) {
	cases := []string{
		"PrivateKey = " + fakePrivateKey,
		"  PresharedKey = " + fakePrivateKey,
		"admin_token: hunter2",
		"DB_PASSWORD=hunter2",
		"client_secret = abc123",
	}
	for _, line := range cases {
		got := redactLine(line)
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("redactLine(%q) = %q, want it redacted", line, got)
		}
		if strings.Contains(got, "hunter2") || strings.Contains(got, fakePrivateKey) || strings.Contains(got, "abc123") {
			t.Errorf("redactLine(%q) left the value in: %q", line, got)
		}
	}
	// And it must not eat ordinary config.
	if got := redactLine("  server current 127.0.0.1:6400 check"); !strings.Contains(got, "6400") {
		t.Errorf("redactLine mangled an ordinary line: %q", got)
	}
}

// Without root the live firewall cannot be read, and the agent must say so
// rather than plan the entire rule set as missing.
func TestUnprivilegedIPTablesIsUnknownNotEverythingMissing(t *testing.T) {
	d := &Desired{Machine: "gateway", IPTables: &IPTablesSection{
		Expected: []iptables.Rule{{Table: "filter", Chain: "WG-FORWARD", Args: []string{"-j", "DROP"}}},
	}}
	obs := (&SystemObserver{euidFn: func() int { return 1000 }}).Observe(d)
	if obs.IPTablesReadable {
		t.Fatal("a non-root observer must not claim it read the firewall")
	}

	p := Compute(d, obs)
	if p.Changed() {
		t.Fatalf("an unreadable firewall must not plan changes: %+v", p.Pending())
	}
	if len(p.Unknown()) != 1 || !strings.Contains(p.Unknown()[0].Detail, "root") {
		t.Fatalf("want one unknown that says why, got %+v", p.Changes)
	}
}

func TestIPTablesPlansMissingAndStale(t *testing.T) {
	want := iptables.Rule{Table: "filter", Chain: "WG-FORWARD", Args: []string{"-i", "wg0", "-j", "DROP"}}
	stale := iptables.Rule{Table: "nat", Chain: "POSTROUTING", Args: []string{"-o", "eth9", "-j", "MASQUERADE"}}

	d := &Desired{Machine: "gateway", IPTables: &IPTablesSection{
		Expected: []iptables.Rule{want},
		Stale:    []iptables.Rule{stale},
	}}
	obs := Observed{
		Files:            map[string]FileState{},
		IPTablesReadable: true,
		LiveRules:        []iptables.Rule{stale},
	}

	p := Compute(d, obs)
	var sawCreate, sawStale bool
	for _, c := range p.Changes {
		if c.Kind == KindCreate && strings.Contains(c.Target, "WG-FORWARD") {
			sawCreate = true
		}
		if c.Kind == KindUpdate && strings.Contains(c.Detail, "stale") {
			sawStale = true
		}
	}
	if !sawCreate {
		t.Errorf("the missing expected rule was not planned: %+v", p.Changes)
	}
	if !sawStale {
		t.Errorf("the stale rule was not planned for removal: %+v", p.Changes)
	}
}

// A nil section means "hz does not manage this here", not "hz wants it empty".
func TestNilSectionPlansNothing(t *testing.T) {
	p := Compute(&Desired{Machine: "gateway"}, Observed{Files: map[string]FileState{}})
	if len(p.Changes) != 0 {
		t.Fatalf("a payload with no sections planned %+v", p.Changes)
	}
}

// The generation is a content hash, so it moves when and only when the
// payload does.
func TestFingerprintTracksContent(t *testing.T) {
	a := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	b := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	c := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n  daemon\n")

	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("identical payloads produced different generations")
	}
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("a changed payload kept its generation")
	}
	if strings.Contains(a.Fingerprint(), "global") {
		t.Fatal("the generation is supposed to be a hash")
	}
}

// The fingerprint must move when a secret rotates, or a re-keyed machine
// would never be told to apply it.
func TestFingerprintMovesWhenASecretRotates(t *testing.T) {
	mk := func(key string) *Desired {
		return &Desired{Machine: "gateway", WireGuard: &WireGuardSection{
			Interface: "wg0", ConfigPath: "/etc/wireguard/wg0.conf",
			Files: []File{{Path: "/etc/wireguard/wg0.conf", Contents: "PrivateKey = " + key + "\n"}},
		}}
	}
	if mk("a").Fingerprint() == mk("b").Fingerprint() {
		t.Fatal("rotating the key did not move the generation")
	}
}

func TestReportSaysInSyncOnlyWhenItIs(t *testing.T) {
	const cfg = "global\n"
	clean := Compute(haproxyDesired(filepath.Join("/etc", "haproxy.cfg"), cfg), Observed{
		Files: map[string]FileState{"/etc/haproxy.cfg": {Exists: true, Contents: cfg}},
	})
	if !strings.Contains(Report(clean), "in sync") {
		t.Fatalf("a clean plan should say so:\n%s", Report(clean))
	}

	dirty := Compute(haproxyDesired("/etc/haproxy.cfg", cfg+"  daemon\n"), Observed{
		Files: map[string]FileState{"/etc/haproxy.cfg": {Exists: true, Contents: cfg}},
	})
	if strings.Contains(Report(dirty), "in sync") {
		t.Fatalf("a dirty plan must not say in sync:\n%s", Report(dirty))
	}
}
