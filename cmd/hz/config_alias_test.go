package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The config manager's CLI noun was renamed from `cm` to `config`. Nobody can
// expand a two-letter abbreviation on sight and `hz cm approve` says nothing
// about what it does.
//
// The rename is only safe if BOTH spellings are proved, not one. `cm` is in
// muscle memory and in scripts on boxes this repo cannot see, so it is kept as
// a deprecated alias — and a kept alias that silently does nothing is a worse
// outcome than a clean break. These tests are the positive control: each
// spelling is driven end to end and its effect asserted.

// captureStderr mirrors captureStdout for the deprecation notice, which is
// deliberately on stderr so a pipeline reading stdout is unaffected by it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// The new spelling reaches the config manager. `dispatch` with no subcommand
// returns runCM's own "subcommand required" error, which nothing else in the
// CLI produces — so seeing it proves the word routed here and not to the
// default "unknown command" arm.
func TestConfigDispatchesToTheConfigManager(t *testing.T) {
	err := dispatch(nil, "config", nil)
	if err == nil {
		t.Fatal("hz config with no subcommand returned no error")
	}
	if !strings.Contains(err.Error(), "config subcommand required") {
		t.Fatalf("hz config did not reach the config manager: %v", err)
	}
	// And the error names the verbs, so a bare `hz config` is self-describing.
	for _, verb := range []string{
		"key", "recovery", "machines", "pending", "approve",
		"deny", "remove", "promote", "show", "resolve",
	} {
		if !strings.Contains(err.Error(), verb) {
			t.Errorf("hz config does not offer the %q verb: %v", verb, err)
		}
	}
}

// The old spelling still works, and reaches exactly the same runner. This is
// the half that a rename usually breaks.
func TestCmStillWorksAsADeprecatedAlias(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() { err = dispatch(nil, "cm", nil) })

	if err == nil {
		t.Fatal("hz cm with no subcommand returned no error")
	}
	if !strings.Contains(err.Error(), "config subcommand required") {
		t.Fatalf("hz cm no longer reaches the config manager: %v", err)
	}
	if !strings.Contains(stderr, "'hz cm' is now 'hz config'") {
		t.Fatalf("hz cm does not say it was renamed; stderr was %q", stderr)
	}
	// The notice must not go to stdout: `hz cm machines --json | jq` is a real
	// invocation and a banner in front of the JSON would break it.
	out := captureStdout(t, func() { _ = dispatch(nil, "cm", nil) })
	if strings.Contains(out, "hz config") {
		t.Fatalf("the deprecation notice is on stdout, which corrupts piped output: %q", out)
	}
}

// Both spellings reach the same runner for a real verb, over the wire, not
// only at the no-argument guard. cmStub records what the CLI actually sent.
func TestBothSpellingsDriveTheSameVerb(t *testing.T) {
	for _, spelling := range []string{"config", "cm"} {
		t.Run(spelling, func(t *testing.T) {
			s := newCMStub()
			s.addMachine(stubMachine())
			c := s.start(t)

			var err error
			out := captureStdout(t, func() {
				_ = captureStderr(t, func() {
					err = dispatch(c, spelling, []string{"machines"})
				})
			})
			if err != nil {
				t.Fatalf("hz %s machines: %v", spelling, err)
			}
			if !strings.Contains(out, "box-1") {
				t.Fatalf("hz %s machines listed nothing:\n%s", spelling, out)
			}
		})
	}
}

// A word that is neither spelling still fails, and the failure points at the
// help. Without this the two cases above would also pass if `dispatch` routed
// everything to the config manager.
func TestAnUnknownCommandIsStillUnknown(t *testing.T) {
	err := dispatch(nil, "cmm", nil)
	if err == nil {
		t.Fatal("hz cmm returned no error")
	}
	if !strings.Contains(err.Error(), "unknown command: cmm") {
		t.Fatalf("hz cmm: %v", err)
	}
}

// The help has to name the new spelling for every verb, and has to mention the
// old one at least once — an operator who types `hz --help | grep cm` after the
// rename must find the sentence that tells them what happened.
func TestUsageNamesTheNewSpelling(t *testing.T) {
	for _, verb := range []string{
		"config key new", "config key ls", "config key export", "config key import",
		"config key current", "config machines", "config pending", "config approve",
		"config deny", "config remove", "config promote", "config show", "config resolve",
	} {
		if !strings.Contains(usage, verb) {
			t.Errorf("hz --help does not document %q", verb)
		}
	}
	if strings.Contains(usage, "\n  cm ") {
		t.Error("hz --help still lists the old 'cm' spelling as a command")
	}
	if !strings.Contains(usage, "'hz cm'") {
		t.Error("hz --help does not tell an operator that 'hz cm' was renamed")
	}
}
