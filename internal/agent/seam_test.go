package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The guard on this package's own seam, matching the one every subsystem
// package carries: the half that COMPUTES a plan must not reach for the
// machine, so `hz-agent diff` gives the same answer to anybody, anywhere, at
// any privilege — and so the plan can be computed for a machine you are not on.
//
// plan.go, diff.go and observed.go are the pure half. observe.go reads,
// apply.go writes, source.go dials; those are where the machine lives.
//
// observed.go joined the list when the report-back landed: it is the last
// function between a machine's claim and a file hz serves back, so Sanitized
// must give a fixture the same answer it gives on a box. A clock or a file
// read in there would make "every string in a stored report went through
// redaction" a property nobody could check offline.
var pureFiles = []string{"plan.go", "diff.go", "observed.go"}

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

// The other direction, and the one that matters for the privilege split: the
// pure half must not be able to reach the apply half either. A Compute that
// could call Apply would make "report the diff" and "change the machine" one
// operation, which is exactly what the inert default exists to keep apart.
func TestPureHalfCannotApply(t *testing.T) {
	forbidden := map[string]bool{
		"Apply":          true,
		"writeIfChanged": true,
		"Observe":        true,
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
			if id, ok := call.Fun.(*ast.Ident); ok && forbidden[id.Name] {
				t.Errorf("%s calls %s at %s — the pure half must not be able to touch the machine",
					name, id.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
}
