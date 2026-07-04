//go:build hzembed

package hzbin

import (
	"embed"
	"sort"
	"strings"
)

// bin holds the cross-compiled hz binaries, named hz-<os>-<arch>. The Makefile
// hz-embed target populates internal/server/hzbin/bin before a tagged build.
//
//go:embed bin
var binFS embed.FS

func get(key string) ([]byte, bool) {
	b, err := binFS.ReadFile("bin/hz-" + key)
	if err != nil {
		return nil, false
	}
	return b, true
}

func available() []string {
	entries, err := binFS.ReadDir("bin")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, strings.TrimPrefix(e.Name(), "hz-"))
	}
	sort.Strings(out)
	return out
}
