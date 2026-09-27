package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// THE PROOF FOR THE BAN HAND-OVER (privilege-audit.md §7 B, §3.2).
//
// Bans are not files, so there is no directory to compare. The move is a
// different shape and so is its proof:
//
//	the request installs NOTHING          TestTheBanRequestPathOnlyEverDeletes
//	recording it DECLARES it              TestABanIsRecordedAndThenDeclared
//	the declaration stops at expiry       TestAnExpiredBanIsNotDeclared
//	lifting it never becomes a DELETE     TestTheServedStaleSetNeverCarriesABan
//
// WHAT IS ALREADY PROVED ELSEWHERE, and is not repeated here: that the rule hz
// declares is byte-identical to the one it used to insert, and that it MEANS
// drop-this-source-in-INPUT, are internal/iptables/bans_test.go's
// TestBanRuleReadbackRoundTrip and TestConfiguredBanClassifiesExpected — both
// of which check the generator against an independently spelled literal rather
// than against itself, which is the property the ip-forwarding hand-over found
// missing in its own compare. That a wiped ban is healed by a reconcile pass
// (the thing that now has to happen, because the request no longer does it) is
// TestReconcileInstallsAMissingBan, at the level of the iptables commands
// issued.

// THE REQUEST PATH RUNS ONE iptables VERB, AND IT IS `-D`.
//
// Asserted by SHAPE over the file's syntax tree, the way shell_guard_test.go
// checks for a shell string, rather than by running anything: the whole point
// of the change is that there is no longer an install to observe, and a test
// that watched for a command that is never issued would pass just as happily
// if the call were still there and iptables merely absent from the test
// machine (CLAUDE.md §15 — an empty result is a claim about the instrument).
//
// `-D` is admitted deliberately and is not a loophole. unbanIP has to remove
// the live rule because a lifted ban classifies UNKNOWN and Reconcile never
// deletes unknown; making it declarative means making it STALE, which would
// widen the delete to every source-DROP in INPUT including an admin's own.
// That is handlers_ban.go's header and TestReconcileLeavesAHandAddedInputDropAlone.
// An `-I`, `-A`, `-C`, a flush or a chain verb appearing here is the
// hand-over being undone.
func TestTheBanRequestPathOnlyEverDeletes(t *testing.T) {
	const file = "handlers_ban.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	// Every argument handed to a command call in this file, IN ORDER, with a
	// non-literal standing in for itself. Order matters: iptables reads a rule
	// spec positionally, and a guard that only collected the set of flags
	// would accept the same tokens naming a different chain.
	var args []string
	var sites int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		sites++
		for _, a := range call.Args {
			lit, ok := a.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				args = append(args, "<expr>")
				continue
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				args = append(args, "<expr>")
				continue
			}
			args = append(args, s)
		}
		return true
	})

	// THE INSTRUMENT FIRST. A parser that found no command calls at all passes
	// every assertion below for the wrong reason, and so would a file that had
	// been renamed out from under this test.
	if sites != 1 {
		t.Fatalf("found %d exec sites in %s, want exactly 1 (unbanIP); "+
			"0 means this guard is not looking at the right file", sites, file)
	}

	// And it is EXACTLY the delete of the rule hz declares. Spelled out here
	// rather than checked flag by flag, because a control found the loose
	// version blind to the thing that matters most: swapping INPUT for FORWARD
	// leaves every flag legal and deletes a rule hz never wrote, while the ban
	// it was meant to lift stays in the kernel for ever.
	want := []string{"iptables", "-D", "INPUT", "-s", "<expr>", "-j", "DROP"}
	if !slicesEqualStr(args, want) {
		t.Errorf("%s runs iptables with\n  %v\nwant exactly\n  %v\n"+
			"(the only verb the web process may still run is the -D that lifts a ban — "+
			"see this file's header: install is declared, removal is not)", file, args, want)
	}

	// The other end of the same rule: what the deleter names and what hz
	// DECLARES have to be one rule, and they are written in two different
	// files. Deriving the expectation above from banRules would have made that
	// agreement free; asserting both against the same independently spelled
	// shape catches either one moving.
	declared := iptables.ExpectedRules(iptables.Inputs{
		WGInterface: "wg0",
		BannedIPs:   []string{"198.51.100.7"},
	})
	if !carriesRule(declared, "filter|INPUT|-s 198.51.100.7/32 -j DROP") {
		t.Errorf("hz declares a ban as something other than a filter INPUT source-DROP, so the "+
			"`iptables -D INPUT -s <ip> -j DROP` above no longer lifts what hz installs:\n%v", declared)
	}

	// AND THE `-D` IS STILL REACHED, which is the half this guard did not have
	// until a control found it missing. Removing the call while leaving the
	// function defined kept the whole tree GREEN: the exec site is still in
	// the file, so everything above passed, and a lifted ban would have gone
	// on dropping packets for ever because nothing else will ever delete an
	// UNKNOWN rule. "Do not widen the delete" and "do not lose the delete" are
	// two different mistakes and each needs its own assertion.
	var called int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "iptablesUnban" {
			called++
		}
		return true
	})
	if called == 0 {
		t.Errorf("%s defines the iptables -D and never calls it; lifting a ban would leave the "+
			"live rule in place for ever, because a lifted ban classifies unknown and Reconcile "+
			"never deletes unknown", file)
	}
}

// RECORDED, THEN DECLARED — the two halves of what banIP now does, end to end.
//
// The request writes a record and nothing else; the payload hz serves to the
// agent carries the rule that record implies. That chain is the hand-over: the
// address stops being reachable because a reconciler read a declaration, not
// because a web request ran iptables.
func TestABanIsRecordedAndThenDeclared(t *testing.T) {
	s, _ := agentTestServer(t)
	const abuser = "198.51.100.7"

	if err := s.banIP(abuser, 0, "brute force", "login", ""); err != nil {
		t.Fatalf("banIP: %v", err)
	}

	// The record.
	var recorded bool
	for _, b := range s.cfg().IPBans {
		if b.IP == abuser {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("banIP returned nil and recorded nothing: %+v", s.cfg().IPBans)
	}

	// The declaration, through the served payload, so the wire is covered.
	d, _ := servedDesired(t, s)
	if d.IPTables == nil {
		t.Fatal("no firewall section; the ban has nowhere to be declared")
	}
	want := "filter|INPUT|-s " + abuser + "/32 -j DROP"
	if !carriesRule(d.IPTables.Expected, want) {
		t.Fatalf("the payload does not declare the ban hz just recorded; want %q in:\n%v",
			want, d.IPTables.Expected)
	}
}

// AN EXPIRED BAN IS NOT DECLARED, AND THAT IS THE WHOLE OF ITS EXPIRY.
//
// activeBanIPs drops it at the source, so no reconciler re-installs the rule
// the 30s reap is on its way to removing. The half that does NOT follow from
// this — taking the live rule out — stays unbanIP's, synchronously, and the
// test below says why it cannot move.
//
// It is also the sharp edge of the new contract: a ban whose timeout is
// shorter than the reconcile interval expires out of the declared set before
// any pass installs it, so no packet is ever dropped. Documented on
// hzclient.BanAdd; demonstrated here.
func TestAnExpiredBanIsNotDeclared(t *testing.T) {
	s, _ := agentTestServer(t)
	const abuser = "198.51.100.7"
	want := "filter|INPUT|-s " + abuser + "/32 -j DROP"

	// One second of ban, recorded two seconds ago.
	cfg := *s.cfg()
	now := time.Now().Unix()
	cfg.IPBans = []config.IPBan{{IP: abuser, Timeout: 1, CreatedAt: now - 2, ExpiresAt: now - 1}}
	s.config.Store(&cfg)

	d, _ := servedDesired(t, s)
	if d.IPTables == nil {
		t.Fatal("no firewall section")
	}
	if carriesRule(d.IPTables.Expected, want) {
		t.Fatalf("hz declares a ban that has already expired; the reconciler would install "+
			"the rule the expiry reap is about to remove:\n%v", d.IPTables.Expected)
	}

	// The control, in the same shape: unexpired, and it IS declared. Without
	// it "not declared" is equally consistent with bans never reaching the
	// payload at all.
	cfg.IPBans = []config.IPBan{{IP: abuser, Timeout: 3600, CreatedAt: now, ExpiresAt: now + 3600}}
	s.config.Store(&cfg)
	d, _ = servedDesired(t, s)
	if !carriesRule(d.IPTables.Expected, want) {
		t.Fatalf("a live ban is not declared either, so this test measures nothing:\n%v", d.IPTables.Expected)
	}
}

// THE DELETE WAS NOT WIDENED, CHECKED ON THE WIRE.
//
// The agent applies sec.Stale, and stale is the ONE class Reconcile deletes.
// internal/iptables pins that StaleRules carries no bans
// (TestBanRemovedFromConfigIsUnknownNotStale); nothing pinned that the SERVED
// set carries none, and iptablesSectionFor passes stale straight through — so
// the day something put bans in that set, an ARMED agent would delete a
// hand-added INPUT DROP on its next pass, with no test between the change and
// the gateway.
//
// A ban is recorded, and a second address is banned and then lifted, so both
// the still-banned and the just-unbanned shapes are in play.
func TestTheServedStaleSetNeverCarriesABan(t *testing.T) {
	s, _ := agentTestServer(t)

	if err := s.banIP("198.51.100.7", 0, "brute force", "login", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.banIP("203.0.113.9", 0, "scanner", "login", ""); err != nil {
		t.Fatal(err)
	}
	// Lift the second one. unbanIP's `iptables -D` is expected to fail on a
	// test machine and is ignored there by design; what matters is the record.
	if err := s.unbanIP("203.0.113.9"); err != nil {
		t.Fatal(err)
	}

	// Give StaleRules something to generate from, otherwise it returns only
	// the forward jumps and the assertion is vacuous.
	cfg := *s.cfg()
	cfg.LastLocalIface = "eth9"
	cfg.LastLanCIDR = "192.0.2.0/24"
	s.config.Store(&cfg)

	d, _ := servedDesired(t, s)
	if d.IPTables == nil {
		t.Fatal("no firewall section")
	}
	if len(d.IPTables.Stale) == 0 {
		t.Fatal("the served stale set is empty, so 'it carries no ban' is vacuous here")
	}
	for _, r := range d.IPTables.Stale {
		if r.Table == "filter" && r.Chain == "INPUT" && len(r.Args) == 4 &&
			r.Args[0] == "-s" && r.Args[3] == "DROP" {
			t.Errorf("the served stale set carries %s; an armed agent DELETES stale rules, "+
				"and that scope is every source-DROP in INPUT including an admin's own", r)
		}
	}
}

// reapplyBans IS GONE, AND NOTHING GREW A REPLACEMENT.
//
// The deletion is the point of the hand-over on the peer-sync side: the pull
// loop merged bans and then shelled iptables for each one, which is the third
// of the three syncServices bypasses the fleet guard is written about. A test
// that only ran banSyncOnce could not tell a deleted call from a silent one,
// so this asks the package's own source whether the name exists at all.
func TestNothingReappliesBans(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	var files int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files++
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			switch id.Name {
			case "reapplyBans", "iptablesBan", "iptablesCheckBan":
				// Its own mention in a comment is not an identifier, so the
				// header in handlers_ban.go does not trip this.
				t.Errorf("%s:%d names %s; the ban install path was handed to the reconciler",
					name, fset.Position(id.Pos()).Line, id.Name)
			}
			return true
		})
	}
	if files == 0 {
		t.Fatal("parsed no files; this guard is not looking at the package")
	}
}

func carriesRule(rules []iptables.Rule, canonical string) bool {
	for _, r := range rules {
		if r.Canonical() == canonical {
			return true
		}
	}
	return false
}
