//go:build hzembed

package hzbin

import (
	"strings"
	"testing"
)

// The tools share a directory and "hz-" is a prefix of "hz-probe-" and
// "hz-agent-", so a naive prefix match lists every probe and agent build as an
// hz one — and an operator installing the CLI would be handed the root daemon.
func TestAvailableSplitsTheTools(t *testing.T) {
	hz := Available(ToolHZ)
	probe := Available(ToolProbe)
	agent := Available(ToolAgent)
	if len(hz) == 0 || len(probe) == 0 || len(agent) == 0 {
		t.Skip("built with -tags hzembed but bin/ is empty or partial; run 'make hz-embed' first")
	}

	for _, key := range hz {
		for _, other := range []string{"probe-", "agent-"} {
			if strings.HasPrefix(key, other) {
				t.Fatalf("the hz listing leaked a %s build: %q", strings.TrimSuffix(other, "-"), key)
			}
		}
	}
	for _, tc := range []struct {
		tool string
		keys []string
	}{{ToolProbe, probe}, {ToolAgent, agent}} {
		for _, key := range tc.keys {
			b, ok := Get(tc.tool, key)
			if !ok || len(b) < 1000 {
				t.Fatalf("%s binary for %s is missing or truncated", tc.tool, key)
			}
		}
	}
}
