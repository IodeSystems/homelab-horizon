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
	"github.com/iodesystems/homelab-horizon/internal/wgkey"
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

	// keys is what hz has recorded per (machine, segment) — the SegmentMember
	// field, in miniature. The stub makes the same rotation-versus-impostor
	// decision the real handler makes, because the CLI's behaviour on a
	// conflict is the thing these tests are about; what it does NOT do is
	// validate, address or save, which are internal/server's to prove.
	keys map[string]string
	// enrolls counts requests, so the two-pass shape is assertable.
	enrolls int
}

func newStubHZ(t *testing.T, declared ...string) *stubHZ {
	t.Helper()
	hz := &stubHZ{
		store:    agent.CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+agent.CredentialsSuffix)},
		declared: map[string]agent.EnrollResponse{},
		adminTok: "the-admin-token",
		keys:     map[string]string{},
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
		hz.enrolls++
		m.SegmentKeys = hz.recordKeys(req)
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

// recordKeys is the stub's half of the key exchange: record on sight, refuse a
// CHANGE unless the request declares a rotation. Same three answers the real
// handler gives for a keyable membership.
func (hz *stubHZ) recordKeys(req agent.EnrollRequest) []agent.SegmentKeyResult {
	var out []agent.SegmentKeyResult
	for _, k := range req.SegmentKeys {
		at := req.Machine + "/" + k.Segment
		held, have := hz.keys[at]
		result := agent.SegmentKeyResult{Segment: k.Segment}
		switch {
		case have && held == k.PublicKey:
			result.Status = agent.SegmentKeyUnchanged
		case have && !req.RotateKeys:
			result.Status = agent.SegmentKeyConflict
			result.Detail = "hz holds a different key; if this is a rotation say so: hz-agent enroll --rotate-keys"
		default:
			hz.keys[at] = k.PublicKey
			result.Status = agent.SegmentKeyRecorded
		}
		out = append(out, result)
	}
	return out
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
	if err := enroll(f, enrollOpts{}, &out); err != nil {
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

	err := enroll(f, enrollOpts{}, new(bytes.Buffer))
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

	err := enroll(f, enrollOpts{}, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), adminTokenEnv) {
		t.Fatalf("want a refusal naming how to supply a credential, got %v", err)
	}
}

// A credential on a terminal is a credential in scrollback. Nothing enrolment
// prints may be the secret.
func TestEnrolmentNeverPrintsTheSecret(t *testing.T) {
	f, _ := enrollFlags(t)
	var out bytes.Buffer
	if err := enroll(f, enrollOpts{}, &out); err != nil {
		t.Fatal(err)
	}
	secret := readTokenFile(f.tokenFile)

	if strings.Contains(out.String(), secret) {
		t.Fatal("enrolment printed the credential")
	}
	// Re-running prints again; that path must be clean too.
	out.Reset()
	if err := enroll(f, enrollOpts{}, &out); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	first := readTokenFile(f.tokenFile)
	if hz.mints != 1 {
		t.Fatalf("first enrolment minted %d times", hz.mints)
	}

	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if got := readTokenFile(f.tokenFile); got != first {
		t.Fatal("re-enrolling replaced a working credential")
	}
	if hz.mints != 1 {
		t.Fatalf("re-enrolment made hz mint again (%d mints)", hz.mints)
	}

	// --rotate is the deliberate replacement, and it retires the old one.
	if err := enroll(f, enrollOpts{Rotate: true}, new(bytes.Buffer)); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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

	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
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

// ---------------------------------------------------------------------------
// The segment keys enrolment now reports
// ---------------------------------------------------------------------------

// keyFiles is every private key file this box wrote, by segment.
func keyFiles(t *testing.T, f *agentFlags, segments ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, seg := range segments {
		b, err := os.ReadFile(f.segmentKeys().Path(seg))
		if err != nil {
			t.Fatalf("no private key for %s: %v", seg, err)
		}
		out[seg] = strings.TrimSpace(string(b))
	}
	return out
}

// THE BOX MINTS AND REPORTS, IN ONE ACT. After enrolment hz holds a public key
// for every segment it says this machine is in — which is exactly what the
// projection needed to stop saying it cannot emit a `[Peer]` block.
func TestEnrolmentReportsAPublicKeyPerSegment(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}

	// The stub declares "gateway" in seg:lan.
	got, ok := hz.keys["gateway/seg:lan"]
	if !ok {
		t.Fatalf("hz was told no key for seg:lan; it holds %+v", hz.keys)
	}
	if !wgkey.Valid(got) {
		t.Fatalf("what reached hz is not a WireGuard key: %q", got)
	}
	// It is the public half of what the box kept, derived from the file rather
	// than taken on the command's word.
	private := keyFiles(t, f, "seg:lan")["seg:lan"]
	derived, err := wgkey.Public(private)
	if err != nil {
		t.Fatal(err)
	}
	if derived != got {
		t.Fatalf("hz holds %q, which is not the public half of this box's key", got)
	}

	// TWO REQUESTS, not two enrolments: the box learns its segments from the
	// first answer and reports keys on the second, and the credential is minted
	// exactly once across both.
	if hz.enrolls != 2 {
		t.Fatalf("enrolment made %d requests, want 2 (credential, then keys)", hz.enrolls)
	}
	if hz.mints != 1 {
		t.Fatalf("the two-pass enrolment minted %d credentials", hz.mints)
	}
}

// THE PRIVATE KEY NEVER LEAVES THE BOX. Asserted on the WIRE — every request
// body, not the argument that built one — because a struct field is easy to
// read correctly and a marshalling mistake is not.
func TestEnrolmentNeverPutsAPrivateKeyOnTheWire(t *testing.T) {
	f, hz := enrollFlags(t)

	var bodies []string
	hz.Config.Handler = watchBodies(hz.Config.Handler, func(body string) { bodies = append(bodies, body) })

	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	private := keyFiles(t, f, "seg:lan")["seg:lan"]
	public, err := wgkey.Public(private)
	if err != nil {
		t.Fatal(err)
	}

	sawPublic := false
	for _, body := range bodies {
		if strings.Contains(body, private) {
			t.Fatal("a private WireGuard key was put on the wire")
		}
		if strings.Contains(body, public) {
			sawPublic = true
		}
	}
	// THE POSITIVE CONTROL. Without it this test passes on an enrolment that
	// reports no keys at all, which is the state this change exists to end.
	if !sawPublic {
		t.Fatalf("no public key crossed the wire either, so the assertion above proves nothing; bodies=%v", bodies)
	}
	// Nor may it reach the terminal: a key in scrollback is a key in a support
	// paste, and the private half is the one that matters.
	var out bytes.Buffer
	if err := enroll(f, enrollOpts{}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), private) {
		t.Fatal("enrolment printed the private key")
	}
}

// A KEY IS MINTED ONCE AND THEN REPORTED. Re-running enrolment — which is what
// `install` does every time — must not rotate: every peer on the segment is
// configured against the old key, so a silent rotation is a fleet of stale
// configs.
func TestReEnrolmentReportsTheSameKey(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	first := keyFiles(t, f, "seg:lan")["seg:lan"]

	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	if got := keyFiles(t, f, "seg:lan")["seg:lan"]; got != first {
		t.Fatal("re-enrolment rotated a working key")
	}

	// --rotate-keys is the deliberate replacement, and hz is told it is one.
	if err := enroll(f, enrollOpts{RotateKeys: true}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	second := keyFiles(t, f, "seg:lan")["seg:lan"]
	if second == first {
		t.Fatal("--rotate-keys kept the old key")
	}
	wantPub, err := wgkey.Public(second)
	if err != nil {
		t.Fatal(err)
	}
	if hz.keys["gateway/seg:lan"] != wantPub {
		t.Fatalf("hz holds %q after a declared rotation, want %q", hz.keys["gateway/seg:lan"], wantPub)
	}
}

// A REFUSED KEY IS LOUD. hz keeping the key it holds is the safe answer, and
// the unsafe version of this is the command exiting 0 while the box believes it
// is peered. The refusal names the segment and the flag that resolves it.
func TestAKeyHZRefusesFailsTheCommandAndNamesTheFix(t *testing.T) {
	f, hz := enrollFlags(t)
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	mine := hz.keys["gateway/seg:lan"]

	// Somebody else's key is recorded for this machine — the shape a rebuilt
	// box, a restored backup or a cloned VM presents.
	_, theirs, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	hz.keys["gateway/seg:lan"] = theirs

	var out bytes.Buffer
	err = enroll(f, enrollOpts{}, &out)
	if err == nil {
		t.Fatal("enrolment reported success while hz refused this box's key")
	}
	for _, want := range []string{"seg:lan", "--rotate-keys", "DIFFERENT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if !strings.Contains(out.String(), "CONFLICT") {
		t.Errorf("the printed report does not flag the conflict:\n%s", out.String())
	}
	// hz kept what it had. The command failing is the whole defence; a failure
	// that had already overwritten the record would be theatre.
	if hz.keys["gateway/seg:lan"] != theirs {
		t.Fatal("the refused enrolment overwrote hz's record anyway")
	}
	if mine == theirs {
		t.Fatal("the fixture did not actually change the key")
	}

	// And the credential half survived: the box can still poll. Losing that
	// over a disputed key would turn a visible problem into two.
	if readTokenFile(f.tokenFile) == "" {
		t.Fatal("a refused key cost the box its credential")
	}
}

// The private keys live at 0600 in a 0700 directory, beside the credential.
func TestThePrivateKeysAreRootOnly(t *testing.T) {
	f, _ := enrollFlags(t)
	// The directory exists world-readable first, which is the state a box that
	// was enrolled before this change is in.
	if err := os.MkdirAll(filepath.Dir(f.tokenFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.segmentKeys().Path("seg:lan"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != agent.SegmentKeyFileMode {
		t.Fatalf("the private key is mode %04o, want %04o", mode, agent.SegmentKeyFileMode)
	}
	dir, err := os.Stat(f.segmentKeys().Dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dir.Mode().Perm(); mode != agent.SegmentKeyDirMode {
		t.Fatalf("the key directory is mode %04o, want %04o", mode, agent.SegmentKeyDirMode)
	}
}

// Reporting a key must not arm anything either: the unit is still inert, and
// --rotate-keys has no way into it.
func TestReportingKeysDoesNotArmTheAgent(t *testing.T) {
	f, _ := enrollFlags(t)
	if err := enroll(f, enrollOpts{}, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "--apply") || strings.Contains(unit, "[Install]") || strings.Contains(unit, "WantedBy") {
		t.Fatalf("enrolment produced an armed unit:\n%s", unit)
	}
	if strings.Contains(unit, "--rotate-keys") || strings.Contains(unit, "keys") {
		t.Fatalf("an enrolment-only concern reached the unit:\n%s", unit)
	}
	// The running agent has no rotate-keys flag at all: rotating a key is an
	// operator act at the box, never something a daemon does on a timer.
	var probe agentFlags
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	probe.register(fs)
	if fs.Lookup("rotate-keys") != nil {
		t.Fatal("--rotate-keys is on the shared flag set, so `run` accepts it")
	}
}
