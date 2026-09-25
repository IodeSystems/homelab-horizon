//go:build hzembed

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/server/hzbin"
)

// The untagged tests prove the WRITING is correct with fake bytes. This one
// proves a real build has bytes to write — which is the whole claim, and the
// one thing a plain `go test ./...` can never see, because `go build ./...`
// omits hzembed and is therefore exactly the configuration where the embed is
// empty.
//
// Run it the way a release is built:
//
//	make hz-embed && go test -tags hzembed ./cmd/homelab-horizon/
func TestARealBuildShipsAnAgentForThisMachine(t *testing.T) {
	if len(hzbin.Available(hzbin.ToolAgent)) == 0 {
		t.Skip("built with -tags hzembed but bin/ is empty; run 'make hz-embed' first")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("hz embeds linux agents only; this is %s", runtime.GOOS)
	}

	b, ok := embeddedAgent()
	if !ok {
		t.Fatalf("a tagged build carries %v but nothing for %s — install would tell every "+
			"operator on this arch that the agent was not shipped",
			hzbin.Available(hzbin.ToolAgent), agentPlatformKey())
	}
	if len(b) < 1_000_000 {
		t.Fatalf("the embedded agent is %d bytes; that is not a Go binary", len(b))
	}

	dir := t.TempDir()
	if err := shipAgent(dir, embeddedAgent, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("shipAgent: %v", err)
	}
	target := filepath.Join(dir, agentBinaryName)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the agent was not placed: %v", err)
	}
	if !bytes.Equal(got, b) {
		t.Fatal("the placed binary is not the embedded one")
	}
	if !bytes.HasPrefix(got, []byte("\x7fELF")) {
		t.Fatalf("what install placed is not an ELF executable: % x", got[:4])
	}
}
