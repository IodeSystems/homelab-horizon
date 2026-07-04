// Package hzbin optionally embeds cross-compiled hz operator-CLI binaries so a
// running server can serve them for a curl|bash install (see the /admin/hz/*
// routes). Binaries are compiled in only under the "hzembed" build tag (the
// Makefile hz-embed target cross-compiles them first); a plain `go build` uses
// the stub in embed_off.go, so CI and `go build ./...` need no prebuilt files.
package hzbin

// Get returns the hz binary bytes for a "<os>-<arch>" key (e.g. "linux-amd64")
// and whether it is available in this build.
func Get(key string) ([]byte, bool) { return get(key) }

// Available lists the embedded platform keys, sorted. Empty without the
// hzembed build tag.
func Available() []string { return available() }
