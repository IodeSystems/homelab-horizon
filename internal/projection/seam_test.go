package projection

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The purity guard, matching the five that already exist (internal/haproxy,
// internal/dnsmasq, internal/iptables, internal/wireguard, internal/agent).
//
// It is stricter in what it protects than those are, because the claim is
// bigger. Theirs is "the renderer does not reach for the machine it renders
// FOR, which happens to be this one". This package's claim is that hz can say
// what a machine it has never touched should look like — so a single os.Getenv
// or os.Hostname in here would silently make every remote machine's projection
// a statement about the gateway instead.
//
// projection.go is the whole pure half. There is no impure half in this
// package at all: the file reads and the database live at the call site.
var pureFiles = []string{"projection.go"}

func TestProjectionStaysPure(t *testing.T) {
	banned := map[string]string{
		"os":            "reads the filesystem or the environment",
		"os/exec":       "runs commands",
		"net":           "talks to the network",
		"net/http":      "talks to the network",
		"time":          "reads the clock",
		"math/rand":     "is not deterministic",
		"crypto/rand":   "is not deterministic",
		"path/filepath": "resolves paths against a real filesystem",
		"database/sql":  "reads a database — registrations arrive as an argument",

		// The database package is the one an author would reach for to "just
		// look the registrations up", which is precisely the move that would
		// make project(global, machineID) unable to run in a test.
		"github.com/iodesystems/homelab-horizon/internal/db": "reads hz's database — instances arrive in Global",
	}
	for _, name := range pureFiles {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[path]; bad {
				t.Errorf("%s imports %q, which %s — the projection takes its inputs as arguments", name, path, why)
			}
		}
	}
}

// The selector check, which the import list cannot make.
//
// internal/wireguard's guard had to walk selectors because its renderer
// genuinely needs net.ParseCIDR while net.LookupHost must stay out. The same
// shape of hole is open here through a package this one legitimately imports:
// internal/config is hz's config MODEL and also the thing that loads and saves
// it, so `config.Load` inside the projection would import cleanly, compile,
// and read a file off the gateway's disk in the middle of a function whose
// whole value is that it does not.
func TestProjectionCannotLoadOrSaveConfig(t *testing.T) {
	forbidden := map[string]string{
		"Load":       "reads the config off disk — the config arrives in Global",
		"LoadConfig": "reads the config off disk — the config arrives in Global",
		"Save":       "writes the config to disk — a projection changes nothing",
		"Hostname":   "asks the kernel which box this is — the machine is a parameter",
		"Getenv":     "reads the environment",
		"Now":        "reads the clock",
	}
	for _, name := range pureFiles {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.Ident:
				fn = f.Name
			case *ast.SelectorExpr:
				fn = f.Sel.Name
			}
			if why, bad := forbidden[fn]; bad {
				t.Errorf("%s calls %s at %s, which %s", name, fn, fset.Position(call.Pos()), why)
			}
			return true
		})
	}
}
