//go:build !uiembed

package uiembed

import "io/fs"

// Stub used by plain `go build` (no uiembed tag): no UI is compiled in, so the
// server falls back to serving it from disk.

func uiFS() (fs.FS, bool) { return nil, false }
