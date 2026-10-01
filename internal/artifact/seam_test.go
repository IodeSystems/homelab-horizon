package artifact

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The pure/privileged seam (CLAUDE.md #12), as internal/agent carries it:
// retention.go DECIDES which artifacts to delete and must not be able to touch
// the filesystem, the clock or the network; store.go and the server act.
var pureFiles = []string{"retention.go"}

func TestPureHalfStaysPure(t *testing.T) {
	banned := map[string]string{
		"os":            "reads or writes the filesystem",
		"os/exec":       "runs commands",
		"net":           "talks to the network",
		"net/http":      "talks to the network",
		"time":          "reads the clock",
		"math/rand":     "is not deterministic",
		"crypto/rand":   "is not deterministic",
		"path/filepath": "resolves paths against a real filesystem",
		"io":            "streams bytes",
	}
	for _, name := range pureFiles {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if len(f.Imports) == 0 {
			t.Fatalf("instrument: %s parsed with no imports at all", name)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[path]; bad {
				t.Errorf("%s imports %q, which %s — the pure half takes its inputs as arguments", name, path, why)
			}
		}
	}
}

func TestPureHalfCannotRemove(t *testing.T) {
	forbidden := map[string]bool{"Remove": true, "Put": true, "Open": true, "List": true, "Has": true}
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
			switch c := call.Fun.(type) {
			case *ast.Ident:
				fn = c.Name
			case *ast.SelectorExpr:
				fn = c.Sel.Name
			}
			if forbidden[fn] {
				t.Errorf("%s calls %s at %s — the decision must not be able to act", name, fn, fset.Position(call.Pos()))
			}
			return true
		})
	}
}
