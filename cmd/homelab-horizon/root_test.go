package main

import (
	"strings"
	"testing"
)

// Every invocation hz has ever documented must keep working. The one that
// matters most is -enable-admin-token: it is the console recovery in
// docs/mfa-lockout-recovery.md, and whoever runs it may be reading a printout
// because they have no other way into the box.
func TestLegacyFlagsStillDispatch(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"version", []string{"-version"}, []string{"version"}},
		{"install", []string{"-install"}, []string{"install"}},
		{"check", []string{"-check"}, []string{"check"}},
		{"config template", []string{"-config-template"}, []string{"config-template"}},
		{"iam policy", []string{"-iam-policy"}, []string{"iam-policy"}},
		{"show systemd", []string{"-show-systemd"}, []string{"show-systemd"}},

		// Root flags: rewritten to double dash, no subcommand.
		{"admin token recovery", []string{"-enable-admin-token"}, []string{"--enable-admin-token"}},
		{"no mcp", []string{"-no-mcp"}, []string{"--no-mcp"}},

		// Values, both spellings.
		{"config with space", []string{"-config", "/etc/hz.json"}, []string{"--config", "/etc/hz.json"}},
		{"config with equals", []string{"-config=/etc/hz.json"}, []string{"--config=/etc/hz.json"}},

		// A mode flag combined with a persistent one, in either order.
		{"check dry run", []string{"-check", "-dry-run"}, []string{"check", "--dry-run"}},
		{"dry run check", []string{"-dry-run", "-check"}, []string{"check", "--dry-run"}},
		{"install with config", []string{"-install", "-config", "/etc/hz.json"},
			[]string{"install", "--config", "/etc/hz.json"}},

		// Already-modern forms pass through untouched.
		{"subcommand", []string{"install"}, []string{"install"}},
		{"double dash", []string{"--config", "/etc/hz.json"}, []string{"--config", "/etc/hz.json"}},
		{"nothing", nil, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := translateLegacyArgs(tc.in)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("translateLegacyArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Serving must be the root's own action: the systemd unit on every existing box
// runs the binary with no arguments, and a tree that printed help instead would
// take those boxes down on upgrade.
func TestBareInvocationServes(t *testing.T) {
	got, translated := translateLegacyArgs(nil)
	if len(got) != 0 {
		t.Fatalf("a bare invocation gained arguments: %v", got)
	}
	if translated {
		t.Error("a bare invocation should not report a translation")
	}

	root := newRoot()
	if root.RunE == nil {
		t.Fatal("the root command has no action, so running hz with no arguments prints help")
	}
	if root.Args == nil {
		t.Error("the root should reject stray positional arguments rather than ignoring them")
	}
}

// The translation only warns when it changed something, because stdout is
// parsed by scripts and a warning on every start is noise in journald.
func TestTranslationReportsOnlyRealChanges(t *testing.T) {
	if _, translated := translateLegacyArgs([]string{"install"}); translated {
		t.Error("a modern subcommand reported a translation")
	}
	if _, translated := translateLegacyArgs([]string{"--config", "/x"}); translated {
		t.Error("a modern flag reported a translation")
	}
	if _, translated := translateLegacyArgs([]string{"-check"}); !translated {
		t.Error("a legacy flag did not report a translation")
	}
}

// Two mode flags at once was ambiguous under the old switch too, which picked by
// case order. Take the first rather than inventing a precedence.
func TestTwoModeFlagsTakeTheFirst(t *testing.T) {
	got, _ := translateLegacyArgs([]string{"-check", "-install"})
	if len(got) == 0 || got[0] != "check" {
		t.Fatalf("got %v, want check first", got)
	}
	for _, a := range got[1:] {
		if a == "install" {
			t.Error("the second mode flag leaked through as an argument")
		}
	}
}

// Every subcommand must exist under the tree, or a legacy flag translates to
// something cobra rejects.
func TestEveryLegacyFlagHasACommand(t *testing.T) {
	root := newRoot()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for flag, sub := range legacyFlags {
		if !have[sub] {
			t.Errorf("%s translates to %q, which is not a command", flag, sub)
		}
	}
}
