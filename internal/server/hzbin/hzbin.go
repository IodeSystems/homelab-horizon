// Package hzbin optionally embeds cross-compiled client binaries so a running
// server can serve them for a curl|bash install (see the /admin/hz/* and
// /admin/hz-probe/* routes). Binaries are compiled in only under the
// "hzembed" build tag (the Makefile hz-embed target cross-compiles them
// first); a plain `go build` uses the stub in embed_off.go, so CI and
// `go build ./...` need no prebuilt files.
package hzbin

import "strings"

// Tools this package can serve. The value is the filename prefix under bin/.
//
// Every one of these is a prefix of the next, which is why available() cannot
// match on the prefix alone — see the note there.
const (
	ToolHZ    = "hz"
	ToolProbe = "hz-probe"
	ToolAgent = "hz-agent"
)

// Get returns a tool's binary for a "<os>-<arch>" key (e.g. "linux-amd64")
// and whether it is available in this build.
func Get(tool, key string) ([]byte, bool) { return get(tool + "-" + key) }

// Available lists the platform keys embedded for a tool, sorted. Empty
// without the hzembed build tag.
func Available(tool string) []string { return available(tool + "-") }

// subTools are the tools whose names extend ToolHZ's. Listing them once means
// adding a fourth tool is one line here rather than a condition nobody
// remembers to update.
//
// Deliberately in the untagged file, with belongsToASubTool below it: the rule
// is pure string handling, and keeping it out of embed_on.go is what lets a
// plain `go test ./...` prove that the hz listing does not leak a probe or an
// agent build. Behind the build tag it would only be exercised by a release.
var subTools = []string{ToolProbe, ToolAgent}

// belongsToASubTool reports whether what follows the "hz-" prefix is the rest
// of a longer tool's name — i.e. whether "probe-linux-amd64" is an hz build
// (it is not) or the tail of an hz-probe one (it is).
func belongsToASubTool(key string) bool {
	for _, tool := range subTools {
		if strings.HasPrefix(key, strings.TrimPrefix(tool, ToolHZ+"-")+"-") {
			return true
		}
	}
	return false
}
