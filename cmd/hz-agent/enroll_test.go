package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

func enrollFlags(t *testing.T) *agentFlags {
	t.Helper()
	dir := t.TempDir()
	return &agentFlags{
		hzURL:         defaultHZURL,
		tokenFile:     filepath.Join(dir, "hz-agent", "token"),
		hzCredentials: filepath.Join(dir, "hz", "config.json"+agent.CredentialsSuffix),
		machine:       "gateway",
		interval:      defaultInterval,
	}
}

// The credential hz-agent writes is the credential hz accepts. Stated from the
// enrolment end, so the two files are proven to agree before any HTTP is
// involved.
func TestEnrolmentGivesTheAgentACredentialHZAccepts(t *testing.T) {
	f := enrollFlags(t)
	var out bytes.Buffer
	if err := enroll(f, false, &out); err != nil {
		t.Fatal(err)
	}

	secret := readTokenFile(f.tokenFile)
	if secret == "" {
		t.Fatal("enrolment wrote no credential")
	}
	store := agent.CredentialStore{Path: f.hzCredentials}
	if m, ok := store.Machine(secret); !ok || m != "gateway" {
		t.Fatalf("hz does not accept what the agent holds: %q %v", m, ok)
	}
}

// A credential on a terminal is a credential in scrollback. Nothing enrolment
// prints may be the secret.
func TestEnrolmentNeverPrintsTheSecret(t *testing.T) {
	f := enrollFlags(t)
	var out bytes.Buffer
	if err := enroll(f, false, &out); err != nil {
		t.Fatal(err)
	}
	secret := readTokenFile(f.tokenFile)

	if strings.Contains(out.String(), secret) {
		t.Fatal("enrolment printed the credential")
	}
	// Re-running prints again; that path must be clean too.
	out.Reset()
	if err := enroll(f, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("re-enrolment printed the credential")
	}
	// And the hash is not a thing to paste around either.
	if strings.Contains(out.String(), agent.HashSecret(secret)) {
		t.Fatal("enrolment printed the stored hash")
	}
}

// Nor may it reach the process list — the half that
// TestUnitPassesTheTokenByFileNotOnTheCommandLine pins for the unit, pinned
// here for enrolment.
func TestEnrolmentPutsNoCredentialInArgv(t *testing.T) {
	f := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	secret := readTokenFile(f.tokenFile)
	for _, arg := range os.Args {
		if strings.Contains(arg, secret) {
			t.Fatal("the credential is on this process's command line")
		}
	}
	// generateUnit is what a running agent is launched from, and enrolment
	// must not have changed that: the unit still points at a file.
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, secret) {
		t.Fatalf("the credential reached the unit:\n%s", unit)
	}
	if strings.Contains(unit, "--hz-credentials") {
		t.Fatalf("the unit carries an enrolment-only flag:\n%s", unit)
	}
}

func TestTheCredentialFileIsRootOnly(t *testing.T) {
	f := enrollFlags(t)
	// The directory already exists and is world-readable — which is the state
	// the audit VM was actually in, because the token directory predates
	// enrolment. MkdirAll would have left it that way.
	if err := os.MkdirAll(filepath.Dir(f.tokenFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != tokenFileMode {
		t.Fatalf("the credential is mode %04o, want %04o", mode, tokenFileMode)
	}
	dir, err := os.Stat(filepath.Dir(f.tokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dir.Mode().Perm(); mode != tokenDirMode {
		t.Fatalf("the credential directory is mode %04o, want %04o", mode, tokenDirMode)
	}
}

// install runs enrolment every time, so it must be safe to re-run: a machine
// that is already enrolled keeps the credential it has.
func TestReEnrolmentKeepsAWorkingCredential(t *testing.T) {
	f := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	first := readTokenFile(f.tokenFile)

	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if got := readTokenFile(f.tokenFile); got != first {
		t.Fatal("re-enrolling replaced a working credential")
	}

	// --rotate is the deliberate replacement, and it retires the old one.
	if err := enroll(f, true, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	second := readTokenFile(f.tokenFile)
	if second == first {
		t.Fatal("--rotate kept the old credential")
	}
	store := agent.CredentialStore{Path: f.hzCredentials}
	if _, ok := store.Machine(first); ok {
		t.Fatal("the rotated-out credential still authenticates")
	}
	if _, ok := store.Machine(second); !ok {
		t.Fatal("the rotated-in credential does not authenticate")
	}
}

// A credential hz never recorded is re-minted rather than trusted. This is the
// state the bug left every box in: a token file that hz does not know.
func TestAnUnrecognisedCredentialIsReplaced(t *testing.T) {
	f := enrollFlags(t)
	if err := os.MkdirAll(filepath.Dir(f.tokenFile), tokenDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tokenFile, []byte("a-token-hz-never-issued\n"), tokenFileMode); err != nil {
		t.Fatal(err)
	}

	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if got := readTokenFile(f.tokenFile); got == "a-token-hz-never-issued" {
		t.Fatal("enrolment kept a credential hz does not accept")
	}
}

// enroll without root refuses before touching anything, like install.
func TestEnrollNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this test asserts the unprivileged refusal")
	}
	f := enrollFlags(t)
	err := runEnroll([]string{"--token-file", f.tokenFile, "--hz-credentials", f.hzCredentials})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("enroll should refuse without root, got %v", err)
	}
	if _, statErr := os.Stat(f.tokenFile); statErr == nil {
		t.Fatal("enroll wrote a credential without root")
	}
}

// Enrolment buys a READ. It must not arm anything: the four inertness
// properties are unchanged by having a credential.
func TestEnrolmentDoesNotArmTheAgent(t *testing.T) {
	f := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "--apply") {
		t.Fatalf("enrolment produced an applying unit:\n%s", unit)
	}
	if strings.Contains(unit, "[Install]") || strings.Contains(unit, "WantedBy") {
		t.Fatalf("enrolment produced an enableable unit:\n%s", unit)
	}
}
