//go:build hzembed

package hzbin

import (
	"strings"
	"testing"
)

// The two tools share a directory and "hz-" is a prefix of "hz-probe-", so a
// naive prefix match lists every probe build as an hz one — and an operator
// installing the CLI would be handed the agent.
func TestAvailableSplitsTheTools(t *testing.T) {
	hz := Available(ToolHZ)
	probe := Available(ToolProbe)
	if len(hz) == 0 || len(probe) == 0 {
		t.Skip("built with -tags hzembed but bin/ is empty; run 'make hz-embed' first")
	}

	for _, key := range hz {
		if strings.HasPrefix(key, "probe-") {
			t.Fatalf("the hz listing leaked a probe build: %q", key)
		}
	}
	for _, key := range probe {
		b, ok := Get(ToolProbe, key)
		if !ok || len(b) < 1000 {
			t.Fatalf("probe binary for %s is missing or truncated", key)
		}
	}
}
