package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// WHAT THESE TESTS COVER, AND WHAT THEY DO NOT.
//
// Item 13 moved the ISSUER to hz, so enrolment is now a request. These tests
// own the CLIENT half: what the command writes, what it refuses to print, what
// it does with hz's answer. The stub below stands in for hz and is deliberately
// thin — it makes the same two decisions the real handler makes (a machine hz
// does not declare is refused; a matching hash is already-enrolled) by calling
// the same primitives, and nothing else.
//
// THE CONTRACT ITSELF IS PINNED AGAINST THE REAL HZ, not against this stub, in
// internal/server/handlers_api_machines_test.go — declare a machine, enrol it
// with the real agent.Enroller over a real socket against setupRoutes(), then
// poll with the credential it issued. That is the test shape the credential bug
// needed: handler and client exercised as a pair, neither green alone.

// stubHZ is an hz that issues credentials. It holds a real CredentialStore, so
// what it records is what hz would record.
type stubHZ struct {
	*httptest.Server
	store    agent.CredentialStore
	declared map[string]agent.EnrollResponse
	adminTok string
	mints    int
}

func newStubHZ(t *testing.T, declared ...string) *stubHZ {
	t.Helper()
	hz := &stubHZ{
		store:    agent.CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+agent.CredentialsSuffix)},
		declared: map[string]agent.EnrollResponse{},
		adminTok: "the-admin-token",
	}
	for _, name := range declared {
		hz.declared[name] = agent.EnrollResponse{Machine: name, Segments: []string{"seg:lan"}}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc(agent.EnrollPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+hz.adminTok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var req agent.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m, ok := hz.declared[req.Machine]
		if !ok {
			http.Error(w, "hz declares no machine named "+req.Machine, http.StatusNotFound)
			return
		}
		if !req.Rotate && req.CurrentHash != "" {
			if existing, found := hz.store.Find(req.Machine); found && existing.Hash == req.CurrentHash {
				m.AlreadyEnrolled = true
				writeStubJSON(w, m)
				return
			}
		}
		secret, err := agent.NewSecret()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := hz.store.Enroll(req.Machine, secret); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hz.mints++
		m.Secret = secret
		writeStubJSON(w, m)
	})
	hz.Server = httptest.NewServer(mux)
	t.Cleanup(hz.Close)
	return hz
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// watchBodies shows every request body to see, then serves it unchanged. What
// crosses the wire is asserted on the wire rather than on the argument that
// built it.
func watchBodies(next http.Handler, see func(body string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			see(string(b))
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		next.ServeHTTP(w, r)
	})
}

// enrollFlags points the command at a stub hz that already declares "gateway",
// with the operator's admin credential in a file the way a real one is.
func enrollFlags(t *testing.T) (*agentFlags, *stubHZ) {
	t.Helper()
	hz := newStubHZ(t, "gateway")
	dir := t.TempDir()
	adminFile := filepath.Join(dir, "config.json.token")
	if err := os.WriteFile(adminFile, []byte(hz.adminTok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &agentFlags{
		hzURL:          hz.URL,
		tokenFile:      filepath.Join(dir, "hz-agent", "token"),
		adminTokenFile: adminFile,
		machine:        "gateway",
		interval:       defaultInterval,
	}, hz
}

// The credential hz ISSUES is the credential hz accepts. Stated from the
// enrolment end, so the two agree before any poll is involved.
func TestEnrolmentGivesTheAgentACredentialHZAccepts(t *testing.T) {
	f, hz := enrollFlags(t)
	var out bytes.Buffer
	if err := enroll(f, false, &out); err != nil {
		t.Fatal(err)
	}

	secret := readTokenFile(f.tokenFile)
	if secret == "" {
		t.Fatal("enrolment stored no credential")
	}
	if m, ok := hz.store.Machine(secret); !ok || m != "gateway" {
		t.Fatalf("hz does not accept what the agent holds: %q %v", m, ok)
	}
}

// The whole point of item 13: a box cannot write itself in. hz issues only for
// a machine it declares, and the refusal names the command that would declare
// one.
func TestEnrollingAMachineHZDoesNotDeclareIsRefused(t *testing.T) {
	f, _ := enrollFlags(t)
	f.machine = "a-box-nobody-declared"

	err := enroll(f, false, new(bytes.Buffer))
	if err == nil {
		t.Fatal("hz enrolled a machine it does not declare")
	}
	if !strings.Contains(err.Error(), "hz machine add") {
		t.Fatalf("the refusal does not say how to declare it: %v", err)
	}
	if _, statErr := os.Stat(f.tokenFile); statErr == nil {
		t.Fatal("a refused enrolment still wrote a credential file")
	}
}

// And it cannot enrol without an admin credential at all — the ask is an
// admin act, so a box with no operator behind it gets nowhere.
func TestEnrolmentNeedsAnAdminCredential(t *testing.T) {
	f, _ := enrollFlags(t)
	f.adminTokenFile = filepath.Join(t.TempDir(), "absent")
	t.Setenv(adminTokenEnv, "")

	err := enroll(f, false, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), adminTokenEnv) {
		t.Fatalf("want a refusal naming how to supply a credential, got %v", err)
	}
}

// A credential on a terminal is a credential in scrollback. Nothing enrolment
// prints may be the secret.
func TestEnrolmentNeverPrintsTheSecret(t *testing.T) {
	f, _ := enrollFlags(t)
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
// here for enrolment. The ADMIN credential is held to the same rule: it has no
// flag at all, only a file and the environment.
func TestEnrolmentPutsNoCredentialInArgv(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	secret := readTokenFile(f.tokenFile)
	for _, arg := range os.Args {
		if strings.Contains(arg, secret) || strings.Contains(arg, hz.adminTok) {
			t.Fatal("a credential is on this process's command line")
		}
	}
	// generateUnit is what a running agent is launched from, and enrolment
	// must not have changed that: the unit still points at a file.
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, secret) || strings.Contains(unit, hz.adminTok) {
		t.Fatalf("a credential reached the unit:\n%s", unit)
	}
	if strings.Contains(unit, "--admin-token-file") {
		t.Fatalf("the unit carries an enrolment-only flag:\n%s", unit)
	}
	// There is no way to put the admin credential in argv even deliberately.
	var probe agentFlags
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	probe.register(fs)
	if fs.Lookup("admin-token") != nil {
		t.Fatal("an --admin-token flag exists; a credential in argv is a credential in ps")
	}
}

func TestTheCredentialFileIsRootOnly(t *testing.T) {
	f, _ := enrollFlags(t)
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
// that is already enrolled keeps the credential it has, and hz mints nothing.
func TestReEnrolmentKeepsAWorkingCredential(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	first := readTokenFile(f.tokenFile)
	if hz.mints != 1 {
		t.Fatalf("first enrolment minted %d times", hz.mints)
	}

	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if got := readTokenFile(f.tokenFile); got != first {
		t.Fatal("re-enrolling replaced a working credential")
	}
	if hz.mints != 1 {
		t.Fatalf("re-enrolment made hz mint again (%d mints)", hz.mints)
	}

	// --rotate is the deliberate replacement, and it retires the old one.
	if err := enroll(f, true, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	second := readTokenFile(f.tokenFile)
	if second == first {
		t.Fatal("--rotate kept the old credential")
	}
	if _, ok := hz.store.Machine(first); ok {
		t.Fatal("the rotated-out credential still authenticates")
	}
	if _, ok := hz.store.Machine(second); !ok {
		t.Fatal("the rotated-in credential does not authenticate")
	}
}

// The secret this box holds never goes back over the wire, not even to ask
// whether it is still current. The HASH does, and that is all hz needs.
func TestReEnrolmentSendsTheHashAndNotTheSecret(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	secret := readTokenFile(f.tokenFile)

	var sawSecret, sawHash bool
	hz.Config.Handler = watchBodies(hz.Config.Handler, func(body string) {
		if strings.Contains(body, secret) {
			sawSecret = true
		}
		if strings.Contains(body, agent.HashSecret(secret)) {
			sawHash = true
		}
	})
	if err := enroll(f, false, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if sawSecret {
		t.Fatal("re-enrolment put the working credential on the wire")
	}
	if !sawHash {
		t.Fatal("re-enrolment sent no hash, so hz cannot have recognised the credential")
	}
}

// A credential hz never recorded is re-minted rather than trusted. This is the
// state the bug left every box in: a token file that hz does not know.
func TestAnUnrecognisedCredentialIsReplaced(t *testing.T) {
	f, _ := enrollFlags(t)
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
	f, _ := enrollFlags(t)
	err := runEnroll([]string{"--token-file", f.tokenFile, "--admin-token-file", f.adminTokenFile, "--hz", f.hzURL})
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
	f, _ := enrollFlags(t)
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
