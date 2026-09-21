package agent

import (
	"path"
	"strings"
)

// This file is the BOUND on removal, and it is PURE — the seam test lists it
// beside plan.go and diff.go.
//
// Writing a file the payload lists is recoverable: hz rendered it, it can
// render it again. Removing one is not, so the rule is stated once, here, and
// every path that can lead to an unlink asks it:
//
//	plan.go   before it will even SAY a file would be removed
//	apply.go  again, immediately before the unlink, on the target from the plan
//
// Two callers, one definition. That is the shape that makes "the agent cannot
// remove a file outside a directory hz claimed" a property rather than a
// habit: widening it means editing this function, and the tests that pin the
// bound name this function, so a widening cannot pass them quietly. If the
// applier trusted the plan's own Kind instead, a Change — a plain struct
// anybody can construct, including something that arrived over a wire — would
// be enough to delete a file.

// prunable answers the only question a removal may ever rest on: is this path
// inside a directory THIS payload claims, does the claim cover its name, and
// is it absent from the payload's own file list?
//
// It returns the subsystem whose reload the removal belongs to, so the applier
// takes that from the bound as well rather than from the plan's label.
//
// Every "no" here is a refusal, never a guess: a relative path, a path with a
// name that is not a plain file name, a directory the payload did not claim, a
// name the claim does not cover, and a file the payload itself writes.
func (d *Desired) prunable(target string) (Subsystem, bool) {
	if d == nil {
		return "", false
	}
	dir, name, ok := splitClean(target)
	if !ok {
		return "", false
	}

	// A file the payload lists is never a removal candidate. This is what
	// stops a claim from deleting the very files it exists to keep: the
	// errors directory claim covers 503.http, and 503.http is in the payload.
	for _, of := range d.allFiles() {
		if fd, fn, ok := splitClean(of.File.Path); ok && fd == dir && fn == name {
			return "", false
		}
	}

	for _, od := range d.dirs() {
		claimed, ok := cleanDir(od.Dir.Path)
		if !ok || claimed != dir {
			continue
		}
		if od.Dir.claims(name) {
			return od.Subsystem, true
		}
	}
	return "", false
}

// claims reports whether this claim covers a bare file name.
//
// Empty Match claims nothing, and a pattern containing a separator claims
// nothing: a Directory is a statement about ONE directory's contents, and a
// pattern that could reach through a separator would make it a statement about
// a subtree that was never listed.
func (dir Directory) claims(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return false
	}
	for _, pattern := range dir.Match {
		if pattern == "" || strings.ContainsRune(pattern, '/') {
			continue
		}
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// cleanDir normalises a claimed directory path, or refuses it.
//
// Absolute only. A relative claim has no single meaning — it would depend on
// the agent's working directory, which is a property of how systemd happened
// to start it.
func cleanDir(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	c := path.Clean(p)
	if c == "/" {
		// The root directory is never a claim. Nothing hz renders lives
		// directly in /, and a claim there would put every top-level entry one
		// glob away from removal.
		return "", false
	}
	return c, true
}

// splitClean splits an absolute file path into its cleaned directory and its
// bare name, refusing anything that is not one.
//
// path.Clean resolves ".." lexically, so a target like
// "/etc/haproxy/errors/../../passwd" becomes "/etc/passwd" and is then tested
// against the claims like any other path — which no claim covers. The
// traversal does not have to be detected as an attack; it just stops being in
// a claimed directory.
func splitClean(p string) (dir, name string, ok bool) {
	if !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	c := path.Clean(p)
	d, n := path.Split(c)
	d = path.Clean(d)
	if n == "" || n == "." || n == ".." || d == "" {
		return "", "", false
	}
	if d != "/" && strings.HasSuffix(d, "/") {
		return "", "", false
	}
	return d, n, true
}
