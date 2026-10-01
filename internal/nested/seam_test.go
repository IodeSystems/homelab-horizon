package nested

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The pure/privileged seam (CLAUDE.md #12), as internal/agent carries it:
// decide.go decides what to fetch, whether the cache answers and what to
// delete; child.go and queue.go dial, read and write.
var pureFiles = []string{"decide.go"}

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
		"log/slog":      "logs — the pure half returns the LOUD line, the caller logs it",
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

func TestPureHalfCannotAct(t *testing.T) {
	forbidden := map[string]bool{
		"Put": true, "Remove": true, "Open": true, "Has": true, "List": true,
		"fetchArtifact": true, "writeCache": true, "writeAtomic": true, "LoadCache": true,
		"poll": true, "forward": true, "Drain": true, "Append": true, "Do": true,
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
