package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fakeAgent(b string) agentSource {
	return func() ([]byte, bool) { return []byte(b), true }
}

func noAgent() ([]byte, bool) { return nil, false }

// The key install asks the embed for must be a name the Makefile actually
// produces. 32-bit ARM is the trap: the Makefile passes GOARM=7 but names the
// file `hz-agent-linux-arm`, so a key of "linux-armv7" would make every
// Raspberry Pi install report "no agent for this machine" on a build that is
// carrying one.
func TestPlatformKeyMatchesWhatTheMakefileBuilds(t *testing.T) {
	raw, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	built := map[string]bool{}
	for _, m := range regexp.MustCompile(`hz-agent-(linux-[a-z0-9]+)`).FindAllStringSubmatch(string(raw), -1) {
		built[m[1]] = true
	}
	if len(built) == 0 {
		t.Fatal("the Makefile cross-compiles no hz-agent binaries; this test is reading the wrong file")
	}

	for _, goarch := range []string{"amd64", "arm64", "arm"} {
		key := platformKey("linux", goarch)
		if !built[key] {
			t.Errorf("install would ask the embed for %q, which the Makefile never builds "+
				"(it builds %v) — that arch silently gets no agent", key, keysOf(built))
		}
	}
	// And the other way: an arch the Makefile ships but nothing can name is
	// dead weight in every release binary.
	for key := range built {
		goarch := strings.TrimPrefix(key, "linux-")
		if platformKey("linux", goarch) != key {
			t.Errorf("the Makefile ships %q but platformKey can never produce it", key)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The happy path: the bytes land, executable, and nothing else appears.
func TestShipAgentWritesTheBinary(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := shipAgent(dir, fakeAgent("AGENT-BYTES"), false, &out); err != nil {
		t.Fatalf("shipAgent: %v", err)
	}

	target := filepath.Join(dir, agentBinaryName)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the agent was not placed: %v", err)
	}
	if string(got) != "AGENT-BYTES" {
		t.Fatalf("wrong contents: %q", got)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %04o, want 0755 — the same as the hz binary beside it, no wider", fi.Mode().Perm())
	}

	// One file. No unit, no temp left behind, nothing else.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != agentBinaryName {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("install wrote more than the binary: %v", names)
	}
	if !strings.Contains(out.String(), "nothing was started") {
		t.Errorf("install must say it started nothing; got:\n%s", out.String())
	}
}

// A second install is a no-op, by CONTENT rather than by existence: the file
// is not rewritten and install says so.
func TestShipAgentIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := shipAgent(dir, fakeAgent("AGENT-BYTES"), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, agentBinaryName)

	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := shipAgent(dir, fakeAgent("AGENT-BYTES"), false, &out); err != nil {
		t.Fatalf("a second install failed: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Errorf("the second install rewrote an identical binary (mtime moved to %v)", fi.ModTime())
	}
	if !strings.Contains(out.String(), "already this build") {
		t.Errorf("install should say the binary is already current; got:\n%s", out.String())
	}
}

// An install over an OLDER agent replaces it — idempotence must not mean
// "leave whatever is there alone" — and says what that does to a running one
// WITHOUT restarting it.
func TestShipAgentReplacesAnOlderBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, agentBinaryName)
	if err := os.WriteFile(target, []byte("OLD-AGENT"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := shipAgent(dir, fakeAgent("NEW-AGENT"), false, &out); err != nil {
		t.Fatalf("shipAgent: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-AGENT" {
		t.Fatalf("the older binary was not replaced: %q", got)
	}
	s := out.String()
	if !strings.Contains(s, "Replaced") {
		t.Errorf("install should say it replaced the binary; got:\n%s", s)
	}
	if !strings.Contains(s, "does NOT restart it") {
		t.Errorf("install must say the new bytes need a restart it is not doing; got:\n%s", s)
	}
}

// Without the hzembed tag there are no bytes. Install must say that in plain
// words and still succeed: the unit really was installed, and failing CI's own
// binary over an embed it never had is the wrong answer.
func TestShipAgentWithoutTheEmbedExplainsAndDoesNotFail(t *testing.T) {
	// Pin the availability rather than inheriting this test binary's build:
	// under -tags hzembed the embed is NOT empty, so without this the test
	// would be asserting the untagged message on the tagged branch. Caught by
	// running the tagged build, which is the only place it could show up.
	orig := agentAvailable
	agentAvailable = func() []string { return nil }
	defer func() { agentAvailable = orig }()

	dir := t.TempDir()
	var out bytes.Buffer
	if err := shipAgent(dir, noAgent, false, &out); err != nil {
		t.Fatalf("an empty embed must not fail the install: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("something was written with no bytes to write: %v", entries)
	}
	s := out.String()
	for _, want := range []string{"NOT shipped", "hzembed", "make build"} {
		if !strings.Contains(s, want) {
			t.Errorf("the empty-embed message must name %q; got:\n%s", want, s)
		}
	}
}

// The other not-shipped case: a release binary that carries agents, on a
// machine none of them runs on. It must not blame the build tag — that sends
// the operator to rebuild something that is already correct.
func TestShipAgentOnAnUnservedArchNamesWhatItHas(t *testing.T) {
	orig := agentAvailable
	agentAvailable = func() []string { return []string{"linux-amd64", "linux-arm", "linux-arm64"} }
	defer func() { agentAvailable = orig }()

	dir := t.TempDir()
	var out bytes.Buffer
	if err := shipAgent(dir, noAgent, false, &out); err != nil {
		t.Fatalf("shipAgent: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "linux-arm64") || !strings.Contains(s, "none of which") {
		t.Errorf("the message must name the arches the build does carry; got:\n%s", s)
	}
	if strings.Contains(s, "hzembed") {
		t.Errorf("a build that HAS agents must not blame the build tag; got:\n%s", s)
	}
}

// --dry-run reports and changes nothing, in both the new and the replace case.
func TestShipAgentDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := shipAgent(dir, fakeAgent("AGENT-BYTES"), true, &out); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("DRY RUN wrote something: %v", entries)
	}
	if !strings.Contains(out.String(), "DRY RUN") || !strings.Contains(out.String(), "would write") {
		t.Errorf("dry run must say what it would do; got:\n%s", out.String())
	}

	target := filepath.Join(dir, agentBinaryName)
	if err := os.WriteFile(target, []byte("OLD-AGENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := shipAgent(dir, fakeAgent("AGENT-BYTES"), true, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "OLD-AGENT" {
		t.Fatal("DRY RUN replaced the binary")
	}
	if !strings.Contains(out.String(), "would replace") {
		t.Errorf("dry run must say it would replace the older binary; got:\n%s", out.String())
	}
}

// THE INERTNESS GUARD. Installing a binary is not arming it.
//
// The one change anybody would "helpfully" make here is a daemon-reload, a
// `systemctl enable hz-agent`, or writing the unit so the agent is ready to
// go. Each of them arms, on every box hz is installed on, a binary whose whole
// design is that somebody decides. So this asserts structurally: the file that
// places the binary cannot run a command at all, and names no unit.
func TestTheBinaryPlacerCannotRunAnything(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "agentbin.go", nil, 0)
	if err != nil {
		t.Fatalf("parse agentbin.go: %v", err)
	}

	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == "os/exec" {
			t.Error("agentbin.go imports os/exec — placing a binary must not be able to " +
				"start, enable or restart anything; the agent is inert on purpose " +
				"(cmd/hz-agent/install.go)")
		}
	}

	// Comments may discuss systemd all they like; code must not name it.
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, banned := range []string{"systemctl", "systemd", ".service", "--apply"} {
			if strings.Contains(s, banned) {
				t.Errorf("agentbin.go has a string containing %q (%q): hz places the binary, "+
					"the agent's own installer owns everything systemd, and two writers of "+
					"one unit is a drift source", banned, s)
			}
		}
		return true
	})
}

// The rule is "any homelab horizon install also installs the agent" — not a
// flag, not a separate verb. So installService must call it, on both paths:
// once to report under --dry-run, once to do it.
func TestInstallShipsTheAgent(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var calls int
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "installService" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "shipAgent" {
				calls++
			}
			return true
		})
	}
	if calls < 2 {
		t.Errorf("installService calls shipAgent %d time(s), want 2 (the --dry-run report and "+
			"the real one) — every install ships the agent, and --dry-run must say so", calls)
	}
}

// There is no --agent / --with-agent flag to forget, and no separate verb.
// Shipping the agent is not opt-in.
func TestShippingTheAgentIsNotAFlag(t *testing.T) {
	for _, c := range newRoot().Commands() {
		if c.Name() != "install" {
			continue
		}
		for _, name := range []string{"agent", "with-agent", "install-agent", "no-agent"} {
			if c.Flags().Lookup(name) != nil {
				t.Errorf("install grew a --%s flag; shipping the agent is every install, "+
					"not an option somebody has to remember", name)
			}
		}
		return
	}
	t.Fatal("the install verb disappeared")
}
