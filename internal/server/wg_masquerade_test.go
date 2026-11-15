package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// THE BYTE-IDENTICAL PROOF FOR wg0.conf's MASQUERADE CLAUSE.
//
// Two writers heal that clause today and will until hz stops writing files at
// all (item 12 step 5, plan/design/privilege-audit.md §7 B):
//
//	hz     reconcileIPTables → WGConfig.HealMasquerade, on the 60s health tick
//	agent  the WireGuard section's Files, written by Apply
//
// Same shape as maintenance_pages_test.go, ipforward_test.go and
// logretention_test.go, and for the same reason: a test that checks only the
// new path proves the new path exists, not that the move is safe. What makes
// it safe is that the two writers cannot disagree about which interface the
// gateway NATs through, and the only way to see that is to run both over
// identically seeded files and compare bytes and mode.
//
// THE INTERFACE NAMES ARE THE REAL ONES. The gateway is about to be moved to
// wireless — enx00051b94b7cc, a USB ethernet adapter, becomes a wlan* name —
// and a stale MASQUERADE bound to an interface that no longer exists is the
// founding outage of this repo. A fixture on eth0/eth1 would test the same
// code and say nothing about the move that is actually queued.

const (
	wgMovedFromIface = "enx00051b94b7cc"
	wgMovedToIface   = "wlan0"
)

// seedWGConf is what a real gateway's wg0.conf looks like: an operator's
// comment, an unmodelled directive, spacing hz does not own, a PostUp and
// PostDown that both NAT through the OLD interface, and a peer below.
func seedWGConf(t *testing.T, path, iface string) string {
	t.Helper()
	contents := "# managed by homelab-horizon — do not hand-edit\n" +
		"[Interface]\n" +
		"PrivateKey = " + fakeWGPrivateKey + "\n" +
		"Address = 10.100.0.1/24\n" +
		"ListenPort=51820\n" +
		"MTU = 1420\n" +
		"PostUp = " + wireguard.ExpectedPostUp(iface) + "\n" +
		"PostDown = " + wireguard.ExpectedPostDown(iface) + "\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + fakeWGPeerKey + "\n" +
		"AllowedIPs = 10.100.0.2/32\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return contents
}

// movedGateway is a test hz that has just been carried to the new desk: its
// config still records the old interface, its wg0.conf still NATs through it,
// and DetectDefaultInterface now answers wlan0.
func movedGateway(t *testing.T) *Server {
	t.Helper()
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.WGInterface = "wg0"
	cfg.VPNRange = "10.100.0.0/24"
	cfg.WGConfigPath = filepath.Join(t.TempDir(), "wireguard", "wg0.conf")
	cfg.LastLocalIface = wgMovedFromIface
	cfg.LastLanCIDR = "192.168.1.0/24"
	s.config.Store(&cfg)
	s.egressIface = routedOut(wgMovedToIface)
	seedWGConf(t, cfg.WGConfigPath, wgMovedFromIface)
	s.wg = wireguard.NewConfig(cfg.WGConfigPath, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	return s
}

// hzHealsTheWireGuardConfig runs hz's OWN writer — the one reconcileIPTables
// reaches on the health tick — over a freshly seeded file, and returns its path.
func hzHealsTheWireGuardConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wireguard", "wg0.conf")
	seedWGConf(t, path, wgMovedFromIface)
	w := wireguard.NewConfig(path, "wg0")
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.HealMasquerade(wgMovedToIface); err != nil {
		t.Fatalf("hz HealMasquerade: %v", err)
	}
	return path
}

func fileSnapshot(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return "mode=" + fi.Mode().Perm().String() + " bytes=" + string(b)
}

// The proof: hz's writer and the agent's payload leave the SAME wg0.conf.
func TestBothWritersLeaveTheSameWireGuardConfig(t *testing.T) {
	s := movedGateway(t)
	agentPath := s.cfg().WGConfigPath
	hzPath := hzHealsTheWireGuardConfig(t)
	if hzPath == agentPath {
		t.Fatal("both writers ran over the same file; the comparison would be a tautology")
	}

	res := agentPass(t, s)

	// THE COMPARISON IS ONLY WORTH ANYTHING IF SOMETHING HAPPENED. Two files
	// neither writer touched are byte-identical too, so pin first that each
	// writer actually moved its own file off the old interface.
	hzState := fileSnapshot(t, hzPath)
	agentState := fileSnapshot(t, agentPath)
	for name, state := range map[string]string{"hz": hzState, "agent": agentState} {
		if strings.Contains(state, wgMovedFromIface) {
			t.Fatalf("%s left the old interface in wg0.conf, so the heal is not being exercised:\n%s", name, state)
		}
		if !strings.Contains(state, "-o "+wgMovedToIface+" -j MASQUERADE") {
			t.Fatalf("%s did not point the MASQUERADE at %s:\n%s", name, wgMovedToIface, state)
		}
	}
	if !slicesContains(res.Wrote, agentPath) {
		t.Fatalf("the agent's own account does not mention writing %s: %v", agentPath, res.Wrote)
	}

	// And now the whole file: bytes AND mode. The mode matters more here than
	// anywhere else this comparison is made — wg0.conf holds the machine's
	// private key, and a 0644 from either writer is the key readable by every
	// account on the box.
	if agentState != hzState {
		t.Fatalf("the two writers leave different files:\n agent %q\n hz    %q", agentState, hzState)
	}
	if !strings.HasPrefix(agentState, "mode=-rw-------") {
		t.Fatalf("wg0.conf is not 0600 after the agent wrote it: %s", agentState)
	}
}

// ⚠ THE MODE COLUMN ABOVE IS ALMOST A TAUTOLOGY, AND THIS IS WHY IT IS NOT
// LEFT AT THAT.
//
// Declaring the payload's wg0.conf 0644 reddened `TestWireGuardSectionCrosses\
// AsTheFileHZMaintains` — which reads the payload — and left the on-disk
// compare GREEN. os.WriteFile applies a permission only when it CREATES the
// inode (agent/apply.go writeIfChanged), and hz READS wg0.conf to build the
// payload, so on the happy path the file always already exists and neither
// writer's declared mode is ever applied to it. Same shape as /proc's
// unenforceable 0644 in ipforward.go, arrived at from the other direction.
//
// The mode is therefore load-bearing in exactly one situation — the file is
// absent when the agent writes it — and that situation is real: a restored box,
// a wiped /etc/wireguard, an operator who deleted the file to start again. It
// is also the only situation in which getting it wrong publishes the machine's
// private key to every account on the box. So it is driven here explicitly,
// with the target removed between the poll and the apply.
func TestTheAgentCreatesAMissingWireGuardConfigAt0600(t *testing.T) {
	s := movedGateway(t)
	path := s.cfg().WGConfigPath

	d, _ := servedDesired(t, s)
	if d.WireGuard == nil {
		t.Fatal("no WireGuard section to apply")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	gens := agentGenerations(t, s)
	obs := agent.NewSystemObserver().WithGenerations(gens).Observe(&d)
	res, err := agent.Apply(&d, agent.Compute(&d, obs), obs, &noopReloader{}, gens)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("agent Apply: %v %v", err, res.Errors)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the agent did not re-create wg0.conf: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the agent created wg0.conf at %04o; it holds the machine's private key and every account on the box can now read it",
			fi.Mode().Perm())
	}
	// And it re-created the HEALED file, not the old one.
	if got := readFileString(t, path); strings.Contains(got, wgMovedFromIface) {
		t.Fatalf("the re-created wg0.conf NATs through the old interface:\n%s", got)
	}
}

// Rerunning either writer over what the OTHER one left changes nothing. Both
// are on the box until step 5, on their own triggers, so convergence in either
// order is the property that matters.
func TestNeitherMasqueradeWriterUndoesTheOther(t *testing.T) {
	s := movedGateway(t)
	path := s.cfg().WGConfigPath

	agentPass(t, s)
	afterAgent := fileSnapshot(t, path)

	// hz's writer, second, over the agent's result.
	w := wireguard.NewConfig(path, "wg0")
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}
	changed, err := w.HealMasquerade(wgMovedToIface)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("hz rewrote a wg0.conf the agent had already healed; on a real box that is a write and a wg syncconf every 60 seconds")
	}
	if after := fileSnapshot(t, path); after != afterAgent {
		t.Fatalf("hz's writer changed what the agent left:\n after %q\n was   %q", after, afterAgent)
	}

	// And the agent, third, over hz's result: nothing more to write.
	res := agentPass(t, s)
	if slicesContains(res.Wrote, path) {
		t.Fatal("the agent rewrote wg0.conf after hz had written it; on a real box that is the tunnel being resynced on every pass")
	}
	if after := fileSnapshot(t, path); after != afterAgent {
		t.Fatalf("a second agent pass changed the file:\n after %q\n was   %q", after, afterAgent)
	}
}

// WHAT THE DECLARED PostUp ACTUALLY DOES, read the way wg-quick reads it.
//
// The blind spot both earlier hand-overs found the hard way: when both writers
// take their bytes from one function, agreement is FREE and says nothing about
// whether the artifact MEANS the right thing. ip-forwarding rendered
// `net.ipv4.ip_forward=0` with the whole tree green; log retention did the same
// with four settings.
//
// So this does not compare strings with the heal. It splits the PostUp into the
// shell commands wg-quick runs, finds the one that installs a MASQUERADE, and
// reads its `-o` argument — the same token `iptables` would read — then checks
// that every OTHER command in the line came through unchanged. Spelled here by
// hand; borrowing the package's own regex would be asking the heal to grade
// itself.
func TestTheDeclaredPostUpNATsThroughTheInterfaceTheBoxUses(t *testing.T) {
	s := movedGateway(t)
	before := wgPostUp(t, readFileString(t, s.cfg().WGConfigPath))

	d, _ := servedDesired(t, s)
	if d.WireGuard == nil || len(d.WireGuard.Files) != 1 {
		t.Fatalf("no wg0.conf in the payload: %+v", d.WireGuard)
	}
	after := wgPostUp(t, d.WireGuard.Files[0].Contents)

	beforeCmds, afterCmds := shellCommands(before), shellCommands(after)
	if len(beforeCmds) != len(afterCmds) {
		t.Fatalf("the declared PostUp runs %d commands, the file's runs %d", len(afterCmds), len(beforeCmds))
	}

	var masqueraded []string
	for i, cmd := range afterCmds {
		if !strings.HasSuffix(cmd, "-j MASQUERADE") {
			// Everything that is not the NAT rule must be untouched: the
			// chain creates, the FORWARD jumps, the INPUT jump. A heal that
			// dropped the WG-INPUT jump would leave an MFA-jailed peer able
			// to reach the gateway's own listeners at the next interface up.
			if cmd != beforeCmds[i] {
				t.Errorf("the heal changed a command that carries no MASQUERADE:\n before %q\n after  %q", beforeCmds[i], cmd)
			}
			continue
		}
		masqueraded = append(masqueraded, cmd)
		fields := strings.Fields(cmd)
		var out string
		for j, f := range fields {
			if f == "-o" && j+1 < len(fields) {
				out = fields[j+1]
			}
		}
		if out == "" {
			t.Fatalf("the MASQUERADE command names no out-interface at all: %q", cmd)
		}
		if out != wgMovedToIface {
			t.Errorf("the declared PostUp NATs through %q; this box routes out of %q", out, wgMovedToIface)
		}
	}
	if len(masqueraded) != 1 {
		t.Fatalf("the PostUp installs %d MASQUERADE rules, want exactly 1: %v", len(masqueraded), masqueraded)
	}

	// The PostDown has to name the same interface, or the teardown leaves the
	// rule it was supposed to remove.
	down := wgPostDown(t, d.WireGuard.Files[0].Contents)
	if !strings.Contains(down, "-o "+wgMovedToIface+" -j MASQUERADE") {
		t.Errorf("PostDown removes a MASQUERADE for a different interface than PostUp installs:\n%s", down)
	}
	if strings.Contains(down, wgMovedFromIface) {
		t.Errorf("PostDown still names the old interface:\n%s", down)
	}
}

// NO DEFAULT ROUTE MEANS PUBLISH THE FILE AS IT IS, not publish nothing and not
// publish a blanked clause.
//
// This is the same instant iptablesSectionFor stands down at — a route flap, a
// boot before the default route settles — and the two answers have to agree
// about the same moment. Blanking the clause would replace a stale-but-nameable
// rule with `-o  -j MASQUERADE`, which wg-quick would fail to install at all;
// withholding the section would tell the agent hz manages no tunnel here.
func TestTheDeclaredConfigStandsDownWithNoDefaultRoute(t *testing.T) {
	s := movedGateway(t)
	// NOT a skip on a machine that has a route: the override says "no default
	// route" outright, so this case is reachable everywhere. Expressing it
	// needed the seam to be a pointer — see Server.egressIface.
	s.egressIface = routedOut("")
	onDisk := readFileString(t, s.cfg().WGConfigPath)

	d, _ := servedDesired(t, s)
	if d.WireGuard == nil {
		t.Fatal("hz withheld the whole WireGuard section during a route flap; the agent reads that as \"hz manages no tunnel here\"")
	}
	if got := d.WireGuard.Files[0].Contents; got != onDisk {
		t.Fatalf("hz rewrote wg0.conf with no interface to name:\n got %q\nwant %q", got, onDisk)
	}
	// And the firewall stood down in the same payload, from the same read.
	if d.IPTables == nil || !d.IPTables.StoodDown {
		t.Fatalf("the firewall did not stand down in the same payload: %+v", d.IPTables)
	}
}

// PERSISTING LastLocalIface WITHOUT HEALING BLINDS THE STALE SET.
//
// This is the one thing that must not be left behind when hz stops reconciling
// (wg_masquerade.go's header). reconcileIPTables heals FIRST and persists
// AFTER, in the same pass, and iptables.StaleRules derives the entire
// old-interface rule set from the field it persists. Advance it without healing
// and the old MASQUERADE stops being STALE — the one class Reconcile deletes —
// and becomes UNKNOWN, which nothing ever removes.
//
// So the persist is part of axis 2 and moves with the heal at step 5. It is not
// axis 1's observe+persist, whatever a checklist line says.
func TestPersistingLastLocalIfaceWithoutHealingBlindsTheStaleSet(t *testing.T) {
	s := movedGateway(t)
	cfg := s.cfg()
	oldMasq := iptables.Rule{Table: "nat", Chain: "POSTROUTING",
		Args: []string{"-o", wgMovedFromIface, "-j", "MASQUERADE"}}
	live := []iptables.Rule{oldMasq}
	expected := iptables.ExpectedRules(iptables.Inputs{
		WGInterface: cfg.WGInterface,
		OutIface:    wgMovedToIface,
		VPNRange:    cfg.VPNRange,
	})

	// While hz still records where the box WAS, the rule is condemned.
	before := iptables.Classify(live, expected, iptables.StaleRules(cfg, nil, "", ""), nil)
	if before[0].State != iptables.StateStale {
		t.Fatalf("with LastLocalIface=%s the old MASQUERADE reads %q, want %q — the premise of this test is gone",
			wgMovedFromIface, before[0].State, iptables.StateStale)
	}

	// The persist alone, with nothing healed.
	advanced := *cfg
	advanced.LastLocalIface = wgMovedToIface
	advanced.LastLanCIDR = "192.168.5.0/24"
	after := iptables.Classify(live, expected, iptables.StaleRules(&advanced, nil, "", ""), nil)
	if after[0].State != iptables.StateUnknown {
		t.Fatalf("after advancing LastLocalIface the old MASQUERADE reads %q; this test is asserting the wrong hazard", after[0].State)
	}

	// And unknown is not a thing Reconcile will ever delete — which is the
	// whole point, asserted against the commands rather than the verdict.
	report := iptables.Reconcile(live, expected, iptables.StaleRules(&advanced, nil, "", ""), nil, wgMovedToIface, wgMovedToIface)
	for _, r := range report.Deleted {
		if r.Canonical() == oldMasq.Canonical() {
			t.Fatal("Reconcile deleted an unknown rule; the stale/unknown boundary has moved and this hazard no longer exists")
		}
	}
}

// hz KEEPS HEALING UNTIL STEP 5, and the un-armed window is what that buys.
//
// `cmd/hz-agent/install.go` emits no `--apply`, so between this hand-over and
// item 12 step 4 the agent writes nothing at all. If hz's own heal had been
// removed in the same commit that declared it, nothing on the box would heal
// wg0.conf and the next reboot would come up on the old interface's NAT.
func TestHZStillHealsTheFileItDeclares(t *testing.T) {
	s := movedGateway(t)
	path := s.cfg().WGConfigPath

	// hz's writer, on its own, with no agent anywhere near the box.
	changed, err := s.wg.HealMasquerade(s.defaultIface())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("hz did not heal wg0.conf after the interface changed")
	}
	if got := readFileString(t, path); strings.Contains(got, wgMovedFromIface) {
		t.Fatalf("hz's own heal left the old interface in the file:\n%s", got)
	}

	// And it takes the interface from the same place the payload does.
	d, _ := servedDesired(t, s)
	if d.WireGuard.Files[0].Contents != readFileString(t, path) {
		t.Fatal("hz's writer and hz's payload disagree about the healed file")
	}
}

// A machine hz did not compute for gets no wg0.conf at all, healed or
// otherwise — the file is read off the local filesystem and hz cannot read
// another machine's.
func TestARemoteMachineGetsNoHealedWireGuardConfig(t *testing.T) {
	s := movedGateway(t)
	d := s.desiredFor("some-other-box")
	if d.WireGuard != nil {
		t.Fatalf("hz published a wg0.conf for a machine it cannot read: %+v", d.WireGuard)
	}
}

// routedOut is the egress-interface override, spelled so that "" is a value
// and not an absence.
func routedOut(iface string) *string { return &iface }

// wgPostUp / wgPostDown pull one directive out of a config the way a reader of
// the file would, without the package's parser.
func wgPostUp(t *testing.T, conf string) string   { return wgDirective(t, conf, "PostUp") }
func wgPostDown(t *testing.T, conf string) string { return wgDirective(t, conf, "PostDown") }

func wgDirective(t *testing.T, conf, key string) string {
	t.Helper()
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key) {
			continue
		}
		rest := strings.TrimPrefix(trimmed, key)
		if i := strings.Index(rest, "="); i >= 0 {
			return strings.TrimSpace(rest[i+1:])
		}
	}
	t.Fatalf("no %s directive in:\n%s", key, conf)
	return ""
}

// shellCommands splits a PostUp/PostDown the way the shell wg-quick hands it to
// does: on `;`, trimmed, empties dropped.
func shellCommands(line string) []string {
	var out []string
	for _, c := range strings.Split(line, ";") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// assert the payload really is the section type we think it is — a compile-time
// nudge so a section rename does not turn these tests into no-ops.
var _ = func(d *agent.Desired) *agent.WireGuardSection { return d.WireGuard }

// ⚠ THE CONTROL THAT UNDER-FIRED, AND WHAT CLOSES IT.
//
// Replacing `s.wg.HealMasquerade(newIface)` in reconcileIPTables with a
// hard-coded "nothing changed" left the WHOLE TREE GREEN. Every test above
// exercises the heal by calling it, so they prove the function works and say
// nothing about whether hz's 60-second loop still reaches it — which is the
// ban hand-over's lesson for the third time: a guard about what a delete is
// ALLOWED to do stays green when the delete stops happening.
//
// It matters more here than it did there, because the agent is INERT until
// item 12 step 4. hz quietly losing this call is not "two writers become one",
// it is NO writer: wg0.conf keeps the old interface's MASQUERADE, every reboot
// comes up NATing through an interface that does not exist, and `hz-agent
// diff` reports in sync because the payload it compares against is generated
// from the same stale file.
//
// WHY THIS IS A SOURCE GUARD AND NOT A DRIVE, said plainly. Driving
// reconcileIPTables for real runs iptables.Reconcile, which shells `iptables`,
// and runs dnsmasq's Reload, which writes a systemd unit and restarts the
// daemon — on whatever machine runs the suite. internal/iptables' runner is
// package-private, so there is no seam to swap from here. The other tests in
// this tree buy their safety with a path field (sysctlDir, journaldDir); there
// is no equivalent for "do not actually run iptables", and inventing one means
// putting a test seam through the founding self-heal. So what is pinned is the
// call site, structurally: the heal is a statement of reconcileIPTables' own
// body, not nested inside one of the legacy-migration branches, so it runs on
// EVERY pass and not only on a box that matched a 2024 PostUp template.
func TestReconcileIPTablesStillCallsTheHeal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reconcile_iptables.go", nil, 0)
	if err != nil {
		t.Fatalf("parse reconcile_iptables.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "reconcileIPTables" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("no reconcileIPTables in reconcile_iptables.go — this guard is looking at the wrong file")
	}

	// THE INSTRUMENT FIRST. A walker that finds nothing would pass the
	// nested-call check below for the wrong reason, so prove it can see a call
	// that is definitely in there.
	if !callsMethodIn(fn.Body.List, "GetPostUp") {
		t.Fatal("the walker cannot find GetPostUp, which reconcileIPTables definitely calls; it is not reading the function body")
	}

	// Top-level statements only — deliberately NOT a walk of the whole body.
	// A HealMasquerade tucked inside `if isLegacyBypassPostUp(...)` would
	// satisfy a whole-body walk while healing only the handful of boxes still
	// on a pre-WG-FORWARD template.
	if !callsMethodIn(fn.Body.List, "HealMasquerade") {
		t.Fatal("reconcileIPTables does not call HealMasquerade at the top level of its body.\n" +
			"hz is the ONLY writer of wg0.conf's MASQUERADE clause until item 12 step 4 arms the agent " +
			"(cmd/hz-agent/install.go emits no --apply), so without this call nothing on the box heals the " +
			"file and every reboot comes up on the old interface's NAT.")
	}
}

// callsMethodIn reports whether any of these statements — at this level, not
// inside a nested block — contains a call to a method of the given name.
//
// Spelled here rather than borrowed: it walks each statement's own expressions
// and refuses to descend into a BlockStmt, which is the property being
// asserted.
func callsMethodIn(stmts []ast.Stmt, method string) bool {
	found := false
	for _, st := range stmts {
		ast.Inspect(st, func(n ast.Node) bool {
			if found {
				return false
			}
			switch v := n.(type) {
			case *ast.BlockStmt:
				// Do not descend. A call inside a branch is not "every pass".
				return false
			case *ast.CallExpr:
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
					found = true
					return false
				}
			}
			return true
		})
	}
	return found
}
