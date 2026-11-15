package iptables

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// pureFiles are the files that must compute desired state without reaching for
// the machine, so that half can run unprivileged (and off-box) once hz-agent
// owns the privileged half. reconcile.go is deliberately absent — it is the
// privileged half.
var pureFiles = []string{"rules.go", "forwards.go", "classify.go"}

// TestRenderStaysPure is the guard on the seam itself.
//
// An import list is a coarse check, but it is the one that catches the way this
// actually rots — someone adds an os/exec to the classifier because running
// iptables-save there was easier than passing the rules in. That is exactly the
// leak this package had: LiveRules and runIptablesSave lived in classify.go.
func TestRenderStaysPure(t *testing.T) {
	banned := map[string]string{
		"os":            "reads or writes the filesystem",
		"os/exec":       "runs commands",
		"net/http":      "talks to the network",
		"time":          "reads the clock",
		"math/rand":     "is not deterministic",
		"crypto/rand":   "is not deterministic",
		"io/ioutil":     "reads or writes the filesystem",
		"path/filepath": "resolves paths against a real filesystem",
	}

	for _, name := range pureFiles {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[path]; bad {
				t.Errorf("%s imports %q, which %s — the pure half takes its inputs as arguments", name, path, why)
			}
		}
	}
}

// netAllowed is every name the pure half may use from package net. All of them
// parse or hold text; none of them opens a socket or consults a resolver.
var netAllowed = map[string]bool{
	"ParseIP":   true,
	"ParseCIDR": true,
	"IP":        true,
	"IPNet":     true,
	"IPv4len":   true,
}

// TestPureHalfUsesNetForParsingOnly is the exception the blanket import ban
// cannot express. forwards.go needs net.ParseIP and net.ParseCIDR to decide
// whether a forward's backend is inside the LAN — string parsing with no
// machine behind it — so `net` is not on the banned list above.
//
// Banning the import outright would push that parsing into the privileged half
// for no reason; allowing the import outright would let a net.Dial or a
// net.LookupHost in unremarked. So the import is allowed and every use of it is
// checked by name instead.
func TestPureHalfUsesNetForParsingOnly(t *testing.T) {
	for _, name := range pureFiles {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		imported := false
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "net" && imp.Name == nil {
				imported = true
			}
		}
		if !imported {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "net" || pkg.Obj != nil {
				return true
			}
			if !netAllowed[sel.Sel.Name] {
				t.Errorf("%s:%d uses net.%s — the pure half may use net only to parse addresses (%s)",
					name, fset.Position(sel.Pos()).Line, sel.Sel.Name, strings.Join(sortedKeys(netAllowed), ", "))
			}
			return true
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// TestExpectedRulesNeedsNoMachine generates a full rule set naming an interface
// and a backend that do not exist on this host. The generator never looks,
// because reading the host is LiveRules' job and its result arrives as an
// argument to Classify.
func TestExpectedRulesNeedsNoMachine(t *testing.T) {
	rules := ExpectedRules(Inputs{
		WGInterface: "wg-not-a-real-iface",
		OutIface:    "eth-not-a-real-iface",
		VPNRange:    "10.100.0.0/24",
		LanCIDR:     "192.168.1.0/24",
		Peers:       []PeerInput{{Name: "laptop", IP: "10.100.0.5"}},
		ServerWGIP:  "10.100.0.1",
		ListenPort:  "8080",
		JailedPeers: map[string]bool{"laptop": true},
		Forwards: []ForwardInput{{
			Service: "game", Proto: "udp", Port: 25565,
			BackendIP: "192.168.1.90", BackendPort: 25565,
		}},
	})
	if len(rules) == 0 {
		t.Fatal("no rules generated")
	}
	want := map[string]bool{
		"nat|POSTROUTING|-o eth-not-a-real-iface -j MASQUERADE":                              false,
		"filter|WG-INPUT|-s 10.100.0.5/32 -j DROP":                                           false,
		"nat|HZ-PREROUTING|-p udp --dport 25565 -j DNAT --to-destination 192.168.1.90:25565": false,
	}
	for _, r := range rules {
		if _, ok := want[r.Canonical()]; ok {
			want[r.Canonical()] = true
		}
	}
	for canon, seen := range want {
		if !seen {
			t.Errorf("want rule %q in:\n%v", canon, rules)
		}
	}
}
