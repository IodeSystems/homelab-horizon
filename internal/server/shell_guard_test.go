package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// THE GUARD ON §5.2 RULE 1: a privileged verb takes typed arguments or nothing.
//
// `systemdRun` is what this exists to stop coming back. It was six lines in
// handlers_api_system_fix.go —
//
//	exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot", args...)
//
// — a variadic root executor, reachable from an HTTP handler, whose whole point
// was to escape hz's own ProtectSystem=strict confinement. Three handlers fed
// it `bash -c "<string hz assembled>"`. It is gone, and so is the fourth site,
// which was an INLINE `exec.Command("systemd-run", …, "bash", "-c", …)` that a
// grep for the helper's name does not find. That is why this checks shapes
// rather than one identifier.
//
// ═══ WHAT THIS DOES **NOT** CLAIM ═══════════════════════════════════════════
//
// It does not say the repo has no shell strings. `internal/wireguard/apply.go`
// and `internal/dnsmasq/unit.go` still build them and still run them through
// systemd-run — `wg syncconf %s <(wg-quick strip %s)`, `cat > %s &&
// systemctl daemon-reload` — and hz's own handlers reach both (s.wg.InterfaceUp,
// s.dns.Start). Those are separate, still-open hand-over items on item 12's
// checklist (privilege-audit.md §3.1), not something this commit fixed, and a
// guard scoped to include them would have to be born red.
//
// So the claim is exactly: THE HTTP LAYER AND THE TWO CLIs ASSEMBLE NO SHELL
// STRING AND START NO TRANSIENT ROOT UNIT OF THEIR OWN. Widening it to
// internal/wireguard and internal/dnsmasq is the commit that moves those two,
// and this list is where that gets recorded.
var shellFreeDirs = []string{
	".",                         // internal/server — the web process
	"../../cmd/homelab-horizon", // where fix-haproxy-logging landed
	"../../cmd/hz-agent",        // where wg-create-config landed
}

// shells are argv[0] values whose second argument is a program in a string.
var shells = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
	"/bin/bash": true, "/bin/sh": true, "/usr/bin/bash": true, "/usr/bin/sh": true,
}

// transientRunners start a command in a unit of their own, which is how a
// confined process escapes its own confinement. `systemd-run` is the one that
// was here; the others are the same move spelt differently.
var transientRunners = map[string]bool{
	"systemd-run": true, "setsid": true, "nsenter": true,
}

// checkNoShellString reports every violation in one file. Taken as a function
// over source so the test below can positive-control it against source it
// wrote on purpose — a guard that cannot fail proves nothing (CLAUDE.md §15).
func checkNoShellString(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}

	var bad []string
	ast.Inspect(f, func(n ast.Node) bool {
		// Any string literal naming a transient runner, whether or not it is
		// the argv[0] of a call we recognise. This is the belt the inline
		// fourth site needed: it was spelt as a direct exec.Command, not
		// through the helper.
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, err := strconv.Unquote(lit.Value)
			if err == nil && transientRunners[s] {
				bad = append(bad, fmt.Sprintf(
					"%s names %q. A privileged verb does not start a transient unit to escape "+
						"its own sandbox — that is §5.2 rule 3 wearing rule 1's clothes. Move the "+
						"work to a CLI verb on the binary that owns the file", filename, s))
			}
			return true
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// EVERY call, not only exec.Command — measured, not assumed. The first
		// version of this checked `exec.Command` call sites, and a control that
		// put `f.run("bash", "-c", …)` into the new CLI fixer caught NOTHING:
		// the fixer runs commands through a seam (a func field, so tests can
		// record instead of exec), and the shell string reaches the real
		// exec.Command one hop later as `name, args...`. A guard that only
		// watches the last hop watches the hop nobody writes the violation in.
		//
		// The shape is what is banned, wherever it is written: a shell name and
		// a "-c" among one call's string literals. A `-c` that is some other
		// program's flag (`haproxy -c -f`) does not match, because `haproxy` is
		// not a shell.

		// Read every argument that is a plain string literal. A non-literal is
		// not automatically a violation — `exec.Command(name, args...)` with a
		// constant name elsewhere is ordinary — but a shell among the literals
		// is, wherever in the argv it sits.
		var literals []string
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				literals = append(literals, s)
			}
		}
		for i, s := range literals {
			if !shells[s] {
				continue
			}
			// `sh` with no -c is a shell being run, not a program in a string;
			// the violation is the pair.
			for _, later := range literals[i+1:] {
				if later == "-c" {
					bad = append(bad, fmt.Sprintf(
						"%s runs %q -c <string>. A verb takes typed arguments or nothing (§5.2 rule 1): "+
							"a command assembled as text cannot be narrowed by validation, and the "+
							"quoting is the caller's problem forever", filename, s))
				}
			}
		}
		return true
	})
	return bad, nil
}

func TestPrivilegedVerbsAssembleNoShellString(t *testing.T) {
	for _, dir := range shellFreeDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v — this guard is reading the wrong tree", dir, err)
		}
		checked := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			checked++
			bad, err := checkNoShellString(filepath.Join(dir, name), nil)
			if err != nil {
				t.Fatalf("parse %s/%s: %v", dir, name, err)
			}
			for _, msg := range bad {
				t.Error(msg)
			}
		}
		// An empty sweep is a claim about the instrument, not about the code.
		if checked == 0 {
			t.Errorf("%s contributed no files to this guard; it is measuring nothing there", dir)
		}
	}
}

// The endpoints are gone from the mux, not merely unreachable from the UI. A
// route left registered is still a route, and this is the half of the deletion
// that a handler-level grep cannot see.
func TestTheShellingEndpointsAreNotRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, gone := range []string{
		`mux, "/api/v1/wg/create-config"`,
		`mux, "/api/v1/haproxy/fix-logging"`,
	} {
		if strings.Contains(body, gone) {
			t.Errorf("server.go still registers %s — it moved to a CLI verb "+
				"(plan/design/privilege-audit.md §7 A)", gone)
		}
	}
}

// THE POSITIVE CONTROL. Each rule is run against source that breaks exactly it.
// Without this, a checker that silently stopped matching — a renamed AST field,
// an unquote that started failing — would look identical to a clean tree, which
// is the failure mode this repo has been burned by five times in one day.
func TestTheShellGuardCatchesViolations(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "the systemdRun helper, verbatim",
			src: "package p\nimport \"os/exec\"\n" +
				"func systemdRun(args ...string) { " +
				"_ = exec.Command(\"systemd-run\", append([]string{\"--pipe\", \"--wait\"}, args...)...) }\n",
			want: "transient unit",
		},
		{
			name: "the inline fourth site, which the helper's name would miss",
			src: "package p\nimport \"os/exec\"\n" +
				"func f(path string) { _ = exec.Command(\"systemd-run\", \"--pipe\", \"bash\", \"-c\", \"cat > \"+path) }\n",
			want: "transient unit",
		},
		{
			name: "a shell string with no systemd-run at all",
			src: "package p\nimport \"os/exec\"\n" +
				"func f(p string) { _ = exec.Command(\"bash\", \"-c\", \"touch \"+p) }\n",
			want: "typed arguments or nothing",
		},
		{
			name: "a shell reaching exec through a seam, which the first version missed",
			src: "package p\n" +
				"type fixer struct{ run func(name string, args ...string) ([]byte, error) }\n" +
				"func (f fixer) go1(p string) { _, _ = f.run(\"bash\", \"-c\", \"apparmor_parser -r \"+p) }\n",
			want: "typed arguments or nothing",
		},
		{
			name: "/bin/sh spelt out",
			src: "package p\nimport \"os/exec\"\n" +
				"func f(p string) { _ = exec.CommandContext(nil, \"/bin/sh\", \"-c\", p) }\n",
			want: "typed arguments or nothing",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad, err := checkNoShellString("synthetic.go", c.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(bad) == 0 {
				t.Fatalf("the guard did not catch it — it is not measuring this rule")
			}
			if !strings.Contains(strings.Join(bad, "\n"), c.want) {
				t.Errorf("caught it with the wrong sentence: %v", bad)
			}
		})
	}

	// And the negative side: the shapes that are FINE must stay fine, or the
	// guard becomes something people disable.
	for _, ok := range []string{
		"package p\nimport \"os/exec\"\nfunc f() { _ = exec.Command(\"systemctl\", \"restart\", \"rsyslog\") }\n",
		"package p\nimport \"os/exec\"\nfunc f(p string) { _ = exec.Command(\"apparmor_parser\", \"-r\", p) }\n",
		"package p\nimport \"os/exec\"\nfunc f(ip string) { _ = exec.Command(\"iptables\", \"-I\", \"INPUT\", \"1\", \"-s\", ip, \"-j\", \"DROP\") }\n",
		"package p\nimport \"os/exec\"\nfunc f(c string) { _ = exec.Command(\"haproxy\", \"-c\", \"-f\", c) }\n",
		"package p\n// systemd-run in a comment is prose, not a call\nfunc f() {}\n",
	} {
		bad, err := checkNoShellString("synthetic.go", ok)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(bad) != 0 {
			t.Errorf("a typed-argument call was flagged: %v\n%s", bad, ok)
		}
	}
}
