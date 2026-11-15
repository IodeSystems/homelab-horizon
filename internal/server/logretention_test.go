package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// THE BYTE-IDENTICAL PROOF FOR THE JOURNALD DROP-IN DIRECTORY.
//
// Two writers fill /etc/systemd/journald.conf.d today and will until hz stops
// writing files at all (item 12 step 5, plan/design/privilege-audit.md §7 B):
//
//	hz     writeJournalDropInAt — the drop-in and its sweep (the fixer button)
//	agent  the generic section's Files, plus the prune its Dirs claim allows
//
// Same shape as maintenance_pages_test.go and ipforward_test.go, and for the
// same reason: a test that checks only the new path proves the new path
// exists, not that the move is safe. What makes it safe is that the two
// writers cannot disagree, and the only way to see that is to run both over
// identically seeded directories and compare names, bytes and modes.
//
// WHAT THIS CANNOT PROVE, AND WHERE THAT IS PROVED INSTEAD. Both writers take
// the drop-in's bytes from renderJournalDropIn, so they AGREE FOR FREE — the
// blind spot the ip-forwarding hand-over found by measurement, where rendering
// net.ipv4.ip_forward=0 left the whole tree green. Nothing below would notice
// Storage=volatile or a misspelt key. That is
// TestTheDropInActuallyKeepsTwelveMonthsOfJournal's job, and it reads the file
// the way journald's own reader in hostfacts.go does.

// seedJournaldDir puts in a directory what a real gateway has in one: OTHER
// PEOPLE'S drop-ins, which hz does not own and must never touch, a stale hz
// drop-in under an older number, and near misses around hz's name shape.
//
// THE OTHER PEOPLE ARE THE POINT OF THE SEED. This directory is shared — a
// distribution package, an operator, a config-management tool all put files in
// it — so a "*.conf" claim would have the agent delete every one of them.
// Running a table of names through both writers is what shows they decide the
// same way, including the empty-prefix case where the glob's `*` matches
// nothing.
func seedJournaldDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		// Somebody else's, and one of them has an opinion about the very same
		// keys. hz owns neither and must leave both alone.
		"10-vendor.conf":   "[Journal]\nRateLimitBurst=5000\n",
		"50-ansible.conf":  "[Journal]\nSystemMaxUse=500M\n",
		"90-operator.conf": "[Journal]\nForwardToSyslog=no\n",
		// hz's own drop-in under an older number. BOTH writers must remove it:
		// two files setting one key is how the two come to leave different
		// directories.
		"50-homelab-horizon.conf": "[Journal]\nStorage=persistent\n",
		// The empty-prefix edge of both predicates.
		"-homelab-horizon.conf": "[Journal]\nStorage=persistent\n",
		// Near misses. Neither writer may take these.
		"99-homelab-horizon.conf.bak": "a backup, not a drop-in\n",
		"homelab-horizon.conf":        "no separator before the name, so not the glob\n",
		"99-homelab-horizon.cfg":      "not .conf\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// journalTestServer is an agentTestServer that MANAGES this machine's journal.
// Without the record hz declares nothing for journald at all, which is
// TestJournalRetentionIsUnmanagedWithoutTheRecord's subject.
func journalTestServer(t *testing.T) *Server {
	t.Helper()
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.JournalRetention = true
	s.config.Store(&cfg)
	return s
}

// hzFillsTheJournaldDir runs hz's OWN writer — the one the fixer button
// reaches — over a freshly seeded directory, and returns the directory it
// left.
func hzFillsTheJournaldDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "journald.conf.d")
	seedJournaldDir(t, dir)
	if err := writeJournalDropInAt(dir); err != nil {
		t.Fatalf("hz writeJournalDropInAt: %v", err)
	}
	return dir
}

// agentFillsTheJournaldDir seeds the directory hz's PAYLOAD points at and
// applies that payload the way hz-agent would — real observer, real plan, real
// Apply — through the served JSON, so a field that did not survive the wire
// shows up here as a difference.
func agentFillsTheJournaldDir(t *testing.T, s *Server) (string, agent.Result) {
	t.Helper()
	dir := s.journaldPaths().DropInDir
	seedJournaldDir(t, dir)
	return dir, agentPass(t, s)
}

// The proof: hz's writer and the agent's payload leave the SAME directory.
func TestBothWritersLeaveTheSameJournaldDirectory(t *testing.T) {
	s := journalTestServer(t)

	hzDir := hzFillsTheJournaldDir(t)
	agentDir, res := agentFillsTheJournaldDir(t, s)
	if hzDir == agentDir {
		t.Fatal("both writers ran over the same directory; the comparison would be a tautology")
	}

	hzState := snapshotDir(t, hzDir)
	agentState := snapshotDir(t, agentDir)

	// THE COMPARISON IS ONLY WORTH ANYTHING IF THE DIRECTORY IS INTERESTING.
	// Two directories neither writer touched are byte-identical too. Pin what
	// has to be in there first, so a scenario that quietly stopped exercising
	// the writers fails here rather than passing as a match.
	for _, name := range []string{
		journalDropInName,                                       // written
		"10-vendor.conf", "50-ansible.conf", "90-operator.conf", // somebody else's, KEPT
		"99-homelab-horizon.conf.bak", "homelab-horizon.conf", // near misses, KEPT
		"99-homelab-horizon.cfg", // near miss, KEPT
	} {
		if _, ok := hzState[name]; !ok {
			t.Fatalf("hz's directory has no %s: %v", name, hzState.names())
		}
	}
	for _, name := range []string{"50-homelab-horizon.conf", "-homelab-horizon.conf"} {
		if _, ok := hzState[name]; ok {
			t.Fatalf("hz did not sweep %s, so the scenario is not exercising the prune", name)
		}
	}

	// And now the whole directory, name by name.
	if got, want := agentState.names(), hzState.names(); !slicesEqualStr(got, want) {
		t.Fatalf("the two writers leave different files:\n agent %v\n hz    %v", got, want)
	}
	for _, name := range hzState.names() {
		if agentState[name] != hzState[name] {
			t.Fatalf("%s differs between the writers:\n agent %s\n hz    %s",
				name, agentState[name], hzState[name])
		}
	}

	// Cross-check the agent's own account against the directory. A prune that
	// silently did nothing and a comparison that was never reached look
	// identical from the filesystem alone.
	var removed []string
	for _, p := range res.Removed {
		if filepath.Dir(p) == agentDir {
			removed = append(removed, filepath.Base(p))
		}
	}
	sort.Strings(removed)
	want := []string{"-homelab-horizon.conf", "50-homelab-horizon.conf"}
	sort.Strings(want)
	if !slicesEqualStr(removed, want) {
		t.Fatalf("the agent removed %v from the drop-in directory, want exactly %v", removed, want)
	}
}

// THE FILE IS NOTHING WITHOUT THE POKE.
//
// journald re-reads its configuration at start, so a drop-in the agent places
// and nobody restarts takes effect at the next reboot — while the PCI card,
// which reads the CONFIGURATION and not the running journal
// (hostfacts.readJournalState), already reports 10.5.1 met. hz's own writer
// restarts the unit in the same request; the payload has to say so too.
func TestThePayloadPokesJournald(t *testing.T) {
	s := journalTestServer(t)
	d, _ := servedDesired(t, s)
	if d.Files == nil {
		t.Fatal("no generic section at all")
	}
	for _, u := range d.Files.Units {
		if u.Name != journaldUnit {
			continue
		}
		if u.Action != agent.UnitRestart {
			t.Fatalf("the payload names %s with action %q; an empty action pokes nothing and the "+
				"retention setting would not apply until the next reboot", journaldUnit, u.Action)
		}
		return
	}
	t.Fatalf("the payload places the drop-in and pokes nothing: %+v", d.Files.Units)
}

// Rerunning either writer over what the OTHER one left changes nothing. Both
// are on the box until step 5, on their own triggers, so convergence in either
// order is the property that matters.
//
// It is DELIBERATELY BLIND TO A WIDENED CLAIM, exactly as its maintenance-page
// and ip-forwarding counterparts are and for the same reason: a directory both
// writers converge on is still converged when they agree on the wrong set. The
// set is the compare's job.
func TestNeitherJournalWriterUndoesTheOther(t *testing.T) {
	s := journalTestServer(t)

	dir, _ := agentFillsTheJournaldDir(t, s)
	afterAgent := snapshotDir(t, dir)

	// hz's writer, second, over the agent's result.
	if err := writeJournalDropInAt(dir); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("hz's writer changed what the agent left:\n after %v\n was   %v", after, afterAgent)
	}

	// And the agent, third, over hz's result: nothing more to write, nothing
	// more to remove. A rewrite here would restart the journal on every pass.
	res := agentPass(t, s)
	for _, p := range res.Wrote {
		if filepath.Dir(p) == dir {
			t.Fatalf("the agent rewrote %s after hz had written it; on a real box that is "+
				"systemd-journald being restarted on every pass", p)
		}
	}
	if len(res.Removed) > 0 {
		t.Fatalf("the agent removed %v on a converged directory", res.Removed)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("a second agent pass changed the directory:\n after %v\n was   %v", after, afterAgent)
	}
}

// WHAT THE FILE MEANS, NOT THAT TWO WRITERS AGREE ABOUT IT.
//
// Both writers take these bytes from renderJournalDropIn, so every comparison
// in this file moves with the constant: rendering Storage=volatile, or
// misspelling MaxRetentionSec, leaves all of them green. That is the exact
// blind spot the ip-forwarding hand-over measured and closed with
// TestTheDropInActuallyEnablesForwarding, and this is its counterpart.
//
// So this test asks the file what it MEANS, through the real consumers:
// journalPersistence and parseSystemdDuration are the two functions
// readJournalState feeds the PCI control from. The key/value scan is spelled
// HERE rather than borrowed from journaldSetting on purpose — a guard that
// parses with the function it is guarding proves nothing — but the verdicts
// are hz's own.
//
// Controls, each of which reddens this and nothing else in the file:
// Storage=volatile; MaxRetentionSec=1month (three months short of the twelve
// PCI DSS 10.5.1 asks for); a misspelt key; dropping the [Journal] header so
// journald reads the settings as belonging to no section.
func TestTheDropInActuallyKeepsTwelveMonthsOfJournal(t *testing.T) {
	body := renderJournalDropIn()

	settings, section := journalSettingsInTest(body)
	if section != "Journal" {
		t.Fatalf("the settings are under [%s]; journald only reads [Journal], so this file configures nothing", section)
	}

	// Persistent, and asked with dirExists=false so the answer comes from the
	// SETTING. A file that relied on /var/log/journal already being there
	// would pass with Storage=auto, which is the shipped default and the state
	// this fixer exists to leave.
	if !journalPersistence(settings["Storage"], false) {
		t.Fatalf("Storage=%q does not make the journal survive a reboot; the audit trail dies with the next one",
			settings["Storage"])
	}

	// Twelve months, the number the requirement names.
	const twelveMonths = 365 * 24 * time.Hour
	if got := parseSystemdDuration(settings["MaxRetentionSec"]); got < twelveMonths {
		t.Fatalf("MaxRetentionSec=%q reads as %s; PCI DSS 10.5.1 wants twelve months (%s)",
			settings["MaxRetentionSec"], got, twelveMonths)
	}

	// And a cap, because the other half of "a year of logs" is a disk that
	// fills. Any bound will do; the absence of one is the finding.
	if cap := settings["SystemMaxUse"]; cap == "" || cap == "0" {
		t.Fatalf("SystemMaxUse=%q sets no size cap; twelve months of logs can fill the disk this gateway routes from", cap)
	}
}

// journalSettingsInTest reads key=value pairs the way journald resolves a
// drop-in — last writer wins, comments are shipped defaults and not settings —
// and reports which section they landed in.
//
// Deliberately spelled here rather than calling journaldSetting: that function
// shells out to systemd-analyze and, more to the point, a guard that checks
// itself with the code under test proves nothing.
func journalSettingsInTest(body string) (map[string]string, string) {
	out := map[string]string{}
	section := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return out, section
}

// hz NOT MANAGING THE JOURNAL IS NOT hz WANTING A SHORT ONE.
//
// Without the record, hz emits NOTHING for journald: no file, no claim, no
// unit. Absence is the honest answer, and here it is also the safe one — a
// claim with no file is an instruction to DELETE the drop-in, and every
// machine whose operator pressed the button before Config.JournalRetention
// existed is in exactly that state. That is what adoptJournalRetention is for,
// and this is the state it rescues them from.
func TestJournalRetentionIsUnmanagedWithoutTheRecord(t *testing.T) {
	s, _ := agentTestServer(t) // JournalRetention unset
	p := s.journaldPaths()
	seedJournaldDir(t, p.DropInDir)

	d, _ := servedDesired(t, s)
	if d.Files == nil {
		t.Fatal("no generic section at all; the static unit should still be declared")
	}
	for _, f := range d.Files.Files {
		if filepath.Dir(f.Path) == p.DropInDir {
			t.Fatalf("hz lists %s on a machine whose journal it does not manage", f.Path)
		}
	}
	for _, dir := range d.Files.Dirs {
		if dir.Path == p.DropInDir {
			t.Fatalf("hz claims %s with nothing to put in it; the agent would delete hz's own drop-in", dir.Path)
		}
	}
	for _, u := range d.Files.Units {
		if u.Name == journaldUnit {
			t.Fatalf("hz asks for a %s restart on a machine whose journal it does not manage", journaldUnit)
		}
	}

	// And the agent, applying that payload, leaves the directory exactly as it
	// found it — including hz's own stale drop-in, which is not hz's to remove
	// while hz has no opinion.
	before := snapshotDir(t, p.DropInDir)
	agentPass(t, s)
	if after := snapshotDir(t, p.DropInDir); !stateEqual(after, before) {
		t.Fatalf("an unmanaged journald directory changed under the agent:\n after %v\n was   %v", after, before)
	}
}

// FIRST SIGHTING ADOPTS, NEVER ACTS.
//
// The machine that has hz's drop-in and no record is the office gateway: the
// button was pressed long before the record existed. Adoption is what lets hz
// DECLARE what is already true, so `hz-agent diff` carries the section instead
// of being in sync by omission (§7 D). It writes no file and pokes no unit.
func TestAdoptingAnExistingDropInRecordsItAndActsOnNothing(t *testing.T) {
	s, _ := agentTestServer(t)
	p := s.journaldPaths()
	seedJournaldDir(t, p.DropInDir)
	if err := os.WriteFile(filepath.Join(p.DropInDir, journalDropInName),
		[]byte(renderJournalDropIn()), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, p.DropInDir)

	s.adoptJournalRetention()

	if !s.cfg().JournalRetention {
		t.Fatal("hz found its own drop-in and did not record that it manages this machine's journal")
	}
	if after := snapshotDir(t, p.DropInDir); !stateEqual(after, before) {
		t.Fatalf("adoption changed the directory; it must only record:\n after %v\n was   %v", after, before)
	}

	// And now the declaration exists, which is the whole point.
	d, _ := servedDesired(t, s)
	if !payloadNames(d, filepath.Join(p.DropInDir, journalDropInName)) {
		t.Fatal("after adoption the payload still does not carry the drop-in")
	}
}

// THE REVERSE MISTAKE CANNOT HAPPEN. A machine with no hz drop-in is not
// adopted into wanting one — otherwise every gateway in the fleet would
// silently acquire a PCI retention policy nobody asked for at its next
// restart.
func TestAdoptionDoesNotInventAnOpinion(t *testing.T) {
	s, _ := agentTestServer(t)
	seedJournaldDir(t, s.journaldPaths().DropInDir) // other people's files only

	s.adoptJournalRetention()

	if s.cfg().JournalRetention {
		t.Fatal("hz adopted a journal policy on a machine that has none of hz's drop-ins")
	}
}
