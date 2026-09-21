package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The header the client writes is the header the server reads. These two
// functions exist as a pair precisely because the bug they replace was two
// halves that each worked against a different idea of the credential.
func TestTheHeaderTheClientWritesIsTheHeaderTheServerReads(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, DesiredPath, nil)
	Authorize(req, "s3cret")
	if got := PresentedSecret(req); got != "s3cret" {
		t.Fatalf("round trip lost the credential: %q", got)
	}
}

// An unauthenticated request must read as unauthenticated, not as a machine
// whose name happens to be the empty string.
func TestNoHeaderPresentsNoSecret(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, DesiredPath, nil)
	if got := PresentedSecret(req); got != "" {
		t.Fatalf("a bare request presented %q", got)
	}
	Authorize(req, "")
	if req.Header.Get("Authorization") != "" {
		t.Fatal("Authorize stamped an empty credential")
	}
	// A scheme that is not Bearer is not an agent credential.
	req.Header.Set("Authorization", "Basic aGk6aGk=")
	if got := PresentedSecret(req); got != "" {
		t.Fatalf("a Basic header was read as an agent credential: %q", got)
	}
}

func TestSecretsAreDistinctAndFullLength(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two mints produced the same credential")
	}
	if len(a) != SecretBytes*2 {
		t.Fatalf("want %d hex chars, got %d", SecretBytes*2, len(a))
	}
}

// hz stores a hash. The secret must not be recoverable from the store, and it
// must not be IN the store — this is the assertion that catches somebody
// "simplifying" Credential by adding a Secret field.
func TestTheStoreHoldsTheHashAndNeverTheSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json"+CredentialsSuffix)
	store := CredentialStore{Path: path}
	const secret = "a-secret-that-must-not-be-on-disk"

	if err := store.Enroll("gateway", secret); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("the secret is in %s", path)
	}
	if !strings.Contains(string(b), HashSecret(secret)) {
		t.Fatalf("the hash is not in %s:\n%s", path, b)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the credential store is mode %04o; it must be 0600", mode)
	}
}

func TestOnlyAnEnrolledSecretNamesAMachine(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+CredentialsSuffix)}

	// A store that does not exist yet answers "nobody", not an error.
	if _, ok := store.Machine("anything"); ok {
		t.Fatal("an empty store accepted a credential")
	}

	if err := store.Enroll("gateway", "right"); err != nil {
		t.Fatal(err)
	}
	if m, ok := store.Machine("right"); !ok || m != "gateway" {
		t.Fatalf("the enrolled secret did not name its machine: %q %v", m, ok)
	}
	if _, ok := store.Machine("wrong"); ok {
		t.Fatal("an unenrolled secret was accepted")
	}
	if _, ok := store.Machine(""); ok {
		t.Fatal("an empty secret was accepted")
	}
	if !store.Enrolled("gateway", "right") {
		t.Fatal("Enrolled disagrees with Machine")
	}
	if store.Enrolled("someone-else", "right") {
		t.Fatal("gateway's credential spoke for another machine")
	}
}

// Rotation RETIRES the old secret rather than adding a second one. Two live
// credentials for one machine is an accumulation nobody would ever prune.
func TestRotationRetiresTheOldSecret(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+CredentialsSuffix)}
	if err := store.Enroll("gateway", "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.Enroll("gateway", "second"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Machine("first"); ok {
		t.Fatal("the rotated-out secret still works")
	}
	if _, ok := store.Machine("second"); !ok {
		t.Fatal("the new secret does not work")
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("rotation left %d records for one machine: %+v", len(creds), creds)
	}
}

// Two machines, two credentials, neither speaking for the other. This is the
// property item 13 inherits: it changes who issues a record, not the fact
// that records are per machine.
func TestCredentialsAreScopedToOneMachine(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+CredentialsSuffix)}
	if err := store.Enroll("gateway", "g-secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Enroll("spare", "s-secret"); err != nil {
		t.Fatal(err)
	}
	if m, _ := store.Machine("g-secret"); m != "gateway" {
		t.Fatalf("gateway's secret named %q", m)
	}
	if m, _ := store.Machine("s-secret"); m != "spare" {
		t.Fatalf("spare's secret named %q", m)
	}
}

func TestEnrolRefusesAnIncompleteCredential(t *testing.T) {
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "config.json"+CredentialsSuffix)}
	if err := store.Enroll("", "secret"); err == nil {
		t.Fatal("a credential with no machine was accepted")
	}
	if err := store.Enroll("gateway", "   "); err == nil {
		t.Fatal("a credential with no secret was accepted")
	}
}
