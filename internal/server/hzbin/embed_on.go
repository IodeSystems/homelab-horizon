//go:build hzembed

package hzbin

import (
	"embed"
	"sort"
	"strings"
)

// bin holds the cross-compiled client binaries, named <tool>-<os>-<arch>. The
// Makefile hz-embed target populates internal/server/hzbin/bin before a
// tagged build.
//
//go:embed bin
var binFS embed.FS

func get(name string) ([]byte, bool) {
	b, err := binFS.ReadFile("bin/" + name)
	if err != nil {
		return nil, false
	}
	return b, true
}

// available lists the entries carrying a prefix, with the prefix stripped.
//
// The tools share a directory and "hz-" is a prefix of both "hz-probe-" and
// "hz-agent-", so matching on the prefix alone would list every probe and
// agent build as an hz one — and an operator installing the CLI would be
// handed the root daemon. An entry belongs to the shorter tool only when what
// follows the prefix is not itself another tool's name.
func available(prefix string) []string {
	entries, err := binFS.ReadDir("bin")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		key := strings.TrimPrefix(e.Name(), prefix)
		if prefix == ToolHZ+"-" && belongsToASubTool(key) {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
