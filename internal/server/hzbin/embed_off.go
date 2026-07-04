//go:build !hzembed

package hzbin

// Stub used by plain `go build` (no hzembed tag): no binaries are compiled in,
// so the server reports the install binaries as unavailable.

func get(string) ([]byte, bool) { return nil, false }

func available() []string { return nil }
