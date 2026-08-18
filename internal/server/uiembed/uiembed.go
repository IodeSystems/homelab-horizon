// Package uiembed optionally compiles the built admin UI into the binary, so a
// release is one file that renders its own login page.
//
// The UI is compiled in only under the "uiembed" build tag (the Makefile
// ui-embed target copies ui/dist into this package first); a plain `go build`
// uses the stub in embed_off.go, so CI and `go build ./...` need no prebuilt
// assets. Same shape as hzbin, for the same reason.
package uiembed

import "io/fs"

// FS returns the embedded admin UI and whether this build has one.
func FS() (fs.FS, bool) { return uiFS() }
