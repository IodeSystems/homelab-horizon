// Package hzbin optionally embeds cross-compiled client binaries so a running
// server can serve them for a curl|bash install (see the /admin/hz/* and
// /admin/hz-probe/* routes). Binaries are compiled in only under the
// "hzembed" build tag (the Makefile hz-embed target cross-compiles them
// first); a plain `go build` uses the stub in embed_off.go, so CI and
// `go build ./...` need no prebuilt files.
package hzbin

// Tools this package can serve. The value is the filename prefix under bin/.
const (
	ToolHZ    = "hz"
	ToolProbe = "hz-probe"
)

// Get returns a tool's binary for a "<os>-<arch>" key (e.g. "linux-amd64")
// and whether it is available in this build.
func Get(tool, key string) ([]byte, bool) { return get(tool + "-" + key) }

// Available lists the platform keys embedded for a tool, sorted. Empty
// without the hzembed build tag.
func Available(tool string) []string { return available(tool + "-") }
