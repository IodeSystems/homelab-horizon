//go:build uiembed

package uiembed

import (
	"embed"
	"io/fs"
)

// dist holds the built frontend. The Makefile ui-embed target populates
// internal/server/uiembed/dist before a tagged build.
//
// all: rather than a bare pattern, because Vite is free to emit files starting
// with _ or . and the default pattern would silently drop them.
//
//go:embed all:dist
var distFS embed.FS

func uiFS() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	// A tagged build with an empty dist would otherwise report a UI it cannot
	// serve, which is worse than reporting none: the operator would get a
	// blank page instead of the page explaining what to install.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
