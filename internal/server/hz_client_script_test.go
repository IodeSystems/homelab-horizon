package server

import (
	"os"
	"strings"
	"testing"
)

// The server serves hzClientScriptContent, not bin/hz-client — the two are
// kept in sync by hand. Editing the script and forgetting to re-sync ships
// the old client while the repo looks correct, which is exactly how a fix
// can be committed, merged and deployed without taking effect.
func TestHZClientScriptMatchesSource(t *testing.T) {
	raw, err := os.ReadFile("../../bin/hz-client")
	if err != nil {
		t.Fatalf("read bin/hz-client: %v", err)
	}
	want := strings.TrimRight(string(raw), "\n")
	if hzClientScriptContent != want {
		t.Errorf("internal/server/hz_client_script.go is out of sync with bin/hz-client\n"+
			"embedded: %d bytes, source: %d bytes\n"+
			"re-sync it (the literal is the script verbatim, minus the trailing newline)",
			len(hzClientScriptContent), len(want))
	}
}

// A backtick anywhere in the script would terminate the Go raw string the
// embedded copy lives in, so the sync silently becomes impossible.
func TestHZClientScriptHasNoBacktick(t *testing.T) {
	raw, err := os.ReadFile("../../bin/hz-client")
	if err != nil {
		t.Fatalf("read bin/hz-client: %v", err)
	}
	if i := strings.IndexByte(string(raw), '`'); i >= 0 {
		line := 1 + strings.Count(string(raw)[:i], "\n")
		t.Errorf("bin/hz-client line %d contains a backtick; it cannot be embedded in a Go raw string", line)
	}
}
