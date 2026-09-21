package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The agent's own credential: how a machine proves to hz that it is that
// machine, and nothing more.
//
// WHY IT IS NOT THE ADMIN TOKEN. The agent used to poll with an hz admin
// credential, and it did not work — `isAdmin` has no Bearer path, so the poll
// answered 401 (plan/privilege-audit.md §1.1). The tempting repair is to grow
// one. That would make the admin token usable as a header on EVERY admin
// surface in hz, which is a fleet-wide widening to fix one endpoint. So the
// agent gets its own credential instead, checked by its own code, accepted by
// exactly one route.
//
// It is also the only shape that satisfies plan/architecture.md's "Two
// channels": what the agent holds must be worth a machine's network shape and
// nothing else. An admin token is worth the estate.
//
// WHY BOTH HALVES LIVE HERE. The bug was not that either half was wrong — it
// was that the client's credential and the server's check were written in
// different packages against different assumptions and were never exercised
// together. Authorize (what hz-agent sends) and PresentedSecret (what hz reads)
// are four lines apart on purpose: they cannot drift without somebody editing
// both. CredentialStore is the same story for the store format — hz verifies
// against it, `hz-agent enroll` writes it, neither owns a private copy.
//
// WHAT hz STORES IS A HASH. hz only ever needs to answer "is this the secret I
// issued", which a SHA-256 comparison does. The secret exists in exactly one
// place, the agent's own token file. A store that leaks tells an attacker which
// machines are enrolled; it does not tell them how to be one.
//
// WHAT ITEM 13 CHANGES. Records are keyed by machine from the first line, and
// nothing in this file knows where a machine name comes from. When the Machine
// record lands, hz mints the secret at enrolment and this store becomes its
// projection. The wire format, the header, the hashing, the verification and
// the whole agent side are unchanged: item 13 replaces the ISSUER
// (`hz-agent enroll` writing a record locally) and nothing else.

const bearerPrefix = "Bearer "

// Authorize stamps a request with an agent credential.
//
// The one place the header is built. hz reads it back with PresentedSecret.
func Authorize(req *http.Request, secret string) {
	if secret == "" {
		return
	}
	req.Header.Set("Authorization", bearerPrefix+secret)
}

// PresentedSecret is the credential a request carries, or "".
//
// The one place the header is parsed.
func PresentedSecret(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
}

// SecretBytes is the entropy in a minted credential. 32 bytes, matching the
// admin token's length, rendered hex.
const SecretBytes = 32

// NewSecret mints a credential.
func NewSecret() (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("minting an agent credential: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashSecret is what hz stores. Never the secret itself.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Credential is one machine's enrolment.
type Credential struct {
	// Machine is who this credential speaks for. The key: one record per
	// machine, which is the shape item 13's Machine record slots into.
	Machine string `json:"machine"`

	// Hash is SHA-256 of the secret, hex. hz never holds the secret.
	Hash string `json:"hash"`

	// CreatedAt is when it was issued, so a rotation is visible in the file.
	CreatedAt int64 `json:"created_at,omitempty"`
}

// CredentialsSuffix names the store beside hz's config, the way the admin
// token already lives at "<config>.token". An operator looking for hz's
// credentials finds both in one directory.
//
// It is deliberately NOT a field in config.Config: the config is what
// peer-sync ships to HA peers and what the backup endpoint zips up. A
// credential in there would ride both of those channels to places nobody
// decided to send it.
const CredentialsSuffix = ".agents"

// DefaultCredentialsPath is the store for hz's default config location. Used
// by hz-agent, which does not read hz's config and so cannot derive it.
const DefaultCredentialsPath = "/etc/homelab-horizon/config.json" + CredentialsSuffix

// CredentialStore is the enrolled set, a JSON file readable only by root.
//
// A file rather than the user database because the agent must authenticate on
// a box whose user store is unavailable — hz tolerates `users == nil`, and a
// gateway whose network config depends on its identity store coming up is a
// worse failure than the one this fixes.
type CredentialStore struct{ Path string }

// Load reads the store. A missing file is an empty store, not an error: a box
// where nothing has enrolled yet is a normal state, and it must answer 401
// rather than fail to start.
func (s CredentialStore) Load() ([]Credential, error) {
	if s.Path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var creds []Credential
	if err := json.Unmarshal(b, &creds); err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.Path, err)
	}
	return creds, nil
}

// Save replaces the store, 0600, via a temp file in the same directory so a
// crash mid-write cannot leave hz with a truncated credential list.
func (s CredentialStore) Save(creds []Credential) error {
	if s.Path == "" {
		return errors.New("no credential store path")
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].Machine < creds[j].Machine })
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agents-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// Enroll records a machine's credential, replacing any record it already had.
//
// Replacing rather than appending is the rotation story: re-running enrolment
// retires the old secret in the same write, so there is never a moment where
// two secrets open the same door and no way to accumulate forgotten ones.
func (s CredentialStore) Enroll(machine, secret string) error {
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return errors.New("a credential needs a machine name")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("a credential needs a secret")
	}
	creds, err := s.Load()
	if err != nil {
		return err
	}
	next := make([]Credential, 0, len(creds)+1)
	for _, c := range creds {
		if c.Machine != machine {
			next = append(next, c)
		}
	}
	next = append(next, Credential{
		Machine:   machine,
		Hash:      HashSecret(secret),
		CreatedAt: time.Now().Unix(),
	})
	return s.Save(next)
}

// Machine reports which machine a secret speaks for.
//
// Constant-time against every stored hash, and it does not stop at the first
// match: an early return would leak, through timing, where in the file a
// machine sits. The store is small enough that walking all of it costs
// nothing.
func (s CredentialStore) Machine(secret string) (string, bool) {
	if strings.TrimSpace(secret) == "" {
		return "", false
	}
	creds, err := s.Load()
	if err != nil {
		return "", false
	}
	want := HashSecret(secret)
	machine, found := "", false
	for _, c := range creds {
		if subtle.ConstantTimeCompare([]byte(c.Hash), []byte(want)) == 1 {
			machine, found = c.Machine, true
		}
	}
	return machine, found
}

// Enrolled reports whether this exact secret is the one recorded for machine.
// What `hz-agent enroll` uses to decide it has nothing to do.
func (s CredentialStore) Enrolled(machine, secret string) bool {
	got, ok := s.Machine(secret)
	return ok && got == machine
}
