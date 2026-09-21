package hzbin

import "testing"

// The tools share one directory and one prefix. "hz-" is a prefix of both
// "hz-probe-" and "hz-agent-", so a naive match would list every probe and
// agent build as an hz one — and an operator asking for the CLI would be
// handed the ROOT daemon.
//
// Untagged on purpose: this is the rule, not the embedding, and a plain
// `go test ./...` has to be able to fail on it.
func TestHZListingDoesNotClaimTheOtherTools(t *testing.T) {
	cases := []struct {
		key       string
		isSubTool bool
	}{
		{"linux-amd64", false},
		{"linux-arm64", false},
		{"darwin-arm64", false},
		{"probe-linux-amd64", true},
		{"agent-linux-amd64", true},
		{"agent-linux-arm", true},
	}
	for _, c := range cases {
		if got := belongsToASubTool(c.key); got != c.isSubTool {
			t.Errorf("belongsToASubTool(%q) = %v, want %v — "+
				"an hz listing that claims this key hands out the wrong binary", c.key, got, c.isSubTool)
		}
	}
}

// Every tool this package can serve must be covered by the split rule, or the
// next one added silently starts appearing in the hz listing.
func TestEverySubToolIsAccountedFor(t *testing.T) {
	for _, tool := range []string{ToolProbe, ToolAgent} {
		found := false
		for _, s := range subTools {
			if s == tool {
				found = true
			}
		}
		if !found {
			t.Errorf("%s extends %q but is not in subTools", tool, ToolHZ)
		}
	}
}
