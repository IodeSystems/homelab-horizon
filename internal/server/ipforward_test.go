package server

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// THE BYTE-IDENTICAL PROOF FOR IP FORWARDING.
//
// Two writers fill /etc/sysctl.d today and will until hz stops writing files
// at all (item 12 step 5, plan/design/privilege-audit.md §7 B):
//
//	hz     wireguard.EnableIPForwarding — the drop-in, its sweep, the flag
//	agent  the generic section's Files, plus the prune its Dirs claim allows
//
// Same shape as maintenance_pages_test.go and for the same reason: a test that
// checks only the new path proves the new path exists, not that the move is
// safe. What makes it safe is that the two writers cannot disagree, and the
// only way to see that is to run both over identically seeded directories and
// compare names, bytes and modes.
//
// WHAT THIS CANNOT PROVE, said plainly. Both writers are pointed at temp
// directories, so the "runtime flag" they write is a REGULAR FILE and procfs
// semantics are out of the comparison's reach — deliberately, because the
// alternative is a test suite that sets the kernel's forwarding flag on
// whatever machine runs it. The procfs-specific claims are in ipforward.go's
// header; the one that is measurable without writing is the newline, and
// TestTheRuntimeFlagIsDeclaredAsTheKernelPrintsIt measures it against the real
// /proc by reading.

// seedSysctlDir puts in a directory what a real gateway has in one: the
// distribution's own drop-ins, which hz does not own and must never touch, a
// stale hz drop-in under an older number, and near misses around hz's name
// shape.
//
// THE NEAR MISSES ARE THE POINT OF THE SEED. hz sweeps with filepath.Match and
// the agent prunes with path.Match against the same glob; a table of names run
// through both writers is what shows they decide the same way — including the
// empty-prefix case, where the glob's `*` matches nothing.
func seedSysctlDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		// The distribution's. Debian and systemd ship these; hz owns none of
		// them, and a "*.conf" claim would delete every one.
		"99-sysctl.conf":           "# /etc/sysctl.conf lives here\n",
		"10-network-security.conf": "net.ipv4.conf.default.rp_filter=2\n",
		"30-maxmap.conf":           "vm.max_map_count=262144\n",
		// Somebody else's opinion about the very same key. hz must leave it
		// alone: it is not in hz's namespace, and 70- already loses to 99-.
		"60-docker-forward.conf": "net.ipv4.ip_forward=1\n",
		// hz's own drop-in under an older number. BOTH writers must remove
		// it: two files setting one key is how the two writers come to leave
		// different directories.
		"60-hz-ip-forward.conf": "net.ipv4.ip_forward=1\n",
		// The empty-prefix edge of both predicates.
		"-hz-ip-forward.conf": "net.ipv4.ip_forward=1\n",
		// Near misses. Neither writer may take these.
		"70-hz-ip-forward.conf.bak": "a backup, not a drop-in\n",
		"hz-ip-forward.conf":        "no separator before hz, so not the glob\n",
		"70-hz-ip-forward.cfg":      "not .conf\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// hzFillsTheSysctlDir runs hz's OWN writer — the one the fixer button and
// `homelab-horizon check --fix` reach — over a freshly seeded directory, and
// returns the directory it left plus the runtime path it wrote.
func hzFillsTheSysctlDir(t *testing.T) (dropInDir, runtime string) {
	t.Helper()
	root := t.TempDir()
	dropInDir = filepath.Join(root, "sysctl.d")
	runtime = filepath.Join(root, "proc-ip_forward")
	seedSysctlDir(t, dropInDir)
	if err := wireguard.EnableIPForwardingAt(dropInDir, runtime); err != nil {
		t.Fatalf("hz EnableIPForwardingAt: %v", err)
	}
	return dropInDir, runtime
}

// agentFillsTheSysctlDir seeds the directory hz's PAYLOAD points at and
// applies that payload the way hz-agent would — real observer, real plan, real
// Apply — through the served JSON, so a field that did not survive the wire
// shows up here as a difference.
func agentFillsTheSysctlDir(t *testing.T, s *Server) (dropInDir, runtime string, res agent.Result) {
	t.Helper()
	p := s.sysctlPaths()
	seedSysctlDir(t, p.DropInDir)
	return p.DropInDir, p.Runtime, agentPass(t, s)
}

// The proof: hz's writer and the agent's payload leave the SAME directory.
func TestBothWritersLeaveTheSameSysctlDirectory(t *testing.T) {
	s, _ := agentTestServer(t)

	hzDir, hzRuntime := hzFillsTheSysctlDir(t)
	agentDir, agentRuntime, res := agentFillsTheSysctlDir(t, s)
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
		wireguard.IPForwardDropInName,                                  // written
		"99-sysctl.conf", "10-network-security.conf", "30-maxmap.conf", // the distribution's, KEPT
		"60-docker-forward.conf",                          // somebody else's, same key, KEPT
		"70-hz-ip-forward.conf.bak", "hz-ip-forward.conf", // near misses, KEPT
		"70-hz-ip-forward.cfg", // near miss, KEPT
	} {
		if _, ok := hzState[name]; !ok {
			t.Fatalf("hz's directory has no %s: %v", name, hzState.names())
		}
	}
	for _, name := range []string{"60-hz-ip-forward.conf", "-hz-ip-forward.conf"} {
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

	// The live flag is the other half of the declaration, and it is not in
	// the directory above. Compare it on its own terms.
	if got, want := readFileString(t, agentRuntime), readFileString(t, hzRuntime); got != want {
		t.Fatalf("the two writers set the runtime flag differently: agent %q, hz %q", got, want)
	}
	if got := readFileString(t, hzRuntime); got != wireguard.IPForwardOn {
		t.Fatalf("hz wrote %q to the runtime flag, want %q", got, wireguard.IPForwardOn)
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
	want := []string{"-hz-ip-forward.conf", "60-hz-ip-forward.conf"}
	sort.Strings(want)
	if !slicesEqualStr(removed, want) {
		t.Fatalf("the agent removed %v from the drop-in directory, want exactly %v", removed, want)
	}
}

// Rerunning either writer over what the OTHER one left changes nothing. Both
// are on the box until step 5, on their own triggers, so convergence in either
// order is the property that matters.
func TestNeitherForwardingWriterUndoesTheOther(t *testing.T) {
	s, _ := agentTestServer(t)

	dir, runtimePath, _ := agentFillsTheSysctlDir(t, s)
	afterAgent := snapshotDir(t, dir)

	// hz's writer, second, over the agent's result.
	if err := wireguard.EnableIPForwardingAt(dir, runtimePath); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("hz's writer changed what the agent left:\n after %v\n was   %v", after, afterAgent)
	}

	// And the agent, third, over hz's result: nothing more to write, nothing
	// more to remove. A rewrite here would be the newline bug — the agent
	// re-setting a kernel flag that is already right, every pass, forever.
	res := agentPass(t, s)
	for _, p := range res.Wrote {
		if filepath.Dir(p) == dir || p == runtimePath {
			t.Fatalf("the agent rewrote %s after hz had written it; on a real box that is the kernel flag being set on every pass", p)
		}
	}
	if len(res.Removed) > 0 {
		t.Fatalf("the agent removed %v on a converged directory", res.Removed)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("a second agent pass changed the directory:\n after %v\n was   %v", after, afterAgent)
	}
}

// THE ONE PROCFS FACT THIS SUITE CAN MEASURE WITHOUT WRITING.
//
// The whole File model for the runtime flag rests on the agent's desired
// contents being byte-equal to what the kernel prints back, because
// writeIfChanged compares contents: "1" against a readback of "1\n" is a
// rewrite on every pass and a plan that never reports in sync. So this reads
// the real /proc entry — reading it changes nothing — and pins that hz
// declares the shape the kernel actually uses.
//
// Skipped where there is no procfs to ask. A skip is honest here; asserting
// the constant against itself would not be.
func TestTheRuntimeFlagIsDeclaredAsTheKernelPrintsIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("no procfs on " + runtime.GOOS)
	}
	b, err := os.ReadFile(wireguard.IPForwardRuntimePath)
	if err != nil {
		t.Skip("cannot read " + wireguard.IPForwardRuntimePath + ": " + err.Error())
	}
	kernel := string(b)
	if !strings.HasSuffix(kernel, "\n") {
		t.Fatalf("the kernel printed %q with no trailing newline; wireguard.IPForwardOn assumes one", kernel)
	}
	if want := strings.TrimSuffix(wireguard.IPForwardOn, "\n") + "\n"; wireguard.IPForwardOn != want {
		t.Fatalf("IPForwardOn is %q, which is not one value followed by one newline", wireguard.IPForwardOn)
	}
	// And the value the kernel would print for "on" is the value hz declares.
	if on := strings.TrimSpace(kernel); on == "1" && kernel != wireguard.IPForwardOn {
		t.Fatalf("forwarding is on and the kernel prints %q, but hz declares %q — the agent would rewrite the flag on every pass",
			kernel, wireguard.IPForwardOn)
	}
}

// NO DIRECTORY CLAIM IN ANY PAYLOAD MAY REACH /proc.
//
// The prune is the one thing the agent does that cannot be undone, and the
// forwarding declaration is the first payload to name a path under /proc — so
// the directory above it is one claim away from being somewhere the agent
// deletes. This walks EVERY claim in a payload built with the REAL paths (the
// rest of this file runs against temp directories, where /proc is not /proc),
// so a claim introduced anywhere later is checked by construction.
func TestNoDirectoryClaimCanReachKernelState(t *testing.T) {
	s, _ := agentTestServer(t)
	d, _ := servedDesired(t, s)

	// The real declaration, with the production constants rather than this
	// test server's seams — otherwise every path is under t.TempDir() and the
	// check is about a directory /proc is not in.
	real := withIPForwarding(nil, s.cfg(), (&Server{}).sysctlPaths())

	var claims []string
	if d.HAProxy != nil {
		for _, dir := range d.HAProxy.Dirs {
			claims = append(claims, dir.Path)
		}
	}
	for _, dir := range real.Dirs {
		claims = append(claims, dir.Path)
	}

	// THE INSTRUMENT FIRST. An enumerator that found nothing passes this for
	// the wrong reason, and so does a payload that never mentions /proc at all.
	if len(claims) == 0 {
		t.Fatal("no directory claims found; the walk is not looking where the claims are")
	}
	if !payloadNames(agent.Desired{Files: real}, wireguard.IPForwardRuntimePath) {
		t.Fatalf("the declaration does not name %s, so 'no claim reaches /proc' is vacuous here",
			wireguard.IPForwardRuntimePath)
	}

	for _, c := range claims {
		if underKernelState(c) {
			t.Fatalf("a payload claims %s — the agent must never be able to unlink kernel state", c)
		}
	}
}

// AND THE AGENT REFUSES SUCH A CLAIM EVEN WHEN A PAYLOAD CARRIES ONE.
//
// The test above says hz does not write the claim; this says writing it would
// not work. The bound has to be the second thing, not the first: a Desired is
// a plain struct and a payload is something that arrived over a wire.
//
// Nothing here touches a real filesystem — the observation is a fixture — so
// the refusal is measured on the rule rather than on whether /proc happened to
// be readable.
func TestAClaimOnKernelStateRemovesNothing(t *testing.T) {
	procDir := filepath.Dir(wireguard.IPForwardRuntimePath)
	fixture := func(dir string) agent.Observed {
		return agent.Observed{
			Files: map[string]agent.FileState{},
			Dirs: map[string]agent.DirState{dir: {Exists: true, Entries: []agent.DirEntry{
				{Name: "ip_forward", Regular: true},
				{Name: "forwarding", Regular: true},
			}}},
		}
	}

	// THE POSITIVE CONTROL, FIRST. The identical claim on an ordinary
	// directory MUST plan removals — otherwise the refusal below would be
	// indistinguishable from a prune that has stopped working altogether.
	ordinary := "/etc/hz-not-real"
	control := &agent.Desired{Machine: "gw", Files: &agent.FilesSection{
		Dirs: []agent.Directory{{Path: ordinary, Match: []string{"*"}}},
	}}
	var controlRemovals int
	for _, c := range agent.Compute(control, fixture(ordinary)).Changes {
		if c.Kind == agent.KindRemove {
			controlRemovals++
		}
	}
	if controlRemovals != 2 {
		t.Fatalf("the control claim planned %d removals, want 2; the prune is not being exercised", controlRemovals)
	}

	d := &agent.Desired{Machine: "gw", Files: &agent.FilesSection{
		Files: []agent.File{{Path: wireguard.IPForwardRuntimePath, Mode: 0o644, Contents: wireguard.IPForwardOn}},
		Dirs:  []agent.Directory{{Path: procDir, Match: []string{"*"}}},
	}}
	var said string
	for _, c := range agent.Compute(d, fixture(procDir)).Changes {
		if c.Kind == agent.KindRemove {
			t.Fatalf("a claim on %s planned to remove %s", procDir, c.Target)
		}
		if c.Target == procDir {
			said = c.Detail
		}
	}
	// Refused OUT LOUD. A claim that vanished from the report would leave the
	// payload's author believing the directory is managed.
	if !strings.Contains(said, "kernel state") {
		t.Fatalf("the refused claim on %s reported %q, which does not say why", procDir, said)
	}
}

// hz NOT ROUTING IS NOT hz WANTING FORWARDING OFF.
//
// The middle of ipforward.go's three states, and the one CLAUDE.md §2 is
// about. A box with no WireGuard interface gets the claim and NO files: hz's
// own leftover drop-in is removed, so it stops turning forwarding on at the
// next boot, and the live flag is left exactly where it is — because that flag
// is kernel-wide and docker, libvirt and anything else that bridges a network
// share it.
//
// Agent-only, and that is not a gap: hz's writer is a fixer a human presses,
// so it has no way to express "forwarding is no longer managed here" at all.
// That asymmetry is itself the argument for the hand-over.
func TestForwardingIsUnmanagedWithoutARoutingInterface(t *testing.T) {
	s, _ := agentTestServer(t)
	p := s.sysctlPaths()

	// Converge first, with forwarding managed.
	agentFillsTheSysctlDir(t, s)
	if _, err := os.Stat(filepath.Join(p.DropInDir, wireguard.IPForwardDropInName)); err != nil {
		t.Fatalf("the drop-in is not there to begin with: %v", err)
	}
	flagBefore := readFileString(t, p.Runtime)

	// The box stops routing.
	cfg := *s.cfg()
	cfg.WGInterface = ""
	s.config.Store(&cfg)

	d, _ := servedDesired(t, s)
	for _, f := range d.Files.Files {
		if f.Path == p.Runtime || f.Path == filepath.Join(p.DropInDir, wireguard.IPForwardDropInName) {
			t.Fatalf("hz still lists %s on a box it does not route for", f.Path)
		}
	}
	var claimed bool
	for _, dir := range d.Files.Dirs {
		if dir.Path == p.DropInDir {
			claimed = true
		}
	}
	if !claimed {
		t.Fatalf("hz dropped its claim on %s, so its own leftover drop-in can never be removed", p.DropInDir)
	}

	res := agentPass(t, s)
	if _, err := os.Stat(filepath.Join(p.DropInDir, wireguard.IPForwardDropInName)); !os.IsNotExist(err) {
		t.Fatalf("the drop-in survived; it would turn forwarding on at every boot (err=%v)", err)
	}
	if !slicesContains(res.Removed, filepath.Join(p.DropInDir, wireguard.IPForwardDropInName)) {
		t.Fatalf("the agent's own account does not mention removing the drop-in: %v", res.Removed)
	}
	// The distribution's files are untouched.
	if _, err := os.Stat(filepath.Join(p.DropInDir, "99-sysctl.conf")); err != nil {
		t.Fatalf("hz's stand-down took the distribution's drop-in too: %v", err)
	}
	// And the live flag is where it was. hz does not get to switch off
	// something it has stopped having an opinion about.
	if after := readFileString(t, p.Runtime); after != flagBefore {
		t.Fatalf("the live flag moved from %q to %q when hz stopped managing forwarding", flagBefore, after)
	}
}

// payloadNames reports whether any file in the payload is at this path.
func payloadNames(d agent.Desired, path string) bool {
	if d.Files == nil {
		return false
	}
	for _, f := range d.Files.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

// underKernelState is this test's own predicate for "names /proc or /sys",
// deliberately spelled here rather than borrowed from internal/agent: a guard
// that checks itself with the function it is guarding proves nothing.
func underKernelState(p string) bool {
	c := filepath.Clean(p)
	for _, root := range []string{"/proc", "/sys"} {
		if c == root || strings.HasPrefix(c, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
