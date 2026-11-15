package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// A realistic wg0.conf. The key is obviously fake and obviously
// key-SHAPED — homelab-horizon is a public repo, and a test fixture that
// looked like a real key would be a worse problem than the one it guards.
const fakeWGConfig = `[Interface]
Address = 10.10.0.1/24
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
ListenPort = 51820

[Peer]
PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
AllowedIPs = 10.10.0.2/32
`

const fakeWGPrivate = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const fakeWGPresharedKey = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="

// wgPlan computes a real plan for a WireGuard section whose file on disk
// differs from the desired one, which is the case that carries a key.
func wgPlan(t *testing.T) (*Desired, Plan, Observed) {
	t.Helper()
	d := &Desired{
		Machine: "gateway",
		WireGuard: &WireGuardSection{
			Interface:  "wg0",
			ConfigPath: "/etc/wireguard/wg0.conf",
			// Secret is NOT set here on purpose: desired.files() forces it,
			// which is layer 1, and a test that set it by hand would be
			// testing its own fixture.
			Files: []File{{Path: "/etc/wireguard/wg0.conf", Mode: 0o600, Contents: fakeWGConfig}},
		},
	}
	obs := Observed{Files: map[string]FileState{
		"/etc/wireguard/wg0.conf": {
			Exists: true,
			Contents: strings.Replace(fakeWGConfig,
				"PrivateKey = "+fakeWGPrivate,
				"PrivateKey = DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=", 1),
		},
	}}
	return d, Compute(d, obs), obs
}

// The whole point of reporting the PLAN and not the Observed: a report built
// from a machine whose wg0.conf differs carries no key, from either side.
func TestAReportCarriesNoKeyMaterial(t *testing.T) {
	d, p, obs := wgPlan(t)
	r := NewStateReport(d, p, obs)

	body := reportText(r)
	for _, needle := range []string{
		fakeWGPrivate,
		fakeWGPresharedKey,
		"DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=", // what was on disk
	} {
		if strings.Contains(body, needle) {
			t.Fatalf("a report carried key material: %q", needle)
		}
	}
	// And it did say something useful — a report that carried nothing at all
	// would pass the check above for the wrong reason.
	if len(r.Changes) != 1 || r.Changes[0].Kind != KindUpdate {
		t.Fatalf("the report lost the change it was supposed to describe: %+v", r.Changes)
	}
	if !strings.Contains(r.Changes[0].Detail, "key material") {
		t.Fatalf("the change does not say why it is not shown: %q", r.Changes[0].Detail)
	}
}

// LAYER 1 ALONE. Secret forcing collapses a wg file to byte counts, and it
// does that WITHOUT redactLine ever running — describeTextChange returns
// before the line sampling that redaction applies to.
//
// Proved by disabling layer 2: the sanitized report and the raw plan are
// byte-identical, so redaction changed nothing, and the key is still absent.
// If layer 1 were doing nothing, the two would differ (redaction would have
// had something to blank) or the key would be there.
func TestSecretForcingHoldsWithoutPatternRedaction(t *testing.T) {
	d, p, obs := wgPlan(t)

	raw := StateReport{Machine: p.Machine, Generation: p.Generation, Changes: p.Changes}
	sanitized := NewStateReport(d, p, obs)

	if reportText(raw) != reportText(sanitized) {
		t.Fatal("pattern redaction changed the report, so this test is not measuring layer 1 alone")
	}
	if strings.Contains(reportText(raw), fakeWGPrivate) {
		t.Fatal("layer 1 did not hold: an unredacted plan carried the private key")
	}
}

// LAYER 2 ALONE. A file the producer did NOT mark secret still must not hand
// over a key, because the producer's flag is a human decision made somewhere
// else and "they will remember" is not a security property.
//
// Proved by disabling layer 1: describeTextChange is called with secret=false
// over the same wg contents, which is exactly what would happen if
// Desired.files() stopped forcing the flag. Redaction has to catch it.
func TestPatternRedactionHoldsWithoutSecretForcing(t *testing.T) {
	unforced := describeTextChange(
		strings.Replace(fakeWGConfig, fakeWGPrivate, "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE=", 1),
		fakeWGConfig,
		false, // layer 1 off
	)
	// Without the flag the differ samples real lines — which is the exposure
	// this layer exists for.
	if !strings.Contains(unforced, "PrivateKey") {
		t.Fatalf("the fixture never reached a PrivateKey line, so nothing was tested: %q", unforced)
	}

	r := StateReport{Changes: []Change{{
		Subsystem: SubsystemWireGuard,
		Target:    "/etc/wireguard/wg0.conf",
		Kind:      KindUpdate,
		Detail:    unforced,
	}}}.Sanitized()

	body := reportText(r)
	for _, needle := range []string{fakeWGPrivate, "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="} {
		if strings.Contains(body, needle) {
			t.Fatalf("layer 2 did not hold: an unforced secret file leaked %q", needle)
		}
	}
	if !strings.Contains(body, "[redacted]") {
		t.Fatal("nothing was redacted, so the key was absent for some other reason")
	}
}

// THE POSITIVE CONTROL for both tests above: the fixture really does contain
// a key, and an unsanitized report really would carry it. Without this, a
// green "no key material" proves only that the test built an empty report.
func TestTheFixtureActuallyCarriesAKey(t *testing.T) {
	leak := StateReport{Changes: []Change{{
		Subsystem: SubsystemWireGuard,
		Target:    "/etc/wireguard/wg0.conf",
		Kind:      KindUpdate,
		// A Detail nobody redacted — a Change is a plain struct and this is
		// what an unsanitized one looks like.
		//
		// The "  + " prefix is the whole point, and this test FAILED when it
		// was written: secretAssignment anchored on optional whitespace only,
		// so a line that already carried a diff marker walked through the
		// SECOND redaction pass untouched — in Report as well as here. A
		// second layer that cannot match the shape the first layer emits is
		// not a second layer. diff.go's regex now allows the marker.
		Detail: "  + PrivateKey = " + fakeWGPrivate,
	}}}
	if !strings.Contains(reportText(leak), fakeWGPrivate) {
		t.Fatal("the fixture key does not survive an unsanitized report, so the guards above prove nothing")
	}
	if strings.Contains(reportText(leak.Sanitized()), fakeWGPrivate) {
		t.Fatal("Sanitized did not redact a hand-built leak")
	}
}

// A report that is not sanitized is not a report hz stores. Sanitizing twice
// must be a no-op, or the two ends of the wire would disagree about what was
// sent.
func TestSanitizeIsIdempotent(t *testing.T) {
	d, p, obs := wgPlan(t)
	once := NewStateReport(d, p, obs)
	if reportText(once) != reportText(once.Sanitized()) {
		t.Fatal("sanitizing twice changed the report")
	}
}

// Every string in a stored report went through layer 2 — including the live
// firewall rules, which carry nothing to redact today and must still not be
// an exception somebody has to remember.
func TestLiveRulesGoThroughRedactionToo(t *testing.T) {
	d := &Desired{Machine: "gateway", IPTables: &IPTablesSection{
		Expected: []iptables.Rule{{Table: "nat", Chain: "POSTROUTING", Args: []string{"-j", "MASQUERADE"}}},
	}}
	obs := Observed{
		Files:            map[string]FileState{},
		IPTablesReadable: true,
		LiveRules: []iptables.Rule{{
			Table: "filter", Chain: "INPUT",
			Args: []string{"-m", "comment", "--comment", "password=hunter2", "-j", "ACCEPT"},
		}},
	}
	r := NewStateReport(d, Compute(d, obs), obs)
	if r.IPTables == nil || len(r.IPTables.Live) != 1 {
		t.Fatalf("the rule did not survive the report: %+v", r.IPTables)
	}
	if strings.Contains(reportText(r), "hunter2") {
		t.Fatal("a live rule carried an unredacted secret-shaped token")
	}
	// The rest of the rule is intact — redaction must not eat the firewall.
	if got := r.IPTables.Live[0].Args[0]; got != "-m" {
		t.Fatalf("redaction mangled a rule argument: %q", got)
	}
}

// An unreadable firewall is an answer, not an empty rule set. This is the
// state hz web itself lands in after item 12, and the one a screen has to be
// able to say out loud.
func TestUnreadableFirewallIsReportedAsSuch(t *testing.T) {
	d := &Desired{Machine: "gateway", IPTables: &IPTablesSection{
		Expected: []iptables.Rule{{Table: "nat", Chain: "POSTROUTING", Args: []string{"-j", "MASQUERADE"}}},
	}}
	obs := Observed{Files: map[string]FileState{}, IPTablesWhy: "reading the live firewall needs root"}
	r := NewStateReport(d, Compute(d, obs), obs)

	if r.IPTables == nil {
		t.Fatal("a managed firewall the agent could not read produced no section at all")
	}
	if r.IPTables.Readable || r.IPTables.Why == "" {
		t.Fatalf("an unreadable firewall did not say so: %+v", r.IPTables)
	}
	if !r.HasTargets() {
		t.Fatal("a machine with a managed firewall reported as having nothing to report")
	}
}

// Nothing to report is not silence, and it is not in-sync-with-work-to-do
// either. A box hz manages nothing on has no changes and no firewall section,
// permanently and correctly.
func TestNothingToReportHasNoTargets(t *testing.T) {
	d := &Desired{Machine: "ci-1"}
	r := NewStateReport(d, Compute(d, Observed{Files: map[string]FileState{}}), Observed{})
	if r.HasTargets() {
		t.Fatalf("a machine with no managed subsystems claimed targets: %+v", r)
	}
	if !r.InSync() || r.Pending() != 0 {
		t.Fatal("a machine with nothing to do is in sync")
	}
}

// The bounds are the backstop against a client that ignores the caps its own
// library applies. hz stores what arrives.
func TestReportBoundsAreEnforced(t *testing.T) {
	huge := make([]Change, maxReportChanges+50)
	for i := range huge {
		huge[i] = Change{Subsystem: SubsystemHAProxy, Target: "/etc/x", Kind: KindUnchanged,
			Detail: strings.Repeat("y", maxReportDetailWidth*3)}
	}
	r := StateReport{Machine: "gateway", Changes: huge}.Sanitized()

	if len(r.Changes) != maxReportChanges {
		t.Fatalf("change cap not applied: %d", len(r.Changes))
	}
	if !r.Truncated {
		t.Fatal("a truncated report did not say it was truncated")
	}
	if got := len(r.Changes[0].Detail); got > maxReportDetailWidth+4 {
		t.Fatalf("detail not bounded: %d bytes", got)
	}
}

// The condition key is what lets hz say "pending since", so it must ignore
// the parts of a Detail that jitter and must not ignore a target changing
// state.
func TestConditionTracksStateNotDetail(t *testing.T) {
	base := StateReport{Generation: "g1", Changes: []Change{
		{Subsystem: SubsystemHAProxy, Target: "/etc/haproxy/haproxy.cfg", Kind: KindUpdate, Detail: "contents differ (+2/-1 lines)"},
	}}
	jittered := base
	jittered.Changes = []Change{{Subsystem: SubsystemHAProxy, Target: "/etc/haproxy/haproxy.cfg",
		Kind: KindUpdate, Detail: "contents differ (+9/-7 lines)"}}
	if base.Condition() != jittered.Condition() {
		t.Fatal("a different diff sample reset the condition clock")
	}

	fixed := base
	fixed.Changes = []Change{{Subsystem: SubsystemHAProxy, Target: "/etc/haproxy/haproxy.cfg", Kind: KindUnchanged}}
	if base.Condition() == fixed.Condition() {
		t.Fatal("a target that stopped being pending kept the same condition")
	}

	moved := base
	moved.Generation = "g2"
	if base.Condition() == moved.Condition() {
		t.Fatal("a new generation kept the same condition")
	}
}

// One record per machine, replaced — and SameSince carried while the machine
// keeps saying the same thing, which is what makes "pending since 4h" true.
func TestStoreReplacesAndCarriesSameSince(t *testing.T) {
	store := ObservedStore{Path: t.TempDir() + "/config.json" + ObservedSuffix}
	pending := StateReport{Machine: "gateway", Generation: "g1", Changes: []Change{
		{Subsystem: SubsystemHAProxy, Target: "/etc/haproxy/haproxy.cfg", Kind: KindUpdate},
	}}

	t0 := time.Unix(1_700_000_000, 0)
	if err := store.Record("gateway", pending, t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(4 * time.Hour)
	if err := store.Record("gateway", pending, t1); err != nil {
		t.Fatal(err)
	}

	all, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("a second report from one machine made %d records", len(all))
	}
	if all[0].ReportedAt != t1.Unix() {
		t.Fatalf("the reading was not replaced: %d", all[0].ReportedAt)
	}
	if all[0].SameSince != t0.Unix() {
		t.Fatalf("SameSince moved while the condition held: %d", all[0].SameSince)
	}

	// A changed condition restarts the clock.
	fixed := pending
	fixed.Changes = []Change{{Subsystem: SubsystemHAProxy, Target: "/etc/haproxy/haproxy.cfg", Kind: KindUnchanged}}
	t2 := t1.Add(time.Minute)
	if err := store.Record("gateway", fixed, t2); err != nil {
		t.Fatal(err)
	}
	if o, ok := store.Get("gateway"); !ok || o.SameSince != t2.Unix() {
		t.Fatalf("a changed condition did not restart the clock: %+v", o)
	}
}

// The store redacts too. It is the last function between a client's JSON and
// a file hz will serve back, and it must not depend on the caller.
func TestStoreSanitizesWhatItIsGiven(t *testing.T) {
	store := ObservedStore{Path: t.TempDir() + "/config.json" + ObservedSuffix}
	leak := StateReport{Machine: "gateway", Changes: []Change{{
		Subsystem: SubsystemWireGuard, Target: "/etc/wireguard/wg0.conf", Kind: KindUpdate,
		Detail: "  + PrivateKey = " + fakeWGPrivate,
	}}}
	if err := store.Record("gateway", leak, time.Now()); err != nil {
		t.Fatal(err)
	}
	o, ok := store.Get("gateway")
	if !ok {
		t.Fatal("nothing was stored")
	}
	if strings.Contains(reportText(o.Report), fakeWGPrivate) {
		t.Fatal("the store wrote an unredacted report to disk")
	}
}

// A machine may not file under another's name at this level either. hz
// refuses the request; the store additionally stamps the name it was told to
// use, so a bug upstream cannot mis-file a record.
func TestStoreFilesUnderTheNameItIsGiven(t *testing.T) {
	store := ObservedStore{Path: t.TempDir() + "/config.json" + ObservedSuffix}
	if err := store.Record("gateway", StateReport{Machine: "some-other-box"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	o, ok := store.Get("gateway")
	if !ok {
		t.Fatal("nothing was filed under the caller's machine")
	}
	if o.Report.Machine != "gateway" {
		t.Fatalf("the stored report kept the claimed name %q", o.Report.Machine)
	}
	if _, ok := store.Get("some-other-box"); ok {
		t.Fatal("a record appeared under the name the report claimed")
	}
}

// reportText is everything a report could possibly disclose, as one string.
// Used by every leak check here so a new field cannot be added outside the
// thing being searched.
func reportText(r StateReport) string {
	var b strings.Builder
	b.WriteString(r.Machine)
	b.WriteString("\n")
	b.WriteString(r.Generation)
	b.WriteString("\n")
	b.WriteString(r.AgentVersion)
	for _, c := range r.Changes {
		b.WriteString("\n")
		b.WriteString(string(c.Subsystem))
		b.WriteString("\n")
		b.WriteString(c.Target)
		b.WriteString("\n")
		b.WriteString(string(c.Kind))
		b.WriteString("\n")
		b.WriteString(c.Detail)
	}
	if r.IPTables != nil {
		b.WriteString("\n")
		b.WriteString(r.IPTables.Why)
		for _, rule := range r.IPTables.Live {
			b.WriteString("\n")
			b.WriteString(rule.String())
		}
	}
	return b.String()
}
